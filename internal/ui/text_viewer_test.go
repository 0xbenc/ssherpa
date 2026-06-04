package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestTextViewRendersWrappedDetails(t *testing.T) {
	model := newTextViewModel(TextViewOptions{
		NoAltScreen: true,
		Title:       "authorized_keys entry",
		Steps:       []string{"action", "key", "details"},
		CurrentStep: 2,
		Summary:     "/home/test/.ssh/authorized_keys  1 key",
		Lines: []string{
			"Fingerprint: SHA256:abc123",
			"Authorized key:",
			"ssh-ed25519 " + strings.Repeat("A", 90) + " alice@example",
		},
	}, termstyle.TerminalTheme().WithNoColor(true))
	model.width = 64
	model.height = 18

	view := model.View()
	text := view.Content
	for _, want := range []string{
		"authorized_keys entry",
		"action",
		"details",
		"Summary",
		"1 key",
		"DETAILS",
		"Fingerprint: SHA256:abc123",
		"Authorized key:",
		"up/down scroll",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if got := termstyle.VisibleWidth(line); got > 64 {
			t.Fatalf("line width = %d, want <= 64: %q\n%s", got, line, text)
		}
	}
}

func TestTextViewScrollsAndQuits(t *testing.T) {
	model := newTextViewModel(TextViewOptions{
		NoAltScreen: true,
		Lines:       []string{"one", "two", "three", "four", "five", "six"},
	}, termstyle.TerminalTheme().WithNoColor(true))
	model.width = 72
	model.height = 10

	updated, cmd := model.Update(keyPress(tea.KeyDown, ""))
	model = updated.(textViewModel)
	if cmd != nil {
		t.Fatalf("down returned unexpected command")
	}
	if model.scroll != 1 {
		t.Fatalf("scroll = %d, want 1", model.scroll)
	}

	updated, cmd = model.Update(keyPress(tea.KeyEnter, ""))
	model = updated.(textViewModel)
	if cmd == nil {
		t.Fatalf("enter did not return quit command")
	}
}
