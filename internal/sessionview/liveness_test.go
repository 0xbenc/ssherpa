package sessionview

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestMapViewShowsLivenessBadges(t *testing.T) {
	rec := state.SessionRecord{
		ID: "c", Depth: 1, TargetAlias: "prod-db1",
		Route: []string{"laptop", "prod-db1"}, StartedAt: time.Unix(1_500_000_000, 0),
		LocalPID:   os.Getpid(),
		Muxer:      &state.MuxerSpec{Type: "tmux"},
		Transcript: &state.TranscriptSpec{Format: "asciicast"},
	}
	view := MapView(ViewOptions{
		Title: "ssherpa session map", StateDir: "/tmp/x",
		Records: []state.SessionRecord{rec}, Map: MapOptions{CurrentID: "c"},
		Theme: termstyle.TerminalTheme().WithNoColor(true), Width: 96, Height: 20, Help: "q",
	})
	text := view.Content
	if !strings.Contains(text, "tmux") {
		t.Fatalf("map should show tmux liveness badge:\n%s", text)
	}
	if !strings.Contains(text, "REC") {
		t.Fatalf("map should show REC badge:\n%s", text)
	}
}

func TestLivenessBadgesEmptyWithoutData(t *testing.T) {
	plain := termstyle.TerminalTheme().WithNoColor(true)
	if got := livenessBadges(state.SessionRecord{ID: "x"}, plain); got != "" {
		t.Fatalf("no muxer/recording -> no badge, got %q", got)
	}
}

// A paused transcript shows REC·paused rather than the active REC badge.
func TestLivenessBadgesPausedTranscript(t *testing.T) {
	plain := termstyle.TerminalTheme().WithNoColor(true)
	rec := state.SessionRecord{
		ID:         "c",
		Transcript: &state.TranscriptSpec{Format: "asciicast", Paused: true},
	}
	got := livenessBadges(rec, plain)
	if !strings.Contains(got, "REC·paused") {
		t.Fatalf("paused transcript should show REC·paused, got %q", got)
	}
}

// RecordedBy is provenance and is set on every session (proxies included),
// so it must not light the REC badge — only a transcript does.
func TestLivenessBadgesRecordedByDoesNotShowREC(t *testing.T) {
	plain := termstyle.TerminalTheme().WithNoColor(true)
	rec := state.SessionRecord{
		ID:         "p",
		Kind:       state.KindProxy,
		RecordedBy: &state.RecordingOrigin{MachineID: "m1", SSHerpaVersion: "test"},
	}
	if got := livenessBadges(rec, plain); strings.Contains(got, "REC") {
		t.Fatalf("RecordedBy alone must not show REC, got %q", got)
	}
}
