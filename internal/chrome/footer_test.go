package chrome

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestFooterCanonicalGrammar(t *testing.T) {
	hints := []KeyHint{{"enter", "connect"}, {"type", "filter"}, {"q", "back"}}
	got := Footer(hints, 0)
	want := "enter connect / type filter / q back"
	if got != want {
		t.Fatalf("Footer = %q, want %q", got, want)
	}
	if strings.Contains(got, "  /  ") {
		t.Fatalf("Footer used the drifted double-space separator: %q", got)
	}
}

func TestFooterOverflowAppendsPlusN(t *testing.T) {
	hints := []KeyHint{
		{"enter", "connect"}, {"/", "filter"}, {"^", "map"},
		{"R", "refresh"}, {"?", "help"}, {"q", "back"},
	}
	got := Footer(hints, 30)
	if w := termstyle.VisibleWidth(got); w > 30 {
		t.Fatalf("Footer overflow width %d exceeds 30: %q", w, got)
	}
	if !strings.Contains(got, "+") {
		t.Fatalf("overflowing Footer should append a +N marker: %q", got)
	}
	if !strings.HasPrefix(got, "enter connect") {
		t.Fatalf("Footer should keep the head hints: %q", got)
	}
}

func TestKVRowAligns(t *testing.T) {
	theme := termstyle.TerminalTheme().WithNoColor(true)
	a := KVRow(theme, "host", "db1", 10)
	b := KVRow(theme, "proxyjump", "bastion", 10)
	// Both values start at the same column (gutter), regardless of label len.
	if strings.Index(a, "db1") != strings.Index(b, "bastion") {
		t.Fatalf("KVRow values not aligned:\n%q\n%q", a, b)
	}
}
