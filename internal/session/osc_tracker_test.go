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
