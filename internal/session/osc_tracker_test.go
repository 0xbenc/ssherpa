package session

import (
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
