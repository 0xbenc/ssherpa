package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/session"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/ssherpa/internal/ui"
)

const usage = `Usage:
  ssherpa [command] [flags]
  ssherpa [connect-flags] [-- ssh-args...]

Available Commands:
  add        Add or update an SSH alias
  edit       Edit or delete SSH aliases
  jump       Connect through one or more ProxyJump hops
  proxy      Start a local SOCKS proxy through an SSH alias
  forward    Open a local TCP port-forward (-L) tunnel through an SSH alias
  authkeys   Manage authorized_keys on this device
  theme      Build and save the terminal UI color schema
  session    Inspect supervised session records
  list       List SSH aliases from OpenSSH config
  show       Show one SSH alias from OpenSSH config
  version    Print build version information
  help       Show this help

Inventory Flags:
  --json             Emit JSON output
  --all              Include wildcard and negated Host patterns
  --filter SUBSTR    Filter aliases by substring
  --user USER        Filter aliases by parsed user
  --config PATH      Read this SSH config root instead of ~/.ssh/config

Connect Flags:
  --print            Print the SSH command instead of running it
  --exec             Run the SSH command; this is the default
  --select ALIAS     Select an alias non-interactively
  --ssh-binary PATH  Use this SSH binary
  --supervise        Run under supervised PTY; this is the default
  --direct           Use the old direct runner without session overlay/state
  --state-dir PATH   Override ssherpa session state directory
  --latency-warn DURATION
                     Enable sidecar probe and warn above threshold
  --latency-disconnect DURATION
                     Disconnect after sustained unhealthy probes; requires --latency-warn
  --composer-key KEY
                     Open local queued-input composer with KEY; default ctrl-g
  --no-composer      Disable local queued-input composer
  --no-kitty         Disable Kitty SSH command detection
  --no-color         Disable color styling
  --theme NAME       Use UI theme: terminal or vivid
  --theme-file PATH  Load UI theme role overrides from PATH

Mutation Commands:
  ssherpa add --alias NAME --host HOST [--user USER] [--port PORT] [--yes]
  ssherpa add --alias NAME --host HOST --dry-run
  ssherpa edit set ALIAS [--host HOST] [--user USER] [--port PORT] [--yes]
  ssherpa edit delete ALIAS [--all-sources] [--yes]
  ssherpa edit delete-all --dry-run
  ssherpa edit delete-all --confirm "delete N aliases"

Route Commands:
  ssherpa jump --dest DEST --hop HOP [--hop HOP] [--print] [--direct]
  ssherpa proxy --select ALIAS [--bind ADDR] [--port PORT] [--print] [--direct]
  ssherpa forward --select ALIAS --local [BIND:]PORT --remote HOST:PORT [--through HOP] [--print] [--direct] [--background] [--reconnect-max N] [--no-reconnect]
  ssherpa forward list [--json] [--state-dir PATH]
  ssherpa forward status SESSION_ID_OR_NAME [--json] [--state-dir PATH]
  ssherpa forward stop SESSION_ID_OR_NAME [--state-dir PATH]

Theme Commands:
  ssherpa theme [--theme terminal|vivid] [--theme-file PATH]

Authorized Keys Commands:
  ssherpa authkeys list [--json]
  ssherpa authkeys add --key "ssh-ed25519 ..." [--yes]
  ssherpa authkeys add --key-file ~/.ssh/id_ed25519.pub [--yes]
  ssherpa authkeys merge --from-dir ./keys [--dry-run]
  ssherpa authkeys replace --from-dir ./keys [--yes]
  ssherpa authkeys delete --fingerprint SHA256:... [--yes]

Session Commands:
  ssherpa session list [--json] [--state-dir PATH]
  ssherpa session map [--json] [--all] [--state-dir PATH]
  ssherpa session show SESSION_ID [--json] [--state-dir PATH]
  ssherpa session prune [--older-than 168h] [--dry-run] [--state-dir PATH]

Phase 10:
  SSH config inventory, picker, supervised SSH execution, and safe SSH config
  add/edit/delete mutations are available. Jump/proxy and authorized_keys
  management are available. Supervised PTY sessions, session maps, and
  upgraded picker UX are available. The TUI defaults to the terminal
  palette, supports theme role overrides, includes a live theme editor,
  and still honors --no-color. In supervised sessions, Ctrl-] opens
  the local active-session map overlay and Ctrl-G opens the local input
  composer. Opt-in sidecar latency warnings and explicit latency
  disconnects are available with supervised sessions.
`

