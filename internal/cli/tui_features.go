package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/ui"
)

func runCheckPicker(flags connectFlags, inventory hostlist.Inventory, stdout io.Writer, stderr io.Writer) (int, bool) {
	saved := pickerSavedForwards(flags.StateDir, nil)
	item, ok, err := ui.ChooseManagement(context.Background(), checkModeItems(len(saved) > 0), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Check reachability",
		Mode:        "choose check scope",
		Steps:       []string{"scope", "target", "results"},
		CurrentStep: 0,
		Summary:     checkModeSummary(len(inventory.Aliases), len(saved)),
		Footer:      "enter select / type filter / arrows move / shift+arrows section / Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: check picker failed: %v\n", err)
		return 1, false
	}
	if !ok || item.Token == "back" {
		fmt.Fprintln(stdout, "[skipped] check cancelled")
		return 0, true
	}

	switch item.Token {
	case "host":
		alias, ok, err := pickAlias(inventory.Aliases, flags.NoColor, flags.ThemeName, flags.ThemeFile, "Check: pick host", stderr)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
			return 1, false
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] check cancelled")
			return 0, true
		}
		return runCheckTUI(append(checkBaseArgs(flags), alias.Name), flags, stderr), true
	case "hosts":
		if len(inventory.Aliases) == 0 {
			fmt.Fprintln(stdout, "[skipped] no aliases available to check")
			return 0, true
		}
		args := checkBaseArgs(flags)
		for _, alias := range inventory.Aliases {
			args = append(args, alias.Name)
		}
		return runCheckTUI(args, flags, stderr), true
	case "forward":
		name, ok, code := pickSavedForwardForCheck(flags, saved, stderr, stdout)
		if !ok {
			return code, code == 0
		}
		return runCheckTUI(append(checkBaseArgs(flags), "--saved-forward", name), flags, stderr), true
	case "forwards":
		return runCheckTUI(append(checkBaseArgs(flags), "--saved-forwards"), flags, stderr), true
	default:
		return 0, true
	}
}

func checkModeItems(hasSavedForwards bool) []ui.ManagementItem {
	items := []ui.ManagementItem{
		{
			Kind:        ui.ItemCheck,
			Token:       "host",
			Title:       "Check one host",
			Description: "pick an SSH alias and run SSH/ICMP checks",
			Group:       "Hosts",
			Badge:       "host",
			Action:      "Choose one host and show its reachability results",
		},
		{
			Kind:        ui.ItemCheck,
			Token:       "hosts",
			Title:       "Check visible hosts",
			Description: "run checks for the current host list",
			Group:       "Hosts",
			Badge:       "all",
			Action:      "Check every visible SSH alias",
		},
	}
	if hasSavedForwards {
		items = append(items,
			ui.ManagementItem{
				Kind:        ui.ItemForwardSaved,
				Token:       "forward",
				Title:       "Check one saved forward",
				Description: "validate a saved tunnel and run reachability checks",
				Group:       "Saved Forwards",
				Badge:       "fwd",
				Action:      "Choose one saved forward and validate its tunnel path",
			},
			ui.ManagementItem{
				Kind:        ui.ItemForwardSaved,
				Token:       "forwards",
				Title:       "Check saved forwards",
				Description: "validate every saved tunnel",
				Group:       "Saved Forwards",
				Badge:       "all",
				Action:      "Validate every saved forward",
			},
		)
	}
	items = append(items, ui.ManagementItem{
		Kind:        ui.ItemKind("back"),
		Token:       "back",
		Title:       "Back",
		Description: "return to the home screen",
		Group:       "Navigation",
		Badge:       "back",
		Action:      "Return without running checks",
	})
	return items
}

func checkSavedForwardItems(saved []ui.SavedForwardItem) []ui.ManagementItem {
	items := make([]ui.ManagementItem, 0, len(saved))
	for _, sf := range saved {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemForwardSaved,
			Token:       sf.Name,
			Title:       sf.Name,
			Description: sf.Description,
			Detail:      sf.Detail,
			Group:       "Saved Forwards",
			Badge:       "fwd",
			Action:      "Run reachability checks for this saved forward",
		})
	}
	return items
}

