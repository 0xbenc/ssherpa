package session

import (
	"fmt"
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/state"
)

func TestOSCTrackerTracksCWDFromBELTerminatedOSC7(t *testing.T) {
	tracker := newOSCTracker()
	state, changed := tracker.Observe([]byte("before\x1b]7;file://prod.example.com/home/alice/project\x07after"))
	if !changed {
		t.Fatalf("Observe did not report a change")
	}
	if state.Host != "prod.example.com" || state.CWD != "/home/alice/project" {
		t.Fatalf("state = %+v, want host and cwd from OSC 7", state)
	}
}

func TestOSCTrackerTracksCWDFromSTTerminatedOSC7AcrossChunks(t *testing.T) {
	tracker := newOSCTracker()
	if _, changed := tracker.Observe([]byte("\x1b]7;file://db.internal/var/lib")); changed {
		t.Fatalf("partial OSC changed state")
	}
	state, changed := tracker.Observe([]byte("/postgres\x1b\\"))
	if !changed {
		t.Fatalf("completed OSC did not change state")
	}
	if state.Host != "db.internal" || state.CWD != "/var/lib/postgres" {
		t.Fatalf("state = %+v, want completed OSC 7 state", state)
	}
}

func TestApplyRemoteStateClearsHostWhenOSC7OmitsIt(t *testing.T) {
	record := buildRecordWithRemoteState("old.example.com", "/old", "")
	changed := applyRemoteStateToRecord(&record, remoteState{CWD: "/new"})
	if !changed {
		t.Fatalf("applyRemoteStateToRecord did not report a change")
	}
	if record.RemoteHost != "" || record.RemoteCWD != "/new" {
		t.Fatalf("record remote state = host %q cwd %q, want blank host and new cwd", record.RemoteHost, record.RemoteCWD)
	}
}

func TestOSCTrackerTracksPromptState(t *testing.T) {
	tracker := newOSCTracker()
	tests := []struct {
		input string
		want  string
	}{
		{"\x1b]133;A\x1b\\", RemotePromptPromptStart},
		{"\x1b]133;B\x1b\\", RemotePromptPrompt},
		{"\x1b]133;C\x1b\\", RemotePromptRunning},
		{"\x1b]133;D;0\x1b\\", RemotePromptPrompt},
	}
	for _, tt := range tests {
		state, changed := tracker.Observe([]byte(tt.input))
		if !changed {
			t.Fatalf("Observe(%q) did not report a change", tt.input)
		}
		if state.Prompt != tt.want {
			t.Fatalf("Observe(%q) prompt = %q, want %q", tt.input, state.Prompt, tt.want)
		}
	}
}

func TestOSCTrackerTracksSessionTelemetry(t *testing.T) {
	record := state.SessionRecord{
		ID:          "child",
		ParentID:    "parent",
		Depth:       2,
		OriginHost:  "laptop",
		TargetAlias: "prod",
		Route:       []string{"bastion", "prod"},
		StartedAt:   fixedClock()(),
	}
	payload, ok := sessionTelemetryOSC(record)
	if !ok {
		t.Fatalf("sessionTelemetryOSC returned !ok")
	}

	observed := newOSCTracker().ObserveAll(payload)
	if observed.RemoteChanged {
		t.Fatalf("session telemetry changed remote prompt/cwd state")
	}
	if len(observed.Mirrors) != 1 {
		t.Fatalf("mirrors = %#v, want one", observed.Mirrors)
	}
	got := observed.Mirrors[0]
	if got.ID != record.ID || got.ParentID != record.ParentID || got.TargetAlias != record.TargetAlias || got.OriginHost != record.OriginHost {
		t.Fatalf("mirror = %#v, want %#v", got, record)
	}
}

func TestOSCTrackerStripsFramedSessionTelemetry(t *testing.T) {
	record := state.SessionRecord{
		ID:          "child",
		ParentID:    "parent",
		TargetAlias: "prod",
		Route:       []string{"bastion", "prod"},
		StartedAt:   fixedClock()(),
	}
	payload, ok := sessionTelemetryFrame(record)
	if !ok {
		t.Fatalf("sessionTelemetryFrame returned !ok")
	}

	tracker := newOSCTracker()
	observed, clean := tracker.ObserveAndFilter(append([]byte("before"), payload[:len(payload)/2]...))
	if len(observed.Mirrors) != 0 {
		t.Fatalf("partial telemetry produced mirrors: %#v", observed.Mirrors)
	}
	if string(clean) != "before" {
		t.Fatalf("partial clean = %q, want before", string(clean))
	}
	observed, clean = tracker.ObserveAndFilter(append(payload[len(payload)/2:], []byte("after")...))
	if len(observed.Mirrors) != 1 || observed.Mirrors[0].ID != "child" {
		t.Fatalf("mirrors = %#v, want child mirror", observed.Mirrors)
	}
	if string(clean) != "after" {
		t.Fatalf("clean = %q, want after", string(clean))
	}
}

func buildRecordWithRemoteState(host string, cwd string, prompt string) state.SessionRecord {
	return state.SessionRecord{RemoteHost: host, RemoteCWD: cwd, RemotePrompt: prompt}
}

