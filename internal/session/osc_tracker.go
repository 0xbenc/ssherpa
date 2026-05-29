package session

import (
	"net/url"
	"strings"

	"github.com/0xbenc/ssherpa/internal/state"
)

const (
	RemotePromptPrompt      = state.RemotePromptPrompt
	RemotePromptRunning     = state.RemotePromptRunning
	RemotePromptPromptStart = state.RemotePromptPromptStart

	oscMaxPayload = 8192
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
	HostSet   bool
	CWDSet    bool
	PromptSet bool
}

type oscTracker struct {
	state int
	buf   []byte
	last  remoteState
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
	changed := false
	for _, b := range data {
		if update, ok := t.feed(b); ok && t.apply(update) {
			changed = true
		}
	}
	return t.last, changed
}

func (t *oscTracker) feed(b byte) (remoteUpdate, bool) {
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
	default:
		return remoteUpdate{}, false
	}
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
	cwd = strings.TrimSpace(u.Path)
	if cwd == "" {
		return "", "", false
	}
	return u.Hostname(), cwd, true
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