type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

func (b BuildInfo) normalized() BuildInfo {
	return BuildInfo{
		Version: defaultString(b.Version, "dev"),
		Commit:  defaultString(b.Commit, "none"),
		Date:    defaultString(b.Date, "unknown"),
	}
}

func Run(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	stdout = writerOrDiscard(stdout)
	stderr = writerOrDiscard(stderr)

	// Hidden supervisor dispatch: the parent process re-execs ssherpa
	// with this flag set after `forward --background`. The child runs
	// the same forward args (minus --background) in detached mode.
	if len(args) > 0 && args[0] == supervisorFlag {
		return runSupervisorChild(args[1:], stdout, stderr)
	}

	if len(args) == 0 {
		return runConnect(args, stdout, stderr)
	}

	switch args[0] {
	case "add":
		return runAdd(args[1:], stdout, stderr)
	case "edit":
		return runEdit(args[1:], stdout, stderr)
	case "jump":
		return runJump(args[1:], stdout, stderr)
	case "proxy":
		return runProxy(args[1:], stdout, stderr)
	case "forward":
		return runForward(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "show":
		return runShow(args[1:], stdout, stderr)
	case "authkeys":
		return runAuthkeys(args[1:], stdout, stderr)
	case "theme":
		return runTheme(args[1:], stdout, stderr)
	case "session":
		return runSession(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "ssherpa: version does not accept arguments: %s\n", strings.Join(args[1:], " "))
			return 1
		}
		printVersion(stdout, build)
		return 0
	case "help", "--help", "-h":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "ssherpa: help does not accept arguments: %s\n", strings.Join(args[1:], " "))
			return 1
		}
		printUsage(stdout)
		return 0
	default:
		return runConnect(args, stdout, stderr)
	}
}

type connectFlags struct {
	inventoryFlags
	Print             bool
	SSHBinary         string
	Supervise         bool
	Direct            bool
	StateDir          string
	LatencyWarn       time.Duration
	LatencyDisconnect time.Duration
	ComposerKey       byte
	ComposerKeyName   string
	NoComposer        bool
	NoKitty           bool
	NoColor           bool
	ThemeName         string
	ThemeFile         string
	Select            string
	SSHArgs           []string
}

func runConnect(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseConnectFlags(args, stderr)
	if !ok {
		return 1
	}
	if !validateLatencyFlags(flags, stderr) {
		return 1
	}
	if !validateComposerFlags(flags, stderr) {
		return 1
	}
	if !validateThemeFlags(flags, stderr) {
		return 1
	}
	if flags.JSON && !flags.Print {
		fmt.Fprintln(stderr, "ssherpa: --json is only supported with --print for connect mode")
		return 1
	}

	graph, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	item, alias, ok, code := selectConnectItem(flags, graph, inventory, stderr)
	if !ok {
		return code
	}
	switch item.Kind {
	case ui.ItemAdd:
		return runAdd(connectFlagsAsAddArgs(flags), stdout, stderr)
	case ui.ItemEdit:
		return runEdit(connectFlagsAsEditArgs(flags), stdout, stderr)
	case ui.ItemProxy:
		return runProxy(connectFlagsAsProxyArgs(flags), stdout, stderr)
	case ui.ItemJump:
		return runJump(connectFlagsAsJumpArgs(flags), stdout, stderr)
	case ui.ItemForward:
		return runForwardBuilder(flags, inventory, stdout, stderr)
	case ui.ItemAuthkeys:
		return runAuthkeys(nil, stdout, stderr)
	case ui.ItemSessions:
		return runSession([]string{"map", "--state-dir", flags.StateDir}, stdout, stderr)
	case ui.ItemTheme:
		return runTheme(connectFlagsAsThemeArgs(flags), stdout, stderr)
	}

	base := resolveSSHCommand(flags)
	metadata := session.Metadata{
		TargetAlias: alias.Name,
		Route:       []string{alias.Name},
	}
	cmd := sshcmd.BuildDirect(base, alias.Name, flags.SSHArgs)

	if flags.Print {
		if flags.JSON {
			if err := sshcmd.WritePrintJSON(stdout, cmd, alias.Name); err != nil {
				fmt.Fprintf(stderr, "ssherpa: write JSON: %v\n", err)
				return 1
			}
			return 0
		}
		fmt.Fprintf(stdout, "[print] %s\n", sshcmd.QuoteArgv(cmd.Argv))
		return 0
	}

	return printOrRunSSH(cmd, flags.connectOptions(sshcmd.BuildProbe(base, metadata.TargetAlias, metadata.Hops)), metadata, stdout, stderr)
}

