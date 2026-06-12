package session

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/0xbenc/ssherpa/internal/state"
)

const (
	RemotePromptPrompt      = state.RemotePromptPrompt
	RemotePromptRunning     = state.RemotePromptRunning
	RemotePromptPromptStart = state.RemotePromptPromptStart

	oscMaxPayload       = 8192
	telemetryMaxPayload = 16384

	// Bounds applied to remote-derived values at parse time. Telemetry
	// frames and OSC payloads are remote input: a hostile or buggy
	// remote can put arbitrary bytes in any string field and arbitrary
	// growth in any list, and the result is later rendered inside the
	// trusted overlay and written to the local state dir.
	remoteMaxStringRunes  = 512
	telemetryMaxIDRunes   = 128
	telemetryMaxEvents    = 64
	telemetryMaxListParts = 32
	telemetryMaxDepth     = 64
)

type remoteState struct {
	Host   string
	CWD    string
	Prompt string
}

type remoteUpdate struct {
	Host      string
	CWD       string
	Prompt    string
	Mirror    *state.SessionRecord
	HostSet   bool
	CWDSet    bool
	PromptSet bool
}

type oscObservation struct {
	Remote        remoteState
	RemoteChanged bool
	Mirrors       []state.SessionRecord
}

type oscTracker struct {
	state        int
	buf          []byte
	last         remoteState
	telemetry    bool
	telemetryBuf []byte
}

const (
	oscStateGround = iota
	oscStateEsc
	oscStateOSC
	oscStateOSCEsc
)

func newOSCTracker() *oscTracker {
	return &oscTracker{}
}

func (t *oscTracker) Observe(data []byte) (remoteState, bool) {
	observed := t.ObserveAll(data)
	return observed.Remote, observed.RemoteChanged
}

func (t *oscTracker) ObserveAll(data []byte) oscObservation {
	observed, _ := t.ObserveAndFilter(data)
	return observed
}

func (t *oscTracker) ObserveAndFilter(data []byte) (oscObservation, []byte) {
	var observed oscObservation
	clean := make([]byte, 0, len(data))
	for _, b := range data {
		if t.telemetry {
			replay, update, ok := t.feedTelemetry(b)
			if len(replay) > 0 {
				clean = append(clean, replay...)
				for _, rb := range replay {
					if oscUpdate, oscOK := t.feedOSC(rb); oscOK {
						observed.applyUpdate(t, oscUpdate)
					}
				}
			}
			if ok {
				observed.Mirrors = append(observed.Mirrors, update)
			}
			continue
		}
		if b == 0x1e {
			t.telemetry = true
			t.telemetryBuf = t.telemetryBuf[:0]
			continue
		}
		clean = append(clean, b)
		update, ok := t.feedOSC(b)
		if ok {
			observed.applyUpdate(t, update)
		}
	}
	observed.Remote = t.last
	return observed, clean
}

func (o *oscObservation) applyUpdate(tracker *oscTracker, update remoteUpdate) {
	if update.Mirror != nil {
		o.Mirrors = append(o.Mirrors, *update.Mirror)
		return
	}
	if tracker.apply(update) {
		o.RemoteChanged = true
	}
}

func (t *oscTracker) feedTelemetry(b byte) ([]byte, state.SessionRecord, bool) {
	if b == 0x1e {
		payload := string(t.telemetryBuf)
		t.telemetry = false
		t.telemetryBuf = t.telemetryBuf[:0]
		record, ok := parseSessionTelemetryFrame(payload)
		if ok {
			return nil, record, true
		}
		replay := make([]byte, 0, len(payload)+2)
		replay = append(replay, 0x1e)
		replay = append(replay, payload...)
		replay = append(replay, 0x1e)
		return replay, state.SessionRecord{}, false
	}
	if len(t.telemetryBuf) >= telemetryMaxPayload {
		replay := make([]byte, 0, len(t.telemetryBuf)+2)
		replay = append(replay, 0x1e)
		replay = append(replay, t.telemetryBuf...)
		replay = append(replay, b)
		t.telemetry = false
		t.telemetryBuf = t.telemetryBuf[:0]
		return replay, state.SessionRecord{}, false
	}
	t.telemetryBuf = append(t.telemetryBuf, b)
	return nil, state.SessionRecord{}, false
}

