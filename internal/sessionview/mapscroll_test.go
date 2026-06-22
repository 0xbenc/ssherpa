package sessionview

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestWindowMapBodyScrolls(t *testing.T) {
	theme := termstyle.TerminalTheme().WithNoColor(true)
	body := make([]string, 20)
	for i := range body {
		body[i] = fmt.Sprintf("line%d", i)
	}
	w0 := windowMapBody(body, 0, 6, theme)
	if len(w0) > 6 {
		t.Fatalf("window exceeds budget: %d", len(w0))
	}
	j0 := strings.Join(w0, "\n")
	if !strings.Contains(j0, "below") || strings.Contains(j0, "above") {
		t.Fatalf("scroll 0 should show only a below indicator:\n%s", j0)
	}
	wMid := windowMapBody(body, 8, 6, theme)
	jMid := strings.Join(wMid, "\n")
	if !strings.Contains(jMid, "above") || !strings.Contains(jMid, "below") {
		t.Fatalf("mid-scroll should show both indicators:\n%s", jMid)
	}
	if !strings.Contains(jMid, "line8") {
		t.Fatalf("mid-scroll should reveal a deeper line:\n%s", jMid)
	}
}

func deepRecords(n int) []state.SessionRecord {
	pid := os.Getpid()
	recs := make([]state.SessionRecord, n)
	for i := 0; i < n; i++ {
		r := state.SessionRecord{
			ID: fmt.Sprintf("n%d", i), Depth: i, TargetAlias: fmt.Sprintf("host-%02d", i),
			StartedAt: time.Unix(1_500_000_000, 0), LocalPID: pid,
		}
		if i > 0 {
			r.ParentID = fmt.Sprintf("n%d", i-1)
		}
		recs[i] = r
	}
	return recs
}

func TestMapModelScrollsNotQuits(t *testing.T) {
	m := mapModel{
		width: 80, height: 10,
		view: ViewOptions{Records: deepRecords(30), Map: MapOptions{CurrentID: "n0"}, Theme: termstyle.TerminalTheme().WithNoColor(true)},
	}
	// "j" scrolls down without quitting.
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	gm := updated.(mapModel)
	if cmd != nil {
		t.Fatal("a scroll key must not quit the map")
	}
	if gm.scroll != 1 {
		t.Fatalf("scroll = %d, want 1 after 'j'", gm.scroll)
	}
	// "q" quits.
	if _, cmd := gm.Update(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd == nil {
		t.Fatal("q should quit the map")
	}
}