func parseConnectFlags(args []string, stderr io.Writer) (connectFlags, bool) {
	var flags connectFlags

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			flags.SSHArgs = append(flags.SSHArgs, args[i+1:]...)
			return flags, true
		case arg == "--json":
			flags.JSON = true
		case arg == "--all":
			flags.All = true
		case arg == "--print":
			flags.Print = true
		case arg == "--exec":
			flags.Print = false
		case arg == "--filter":
			value, ok := nextArg(args, &i, stderr, "--filter")
			if !ok {
				return flags, false
			}
			flags.Filter = value
		case strings.HasPrefix(arg, "--filter="):
			flags.Filter = strings.TrimPrefix(arg, "--filter=")
		case arg == "--user":
			value, ok := nextArg(args, &i, stderr, "--user")
			if !ok {
				return flags, false
			}
			flags.User = value
		case strings.HasPrefix(arg, "--user="):
			flags.User = strings.TrimPrefix(arg, "--user=")
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--ssh-binary":
			value, ok := nextArg(args, &i, stderr, "--ssh-binary")
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case strings.HasPrefix(arg, "--ssh-binary="):
			flags.SSHBinary = strings.TrimPrefix(arg, "--ssh-binary=")
		case arg == "--supervise":
			flags.Supervise = true
			flags.Direct = false
		case arg == "--direct" || arg == "--no-supervise":
			flags.Direct = true
			flags.Supervise = false
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--latency-warn":
			value, ok := nextArg(args, &i, stderr, "--latency-warn")
			if !ok {
				return flags, false
			}
			duration, ok := parseDuration(value, stderr, "--latency-warn")
			if !ok {
				return flags, false
			}
			flags.LatencyWarn = duration
		case strings.HasPrefix(arg, "--latency-warn="):
			duration, ok := parseDuration(strings.TrimPrefix(arg, "--latency-warn="), stderr, "--latency-warn")
			if !ok {
				return flags, false
			}
			flags.LatencyWarn = duration
		case arg == "--latency-disconnect":
			value, ok := nextArg(args, &i, stderr, "--latency-disconnect")
			if !ok {
				return flags, false
			}
			duration, ok := parseDuration(value, stderr, "--latency-disconnect")
			if !ok {
				return flags, false
			}
			flags.LatencyDisconnect = duration
		case strings.HasPrefix(arg, "--latency-disconnect="):
			duration, ok := parseDuration(strings.TrimPrefix(arg, "--latency-disconnect="), stderr, "--latency-disconnect")
			if !ok {
				return flags, false
			}
			flags.LatencyDisconnect = duration
		case arg == "--composer-key":
			value, ok := nextArg(args, &i, stderr, "--composer-key")
			if !ok {
				return flags, false
			}
			key, name, ok := parseControlKey(value, stderr, "--composer-key")
			if !ok {
				return flags, false
			}
			flags.ComposerKey = key
			flags.ComposerKeyName = name
		case strings.HasPrefix(arg, "--composer-key="):
			key, name, ok := parseControlKey(strings.TrimPrefix(arg, "--composer-key="), stderr, "--composer-key")
			if !ok {
				return flags, false
			}
			flags.ComposerKey = key
			flags.ComposerKeyName = name
		case arg == "--no-composer":
			flags.NoComposer = true
		case arg == "--select":
			value, ok := nextArg(args, &i, stderr, "--select")
			if !ok {
				return flags, false
			}
			flags.Select = value
		case strings.HasPrefix(arg, "--select="):
			flags.Select = strings.TrimPrefix(arg, "--select=")
		case arg == "--no-kitty":
			flags.NoKitty = true
		case arg == "--no-color":
			flags.NoColor = true
		case arg == "--theme":
			value, ok := nextArg(args, &i, stderr, "--theme")
			if !ok {
				return flags, false
			}
			flags.ThemeName = value
		case strings.HasPrefix(arg, "--theme="):
			flags.ThemeName = strings.TrimPrefix(arg, "--theme=")
		case arg == "--theme-file":
			value, ok := nextArg(args, &i, stderr, "--theme-file")
			if !ok {
				return flags, false
			}
			flags.ThemeFile = value
		case strings.HasPrefix(arg, "--theme-file="):
			flags.ThemeFile = strings.TrimPrefix(arg, "--theme-file=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown flag %q\n", arg)
			return flags, false
		default:
			flags.SSHArgs = append(flags.SSHArgs, arg)
		}
	}

	return flags, true
}

