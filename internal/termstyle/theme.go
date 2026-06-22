package termstyle

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Role string

const (
	RoleTitle      Role = "title"
	RolePrimary    Role = "primary"
	RoleSecondary  Role = "secondary"
	RoleAccent     Role = "accent"
	RoleMuted      Role = "muted"
	RoleSubtle     Role = "subtle"
	RoleForeground Role = "foreground"
	RoleSelected   Role = "selected"
	RoleBorder     Role = "border"
	RoleSuccess    Role = "success"
	RoleWarning    Role = "warning"
	RoleDanger     Role = "danger"
	RoleInfo       Role = "info"
	RoleSearch     Role = "search"
	RolePill       Role = "pill"
)

type Theme struct {
	Name    string
	NoColor bool
	Codes   map[Role]string
}

type ThemeOptions struct {
	Name            string
	File            string
	NoColor         bool
	Env             []string
	SkipDefaultFile bool
}

type ThemeConfig struct {
	BaseName string
	Codes    map[Role]string
	Specs    map[Role]string
	// Warnings collects non-fatal parse diagnostics, such as role keys
	// this binary does not know about. Unknown keys are tolerated so a
	// theme.conf written by a newer ssherpa never hard-fails an older
	// binary; callers decide where to surface the warnings.
	Warnings []string
}

func TerminalTheme() Theme {
	return Theme{
		Name: "terminal",
		Codes: map[Role]string{
			RoleTitle:      "1;36",
			RolePrimary:    "36",
			RoleSecondary:  "34",
			RoleAccent:     "33",
			RoleMuted:      "90",
			RoleSubtle:     "2",
			RoleForeground: "39",
			RoleSelected:   "39;4",
			RoleBorder:     "90",
			RoleSuccess:    "32",
			RoleWarning:    "33",
			RoleDanger:     "31",
			RoleInfo:       "35",
			RoleSearch:     "1;39",
			RolePill:       "1;7",
		},
	}
}

func VividTheme() Theme {
	return Theme{
		Name: "vivid",
		Codes: map[Role]string{
			RoleTitle:      "1;38;2;96;221;255",
			RolePrimary:    "1;38;2;96;221;255",
			RoleSecondary:  "1;38;2;120;183;255",
			RoleAccent:     "1;38;2;255;209;102",
			RoleMuted:      "38;2;132;145;160",
			RoleSubtle:     "38;2;58;69;87",
			RoleForeground: "38;2;235;239;245",
			RoleSelected:   "1;38;2;255;255;255",
			RoleBorder:     "38;2;58;69;87",
			RoleSuccess:    "1;38;2;134;239;172",
			RoleWarning:    "1;38;2;255;209;102",
			RoleDanger:     "1;38;2;255;151;112",
			RoleInfo:       "1;38;2;214;160;255",
			RoleSearch:     "1;38;2;255;255;255",
			RolePill:       "1;38;2;25;30;38;48;2;96;221;255",
		},
	}
}

func Roles() []Role {
	return []Role{
		RoleTitle,
		RolePrimary,
		RoleSecondary,
		RoleAccent,
		RoleMuted,
		RoleSubtle,
		RoleForeground,
		RoleSelected,
		RoleBorder,
		RoleSuccess,
		RoleWarning,
		RoleDanger,
		RoleInfo,
		RoleSearch,
		RolePill,
	}
}

func BuiltinTheme(name string) (Theme, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "terminal", "default", "auto":
		return TerminalTheme(), true
	case "vivid", "rgb", "truecolor":
		return VividTheme(), true
	default:
		return Theme{}, false
	}
}

func ResolveTheme(opts ThemeOptions) (Theme, error) {
	env := themeEnv(opts.Env)
	file, explicitFile := resolveThemeFile(opts.File, env, opts.SkipDefaultFile)

	var cfg ThemeConfig
	if file != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			if explicitFile || !errors.Is(err, fs.ErrNotExist) {
				return Theme{}, fmt.Errorf("load theme config %s: %w", file, err)
			}
		} else {
			parsed, err := ParseThemeConfig(data)
			if err != nil {
				return Theme{}, fmt.Errorf("load theme config %s: %w", file, err)
			}
			cfg = parsed
		}
	}

	// Honor the config's base theme (`theme = vivid`) so the truecolor
	// VividTheme is actually reachable — previously this hardcoded
	// TerminalTheme, making BaseName dead. Bubble Tea v2's renderer
	// downsamples the resulting truecolor SGR to the terminal's real color
	// profile, so 256/16/mono terminals stay correct. The CLI Name stays
	// deprecated/ignored (see TestResolveThemeIgnoresDeprecatedThemeName).
	base := TerminalTheme()
	if cfg.BaseName != "" {
		if b, ok := BuiltinTheme(cfg.BaseName); ok {
			base = b
		}
	}
	theme := base.Normalized()
	if len(cfg.Codes) > 0 {
		theme.Name = "custom"
		for role, code := range cfg.Codes {
			theme.Codes[role] = code
		}
	}
	if opts.NoColor || envTruthy(env["SSHERPA_NO_COLOR"]) || env["NO_COLOR"] != "" {
		theme.NoColor = true
	}
	return theme.Normalized(), nil
}