func (t *oscTracker) feedOSC(b byte) (remoteUpdate, bool) {
	switch t.state {
	case oscStateGround:
		if b == 0x1b {
			t.state = oscStateEsc
		}
	case oscStateEsc:
		switch b {
		case ']':
			t.buf = t.buf[:0]
			t.state = oscStateOSC
		case 0x1b:
			t.state = oscStateEsc
		default:
			t.state = oscStateGround
		}
	case oscStateOSC:
		switch b {
		case 0x07:
			return t.finishOSC()
		case 0x1b:
			t.state = oscStateOSCEsc
		default:
			t.appendOSCByte(b)
		}
	case oscStateOSCEsc:
		if b == '\\' {
			return t.finishOSC()
		}
		t.appendOSCByte(0x1b)
		t.appendOSCByte(b)
		t.state = oscStateOSC
	}
	return remoteUpdate{}, false
}

func (t *oscTracker) feed(b byte) (remoteUpdate, bool) {
	if t.telemetry {
		_, record, ok := t.feedTelemetry(b)
		if ok {
			return remoteUpdate{Mirror: &record}, true
		}
		return remoteUpdate{}, false
	}
	if b == 0x1e {
		t.telemetry = true
		t.telemetryBuf = t.telemetryBuf[:0]
		return remoteUpdate{}, false
	}
	return t.feedOSC(b)
}

func (t *oscTracker) observeWithoutFilter(data []byte) oscObservation {
	var observed oscObservation
	for _, b := range data {
		update, ok := t.feed(b)
		if !ok {
			continue
		}
		observed.applyUpdate(t, update)
	}
	observed.Remote = t.last
	return observed
}

func (t *oscTracker) appendOSCByte(b byte) {
	if len(t.buf) >= oscMaxPayload {
		t.state = oscStateGround
		t.buf = t.buf[:0]
		return
	}
	t.buf = append(t.buf, b)
}

func (t *oscTracker) finishOSC() (remoteUpdate, bool) {
	payload := string(t.buf)
	t.buf = t.buf[:0]
	t.state = oscStateGround
	return parseOSC(payload)
}

func (t *oscTracker) apply(update remoteUpdate) bool {
	changed := false
	if update.HostSet && update.Host != t.last.Host {
		t.last.Host = update.Host
		changed = true
	}
	if update.CWDSet && update.CWD != t.last.CWD {
		t.last.CWD = update.CWD
		changed = true
	}
	if update.PromptSet && update.Prompt != t.last.Prompt {
		t.last.Prompt = update.Prompt
		changed = true
	}
	return changed
}

func parseOSC(payload string) (remoteUpdate, bool) {
	code, rest, ok := strings.Cut(payload, ";")
	if !ok {
		return remoteUpdate{}, false
	}
	switch code {
	case "7":
		host, cwd, ok := parseOSC7(rest)
		if !ok {
			return remoteUpdate{}, false
		}
		return remoteUpdate{Host: host, CWD: cwd, HostSet: true, CWDSet: true}, true
	case "133":
		prompt, ok := parseOSC133(rest)
		if !ok {
			return remoteUpdate{}, false
		}
		return remoteUpdate{Prompt: prompt, PromptSet: true}, true
	case "777":
		record, ok := parseSessionTelemetryOSC(rest)
		if !ok {
			return remoteUpdate{}, false
		}
		return remoteUpdate{Mirror: &record}, true
	default:
		return remoteUpdate{}, false
	}
}

func sessionTelemetryOSC(record state.SessionRecord) ([]byte, bool) {
	payload, ok := sessionTelemetryPayload(record)
	if !ok {
		return nil, false
	}
	return []byte("\x1b]777;ssherpa-session;" + payload + "\x07"), true
}

