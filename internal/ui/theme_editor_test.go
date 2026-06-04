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
	for _, want := range []string{"╭ SSHERPA THEME BUILDER", "Config", "Contrast", "SCHEMA", "PREVIEW", "theme.conf", "primary", "s save"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("view contains ANSI escapes with NoColor: %q", text)
	}
}

func TestThemeEditorViewStaysInsideNarrowFrame(t *testing.T) {
	model := newThemeEditorModel(ThemeEditorOptions{
		NoAltScreen: true,
		NoColor:     true,
		ConfigPath:  "/tmp/very/long/path/to/ssherpa/theme/config/file/theme.conf",
		Warning:     "existing theme config did not parse; saving will replace it",
	})
	model.width = 52
	model.height = 18

	text := model.View().Content
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if got := termstyle.VisibleWidth(line); got > 52 {
			t.Fatalf("line width = %d, want <= 52: %q\n%s", got, line, text)
		}
	}
	for _, want := range []string{"SSHERPA THEME", "Warning", "SCHEMA", "PREVIEW"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}

func TestThemeEditorAcceptsRawRoleEdit(t *testing.T) {
	model := newThemeEditorModel(ThemeEditorOptions{})
	model.cursor = 1 // primary
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
	model.cursor = 1 // primary
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

func TestThemeEditorCyclesRolePresets(t *testing.T) {
	model := newThemeEditorModel(ThemeEditorOptions{})
	model.cursor = 1 // primary
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
