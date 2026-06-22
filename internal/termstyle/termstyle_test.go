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

func TestStripAndVisibleWidthCSISequences(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		width int // 0 -> default len([]rune(want)); set where cell width differs
	}{
		{"sgr", "\x1b[1;31mred\x1b[0m", "red", 0},
		{"ich at-final", "abc\x1b[1@123 def", "abc123 def", 0},
		{"hpa backtick-final", "a\x1b[5`99,99 b", "a99,99 b", 0},
		{"keypad tilde-final", "x\x1b[5~y", "xy", 0},
		{"brace finals", "\x1b[3{a\x1b[4}b\x1b[5|c", "abc", 0},
		{"cjk after non-letter final", "\x1b[1@日本語x", "日本語x", 7},
		{"cursor style intermediate", "\x1b[2 qhello", "hello", 0},
		{"private params", "\x1b[?25hshown", "shown", 0},
		{"mixed text", "one\x1b[32mtwo\x1b[0mthree", "onetwothree", 0},
		{"truncated params at end", "abc\x1b[12", "abc", 0},
		{"bare introducer at end", "abc\x1b[", "abc", 0},
		{"malformed interior abort", "\x1b[12\x1b[31mxyz", "xyz", 0},
		{"embedded c0 control continues sequence", "a\x1b[31\x08mb", "ab", 0},
		{"embedded esc aborts sequence", "a\x1b[31\x1b[32mb", "ab", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Strip(tc.input); got != tc.want {
				t.Fatalf("Strip(%q) = %q, want %q", tc.input, got, tc.want)
			}
			wantWidth := tc.width
			if wantWidth == 0 {
				wantWidth = len([]rune(tc.want))
			}
			if got := VisibleWidth(tc.input); got != wantWidth {
				t.Fatalf("VisibleWidth(%q) = %d, want %d", tc.input, got, wantWidth)
			}
		})
	}
}

func TestStripAndVisibleWidthNonCSISequences(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
		width int // 0 -> default len([]rune(want))
	}{
		{"keypad modes", "a\x1b=b\x1b>c", "abc", 0},
		{"ris", "\x1bcx", "x", 0},
		{"save restore cursor", "\x1b7hi\x1b8", "hi", 0},
		{"charset designations", "\x1b(Bhello\x1b)0", "hello", 0},
		{"decaln", "\x1b#8x", "x", 0},
		{"ss3 key", "\x1bOPdone", "done", 0},
		{"ss2 shifted char", "a\x1bNxb", "ab", 0},
		{"ss3 multibyte rune not split", "\x1bO日x", "x", 0},
		{"osc bel", "\x1b]0;title\x07text", "text", 0},
		{"osc st", "\x1b]0;title\x1b\\text", "text", 0},
		{"osc unterminated", "\x1b]0;title", "", 0},
		{"osc aborted by csi", "\x1b]0;ti\x1b[31mred", "red", 0},
		{"dcs st", "\x1bPq#0;2;0;0;0\x1b\\after", "after", 0},
		{"dcs ignores bel", "\x1bPdata\x07more\x1b\\end", "end", 0},
		{"dcs unterminated", "\x1bPdata", "", 0},
		{"sos", "\x1bXpayload\x1b\\a", "a", 0},
		{"pm", "\x1b^payload\x1b\\b", "b", 0},
		{"apc", "\x1b_payload\x1b\\c", "c", 0},
		{"bare esc at end", "abc\x1b", "abc", 0},
		{"esc before control byte", "\x1b\x01x", "\x01x", 1},
		{"ss3 at end", "abc\x1bO", "abc", 0},
		{"charset truncated at end", "abc\x1b(", "abc", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Strip(tc.input); got != tc.want {
				t.Fatalf("Strip(%q) = %q, want %q", tc.input, got, tc.want)
			}
			wantWidth := tc.width
			if wantWidth == 0 {
				wantWidth = len([]rune(tc.want))
			}
			if got := VisibleWidth(tc.input); got != wantWidth {
				t.Fatalf("VisibleWidth(%q) = %d, want %d", tc.input, got, wantWidth)
			}
		})
	}
}

// TestBoxGlyphsStayWidthOne guards the locale landmine: the box/rule glyphs the
// chrome draws must measure as a single cell so the borders can never tear.
// The measure honors only RUNEWIDTH_EASTASIAN (not LANG), so this holds even
// under a CJK locale.
func TestTruncateWithMarker(t *testing.T) {
	if got := TruncateWith("hello world", 6, "…"); got != "hello…" {
		t.Fatalf("TruncateWith = %q, want hello…", got)
	}
	if w := VisibleWidth(TruncateWith("hello world", 6, "…")); w != 6 {
		t.Fatalf("TruncateWith width = %d, want 6", w)
	}
	if got := TruncateWith("日本語表記", 3, "…"); VisibleWidth(got) > 3 {
		t.Fatalf("TruncateWith wide = %q exceeds 3 cells", got)
	}
}

