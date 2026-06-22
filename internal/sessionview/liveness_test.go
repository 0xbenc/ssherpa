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
		RecordedBy: &state.RecordingOrigin{},
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
