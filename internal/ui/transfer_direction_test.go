package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func newTestTransferDirection() transferDirectionModel {
	return transferDirectionModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		width:       88,
		height:      18,
	}
}

func updateTransferDirection(m transferDirectionModel, msgs ...tea.Msg) transferDirectionModel {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(transferDirectionModel)
	}
	return m
}

func TestTransferDirectionViewRendersFramedChooser(t *testing.T) {
	m := newTestTransferDirection()

	view := m.View()
	text := view.Content
	for _, want := range []string{
		"SSHERPA FILE TRANSFER",
		"● direction",
		"○ source",
		"MODE",
		"choose direction",
		"DIRECTION",
		"[SEND]",
		"Send file",
		"local -> remote",
		"[RECV]",
		"Receive file",
		"remote -> local",
		"enter select",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("view contains ANSI escapes with NoColor:\n%s", text)
	}
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
}

func TestTransferDirectionKeyboardSelection(t *testing.T) {
	m := newTestTransferDirection()

	m = updateTransferDirection(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyEnter, ""))
	if m.selected != TransferDirectionReceive {
		t.Fatalf("selected = %q, want receive", m.selected)
	}

	m = newTestTransferDirection()
	m = updateTransferDirection(m, keyPress('r', "r"))
	if m.selected != TransferDirectionReceive {
		t.Fatalf("shortcut selected = %q, want receive", m.selected)
	}

	m = newTestTransferDirection()
	m = updateTransferDirection(m, keyPress('s', "s"))
	if m.selected != TransferDirectionSend {
		t.Fatalf("shortcut selected = %q, want send", m.selected)
	}
}

func TestTransferDirectionCancel(t *testing.T) {
	m := newTestTransferDirection()

	m = updateTransferDirection(m, keyPress('q', "q"))

	if !m.canceled {
		t.Fatalf("canceled = false, want true")
	}
	if m.selected != "" {
		t.Fatalf("selected = %q, want empty", m.selected)
	}
}

func TestTransferDirectionViewStaysInsideNarrowFrame(t *testing.T) {
	m := newTestTransferDirection()
	m.width = 60

	text := m.View().Content
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if got := termstyle.VisibleWidth(line); got > 60 {
			t.Fatalf("line width = %d, want <= 60: %q\n%s", got, line, text)
		}
	}
	for _, want := range []string{"SSHERPA FILE TRANSFER", "[SEND]", "[RECV]"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}