func checkModeSummary(aliasCount int, savedForwardCount int) string {
	if savedForwardCount <= 0 {
		return checkCountLabel(aliasCount, "alias", "aliases")
	}
	return checkCountLabel(aliasCount, "alias", "aliases") + "  " + checkCountLabel(savedForwardCount, "forward", "forwards")
}

func checkCountLabel(count int, singular string, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func runCheckTUI(args []string, connect connectFlags, stderr io.Writer) int {
	flags, ok := parseCheckFlags(args, stderr)
	if !ok {
		return 1
	}
	if flags.Timeout <= 0 {
		flags.Timeout = 5 * time.Second
	}
	if flags.ICMPTimeout <= 0 {
		flags.ICMPTimeout = 2 * time.Second
	}
	out, code := runCheckWithFlags(flags, stderr)
	if code != 0 {
		return code
	}
	results := make([]ui.CheckResult, 0, len(out.Results))
	for _, result := range out.Results {
		results = append(results, ui.CheckResult{
			Kind:            result.Kind,
			Name:            result.Name,
			Status:          result.Status,
			SSHRttMillis:    result.SSHRttMillis,
			SSHError:        result.SSHError,
			ICMPStatus:      result.ICMPStatus,
			ICMPRttMillis:   result.ICMPRttMillis,
			LocalBindStatus: result.LocalBindStatus,
			Message:         result.Message,
		})
	}
	if err := ui.ShowCheckResults(context.Background(), ui.CheckResultsOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     connect.NoColor,
		ThemeName:   connect.ThemeName,
		ThemeFile:   connect.ThemeFile,
		CheckedAt:   out.CheckedAt,
		OK:          out.OK,
		Results:     results,
	}); err != nil {
		fmt.Fprintf(stderr, "ssherpa: check results failed: %v\n", err)
		return 1
	}
	if out.OK {
		return 0
	}
	return 2
}

func pickSavedForwardForCheck(flags connectFlags, saved []ui.SavedForwardItem, stderr io.Writer, stdout io.Writer) (string, bool, int) {
	item, ok, err := ui.ChooseManagement(context.Background(), checkSavedForwardItems(saved), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Check: pick saved forward",
		Mode:        "choose saved forward to check",
		Steps:       []string{"scope", "target", "results"},
		CurrentStep: 1,
		Summary:     checkCountLabel(len(saved), "forward", "forwards"),
		Footer:      "enter select / type filter / arrows move / shift+arrows section / Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return "", false, 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] check cancelled")
		return "", false, 0
	}
	return item.Token, true, 0
}

func checkBaseArgs(flags connectFlags) []string {
	var args []string
	if flags.Config != "" {
		args = append(args, "--config", flags.Config)
	}
	if flags.StateDir != "" {
		args = append(args, "--state-dir", flags.StateDir)
	}
	if flags.SSHBinary != "" {
		args = append(args, "--ssh-binary", flags.SSHBinary)
	}
	return args
}

func runDocsPicker(stdout io.Writer, stderr io.Writer, flags connectFlags) (int, bool) {
	item, ok, err := ui.ChooseManagement(context.Background(), docsArtifactItems(), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Completions and manpage",
		Mode:        "choose artifact path",
		Steps:       []string{"artifact", "path"},
		CurrentStep: 0,
		Summary:     "4 artifacts",
		Footer:      "enter show path / type filter / arrows move / shift+arrows section / Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: docs picker failed: %v\n", err)
		return 1, false
	}
	if !ok || item.Token == "back" {
		fmt.Fprintln(stdout, "[skipped] docs cancelled")
		return 0, true
	}
	printArtifactInfo(stdout, item.Token)
	return 0, true
}

func printArtifactInfo(stdout io.Writer, token string) {
	artifact, ok := docsArtifactByToken(token)
	if !ok {
		return
	}
	if path, found := locateRepoArtifact(artifact.RelPath); found {
		fmt.Fprintf(stdout, "%s\n", path)
		fmt.Fprintln(stdout, artifact.Hint)
		return
	}
	// Installed binary: the repo-relative file exists in a source
	// checkout or an extracted release archive, but not next to a
	// brew/deb/rpm-installed binary. Point at the locations the
	// packages install to instead of printing a path that does not
	// exist.
	fmt.Fprintf(stdout, "%s is not present next to this binary; package installs place it at:\n", artifact.RelPath)
	for _, location := range artifact.InstallPaths {
		fmt.Fprintf(stdout, "  %s\n", location)
	}
	fmt.Fprintln(stdout, artifact.Hint)
}