// TestParseOSC7SanitizesControlCharacters: url.Parse percent-decodes
// the OSC 7 path, so a remote could smuggle raw ESC/BEL into
// RemoteCWD/RemoteHost and replay them inside the trusted overlay.
// Control characters must be stripped at parse time.
func TestParseOSC7SanitizesControlCharacters(t *testing.T) {
	tracker := newOSCTracker()
	observed, changed := tracker.Observe([]byte("\x1b]7;file://evil.example/%1b%5b31mowned%07/home\x07"))
	if !changed {
		t.Fatalf("Observe did not report a change")
	}
	if observed.CWD != "/[31mowned/home" {
		t.Fatalf("CWD = %q, want control characters stripped", observed.CWD)
	}
	if strings.ContainsAny(observed.CWD+observed.Host, "\x1b\x07") {
		t.Fatalf("remote state contains control bytes: cwd=%q host=%q", observed.CWD, observed.Host)
	}
}

func TestTelemetryMirrorSanitizesAndClampsForgedRecords(t *testing.T) {
	longRoute := make([]string, 40)
	for i := range longRoute {
		longRoute[i] = fmt.Sprintf("hop-%d", i)
	}
	events := make([]state.SessionEvent, 70)
	for i := range events {
		events[i] = state.SessionEvent{Type: "x", Message: "m"}
	}
	forged := state.SessionRecord{
		ID:               "child",
		ParentID:         "parent",
		Depth:            1 << 20,
		TargetAlias:      "prod\x1b]0;owned\x07",
		OriginHost:       "laptop\u009b31m",
		RemoteCWD:        "/home\x1b[2J",
		DisconnectReason: "bye\x07bel",
		Route:            longRoute,
		Events:           events,
		ControlPath:      "/tmp/evil.sock",
		Transcript:       &state.TranscriptSpec{Path: "/etc/passwd", Format: "asciicast"},
		StartedAt:        fixedClock()(),
	}
	// The RS-framed variant allows the larger payload needed to carry
	// the oversized lists (the OSC variant caps at oscMaxPayload).
	payload, ok := sessionTelemetryFrame(forged)
	if !ok {
		t.Fatalf("sessionTelemetryFrame returned !ok")
	}

	observed := newOSCTracker().ObserveAll(payload)
	if len(observed.Mirrors) != 1 {
		t.Fatalf("mirrors = %#v, want one", observed.Mirrors)
	}
	got := observed.Mirrors[0]
	if got.TargetAlias != "prod]0;owned" {
		t.Fatalf("TargetAlias = %q, want control characters stripped", got.TargetAlias)
	}
	if got.OriginHost != "laptop31m" {
		t.Fatalf("OriginHost = %q, want C1 control stripped", got.OriginHost)
	}
	if strings.ContainsAny(got.RemoteCWD+got.DisconnectReason, "\x1b\x07") {
		t.Fatalf("mirror retains control bytes: %#v", got)
	}
	if len(got.Route) != 32 {
		t.Fatalf("Route entries = %d, want clamped to 32", len(got.Route))
	}
	if len(got.Events) != 64 {
		t.Fatalf("Events = %d, want clamped to 64", len(got.Events))
	}
	if got.Depth != 64 {
		t.Fatalf("Depth = %d, want clamped to 64", got.Depth)
	}
	if got.ControlPath != "" || got.Transcript != nil {
		t.Fatalf("machine-local references survived: ControlPath=%q Transcript=%#v", got.ControlPath, got.Transcript)
	}
}

func TestTelemetryMirrorClampsOverlongStringsAndNegativeDepth(t *testing.T) {
	forged := state.SessionRecord{
		ID:          "child",
		ParentID:    "parent",
		Depth:       -3,
		TargetAlias: strings.Repeat("a", 4096),
		StartedAt:   fixedClock()(),
	}
	payload, ok := sessionTelemetryFrame(forged)
	if !ok {
		t.Fatalf("sessionTelemetryFrame returned !ok")
	}
	observed := newOSCTracker().ObserveAll(payload)
	if len(observed.Mirrors) != 1 {
		t.Fatalf("mirrors = %#v, want one", observed.Mirrors)
	}
	got := observed.Mirrors[0]
	if len(got.TargetAlias) != 512 {
		t.Fatalf("TargetAlias length = %d, want clamped to 512", len(got.TargetAlias))
	}
	if got.Depth != 0 {
		t.Fatalf("Depth = %d, want negative depth clamped to 0", got.Depth)
	}
}

func TestTelemetryMirrorRejectsUnsafeSessionIDs(t *testing.T) {
	for _, id := range []string{"../../evil", "a/b", `a\b`, "..", " padded "} {
		forged := state.SessionRecord{ID: id, ParentID: "parent", StartedAt: fixedClock()()}
		payload, ok := sessionTelemetryOSC(forged)
		if !ok {
			t.Fatalf("sessionTelemetryOSC(%q) returned !ok", id)
		}
		observed := newOSCTracker().ObserveAll(payload)
		if len(observed.Mirrors) != 0 {
			t.Fatalf("id %q produced a mirror: %#v", id, observed.Mirrors)
		}
	}
}

func TestOSCTrackerIgnoresUnknownAndDuplicateSequences(t *testing.T) {
	tracker := newOSCTracker()
	if _, changed := tracker.Observe([]byte("\x1b]0;window title\x07")); changed {
		t.Fatalf("unknown OSC changed state")
	}
	state, changed := tracker.Observe([]byte("\x1b]133;C\x07"))
	if !changed || state.Prompt != RemotePromptRunning {
		t.Fatalf("first prompt state = %+v changed=%v, want running change", state, changed)
	}
	if _, changed := tracker.Observe([]byte("\x1b]133;C\x07")); changed {
		t.Fatalf("duplicate prompt state reported as changed")
	}
}