func TestBoxGlyphsStayWidthOne(t *testing.T) {
	for _, g := range []string{"╭", "╮", "╰", "╯", "├", "┤", "─", "│"} {
		if got := VisibleWidth(g); got != 1 {
			t.Errorf("VisibleWidth(%q) = %d, want 1 (box glyph must be one cell)", g, got)
		}
	}
}

func TestVisibleWidthCellAccurate(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{
		{"abc", 3},
		{"日本語", 6},
		{"a日b", 4},
		{"🔐", 2},
		{Apply(false, "1;31", "日本"), 4},
		{"╭───╮", 5},
	} {
		if got := VisibleWidth(tc.in); got != tc.want {
			t.Errorf("VisibleWidth(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestSanitizeDropsRawControls(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"plain passthrough", "hello world", "hello world"},
		{"tab kept", "a\tb", "a\tb"},
		{"escape sequence stripped", "a\x1b[31mb", "ab"},
		{"carriage return dropped", "real\rSPOOF", "realSPOOF"},
		{"newline dropped", "one\ntwo", "onetwo"},
		{"bel dropped", "ding\x07dong", "dingdong"},
		{"backspace dropped", "ab\x08c", "abc"},
		{"del dropped", "ab\x7fc", "abc"},
		{"c1 csi dropped", "prod\u009b31mEVIL", "prod31mEVIL"},
		{"c1 osc dropped", "x\u009d0;t\u009cy", "x0;ty"},
		{"multibyte preserved", "日本語", "日本語"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sanitize(tc.input); got != tc.want {
				t.Fatalf("Sanitize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		name  string
		input string
		width int
		want  string
	}{
		{"zero width", "hello", 0, ""},
		{"negative width", "hello", -3, ""},
		{"fits", "hello", 8, "hello"},
		{"exact", "hello", 5, "hello"},
		{"plain cut", "hello world", 8, "hello w~"},
		{"width one", "hello", 1, "h"},
		{"empty", "", 4, ""},
		{"multibyte cut", "日本語表記", 3, "日~"},
		{"wide rune not split", "日本語", 4, "日~"},
		{"styled fits", "\x1b[1;36mhi\x1b[0m", 5, "\x1b[1;36mhi\x1b[0m"},
		{"styled cut appends reset", "\x1b[1;36mhello world\x1b[0m", 8, "\x1b[1;36mhello w~\x1b[0m"},
		{"styled width one", "\x1b[1;36mhello\x1b[0m", 1, "\x1b[1;36mh\x1b[0m"},
		{"reset already kept", "\x1b[31mab\x1b[0mcdefgh", 4, "\x1b[31mab\x1b[0mc~"},
		{"never splits sequence", "\x1b[38;5;196mxy0123456\x1b[0m", 3, "\x1b[38;5;196mxy~\x1b[0m"},
		{"non sgr escape kept intact", "ab\x1b[2Kcdefghij", 4, "ab\x1b[2Kc~"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Truncate(tc.input, tc.width)
			if got != tc.want {
				t.Fatalf("Truncate(%q, %d) = %q, want %q", tc.input, tc.width, got, tc.want)
			}
			if tc.width >= 0 && VisibleWidth(got) > tc.width {
				t.Fatalf("VisibleWidth(Truncate(%q, %d)) = %d, exceeds width", tc.input, tc.width, VisibleWidth(got))
			}
		})
	}
}

func TestResolveThemeNoColorEnv(t *testing.T) {
	cases := []struct {
		name string
		opts ThemeOptions
		want bool
	}{
		{"default colored", ThemeOptions{Env: []string{}, SkipDefaultFile: true}, false},
		{"option flag", ThemeOptions{NoColor: true, Env: []string{}, SkipDefaultFile: true}, true},
		{"NO_COLOR any value", ThemeOptions{Env: []string{"NO_COLOR=1"}, SkipDefaultFile: true}, true},
		{"NO_COLOR empty ignored", ThemeOptions{Env: []string{"NO_COLOR="}, SkipDefaultFile: true}, false},
		{"SSHERPA_NO_COLOR truthy", ThemeOptions{Env: []string{"SSHERPA_NO_COLOR=true"}, SkipDefaultFile: true}, true},
		{"SSHERPA_NO_COLOR falsy", ThemeOptions{Env: []string{"SSHERPA_NO_COLOR=0"}, SkipDefaultFile: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			theme, err := ResolveTheme(tc.opts)
			if err != nil {
				t.Fatalf("ResolveTheme returned error: %v", err)
			}
			if theme.NoColor != tc.want {
				t.Fatalf("NoColor = %v, want %v", theme.NoColor, tc.want)
			}
			styled := theme.Style(RolePrimary, "prod")
			if tc.want && styled != "prod" {
				t.Fatalf("Style with NoColor = %q, want plain text", styled)
			}
			if !tc.want && !strings.Contains(styled, "\x1b[") {
				t.Fatalf("Style without NoColor = %q, want escape codes", styled)
			}
		})
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

func TestDefaultThemeUsesPaletteCodes(t *testing.T) {
	theme, err := ResolveTheme(ThemeOptions{Env: []string{}, SkipDefaultFile: true})
	if err != nil {
		t.Fatalf("ResolveTheme returned error: %v", err)
	}
	got := theme.Style(RolePrimary, "prod")

	if !strings.Contains(got, "\x1b[36m") {
		t.Fatalf("primary style = %q, want terminal palette cyan", got)
	}
	if strings.Contains(got, "38;2;") {
		t.Fatalf("primary style = %q, want no truecolor in default theme", got)
	}
	if got := theme.Style(RoleSelected, "prod"); !strings.Contains(got, "\x1b[39;4m") {
		t.Fatalf("selected style = %q, want foreground underline", got)
	}
}

func TestResolveThemeParsesConfigOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.conf")
	data := []byte(`
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

	cfg, err := ParseThemeConfig(data)
	if err != nil {
		t.Fatalf("ParseThemeConfig returned error: %v", err)
	}
	if got := cfg.Specs[RolePill]; got != "bold reverse" {
		t.Fatalf("pill spec = %q, want bold reverse", got)
	}
}

func TestResolveThemeIgnoresDeprecatedThemeName(t *testing.T) {
	theme, err := ResolveTheme(ThemeOptions{Name: "vivid", Env: []string{}, SkipDefaultFile: true})
	if err != nil {
		t.Fatalf("ResolveTheme returned error: %v", err)
	}

	if got := theme.Style(RolePrimary, "prod"); !strings.Contains(got, "\x1b[36m") {
		t.Fatalf("primary style = %q, want default palette cyan", got)
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

// TestParseThemeConfigToleratesUnknownRoleKeys is the WP10
// forward-compatibility contract: a theme.conf written by a newer
// ssherpa (with role keys this binary does not know) must parse with
// warnings, not hard-fail, while known roles still apply.
func TestParseThemeConfigToleratesUnknownRoleKeys(t *testing.T) {
	data := []byte(`
primary = magenta
hyperlink = bright-blue
sparkline-glow = bold cyan
danger = 1;31
`)

	cfg, err := ParseThemeConfig(data)
	if err != nil {
		t.Fatalf("ParseThemeConfig returned error: %v", err)
	}
	if got := cfg.Specs[RolePrimary]; got != "magenta" {
		t.Fatalf("primary spec = %q, want magenta", got)
	}
	if got := cfg.Specs[RoleDanger]; got != "1;31" {
		t.Fatalf("danger spec = %q, want 1;31", got)
	}
	if len(cfg.Warnings) != 2 {
		t.Fatalf("Warnings = %#v, want 2 entries", cfg.Warnings)
	}
	if !strings.Contains(cfg.Warnings[0], `unknown theme role "hyperlink"`) {
		t.Fatalf("Warnings[0] = %q, want unknown hyperlink warning", cfg.Warnings[0])
	}
	if !strings.Contains(cfg.Warnings[1], "line 4") || !strings.Contains(cfg.Warnings[1], "sparkline_glow") {
		t.Fatalf("Warnings[1] = %q, want line 4 sparkline_glow warning", cfg.Warnings[1])
	}
}

// TestParseThemeConfigStillRejectsMalformedLines pins what stays a hard
// error after unknown keys became warnings: lines without an
// assignment, and invalid style values for KNOWN roles.
func TestParseThemeConfigStillRejectsMalformedLines(t *testing.T) {
	cases := []struct {
		name string
		data string
	}{
		{"no assignment", "primary magenta\n"},
		{"bad value for known role", "primary = imaginary\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseThemeConfig([]byte(c.data)); err == nil {
				t.Fatalf("ParseThemeConfig(%q) returned nil error", c.data)
			}
		})
	}
}

// TestParseThemeConfigAcceptsBaseKeySilently pins the legacy
// `theme`/`base` key: parsed into BaseName, no warning, no error.
func TestParseThemeConfigAcceptsBaseKeySilently(t *testing.T) {
	cfg, err := ParseThemeConfig([]byte("theme = vivid\nbase = terminal\n"))
	if err != nil {
		t.Fatalf("ParseThemeConfig returned error: %v", err)
	}
	if cfg.BaseName != "terminal" {
		t.Fatalf("BaseName = %q, want terminal (last assignment wins)", cfg.BaseName)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("Warnings = %#v, want none", cfg.Warnings)
	}
}

// TestResolveThemeToleratesUnknownRoleKeys drives the same contract
// through ResolveTheme: the theme loads and known overrides apply even
// when the file carries future role keys.
func TestResolveThemeToleratesUnknownRoleKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.conf")
	data := []byte("primary = magenta\nhyperlink = bright-blue\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write theme config: %v", err)
	}

	theme, err := ResolveTheme(ThemeOptions{File: path, Env: []string{}, SkipDefaultFile: true})
	if err != nil {
		t.Fatalf("ResolveTheme returned error: %v", err)
	}
	if got := theme.Style(RolePrimary, "prod"); !strings.Contains(got, "\x1b[35m") {
		t.Fatalf("primary style = %q, want magenta override applied", got)
	}
}