func ParseThemeConfig(data []byte) (ThemeConfig, error) {
	cfg := ThemeConfig{
		Codes: make(map[Role]string),
		Specs: make(map[Role]string),
	}
	lines := strings.Split(string(data), "\n")
	for index, raw := range lines {
		line := stripThemeComment(raw)
		if line == "" {
			continue
		}
		key, value, ok := cutThemeAssignment(line)
		if !ok {
			return ThemeConfig{}, fmt.Errorf("line %d: expected key=value", index+1)
		}
		key = normalizeThemeKey(key)
		value = strings.TrimSpace(value)
		switch key {
		case "theme", "base":
			if value == "" {
				return ThemeConfig{}, fmt.Errorf("line %d: theme cannot be empty", index+1)
			}
			cfg.BaseName = value
		default:
			role, ok := roleForKey(key)
			if !ok {
				cfg.Warnings = append(cfg.Warnings, fmt.Sprintf("line %d: unknown theme role %q ignored", index+1, key))
				continue
			}
			code, err := ParseStyleSpec(value)
			if err != nil {
				return ThemeConfig{}, fmt.Errorf("line %d: %w", index+1, err)
			}
			cfg.Codes[role] = code
			cfg.Specs[role] = value
		}
	}
	return cfg, nil
}

func ParseStyleSpec(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "none" || value == "plain" {
		return "", nil
	}
	if rawSGR(value) {
		return normalizeSGR(value), nil
	}

	tokens := strings.FieldsFunc(value, func(r rune) bool {
		return r == ' ' || r == '\t' || r == ',' || r == '+'
	})
	var parts []string
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		if rawSGR(token) {
			parts = append(parts, strings.Split(normalizeSGR(token), ";")...)
			continue
		}
		if code, ok := styleTokenCodes[token]; ok {
			parts = append(parts, code)
			continue
		}
		if code, ok := colorTokenCode(token, false); ok {
			parts = append(parts, code)
			continue
		}
		if strings.HasPrefix(token, "fg-") {
			if code, ok := colorTokenCode(strings.TrimPrefix(token, "fg-"), false); ok {
				parts = append(parts, code)
				continue
			}
		}
		if strings.HasPrefix(token, "bg-") {
			if code, ok := colorTokenCode(strings.TrimPrefix(token, "bg-"), true); ok {
				parts = append(parts, code)
				continue
			}
		}
		return "", fmt.Errorf("unknown style token %q", token)
	}
	return strings.Join(parts, ";"), nil
}

func (t Theme) IsZero() bool {
	return t.Name == "" && len(t.Codes) == 0 && !t.NoColor
}

func (t Theme) Normalized() Theme {
	base, _ := BuiltinTheme(t.Name)
	if base.IsZero() {
		base = TerminalTheme()
	}
	base.NoColor = t.NoColor
	if t.Name != "" {
		base.Name = t.Name
	}
	if base.Codes == nil {
		base.Codes = make(map[Role]string)
	} else {
		base.Codes = copyRoleCodes(base.Codes)
	}
	for role, code := range t.Codes {
		base.Codes[role] = code
	}
	return base
}

func (t Theme) WithNoColor(noColor bool) Theme {
	t = t.Normalized()
	t.NoColor = noColor
	return t
}

func (t Theme) Style(role Role, value string) string {
	code, ok := t.Codes[role]
	if !ok {
		t = t.Normalized()
		code = t.Codes[role]
	}
	return Apply(t.NoColor, code, value)
}

func copyRoleCodes(codes map[Role]string) map[Role]string {
	copied := make(map[Role]string, len(codes))
	for role, code := range codes {
		copied[role] = code
	}
	return copied
}