func validateLatencyFlags(flags connectFlags, stderr io.Writer) bool {
	if flags.LatencyWarn == 0 && flags.LatencyDisconnect == 0 {
		return true
	}
	if flags.Direct {
		fmt.Fprintln(stderr, "ssherpa: latency watchdog requires supervised mode; remove --direct")
		return false
	}
	if flags.LatencyDisconnect > 0 && flags.LatencyWarn == 0 {
		fmt.Fprintln(stderr, "ssherpa: --latency-disconnect requires --latency-warn")
		return false
	}
	return true
}

func validateComposerFlags(flags connectFlags, stderr io.Writer) bool {
	if flags.NoComposer && flags.ComposerKey != 0 {
		fmt.Fprintln(stderr, "ssherpa: --composer-key cannot be used with --no-composer")
		return false
	}
	if flags.Direct && (flags.NoComposer || flags.ComposerKey != 0) {
		fmt.Fprintln(stderr, "ssherpa: composer flags require supervised mode; remove --direct")
		return false
	}
	return true
}

func validateThemeFlags(flags connectFlags, stderr io.Writer) bool {
	if flags.ThemeName == "" && flags.ThemeFile == "" {
		return true
	}
	_, err := termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:            flags.ThemeName,
		File:            flags.ThemeFile,
		NoColor:         flags.NoColor,
		SkipDefaultFile: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return false
	}
	return true
}

func parseControlKey(value string, stderr io.Writer, flag string) (byte, string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.TrimPrefix(normalized, "^")
	normalized = strings.TrimPrefix(normalized, "ctrl-")
	normalized = strings.TrimPrefix(normalized, "ctrl+")
	if normalized == "" {
		fmt.Fprintf(stderr, "ssherpa: %s must be a control key like ctrl-g\n", flag)
		return 0, "", false
	}

	var key byte
	var label string
	switch normalized {
	case "space":
		key = 0x00
		label = "Ctrl-Space"
	case "[":
		key = 0x1b
		label = "Esc"
	case "\\":
		key = 0x1c
		label = "Ctrl-\\"
	case "]":
		key = 0x1d
		label = "Ctrl-]"
	case "^":
		key = 0x1e
		label = "Ctrl-^"
	case "_":
		key = 0x1f
		label = "Ctrl-_"
	default:
		if len(normalized) != 1 || normalized[0] < 'a' || normalized[0] > 'z' {
			fmt.Fprintf(stderr, "ssherpa: %s must be a control key like ctrl-g\n", flag)
			return 0, "", false
		}
		key = normalized[0] - 'a' + 1
		label = "Ctrl-" + strings.ToUpper(normalized)
	}
	if key == 0x00 || key == 0x03 || key == 0x04 || key == 0x0d || key == 0x0a || key == 0x1b || key == 0x7f || key == session.OverlayHotkey {
		fmt.Fprintf(stderr, "ssherpa: %s cannot use reserved key %s\n", flag, label)
		return 0, "", false
	}
	return key, label, true
}

