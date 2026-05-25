package termstyle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVisibleWidthIgnoresANSIEscapes(t *testing.T) {
	value := Apply(false, "1;31", "prod")

	if got := VisibleWidth(value); got != 4 {
		t.Fatalf("VisibleWidth = %d, want 4", got)
	}
	if got := Strip(value); got != "prod" {
		t.Fatalf("Strip = %q, want prod", got)
	}
}

func TestPadRightUsesVisibleWidth(t *testing.T) {
	value := Apply(false, "1;31", "ok")
	got := PadRight(value, 4)

	if VisibleWidth(got) != 4 {
		t.Fatalf("visible width = %d, want 4", VisibleWidth(got))
	}
	if Strip(got) != "ok  " {
		t.Fatalf("Strip(PadRight) = %q, want padded text", Strip(got))
	}
}

func TestTerminalThemeUsesPaletteCodes(t *testing.T) {
	theme, err := ResolveTheme(ThemeOptions{Env: []string{}, SkipDefaultFile: true})
	if err != nil {
		t.Fatalf("ResolveTheme returned error: %v", err)
	}
	got := theme.Style(RolePrimary, "prod")

	if !strings.Contains(got, "\x1b[36m") {
		t.Fatalf("primary style = %q, want terminal palette cyan", got)
	}
	if strings.Contains(got, "38;2;") {
		t.Fatalf("primary style = %q, want no truecolor in terminal theme", got)
	}
}

func TestResolveThemeParsesConfigOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.conf")
	data := []byte(`
theme = terminal
primary = magenta
secondary = bright-blue
pill = bold reverse
danger = 1;31
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write theme config: %v", err)
	}

	theme, err := ResolveTheme(ThemeOptions{File: path, Env: []string{}, SkipDefaultFile: true})
	if err != nil {
		t.Fatalf("ResolveTheme returned error: %v", err)
	}

	if got := theme.Style(RolePrimary, "prod"); !strings.Contains(got, "\x1b[35m") {
		t.Fatalf("primary style = %q, want magenta", got)
	}
	if got := theme.Style(RolePill, "mode"); !strings.Contains(got, "\x1b[1;7m") {
		t.Fatalf("pill style = %q, want bold reverse", got)
	}
}

func TestResolveThemeSupportsVividBuiltin(t *testing.T) {
	theme, err := ResolveTheme(ThemeOptions{Name: "vivid", Env: []string{}, SkipDefaultFile: true})
	if err != nil {
		t.Fatalf("ResolveTheme returned error: %v", err)
	}

	if got := theme.Style(RolePrimary, "prod"); !strings.Contains(got, "38;2;") {
		t.Fatalf("primary style = %q, want truecolor", got)
	}
}

func TestResolveThemeReportsInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.conf")
	if err := os.WriteFile(path, []byte("primary = imaginary\n"), 0o600); err != nil {
		t.Fatalf("write theme config: %v", err)
	}

	if _, err := ResolveTheme(ThemeOptions{File: path, Env: []string{}, SkipDefaultFile: true}); err == nil {
		t.Fatalf("ResolveTheme returned nil error, want invalid style error")
	}
}