func ThemeConfigPath(file string, env []string) (string, error) {
	path, _ := resolveThemeFile(file, themeEnv(env), false)
	if path == "" {
		return "", errors.New("could not resolve theme config path")
	}
	return path, nil
}

func resolveThemeFile(file string, env map[string]string, skipDefault bool) (string, bool) {
	if strings.TrimSpace(file) != "" {
		return expandThemePath(file), true
	}
	if value := strings.TrimSpace(env["SSHERPA_THEME_FILE"]); value != "" {
		return expandThemePath(value), true
	}
	if skipDefault {
		return "", false
	}
	configDir, err := os.UserConfigDir()
	if err != nil || configDir == "" {
		return "", false
	}
	return filepath.Join(configDir, "ssherpa", "theme.conf"), false
}

func expandThemePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func themeEnv(env []string) map[string]string {
	if env == nil {
		env = os.Environ()
	}
	values := make(map[string]string, len(env))
	for _, item := range env {
		key, value, ok := strings.Cut(item, "=")
		if ok {
			values[key] = value
		}
	}
	return values
}

func envTruthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func stripThemeComment(line string) string {
	if index := strings.IndexByte(line, '#'); index >= 0 {
		line = line[:index]
	}
	return strings.TrimSpace(line)
}

func cutThemeAssignment(line string) (string, string, bool) {
	if key, value, ok := strings.Cut(line, "="); ok {
		return strings.TrimSpace(key), strings.TrimSpace(value), true
	}
	if key, value, ok := strings.Cut(line, ":"); ok {
		return strings.TrimSpace(key), strings.TrimSpace(value), true
	}
	return "", "", false
}

func normalizeThemeKey(key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.ReplaceAll(key, "-", "_")
	return key
}

func roleForKey(key string) (Role, bool) {
	role, ok := roleAliases[key]
	return role, ok
}

func rawSGR(value string) bool {
	if value == "" {
		return false
	}
	parts := strings.Split(value, ";")
	for _, part := range parts {
		if part == "" {
			return false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func normalizeSGR(value string) string {
	parts := strings.Split(value, ";")
	for i, part := range parts {
		parts[i] = strings.TrimSpace(part)
	}
	return strings.Join(parts, ";")
}

func colorTokenCode(token string, background bool) (string, bool) {
	token = strings.ReplaceAll(token, "_", "-")
	code, ok := colorTokenCodes[token]
	if !ok {
		return "", false
	}
	if !background {
		return code, true
	}
	n, err := strconv.Atoi(code)
	if err != nil {
		return "", false
	}
	switch {
	case n == 39:
		return "49", true
	case n >= 30 && n <= 37:
		return strconv.Itoa(n + 10), true
	case n >= 90 && n <= 97:
		return strconv.Itoa(n + 10), true
	default:
		return "", false
	}
}

var roleAliases = map[string]Role{
	"title":      RoleTitle,
	"primary":    RolePrimary,
	"secondary":  RoleSecondary,
	"accent":     RoleAccent,
	"muted":      RoleMuted,
	"subtle":     RoleSubtle,
	"dim":        RoleSubtle,
	"foreground": RoleForeground,
	"fg":         RoleForeground,
	"text":       RoleForeground,
	"selected":   RoleSelected,
	"selection":  RoleSelected,
	"border":     RoleBorder,
	"rule":       RoleBorder,
	"success":    RoleSuccess,
	"warning":    RoleWarning,
	"danger":     RoleDanger,
	"error":      RoleDanger,
	"info":       RoleInfo,
	"search":     RoleSearch,
	"pill":       RolePill,
}

var styleTokenCodes = map[string]string{
	"reset":     "0",
	"bold":      "1",
	"faint":     "2",
	"dim":       "2",
	"italic":    "3",
	"underline": "4",
	"reverse":   "7",
	"inverse":   "7",
}

var colorTokenCodes = map[string]string{
	"default":        "39",
	"foreground":     "39",
	"fg":             "39",
	"black":          "30",
	"red":            "31",
	"green":          "32",
	"yellow":         "33",
	"blue":           "34",
	"magenta":        "35",
	"purple":         "35",
	"cyan":           "36",
	"white":          "37",
	"gray":           "90",
	"grey":           "90",
	"bright-black":   "90",
	"bright-red":     "91",
	"bright-green":   "92",
	"bright-yellow":  "93",
	"bright-blue":    "94",
	"bright-magenta": "95",
	"bright-purple":  "95",
	"bright-cyan":    "96",
	"bright-white":   "97",
}