func sessionTelemetryFrame(record state.SessionRecord) ([]byte, bool) {
	payload, ok := sessionTelemetryPayload(record)
	if !ok {
		return nil, false
	}
	return []byte("\x1essherpa-session:" + payload + "\x1e"), true
}

func sessionTelemetryPayload(record state.SessionRecord) (string, bool) {
	data, err := json.Marshal(record)
	if err != nil {
		return "", false
	}
	return base64.StdEncoding.EncodeToString(data), true
}

func parseSessionTelemetryOSC(value string) (state.SessionRecord, bool) {
	tag, payload, ok := strings.Cut(value, ";")
	if !ok || tag != "ssherpa-session" {
		return state.SessionRecord{}, false
	}
	return parseSessionTelemetryPayload(payload)
}

func parseSessionTelemetryFrame(value string) (state.SessionRecord, bool) {
	payload, ok := strings.CutPrefix(value, "ssherpa-session:")
	if !ok {
		return state.SessionRecord{}, false
	}
	return parseSessionTelemetryPayload(payload)
}

func parseSessionTelemetryPayload(payload string) (state.SessionRecord, bool) {
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return state.SessionRecord{}, false
	}
	var record state.SessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return state.SessionRecord{}, false
	}
	if strings.TrimSpace(record.ID) == "" {
		return state.SessionRecord{}, false
	}
	return clampTelemetryRecord(record)
}

// clampTelemetryRecord bounds and sanitizes a SessionRecord received
// over the wire before it can become a local mirror. Control
// characters are stripped from every string field, lists and depth are
// clamped, machine-local references (control socket, transcript file,
// import provenance) that would otherwise point local tooling at
// attacker-chosen paths are dropped, and a record whose id cannot be a
// safe filename is rejected outright.
func clampTelemetryRecord(record state.SessionRecord) (state.SessionRecord, bool) {
	record.ID = sanitizeRemoteString(record.ID, telemetryMaxIDRunes)
	if !safeTelemetryID(record.ID) {
		return state.SessionRecord{}, false
	}
	record.ParentID = sanitizeRemoteString(record.ParentID, telemetryMaxIDRunes)
	if record.ParentID != "" && !safeTelemetryID(record.ParentID) {
		record.ParentID = ""
	}
	record.OriginHost = sanitizeRemoteString(record.OriginHost, remoteMaxStringRunes)
	record.TargetAlias = sanitizeRemoteString(record.TargetAlias, remoteMaxStringRunes)
	record.RemoteHost = sanitizeRemoteString(record.RemoteHost, remoteMaxStringRunes)
	record.RemoteCWD = sanitizeRemoteString(record.RemoteCWD, remoteMaxStringRunes)
	record.RemotePrompt = sanitizeRemoteString(record.RemotePrompt, remoteMaxStringRunes)
	record.Kind = sanitizeRemoteString(record.Kind, remoteMaxStringRunes)
	record.RunnerMode = sanitizeRemoteString(record.RunnerMode, remoteMaxStringRunes)
	record.DisconnectReason = sanitizeRemoteString(record.DisconnectReason, remoteMaxStringRunes)
	record.Route = sanitizeRemoteList(record.Route, telemetryMaxListParts)
	record.Hops = sanitizeRemoteList(record.Hops, telemetryMaxListParts)
	record.SSHArgv = sanitizeRemoteList(record.SSHArgv, telemetryMaxListParts)
	if record.Depth < 0 {
		record.Depth = 0
	}
	if record.Depth > telemetryMaxDepth {
		record.Depth = telemetryMaxDepth
	}
	if len(record.Events) > telemetryMaxEvents {
		record.Events = record.Events[:telemetryMaxEvents]
	}
	for i := range record.Events {
		record.Events[i].Type = sanitizeRemoteString(record.Events[i].Type, remoteMaxStringRunes)
		record.Events[i].Message = sanitizeRemoteString(record.Events[i].Message, remoteMaxStringRunes)
	}
	record.ControlPath = ""
	record.Transcript = nil
	record.Import = nil
	if record.RecordedBy != nil {
		origin := *record.RecordedBy
		origin.MachineID = sanitizeRemoteString(origin.MachineID, remoteMaxStringRunes)
		origin.SSHerpaVersion = sanitizeRemoteString(origin.SSHerpaVersion, remoteMaxStringRunes)
		record.RecordedBy = &origin
	}
	if record.Forward != nil {
		forward := *record.Forward
		forward.LocalBind = sanitizeRemoteString(forward.LocalBind, remoteMaxStringRunes)
		forward.RemoteHost = sanitizeRemoteString(forward.RemoteHost, remoteMaxStringRunes)
		forward.Through = sanitizeRemoteString(forward.Through, remoteMaxStringRunes)
		forward.SavedAlias = sanitizeRemoteString(forward.SavedAlias, remoteMaxStringRunes)
		record.Forward = &forward
	}
	if record.Proxy != nil {
		proxy := *record.Proxy
		proxy.Bind = sanitizeRemoteString(proxy.Bind, remoteMaxStringRunes)
		proxy.SavedAlias = sanitizeRemoteString(proxy.SavedAlias, remoteMaxStringRunes)
		record.Proxy = &proxy
	}
	return record, true
}

