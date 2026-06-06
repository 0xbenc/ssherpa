package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestConfirmDefaultsToYes(t *testing.T) {
	m := newConfirmModel(ConfirmOptions{
		NoAltScreen: true,
		Title:       "Add SSH alias",
		Message:     "prod to 1 file",
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

func TestConfirmDeleteDefaultsToNo(t *testing.T) {
	m := newConfirmModel(ConfirmOptions{
		NoAltScreen: true,
		Title:       "Delete SSH alias",
		Message:     "prod from 1 file",
		Danger:      true,
	}, termstyle.Theme{})

	view := m.View().Content
	if !strings.Contains(view, "[ No ]") {
		t.Fatalf("default no button not selected:\n%s", view)
	}
	if !strings.Contains(view, "  Yes  ") {
		t.Fatalf("yes button missing:\n%s", view)
	}

	m = updateConfirm(m, keyPress(tea.KeyEnter, ""))
	if !m.answered || m.selectedYes || m.canceled {
		t.Fatalf("model = %+v, want answered no", m)
	}
}

func TestConfirmDeleteCanChooseYes(t *testing.T) {
	m := newConfirmModel(ConfirmOptions{NoAltScreen: true, Danger: true}, termstyle.Theme{})
	m = updateConfirm(m, keyPress(tea.KeyLeft, ""))
	if !m.selectedYes {
		t.Fatalf("left key should select yes")
	}
	view := m.View().Content
	if !strings.Contains(view, "[ Yes ]") {
		t.Fatalf("yes button not selected:\n%s", view)
	}

	m = updateConfirm(m, keyPress(tea.KeyEnter, ""))
	if !m.answered || !m.selectedYes || m.canceled {
		t.Fatalf("model = %+v, want answered yes", m)
	}
}

func updateConfirm(m confirmModel, msgs ...tea.Msg) confirmModel {
	for _, msg := range msgs {
		newModel, _ := m.Update(msg)
		m = newModel.(confirmModel)
	}
	return m
}