func selectConnectItem(flags connectFlags, graph *sshconfig.Graph, inventory hostlist.Inventory, stderr io.Writer) (ui.Item, hostlist.Alias, bool, int) {
	if flags.Select != "" {
		alias := findAlias(inventory.Aliases, flags.Select)
		if alias == nil {
			fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", flags.Select)
			return ui.Item{}, hostlist.Alias{}, false, 2
		}
		return ui.Item{Kind: ui.ItemAlias, Token: alias.Name, Title: alias.Name}, *alias, true, 0
	}

	sessionCount, activeSessions := pickerSessionCounts(flags.StateDir)
	item, ok, err := ui.Pick(context.Background(), ui.BuildItemsWithOptions(inventory.Aliases, ui.BuildItemsOptions{
		SessionCount:       sessionCount,
		ActiveSessionCount: activeSessions,
	}), ui.PickOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "ssherpa",
		Subtitle:    pickerMode(flags),
		Summary:     pickerSummary(flags, graph, inventory, sessionCount, activeSessions),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return ui.Item{}, hostlist.Alias{}, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] no selection made")
		return ui.Item{}, hostlist.Alias{}, false, 0
	}
	if item.Kind != ui.ItemAlias {
		return item, hostlist.Alias{}, true, 0
	}

	alias := findAlias(inventory.Aliases, item.Token)
	if alias == nil {
		fmt.Fprintf(stderr, "ssherpa: selected alias %q disappeared\n", item.Token)
		return ui.Item{}, hostlist.Alias{}, false, 2
	}
	return item, *alias, true, 0
}

func pickerMode(flags connectFlags) string {
	switch {
	case flags.Print:
		return "print mode"
	case flags.Direct:
		return "exec mode"
	default:
		return "supervised mode"
	}
}

func pickerSummary(flags connectFlags, graph *sshconfig.Graph, inventory hostlist.Inventory, sessionCount int, activeSessions int) []string {
	var summary []string
	warnings := len(inventory.Diagnostics)
	for _, alias := range inventory.Aliases {
		warnings += len(alias.Warnings)
	}
	summary = append(summary, fmt.Sprintf("%d hosts  %d warning(s)  %d active session(s)", len(inventory.Aliases), warnings, activeSessions))
	if graph != nil && graph.RootPath != "" {
		summary = append(summary, "config  "+graph.RootPath)
	}
	var scope []string
	if flags.All {
		scope = append(scope, "including patterns")
	}
	if flags.Filter != "" {
		scope = append(scope, "filter="+flags.Filter)
	}
	if flags.User != "" {
		scope = append(scope, "user="+flags.User)
	}
	if flags.LatencyWarn > 0 {
		scope = append(scope, "latency-warn="+flags.LatencyWarn.String())
	}
	if flags.LatencyDisconnect > 0 {
		scope = append(scope, "latency-disconnect="+flags.LatencyDisconnect.String())
	}
	if flags.NoComposer {
		scope = append(scope, "composer=off")
	} else if flags.ComposerKeyName != "" {
		scope = append(scope, "composer="+flags.ComposerKeyName)
	}
	if flags.ThemeName != "" {
		scope = append(scope, "theme="+flags.ThemeName)
	}
	if flags.ThemeFile != "" {
		scope = append(scope, "theme-file="+flags.ThemeFile)
	}
	if activeSessions > 0 {
		scope = append(scope, fmt.Sprintf("active-sessions=%d", activeSessions))
	}
	if len(scope) > 0 {
		summary = append(summary, strings.Join(scope, "  "))
	}
	return summary
}

