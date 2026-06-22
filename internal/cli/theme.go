package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/ssherpa/internal/ui"
)

const themeUsage = `Usage:
  ssherpa theme [--theme-file PATH] [--no-color]
  ssherpa theme export PATH
  ssherpa theme import PATH

Open the interactive theme editor. The editor previews the picker and
overlay palette live, then writes a theme config when you press s.

  export PATH  Write the active theme to a portable .theme file that any
               termtheme-based app (e.g. passage) can import.
  import PATH  Replace the active theme with a .theme file (backs up the
               previous one). Roles ssherpa does not paint are preserved.

Theme Flags:
  --theme-file PATH  Edit this theme config path
  --no-color         Disable color styling while editing
`

type themeFlags struct {
	ThemeName string
	ThemeFile string
	NoColor   bool
}

func runTheme(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "export":
			return runThemeExport(args[1:], stdout, stderr)
		case "import":
			return runThemeImport(args[1:], stdout, stderr)
		}
	}
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, themeUsage)
		return 0
	}
	flags, ok := parseThemeFlags(args, stderr)
	if !ok {
		return 1
	}

	path, err := termstyle.ThemeConfigPath(flags.ThemeFile, nil)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	cfg, warning, err := loadThemeConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	result, ok, err := ui.EditTheme(context.Background(), ui.ThemeEditorOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		Config:      cfg,
		ConfigPath:  path,
		Warning:     warning,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: theme editor failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] theme edit cancelled")
		return 0
	}

	writeResult, err := fsutil.AtomicWriteFile(path, formatThemeConfig(result.Config), fsutil.WriteOptions{
		Backup:       true,
		BackupPrefix: "ssherpa-theme-backup",
		Mode:         0o600,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: write theme config: %v\n", err)
		return 1
	}
	if writeResult.Changed {
		fmt.Fprintf(stdout, "[theme] wrote %s\n", writeResult.Path)
		if writeResult.BackupPath != "" {
			fmt.Fprintf(stdout, "[backup] %s\n", writeResult.BackupPath)
		}
		return 0
	}
	fmt.Fprintf(stdout, "[theme] unchanged %s\n", writeResult.Path)
	return 0
}

func parseThemeFlags(args []string, stderr io.Writer) (themeFlags, bool) {
	var flags themeFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--theme":
			if _, ok := nextArg(args, &i, stderr, "--theme"); !ok {
				return flags, false
			}
		case strings.HasPrefix(arg, "--theme="):
			// Deprecated no-op. The theme config is now the single source of
			// color styling; keep accepting the flag so older aliases/scripts do
			// not fail just because they still pass --theme.
		case arg == "--theme-file":
			value, ok := nextArg(args, &i, stderr, "--theme-file")
			if !ok {
				return flags, false
			}
			flags.ThemeFile = value
		case strings.HasPrefix(arg, "--theme-file="):
			flags.ThemeFile = strings.TrimPrefix(arg, "--theme-file=")
		case arg == "--no-color":
			flags.NoColor = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown theme flag %q\n", arg)
			return flags, false
		default:
			fmt.Fprintf(stderr, "ssherpa: theme does not accept positional arguments: %s\n", arg)
			return flags, false
		}
	}
	return flags, true
}

// runThemeExport writes the active theme to a portable .theme file that any
// sibling app (passage, future TUIs) can import.
func runThemeExport(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, themeUsage)
		return 0
	}
	flags, dest, ok := parseThemePortingArgs(args, stderr)
	if !ok {
		return 1
	}
	path, err := termstyle.ThemeConfigPath(flags.ThemeFile, nil)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	cfg, _, err := loadThemeConfig(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	base, ok := termstyle.BuiltinTheme(cfg.BaseName)
	if !ok {
		base = termstyle.TerminalTheme()
	}
	if err := os.WriteFile(dest, termstyle.ExportTheme(cfg, base, "ssherpa", ""), 0o644); err != nil {
		fmt.Fprintf(stderr, "ssherpa: write %s: %v\n", dest, err)
		return 1
	}
	fmt.Fprintf(stdout, "[theme] exported %s\n", dest)
	return 0
}

