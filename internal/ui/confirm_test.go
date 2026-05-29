package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestConfirmDeleteDefaultsToYes(t *testing.T) {
	m := newConfirmModel(ConfirmOptions{
		NoAltScreen: true,
		Title:       "Delete SSH alias",
		Message:     "prod from 1 file",
	}, termstyle.Theme{})

	view := m.View()
	text := view.Content
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
	if !strings.Contains(text, "[ Yes ]") {
		t.Fatalf("default yes button not selected:\n%s", text)
	}
	if !strings.Contains(text, "  No  ") {
		t.Fatalf("no button missing:\n%s", text)
	}

	m = updateConfirm(m, keyPress(tea.KeyEnter, ""))
	if !m.answered || !m.selectedYes || m.canceled {
		t.Fatalf("model = %+v, want answered yes", m)
	}
}

func TestConfirmDeleteCanChooseNo(t *testing.T) {
	m := newConfirmModel(ConfirmOptions{NoAltScreen: true}, termstyle.Theme{})
	m = updateConfirm(m, keyPress(tea.KeyRight, ""))
	if m.selectedYes {
		t.Fatalf("right key should select no")
	}
	view := m.View().Content
	if !strings.Contains(view, "[ No ]") {
		t.Fatalf("no button not selected:\n%s", view)
	}

	m = updateConfirm(m, keyPress(tea.KeyEnter, ""))
	if !m.answered || m.selectedYes || m.canceled {
		t.Fatalf("model = %+v, want answered no", m)
	}
}

func updateConfirm(m confirmModel, msgs ...tea.Msg) confirmModel {
	for _, msg := range msgs {
		newModel, _ := m.Update(msg)
		m = newModel.(confirmModel)
	}
	return m
}