// locateRepoArtifact resolves a repo-relative artifact path against the
// working directory and the running binary's directory — the layouts of
// a source checkout and an extracted release archive. It returns a path
// only when the file actually exists; printing a cwd-joined path that
// does not exist sent installed-binary users chasing phantom files.
func locateRepoArtifact(rel string) (string, bool) {
	var candidates []string
	if abs, err := filepath.Abs(rel); err == nil {
		candidates = append(candidates, abs)
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), rel))
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

type docsArtifact struct {
	Token   string
	Title   string
	RelPath string
	Badge   string
	Group   string
	Hint    string
	// InstallPaths are the locations the release packages install this
	// artifact to, shown when the repo-relative file is not present.
	InstallPaths []string
}

func docsArtifacts() []docsArtifact {
	return []docsArtifact{
		{
			Token:   "bash",
			Title:   "Bash completion",
			RelPath: "completions/ssherpa.bash",
			Badge:   "bash",
			Group:   "Completions",
			Hint:    "source this file or install it as bash completion for ssherpa",
			InstallPaths: []string{
				"/usr/share/bash-completion/completions/ssherpa (deb/rpm)",
				"$(brew --prefix)/etc/bash_completion.d/ssherpa (Homebrew)",
			},
		},
		{
			Token:   "zsh",
			Title:   "Zsh completion",
			RelPath: "completions/ssherpa.zsh",
			Badge:   "zsh",
			Group:   "Completions",
			Hint:    "install this file as _ssherpa in a directory on fpath",
			InstallPaths: []string{
				"/usr/share/zsh/site-functions/_ssherpa (deb/rpm)",
				"$(brew --prefix)/share/zsh/site-functions/_ssherpa (Homebrew)",
			},
		},
		{
			Token:   "fish",
			Title:   "Fish completion",
			RelPath: "completions/ssherpa.fish",
			Badge:   "fish",
			Group:   "Completions",
			Hint:    "install this file as ssherpa.fish in fish vendor_completions.d",
			InstallPaths: []string{
				"/usr/share/fish/vendor_completions.d/ssherpa.fish (deb/rpm)",
				"$(brew --prefix)/share/fish/vendor_completions.d/ssherpa.fish (Homebrew)",
			},
		},
		{
			Token:   "man",
			Title:   "Manpage",
			RelPath: "man/ssherpa.1",
			Badge:   "man",
			Group:   "Manual",
			Hint:    "view with: man ssherpa (installed) or man ./man/ssherpa.1 (source checkout)",
			InstallPaths: []string{
				"/usr/share/man/man1/ssherpa.1 (deb/rpm)",
				"$(brew --prefix)/share/man/man1/ssherpa.1 (Homebrew)",
			},
		},
	}
}

func docsArtifactItems() []ui.ManagementItem {
	artifacts := docsArtifacts()
	items := make([]ui.ManagementItem, 0, len(artifacts)+1)
	for _, artifact := range artifacts {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemDocs,
			Token:       artifact.Token,
			Title:       artifact.Title,
			Description: artifact.RelPath,
			Detail:      artifact.Hint,
			Group:       artifact.Group,
			Badge:       artifact.Badge,
			Action:      "Print this artifact path",
		})
	}
	items = append(items, ui.ManagementItem{
		Kind:        ui.ItemKind("back"),
		Token:       "back",
		Title:       "Back",
		Description: "return to the home screen",
		Group:       "Navigation",
		Badge:       "back",
		Action:      "Return without printing an artifact path",
	})
	return items
}

func docsArtifactByToken(token string) (docsArtifact, bool) {
	for _, artifact := range docsArtifacts() {
		if artifact.Token == token {
			return artifact, true
		}
	}
	return docsArtifact{}, false
}

func savedForwardNames(stateDirOverride string) []string {
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		return nil
	}
	forwards, err := state.ListForwards(stateDir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(forwards))
	for _, forward := range forwards {
		names = append(names, forward.Name)
	}
	return names
}