// runThemeImport loads a portable .theme file and writes it as the active theme
// config (with an atomic backup of the previous one).
func runThemeImport(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, themeUsage)
		return 0
	}
	flags, src, ok := parseThemePortingArgs(args, stderr)
	if !ok {
		return 1
	}
	data, err := os.ReadFile(src)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read %s: %v\n", src, err)
		return 1
	}
	cfg, meta, err := termstyle.ImportTheme(data)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %s is not a valid theme: %v\n", src, err)
		return 1
	}
	for _, w := range meta.Warnings {
		fmt.Fprintf(stderr, "ssherpa: note: %s\n", w)
	}
	path, err := termstyle.ThemeConfigPath(flags.ThemeFile, nil)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	writeResult, err := fsutil.AtomicWriteFile(path, formatThemeConfig(cfg), fsutil.WriteOptions{
		Backup:       true,
		BackupPrefix: "ssherpa-theme-backup",
		Mode:         0o600,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: write theme config: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "[theme] imported %s -> %s\n", src, writeResult.Path)
	if writeResult.BackupPath != "" {
		fmt.Fprintf(stdout, "[backup] %s\n", writeResult.BackupPath)
	}
	return 0
}

// parseThemePortingArgs parses the flags accepted by export/import plus exactly
// one positional PATH.
func parseThemePortingArgs(args []string, stderr io.Writer) (themeFlags, string, bool) {
	var flags themeFlags
	var path string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--theme-file":
			value, ok := nextArg(args, &i, stderr, "--theme-file")
			if !ok {
				return flags, "", false
			}
			flags.ThemeFile = value
		case strings.HasPrefix(arg, "--theme-file="):
			flags.ThemeFile = strings.TrimPrefix(arg, "--theme-file=")
		case arg == "--no-color":
			flags.NoColor = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown theme flag %q\n", arg)
			return flags, "", false
		default:
			if path != "" {
				fmt.Fprintln(stderr, "ssherpa: theme export/import takes exactly one PATH")
				return flags, "", false
			}
			path = arg
		}
	}
	if path == "" {
		fmt.Fprintln(stderr, "ssherpa: theme export/import needs a PATH")
		return flags, "", false
	}
	return flags, path, true
}

func loadThemeConfig(path string) (termstyle.ThemeConfig, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return termstyle.ThemeConfig{}, "", nil
		}
		return termstyle.ThemeConfig{}, "", fmt.Errorf("read theme config %s: %w", path, err)
	}
	cfg, err := termstyle.ParseThemeConfig(data)
	if err != nil {
		return termstyle.ThemeConfig{}, "existing theme config did not parse; saving will replace it", nil
	}
	if len(cfg.Warnings) > 0 {
		// Unknown keys (likely written by a newer ssherpa) are kept out
		// of the editor and dropped on save; tell the user up front.
		return cfg, "ignored: " + strings.Join(cfg.Warnings, "; "), nil
	}
	return cfg, "", nil
}

func formatThemeConfig(cfg termstyle.ThemeConfig) []byte {
	var b strings.Builder
	b.WriteString("# ssherpa theme config\n")
	b.WriteString("# Edit with: ssherpa theme\n\n")
	// Persist the chosen base palette so `theme = vivid` survives a round-trip.
	// Without this, re-saving a vivid theme silently reverted it to terminal.
	// Terminal is the implicit default, so it is left out to keep configs lean.
	if base := strings.TrimSpace(cfg.BaseName); base != "" {
		if t, ok := termstyle.BuiltinTheme(base); ok && t.Name != "terminal" {
			b.WriteString("theme = ")
			b.WriteString(t.Name)
			b.WriteString("\n\n")
		}
	}
	rendered := make(map[termstyle.Role]bool)
	writeRole := func(role termstyle.Role) {
		spec := strings.TrimSpace(cfg.Specs[role])
		if spec == "" {
			return
		}
		b.WriteString(string(role))
		b.WriteString(" = ")
		b.WriteString(spec)
		b.WriteString("\n")
		rendered[role] = true
	}
	for _, role := range termstyle.Roles() {
		writeRole(role)
	}
	// Preserve roles ssherpa does not itself render (e.g. selected_bar from a
	// theme authored in a sibling app) so a round-trip through `ssherpa theme`
	// never drops them. Sorted for stable output.
	var passthrough []termstyle.Role
	for role := range cfg.Specs {
		if !rendered[role] {
			passthrough = append(passthrough, role)
		}
	}
	sort.Slice(passthrough, func(i, j int) bool { return passthrough[i] < passthrough[j] })
	for _, role := range passthrough {
		writeRole(role)
	}
	return []byte(b.String())
}

func connectFlagsAsThemeArgs(flags connectFlags) []string {
	var args []string
	if flags.ThemeFile != "" {
		args = append(args, "--theme-file", flags.ThemeFile)
	}
	if flags.NoColor {
		args = append(args, "--no-color")
	}
	return args
}