func pickerSessionCounts(stateDir string) (total int, active int) {
	dir, err := state.ResolveDir(stateDir)
	if err != nil {
		return 0, 0
	}
	records, err := state.ListRecords(dir)
	if err != nil {
		return 0, 0
	}
	for _, record := range records {
		if record.Status() == "active" {
			active++
		}
	}
	return len(records), active
}

type inventoryFlags struct {
	All    bool
	JSON   bool
	Filter string
	User   string
	Config string
}

type graphSummary struct {
	RootPath string                  `json:"root_path"`
	Files    []sshconfig.File        `json:"files"`
	Includes []sshconfig.IncludeEdge `json:"includes,omitempty"`
}

type listOutput struct {
	Config      graphSummary           `json:"config"`
	Aliases     []hostlist.Alias       `json:"aliases"`
	Diagnostics []sshconfig.Diagnostic `json:"diagnostics,omitempty"`
}

type showOutput struct {
	Config      graphSummary           `json:"config"`
	Alias       *hostlist.Alias        `json:"alias"`
	Diagnostics []sshconfig.Diagnostic `json:"diagnostics,omitempty"`
}

func runList(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, rest, ok := parseInventoryFlags(args, stderr)
	if !ok {
		return 1
	}
	if len(rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: list does not accept positional arguments: %s\n", strings.Join(rest, " "))
		return 1
	}

	graph, inventory, err := loadInventory(flags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	if flags.JSON {
		writeJSON(stdout, listOutput{
			Config:      summarizeGraph(graph),
			Aliases:     inventory.Aliases,
			Diagnostics: inventory.Diagnostics,
		})
		return 0
	}

	for _, alias := range inventory.Aliases {
		fmt.Fprintf(stdout, "%s\t%s\n", alias.Name, displayAlias(alias))
	}
	return 0
}

func runShow(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, rest, ok := parseInventoryFlags(args, stderr)
	if !ok {
		return 1
	}
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: show requires exactly one alias")
		return 1
	}

	graph, err := loadGraph(flags.Config)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	inventory := hostlist.Build(graph, hostlist.Options{All: true, IgnoreGitUser: false})

	aliasName := rest[0]
	alias := findAlias(inventory.Aliases, aliasName)
	if flags.JSON {
		writeJSON(stdout, showOutput{
			Config:      summarizeGraph(graph),
			Alias:       alias,
			Diagnostics: inventory.Diagnostics,
		})
		if alias == nil {
			return 2
		}
		return 0
	}

	if alias == nil {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", aliasName)
		return 2
	}

	printAlias(stdout, *alias)
	return 0
}

func parseInventoryFlags(args []string, stderr io.Writer) (inventoryFlags, []string, bool) {
	var flags inventoryFlags
	var rest []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = append(rest, args[i+1:]...)
			return flags, rest, true
		case arg == "--json":
			flags.JSON = true
		case arg == "--all":
			flags.All = true
		case arg == "--filter":
			value, ok := nextArg(args, &i, stderr, "--filter")
			if !ok {
				return flags, nil, false
			}
			flags.Filter = value
		case strings.HasPrefix(arg, "--filter="):
			flags.Filter = strings.TrimPrefix(arg, "--filter=")
		case arg == "--user":
			value, ok := nextArg(args, &i, stderr, "--user")
			if !ok {
				return flags, nil, false
			}
			flags.User = value
		case strings.HasPrefix(arg, "--user="):
			flags.User = strings.TrimPrefix(arg, "--user=")
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, nil, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--help" || arg == "-h":
			printUsage(stderr)
			return flags, nil, false
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown flag %q\n", arg)
			return flags, nil, false
		default:
			rest = append(rest, arg)
		}
	}

	return flags, rest, true
}

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func nextArg(args []string, i *int, stderr io.Writer, flag string) (string, bool) {
	if *i+1 >= len(args) {
		fmt.Fprintf(stderr, "ssherpa: %s requires a value\n", flag)
		return "", false
	}
	*i = *i + 1
	return args[*i], true
}

