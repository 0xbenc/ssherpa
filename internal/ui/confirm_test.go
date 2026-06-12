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

func TestWrapConfirmTextHonorsExplicitNewlines(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  []string
	}{
		{
			name:  "entries stay line-per-entry",
			value: "Delete 2 keys?\nssh-ed25519 AAAA alice@host\nssh-ed25519 BBBB bob@host",
			width: 40,
			want:  []string{"Delete 2 keys?", "ssh-ed25519 AAAA alice@host", "ssh-ed25519 BBBB bob@host"},
		},
		{
			name:  "blank separator lines survive",
			value: "Import bundle?\n\nTarget: prod\nRoute: here -> prod",
			width: 40,
			want:  []string{"Import bundle?", "", "Target: prod", "Route: here -> prod"},
		},
		{
			name:  "long lines still word wrap",
			value: "first second\nthird fourth fifth",
			width: 6,
			want:  []string{"first", "second", "third", "fourth", "fifth"},
		},
		{
			name:  "all blank input renders nothing",
			value: " \n ",
			width: 10,
			want:  nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := wrapConfirmText(tt.value, tt.width)
			if len(got) != len(tt.want) {
				t.Fatalf("wrapConfirmText = %#v, want %#v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("line %d = %q, want %q (all: %#v)", i, got[i], tt.want[i], got)
				}
			}
		})
	}
}

func TestConfirmViewKeepsFrameWidthWithMultilineMessage(t *testing.T) {
	m := newConfirmModel(ConfirmOptions{
		NoAltScreen: true,
		Title:       "Confirm transcript import",
		Message:     "Import bundle?\n\nTarget: prod\nRoute: here -> bastion -> prod\nSource session: 20260604T120000.000000000Z-bundle",
		Danger:      true,
	}, termstyle.Theme{})
	m.width = 72

	lines := strings.Split(strings.TrimRight(m.View().Content, "\n"), "\n")
	if len(lines) < 8 {
		t.Fatalf("expected a multi-line frame, got %d lines:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	width := termstyle.VisibleWidth(lines[0])
	for i, line := range lines {
		if got := termstyle.VisibleWidth(line); got != width {
			t.Fatalf("line %d width = %d, want %d:\n%s", i, got, width, strings.Join(lines, "\n"))
		}
	}
	content := m.View().Content
	for _, want := range []string{"Target: prod", "Route: here -> bastion -> prod"} {
		if !strings.Contains(content, want) {
			t.Fatalf("confirm view missing %q on its own line:\n%s", want, content)
		}
	}
}