// safeTelemetryID mirrors state's session-id validation: the id names
// a file in the local state dir, so anything that is not a plain,
// trimmed path component is rejected (state.WriteRecord would refuse
// it anyway; rejecting here keeps the bad id out of every other sink).
func safeTelemetryID(id string) bool {
	if id == "" || id != strings.TrimSpace(id) {
		return false
	}
	if id != filepath.Base(id) || strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return false
	}
	return true
}

// sanitizeRemoteString strips control characters (C0, DEL, and the C1
// range) out of a remote-derived string and clamps it to maxRunes.
// Sanitizing at parse time means every later sink — the overlay, the
// session map, records on disk, JSON output — only ever sees clean
// values, so a remote cannot smuggle escape sequences into the
// trusted local rendering path (e.g. percent-encoded ESC in an OSC 7
// cwd).
func sanitizeRemoteString(value string, maxRunes int) string {
	var b strings.Builder
	count := 0
	for _, r := range value {
		if r < 0x20 || r == 0x7f || (0x80 <= r && r <= 0x9f) {
			continue
		}
		if maxRunes > 0 && count >= maxRunes {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

func sanitizeRemoteList(values []string, maxParts int) []string {
	if values == nil {
		return nil
	}
	if maxParts > 0 && len(values) > maxParts {
		values = values[:maxParts]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, sanitizeRemoteString(value, remoteMaxStringRunes))
	}
	return out
}

func parseOSC7(value string) (host string, cwd string, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "file://") {
		return "", "", false
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", "", false
	}
	// url.Parse percent-decodes the path, so %1b et al. would put raw
	// control bytes into RemoteCWD — which later renders inside the
	// trusted overlay. Sanitize here, at the trust boundary.
	cwd = sanitizeRemoteString(strings.TrimSpace(u.Path), remoteMaxStringRunes)
	if cwd == "" {
		return "", "", false
	}
	return sanitizeRemoteString(u.Hostname(), remoteMaxStringRunes), cwd, true
}

func parseOSC133(value string) (string, bool) {
	marker, _, _ := strings.Cut(value, ";")
	switch marker {
	case "B", "D":
		return RemotePromptPrompt, true
	case "C":
		return RemotePromptRunning, true
	case "A":
		return RemotePromptPromptStart, true
	default:
		return "", false
	}
}

func applyRemoteStateToRecord(record *state.SessionRecord, observed remoteState) bool {
	changed := false
	if observed.CWD != "" && observed.Host != record.RemoteHost {
		record.RemoteHost = observed.Host
		changed = true
	}
	if observed.CWD != "" && observed.CWD != record.RemoteCWD {
		record.RemoteCWD = observed.CWD
		changed = true
	}
	if observed.Prompt != "" && observed.Prompt != record.RemotePrompt {
		record.RemotePrompt = observed.Prompt
		changed = true
	}
	return changed
}