func loadInventory(flags inventoryFlags) (*sshconfig.Graph, hostlist.Inventory, error) {
	graph, err := loadGraph(flags.Config)
	if err != nil {
		return nil, hostlist.Inventory{}, err
	}

	inventory := hostlist.Build(graph, hostlist.Options{
		All:           flags.All,
		Filter:        flags.Filter,
		User:          flags.User,
		IgnoreGitUser: ignoreGitUserFromEnv(),
	})
	return graph, inventory, nil
}

func loadGraph(configPath string) (*sshconfig.Graph, error) {
	rootPath, home, err := rootAndHome(configPath)
	if err != nil {
		return nil, err
	}

	graph, err := sshconfig.Load(sshconfig.LoadOptions{RootPath: rootPath, HomeDir: home})
	if err != nil {
		return nil, err
	}
	return graph, nil
}

func rootAndHome(configPath string) (string, string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("resolve home directory: %w", err)
	}

	if configPath != "" {
		if !filepath.IsAbs(configPath) {
			configPath, err = filepath.Abs(configPath)
			if err != nil {
				return "", "", fmt.Errorf("resolve config path: %w", err)
			}
		}
		return filepath.Clean(configPath), home, nil
	}

	return filepath.Join(home, ".ssh", "config"), home, nil
}

func ignoreGitUserFromEnv() bool {
	return !envBoolDisabled("SSHERPA_IGNORE_USER_GIT")
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func envBoolDisabled(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

func summarizeGraph(graph *sshconfig.Graph) graphSummary {
	return graphSummary{
		RootPath: graph.RootPath,
		Files:    append([]sshconfig.File(nil), graph.Files...),
		Includes: append([]sshconfig.IncludeEdge(nil), graph.Includes...),
	}
}

func findAlias(aliases []hostlist.Alias, name string) *hostlist.Alias {
	for i := range aliases {
		if aliases[i].Name == name {
			return &aliases[i]
		}
	}
	return nil
}

func displayAlias(alias hostlist.Alias) string {
	var b strings.Builder
	if alias.User != "" {
		b.WriteString(alias.User)
		b.WriteByte('@')
	}
	if alias.HostName != "" {
		b.WriteString(alias.HostName)
	}
	if alias.Port != "" {
		b.WriteByte(':')
		b.WriteString(alias.Port)
	}
	if len(alias.IdentityFiles) > 0 {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteByte('[')
		b.WriteString(strings.Join(alias.IdentityFiles, ", "))
		b.WriteByte(']')
	}
	if b.Len() == 0 {
		return "(no HostName in config)"
	}
	return b.String()
}

func printAlias(w io.Writer, alias hostlist.Alias) {
	fmt.Fprintf(w, "Name: %s\n", alias.Name)
	fmt.Fprintf(w, "Source: %s:%d\n", alias.SourcePath, alias.SourceLine)
	fmt.Fprintf(w, "Patterns: %s\n", strings.Join(alias.RawPatterns, " "))
	fmt.Fprintf(w, "Target: %s\n", displayAlias(alias))
	if alias.IsPattern {
		fmt.Fprintln(w, "Pattern: true")
	}
	if alias.IsConditional {
		fmt.Fprintln(w, "Conditional: true")
	}
	for _, warning := range alias.Warnings {
		fmt.Fprintf(w, "Warning: %s\n", warning)
	}
}

func writeJSON(w io.Writer, value any) {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(value)
}

func printVersion(w io.Writer, build BuildInfo) {
	build = build.normalized()
	fmt.Fprintf(w, "ssherpa %s\n", build.Version)
	fmt.Fprintf(w, "commit: %s\n", build.Commit)
	fmt.Fprintf(w, "built: %s\n", build.Date)
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, usage)
}

func defaultString(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}
