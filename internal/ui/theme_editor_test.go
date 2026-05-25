package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestThemeEditorViewHonorsNoColor(t *testing.T) {
	model := newThemeEditorModel(ThemeEditorOptions{
		NoAltScreen: true,
		NoColor:     true,
		ConfigPath:  "/tmp/theme.conf",
	})

	view := model.View()
	text := view.Content
	for _, want := range []string{"SSHERPA THEME BUILDER", "SCHEMA", "PREVIEW", "theme.conf", "primary"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("view contains ANSI escapes with NoColor: %q", text)
	}
}

func TestThemeEditorAcceptsRawRoleEdit(t *testing.T) {
	model := newThemeEditorModel(ThemeEditorOptions{})
	model.cursor = 2 // primary
	model.startEdit()
	model.editBuffer = "bold magenta"

	updated, _ := model.updateEdit(keyMsg("enter"))
	model = updated.(themeEditorModel)

	if model.editMode {
		t.Fatalf("editMode = true, want false")
	}
	cfg := model.config()
	if got := cfg.Specs[termstyle.RolePrimary]; got != "bold magenta" {
		t.Fatalf("primary spec = %q, want bold magenta", got)
	}
	if got := cfg.Codes[termstyle.RolePrimary]; got != "1;35" {
		t.Fatalf("primary code = %q, want 1;35", got)
	}
}

func TestThemeEditorRejectsInvalidRawRoleEdit(t *testing.T) {
	model := newThemeEditorModel(ThemeEditorOptions{})
	model.cursor = 2 // primary
	model.startEdit()
	model.editBuffer = "imaginary"

	updated, _ := model.updateEdit(keyMsg("enter"))
	model = updated.(themeEditorModel)

	if !model.editMode {
		t.Fatalf("editMode = false, want to stay editing")
	}
	if !strings.Contains(model.message, "unknown style token") {
		t.Fatalf("message = %q, want parse error", model.message)
	}
}

func TestThemeEditorCyclesBaseAndRolePresets(t *testing.T) {
	model := newThemeEditorModel(ThemeEditorOptions{})
	model.cycleBase(1)
	if model.base != "vivid" {
		t.Fatalf("base = %q, want vivid", model.base)
	}

	model.cursor = 2 // primary
	model.cycleCurrent(1)
	if got := model.values[termstyle.RolePrimary]; got != "default" {
		t.Fatalf("primary = %q, want default", got)
	}
	model.clearCurrent()
	if _, ok := model.values[termstyle.RolePrimary]; ok {
		t.Fatalf("primary override still present after clear")
	}
}

func keyMsg(value string) tea.KeyPressMsg {
	switch value {
	case "enter":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter})
	case "esc":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape})
	case "backspace":
		return tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace})
	default:
		runes := []rune(value)
		if len(runes) == 0 {
			return tea.KeyPressMsg(tea.Key{})
		}
		return tea.KeyPressMsg(tea.Key{Code: runes[0], Text: value})
	}
}
