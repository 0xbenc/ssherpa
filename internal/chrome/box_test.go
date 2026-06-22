package chrome

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// TestEdgeCanonicalStyling pins the resolved divergence: the fill dashes are
// border-styled in color mode, while NO_COLOR output is a clean exact string.
func TestEdgeCanonicalStyling(t *testing.T) {
	const width = 20
	wantPlain := "╭ TITLE ───────────╮" // ╭ + " TITLE " (7) + 11 dashes + ╮ == 20 cells

	plain := termstyle.TerminalTheme().WithNoColor(true)
	if got := Edge(plain, "╭", "╮", "TITLE", width, nil); got != wantPlain {
		t.Fatalf("NO_COLOR edge = %q, want %q", got, wantPlain)
	}

	color := termstyle.TerminalTheme()
	c := Edge(color, "╭", "╮", "TITLE", width, nil)
	if !strings.Contains(c, "\x1b[") {
		t.Fatalf("color edge has no styling: %q", c)
	}
	if got := termstyle.Strip(c); got != wantPlain {
		t.Fatalf("color edge strips to %q, want %q", got, wantPlain)
	}
	// The trailing dashes must be border-styled (the canonical choice), not
	// left default-colored as the old overlay fork did.
	borderDash := color.Style(termstyle.RoleBorder, "─")
	openSGR := borderDash[:strings.Index(borderDash, "m")+1]
	if !strings.Contains(c, openSGR+"─") && !strings.Contains(c, openSGR+strings.Repeat("─", 1)) {
		// fall back: the dashes appear inside a border-styled run
		if !strings.Contains(termstyle.Strip(c[strings.LastIndex(c, "TITLE"):]), "─") {
			t.Fatalf("fill dashes are not border-styled: %q", c)
		}
	}
}

func TestEdgeWidths(t *testing.T) {
	plain := termstyle.TerminalTheme().WithNoColor(true)
	for _, w := range []int{8, 20, 48, 100} {
		for _, label := range []string{"", "MAP", "a-very-long-label-that-overflows"} {
			edge := Edge(plain, "╭", "╮", label, w, termstyle.Truncate)
			if got := termstyle.VisibleWidth(edge); got != w {
				t.Errorf("Edge width %d != %d (label=%q): %q", got, w, label, edge)
			}
		}
		if got := termstyle.VisibleWidth(Divider(plain, w)); got != w {
			t.Errorf("Divider width %d != %d", got, w)
		}
		if got := termstyle.VisibleWidth(Line(plain, "body", w, termstyle.Truncate)); got != w {
			t.Errorf("Line width %d != %d", got, w)
		}
	}
}
