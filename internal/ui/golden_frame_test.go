package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

// assertBorderIntegrity is the S5 invariant: every framed line of a rendered
// screen (top/divider/bottom edges and "│ … │" body rows) has the same cell
// width, so a wide rune (CJK/emoji) can never tear the border. It derives the
// frame width from the lines themselves, so it is independent of each screen's
// own width clamp.
func assertBorderIntegrity(t *testing.T, name, content string) {
	t.Helper()
	frameWidth := -1
	for _, line := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		plain := termstyle.Strip(line)
		if plain == "" {
			continue
		}
		switch []rune(plain)[0] {
		case '╭', '╰', '├', '│':
			w := termstyle.VisibleWidth(line)
			if frameWidth == -1 {
				frameWidth = w
			} else if w != frameWidth {
				t.Errorf("%s: framed line width %d != frame width %d: %q", name, w, frameWidth, plain)
			}
		}
	}
	if frameWidth == -1 {
		t.Errorf("%s: no framed lines found", name)
	}
}

// wide-rune host names that would tear a rune-counted border.
var borderTortureAliases = []hostlist.Alias{
	{Name: "日本-east", HostName: "tokyo.example.com"},
	{Name: "🔐secrets", HostName: "vault.internal"},
	{Name: "prod-db1", HostName: "db1.internal"},
	{Name: "münchen-café", HostName: "de.internal"},
}

func TestPickerBorderIntegrity(t *testing.T) {
	for _, width := range []int{48, 56, 72, 100, 120} {
		for _, nc := range []bool{true, false} {
			m := newPickerModel(BuildItems(borderTortureAliases), PickOptions{NoAltScreen: true, NoColor: nc, Refreshable: true})
			m.width = width
			m.height = 24
			m.query = "db" // exercise the highlight path too
			m.applyFilter()
			assertBorderIntegrity(t, "picker", m.View().Content)
		}
	}
}

func TestHostChooserBorderIntegrity(t *testing.T) {
	for _, width := range []int{48, 72, 100} {
		m, err := newHostChooserModel(hostChooserItemsFromAliases(borderTortureAliases), hostChooserBaseOptions{
			NoAltScreen: true, NoColor: true, Title: "PICK HOST",
		})
		if err != nil {
			t.Fatalf("newHostChooserModel: %v", err)
		}
		m.width = width
		m.height = 24
		assertBorderIntegrity(t, "host_chooser", m.View().Content)
	}
}
