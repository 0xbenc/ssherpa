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
	"github.com/0xbenc/ssherpa/internal/incoming"
	"github.com/0xbenc/ssherpa/internal/session"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/ssherpa/internal/transcript"
	"github.com/0xbenc/ssherpa/internal/ui"
)

const usage = `Usage:
  ssherpa [command] [flags]
  ssherpa [connect-flags] [-- ssh-args...]

Available Commands:
  add        Add or update an SSH alias
  edit       Edit or delete SSH aliases and saved forwards
  jump       Connect through one or more ProxyJump hops
  proxy      Start a local SOCKS proxy through an SSH alias
  forward    Open a local TCP port-forward (-L) tunnel through an SSH alias
  send       Send a local file to an SSH alias with SFTP
  receive    Receive a remote file from an SSH alias with SFTP (alias: recv)
  check      Test SSH aliases and saved forwards
  incoming   Inspect and mark incoming SSH sessions
  authkeys   Manage local authorized_keys or seed keys to SSH aliases
  theme      Build and save the terminal UI color schema
  session    Inspect supervised session records and transcripts
  list       List SSH aliases from OpenSSH config
  show       Show one SSH alias from OpenSSH config
  version    Print build version information
  help       Show help for ssherpa or for one command

Run "ssherpa help COMMAND" or "ssherpa COMMAND --help" for command usage.
Run ssherpa with no command to open the interactive picker; run
"ssherpa help connect" for connect-mode and supervised-session flags.
`

const connectUsage = `Usage:
  ssherpa [connect-flags] [-- ssh-args...]
  ssherpa --select ALIAS [connect-flags] [-- ssh-args...]

Without --select, ssherpa opens the interactive picker. Everything after
"--" is passed to ssh unchanged.

Inventory Flags:
  --json             Emit JSON output; connect mode supports it with --print
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
  --overlay-key KEY  Open session-map overlay with KEY; default ctrl-^
  --no-record        Disable output transcript recording for this supervised session
  --record-max-bytes BYTES
                     Cap transcript size; default 50MB
  --no-kitty         Disable Kitty SSH command detection
  --no-color         Disable color styling
  --theme-file PATH  Load UI theme config from PATH

In supervised sessions, the overlay key (default Ctrl-^) opens the local
session map. Inside the overlay: T toggles recording, S sends a file,
V receives a file, and X then X pulls the escape rope. Mashing the
overlay key three times quickly pulls the panic rope without confirmation.
Ctrl-G (rebindable with --composer-key) opens the local input composer.
`

const addUsage = `Usage:
  ssherpa add --alias NAME --host HOST [--user USER] [--port PORT]
              [--identity PATH] [--identities-only]
              [--config PATH] [--dry-run] [--yes]

Add or update an SSH alias with previewable, backed-up config writes.
Run "ssherpa add" without flags for the interactive form.

Examples:
  ssherpa add --alias NAME --host HOST [--user USER] [--port PORT] [--yes]
  ssherpa add --alias NAME --host HOST --dry-run
`

const editUsage = `Usage:
  ssherpa edit set ALIAS [--host HOST] [--user USER] [--port PORT] [--yes]
  ssherpa edit delete ALIAS [--all-sources] [--state-dir PATH] [--yes]
  ssherpa edit delete-all [--state-dir PATH] --dry-run
  ssherpa edit delete-all --confirm "delete N aliases"

Edit or delete SSH aliases with previewable, backed-up config writes.
Run "ssherpa edit" without arguments for the interactive editor.
"ssherpa edit remove" is an alias for "ssherpa edit delete".
`

const jumpUsage = `Usage:
  ssherpa jump --dest DEST --hop HOP [--hop HOP] [--print] [--direct]
  ssherpa jump DEST HOP [HOP...] [connect-flags] [-- ssh-args...]

Connect to DEST through one or more ProxyJump hops. Also accepts
connect flags such as --print, --direct, --state-dir, and -- ssh-args.
`

const proxyUsage = `Usage:
  ssherpa proxy --select ALIAS_OR_SAVED [--bind ADDR] [--port PORT] [--background] [--print] [--direct]
  ssherpa proxy list [--json] [--state-dir PATH]
  ssherpa proxy status SESSION_ID_OR_NAME [--json] [--state-dir PATH]
  ssherpa proxy stop SESSION_ID_OR_NAME [--state-dir PATH]
  ssherpa proxy saved list [--json] [--state-dir PATH]
  ssherpa proxy saved show NAME [--json] [--state-dir PATH]
  ssherpa proxy saved save NAME --select ALIAS --port PORT [--bind ADDR] [--description TEXT] [--yes]
  ssherpa proxy saved edit NAME [--select ALIAS] [--port PORT] [--bind ADDR] [--description TEXT|--clear-description]
  ssherpa proxy saved delete NAME [--yes]
  ssherpa proxy saved rename OLD NEW [--yes]

Start, inspect, and stop local SOCKS proxies through an SSH alias, and
manage the saved proxy catalog. Default bind is 127.0.0.1:1080.
`

const forwardUsage = `Usage:
  ssherpa forward --select ALIAS --local [BIND:]PORT --remote HOST:PORT [--through HOP] [--print] [--direct] [--background] [--reconnect-max N] [--no-reconnect]
  ssherpa forward list [--json] [--state-dir PATH]
  ssherpa forward status SESSION_ID_OR_NAME [--json] [--state-dir PATH]
  ssherpa forward stop SESSION_ID_OR_NAME [--state-dir PATH]
  ssherpa forward saved list [--json] [--state-dir PATH]
  ssherpa forward saved show NAME [--json] [--state-dir PATH]
  ssherpa forward saved save NAME --select ALIAS --local [BIND:]PORT --remote HOST:PORT [--through HOP] [--description TEXT] [--yes]
  ssherpa forward saved edit NAME [--select ALIAS] [--local ...] [--remote ...] [--through HOP|--clear-through] [--description TEXT|--clear-description]
  ssherpa forward saved delete NAME [--yes]
  ssherpa forward saved rename OLD NEW [--yes]

Open, inspect, and stop local TCP port-forward (-L) tunnels through an
SSH alias, and manage the saved forward catalog. Reconnect reacts to
ssh exiting, not to a silently dead link; pair with ServerAliveInterval
in your ssh config (see docs/non-interactive.md).
`

const sendUsage = `Usage:
  ssherpa send LOCAL_FILE --select ALIAS [--remote REMOTE_PATH] [--force] [--print]
  ssherpa send LOCAL_FILE ALIAS [...]

Send one local file to an SSH alias with OpenSSH SFTP. Existing remote
destinations are not overwritten unless --force is set.
`

const receiveUsage = `Usage:
  ssherpa receive REMOTE_PATH --select ALIAS [--local LOCAL_PATH] [--force] [--print]
  ssherpa receive REMOTE_PATH ALIAS [...]

Receive one remote file from an SSH alias with OpenSSH SFTP. Existing
local destinations are not overwritten unless --force is set.
"ssherpa recv" is an alias for "ssherpa receive".
`

const checkUsage = `Usage:
  ssherpa check ALIAS... [--json] [--timeout 5s] [--icmp-timeout 2s] [--no-icmp]
  ssherpa check --filter SUBSTR [--user USER] [--all] [--json]
  ssherpa check --saved-forward NAME [--json]
  ssherpa check --saved-forwards [--json]

Test SSH aliases and saved forwards with a BatchMode SSH probe and a
best-effort ICMP latency probe. Also accepts --config PATH,
--state-dir PATH, and --ssh-binary PATH. Exits 0 when every check
passed and 2 when any check failed or no checks matched the selector.
`

const authkeysUsage = `Usage:
  ssherpa authkeys list [--json]
  ssherpa authkeys add --key "ssh-ed25519 ..." [--yes]
  ssherpa authkeys add --key-file ~/.ssh/id_ed25519.pub [--yes]
  ssherpa authkeys merge --from-dir ./keys [--dry-run]
  ssherpa authkeys seed --key-file ~/.ssh/id_ed25519.pub --target ALIAS [--hop ALIAS=HOP[,HOP...]] [--yes]
  ssherpa authkeys revoke --key-file ~/.ssh/id_ed25519.pub --target ALIAS [--hop ALIAS=HOP[,HOP...]] [--yes]
  ssherpa authkeys replace --from-dir ./keys [--yes]
  ssherpa authkeys delete --fingerprint SHA256:... [--all-matching] [--yes]

Manage authorized_keys on this device with validated, previewable,
backed-up writes. Run "ssherpa authkeys" without a subcommand for the
interactive manager. "delete" requires --all-matching to remove more
than one entry sharing a fingerprint non-interactively.
`

const listUsage = `Usage:
  ssherpa list [--json] [--all] [--filter SUBSTR] [--user USER] [--config PATH]

List SSH aliases parsed from OpenSSH config, including Include files.
Hosts whose parsed User is "git" are hidden by default; set
SSHERPA_IGNORE_USER_GIT=0 to show them.
`

const showUsage = `Usage:
  ssherpa show ALIAS [--json] [--config PATH]

Show one parsed SSH alias. Exits 2 when the alias is not found.
`

const versionUsage = `Usage:
  ssherpa version

Print build version information.
`

const helpUsage = `Usage:
  ssherpa help [COMMAND]

Show the command overview, or usage for one command.
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
		return runConnect(args, stdout, stderr, build)
	}

	// Per-command --help: print the command's own usage block instead
	// of the global overview. hasHelpFlag ignores anything after "--",
	// so ssh passthrough args like `ssherpa prod -- --help` still reach
	// ssh unchanged.
	if len(args) > 1 && hasHelpFlag(args[1:]) {
		if topicUsage, ok := helpTopicUsage(args[0]); ok {
			fmt.Fprint(stdout, topicUsage)
			return 0
		}
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
	case "send":
		return runSend(args[1:], stdout, stderr)
	case "receive", "recv":
		return runReceive(args[1:], stdout, stderr)
	case "check":
		return runCheck(args[1:], stdout, stderr)
	case "incoming":
		return runIncoming(args[1:], stdout, stderr)
	case "list":
		return runList(args[1:], stdout, stderr)
	case "show":
		return runShow(args[1:], stdout, stderr)
	case "authkeys":
		return runAuthkeys(args[1:], stdout, stderr)
	case "theme":
		return runTheme(args[1:], stdout, stderr)
	case "session":
		return runSession(args[1:], stdout, stderr, build)
	case "version", "--version", "-v":
		if len(args) > 1 {
			fmt.Fprintf(stderr, "ssherpa: version does not accept arguments: %s\n", strings.Join(args[1:], " "))
			return 1
		}
		printVersion(stdout, build)
		return 0
	case "help", "--help", "-h":
		return runHelp(args[1:], stdout, stderr)
	default:
		return runConnect(args, stdout, stderr, build)
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
	OverlayKey        byte
	OverlayKeyName    string
	NoRecord          bool
	RecordMaxBytes    int64
	NoKitty           bool
	NoColor           bool
	ThemeName         string
	ThemeFile         string
	Select            string
	SSHArgs           []string
}

func runConnect(args []string, stdout io.Writer, stderr io.Writer, build BuildInfo) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, connectUsage)
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
	reportThemeWarnings(flags.ThemeFile, stderr)

	for {
		graph, inventory, err := loadInventory(flags.inventoryFlags)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 2
		}

		item, alias, ok, code := selectConnectItem(flags, graph, inventory, stderr, build)
		if !ok {
			return code
		}
		switch item.Kind {
		case ui.ItemRefresh:
			// "R" on the home page: re-loop to reload the inventory
			// and live session/tunnel state, then re-render the picker.
			continue
		case ui.ItemAdd:
			code := runAdd(connectFlagsAsAddArgs(flags), stdout, stderr)
			if code != 0 || flags.Select != "" {
				return code
			}
			continue
		case ui.ItemEdit:
			code := runEdit(connectFlagsAsEditArgs(flags), stdout, stderr)
			if code != 0 || flags.Select != "" {
				return code
			}
			continue
		case ui.ItemProxy:
			code, returnHome := runProxyBuilder(flags, inventory, stdout, stderr)
			if returnHome && code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemJump:
			code := runJump(connectFlagsAsJumpArgs(flags), stdout, stderr)
			if code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemForward:
			code, returnHome := runForwardBuilder(flags, inventory, stdout, stderr)
			if returnHome && code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemTransferFile:
			code, returnHome := runTransferFileBuilder(flags, inventory, stdout, stderr)
			if returnHome && code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemCheck:
			code, returnHome := runCheckPicker(flags, inventory, stdout, stderr)
			if returnHome && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemForwardSaved:
			code, returnHome := runForwardPreset(flags, item, stdout, stderr)
			if returnHome && code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemProxySaved:
			code, returnHome := runProxyPreset(flags, item, stdout, stderr)
			if returnHome && code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemForwardActive:
			// One-tap stop: item.Token is the session ID; runForward
			// dispatches to runForwardStop which SIGHUPs the daemon.
			stopArgs := []string{"stop"}
			if flags.StateDir != "" {
				stopArgs = append(stopArgs, "--state-dir", flags.StateDir)
			}
			stopArgs = append(stopArgs, item.Token)
			code := runForward(stopArgs, stdout, stderr)
			if code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemProxyActive:
			stopArgs := []string{"stop"}
			if flags.StateDir != "" {
				stopArgs = append(stopArgs, "--state-dir", flags.StateDir)
			}
			stopArgs = append(stopArgs, item.Token)
			code := runProxy(stopArgs, stdout, stderr)
			if code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemStopAllActive:
			stopArgs := []string{"stop-all"}
			if flags.StateDir != "" {
				stopArgs = append(stopArgs, "--state-dir", flags.StateDir)
			}
			code := runSession(stopArgs, stdout, stderr, build)
			if code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemAuthkeys:
			code := runAuthkeys(nil, stdout, stderr)
			if code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemIncoming:
			code := runIncomingList(nil, stdout, stderr)
			if code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemSessions:
			code, returnHome := runSessionToolsPicker(flags, stdout, stderr, build)
			if returnHome && code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemTheme:
			code := runTheme(connectFlagsAsThemeArgs(flags), stdout, stderr)
			if code == 0 && flags.Select == "" {
				continue
			}
			return code
		case ui.ItemDocs:
			code, returnHome := runDocsPicker(stdout, stderr, flags)
			if returnHome && code == 0 && flags.Select == "" {
				continue
			}
			return code
		}

		base := resolveSSHCommand(flags)
		metadata := session.Metadata{
			TargetAlias: alias.Name,
			Route:       []string{alias.Name},
		}
		cmd := sshcmd.BuildDirect(base, alias.Name, flags.SSHArgs)

		if flags.Print {
			printCmd := sshcmd.WithConnectTimeout(cmd, sshcmd.DefaultConnectTimeoutSeconds)
			if !flags.Direct {
				printCmd = sshcmd.WithSessionEnvForwarding(printCmd)
			}
			if flags.JSON {
				if err := sshcmd.WritePrintJSON(stdout, printCmd, alias.Name); err != nil {
					fmt.Fprintf(stderr, "ssherpa: write JSON: %v\n", err)
					return 1
				}
				return 0
			}
			fmt.Fprintf(stdout, "[print] %s\n", sshcmd.QuoteArgv(printCmd.Argv))
			return 0
		}

		return printOrRunSSH(cmd, flags.connectOptions(sshcmd.BuildProbe(base, metadata.TargetAlias, metadata.Hops)), metadata, stdout, stderr)
	}
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
			value, ok = requireBinaryFlagValue(value, "--ssh-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case strings.HasPrefix(arg, "--ssh-binary="):
			value, ok := requireBinaryFlagValue(strings.TrimPrefix(arg, "--ssh-binary="), "--ssh-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
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
		case arg == "--overlay-key":
			value, ok := nextArg(args, &i, stderr, "--overlay-key")
			if !ok {
				return flags, false
			}
			key, name, ok := parseControlKey(value, stderr, "--overlay-key")
			if !ok {
				return flags, false
			}
			flags.OverlayKey = key
			flags.OverlayKeyName = name
		case strings.HasPrefix(arg, "--overlay-key="):
			key, name, ok := parseControlKey(strings.TrimPrefix(arg, "--overlay-key="), stderr, "--overlay-key")
			if !ok {
				return flags, false
			}
			flags.OverlayKey = key
			flags.OverlayKeyName = name
		case arg == "--no-record":
			flags.NoRecord = true
		case arg == "--record-max-bytes":
			value, ok := nextArg(args, &i, stderr, "--record-max-bytes")
			if !ok {
				return flags, false
			}
			size, err := transcript.ParseSize(value)
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: --record-max-bytes: %v\n", err)
				return flags, false
			}
			flags.RecordMaxBytes = size
		case strings.HasPrefix(arg, "--record-max-bytes="):
			size, err := transcript.ParseSize(strings.TrimPrefix(arg, "--record-max-bytes="))
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: --record-max-bytes: %v\n", err)
				return flags, false
			}
			flags.RecordMaxBytes = size
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
	if flags.Direct && flags.OverlayKey != 0 {
		fmt.Fprintln(stderr, "ssherpa: --overlay-key requires supervised mode; remove --direct")
		return false
	}
	overlayKey, overlayName := session.OverlayHotkey, session.OverlayHotkeyName
	if flags.OverlayKey != 0 {
		overlayKey, overlayName = flags.OverlayKey, flags.OverlayKeyName
	}
	composerKey, composerName := session.ComposerHotkey, session.ComposerHotkeyName
	if flags.ComposerKey != 0 {
		composerKey, composerName = flags.ComposerKey, flags.ComposerKeyName
	}
	if !flags.NoComposer && overlayKey == composerKey {
		fmt.Fprintf(stderr, "ssherpa: overlay key %s conflicts with composer key %s; change one of them\n", overlayName, composerName)
		return false
	}
	return true
}

// reportThemeWarnings surfaces non-fatal theme-config diagnostics
// (unknown role keys, most likely written by a newer ssherpa) once at
// startup. The theme still loads — known roles apply — but the
// operator should hear that part of the config was ignored.
func reportThemeWarnings(themeFile string, stderr io.Writer) {
	path, err := termstyle.ThemeConfigPath(themeFile, nil)
	if err != nil || path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	cfg, err := termstyle.ParseThemeConfig(data)
	if err != nil {
		return
	}
	for _, warning := range cfg.Warnings {
		fmt.Fprintf(stderr, "ssherpa: theme config %s: %s\n", path, warning)
	}
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
	if key == 0x00 || key == 0x03 || key == 0x04 || key == 0x0d || key == 0x0a || key == 0x1b || key == 0x7f {
		fmt.Fprintf(stderr, "ssherpa: %s cannot use reserved key %s\n", flag, label)
		return 0, "", false
	}
	return key, label, true
}

func selectConnectItem(flags connectFlags, graph *sshconfig.Graph, inventory hostlist.Inventory, stderr io.Writer, build BuildInfo) (ui.Item, hostlist.Alias, bool, int) {
	if flags.Select != "" {
		alias := findAlias(inventory.Aliases, flags.Select)
		if alias == nil {
			fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", flags.Select)
			return ui.Item{}, hostlist.Alias{}, false, 2
		}
		return ui.Item{Kind: ui.ItemAlias, Token: alias.Name, Title: alias.Name}, *alias, true, 0
	}

	sessionCount, activeSessions := pickerSessionCounts(flags.StateDir)
	activeTunnels := pickerActiveTunnels(flags.StateDir)
	activeProxies := pickerActiveProxies(flags.StateDir)
	incomingSSH := pickerIncomingSessions()
	stoppableSessions := pickerStoppableSessionCount(flags.StateDir)
	savedForwards := pickerSavedForwards(flags.StateDir, activeSavedNames(activeTunnels))
	savedProxies := pickerSavedProxies(flags.StateDir, activeSavedNames(activeProxies))
	item, ok, err := ui.Pick(context.Background(), ui.BuildItemsWithOptions(inventory.Aliases, ui.BuildItemsOptions{
		SessionCount:       sessionCount,
		ActiveSessionCount: activeSessions,
		SavedForwards:      savedForwards,
		SavedProxies:       savedProxies,
		ActiveTunnels:      activeTunnels,
		ActiveProxies:      activeProxies,
		StopAllActiveCount: stoppableSessions,
		IncomingSSH:        incomingSSH,
	}), ui.PickOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "ssherpa",
		Version:     pickerVersionLabel(build),
		Subtitle:    pickerMode(flags),
		Summary:     pickerSummary(flags, graph, inventory, sessionCount, activeSessions, len(activeTunnels)+len(activeProxies), len(incomingSSH)),
		Refreshable: true,
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

// pickerVersionLabel renders the build version (or "dev" when ldflags
// didn't inject one — go run / go build without -X) for the
// home-page header. Leading "v" is added for tagged builds so the
// label reads "v1.1.0" rather than "1.1.0"; "dev" passes through
// unchanged so a developer build is obvious at a glance.
func pickerVersionLabel(build BuildInfo) string {
	v := strings.TrimSpace(build.Version)
	if v == "" || v == "dev" {
		return "dev"
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
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

func pickerSummary(flags connectFlags, graph *sshconfig.Graph, inventory hostlist.Inventory, sessionCount int, activeSessions int, activeTunnels int, incomingSSH int) []string {
	var summary []string
	warnings := len(inventory.Diagnostics)
	for _, alias := range inventory.Aliases {
		warnings += len(alias.Warnings)
	}
	counts := []string{
		countLabel(len(inventory.Aliases), "host"),
		countLabel(warnings, "warning"),
		countLabel(activeSessions, "session"),
		countLabel(activeTunnels, "tunnel"),
	}
	if incomingSSH > 0 {
		counts = append(counts, countLabel(incomingSSH, "incoming"))
	}
	summary = append(summary, strings.Join(counts, "  "))
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
	if flags.OverlayKeyName != "" {
		scope = append(scope, "overlay="+flags.OverlayKeyName)
	}
	if flags.NoRecord {
		scope = append(scope, "recording=off")
	} else if flags.RecordMaxBytes > 0 {
		scope = append(scope, fmt.Sprintf("record-max=%d", flags.RecordMaxBytes))
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

func pickerIncomingSessions() []ui.IncomingItem {
	sessions, err := incoming.List(incoming.Options{Env: os.Environ()})
	if err != nil {
		return nil
	}
	now := time.Now()
	items := make([]ui.IncomingItem, 0, len(sessions))
	for _, session := range sessions {
		title := strings.TrimSpace(session.User + " " + session.TTY)
		if title == "" {
			title = defaultString(session.TTY, "incoming ssh")
		}
		items = append(items, ui.IncomingItem{
			Token:       session.TTY,
			Title:       title,
			Description: incomingDescription(session, now),
			Detail:      incomingDetail(session),
			SSHerpa:     session.SSHerpa,
		})
	}
	return items
}

func incomingDescription(session incoming.Session, now time.Time) string {
	parts := []string{}
	if session.ClientIP != "" {
		parts = append(parts, "from "+session.ClientIP)
	} else if session.Host != "" {
		parts = append(parts, "from "+session.Host)
	}
	if session.SSHerpa {
		parts = append(parts, "ssherpa")
	} else {
		parts = append(parts, "ssh")
	}
	if !session.LoginAt.IsZero() {
		uptime := now.Sub(session.LoginAt)
		if uptime > 0 {
			parts = append(parts, "up "+humanShortDuration(uptime))
		}
	}
	return strings.Join(parts, " · ")
}

func incomingDetail(session incoming.Session) string {
	parts := []string{}
	if len(session.Route) > 0 {
		parts = append(parts, "route "+strings.Join(session.Route, " -> "))
	}
	if session.OriginHost != "" {
		parts = append(parts, "origin "+session.OriginHost)
	}
	if session.SSHerpaSessionID != "" {
		parts = append(parts, "session "+session.SSHerpaSessionID)
	}
	if len(parts) == 0 && session.Raw != "" {
		return session.Raw
	}
	return strings.Join(parts, " · ")
}

func countLabel(count int, singular string) string {
	label := singular
	if count != 1 && singular != "incoming" {
		label += "s"
	}
	return fmt.Sprintf("%d %s", count, label)
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
		if record.RemoteMirror {
			continue
		}
		if state.ProcessAlive(record) {
			active++
		}
	}
	total = 0
	for _, record := range records {
		if !record.RemoteMirror {
			total++
		}
	}
	return total, active
}

func pickerStoppableSessionCount(stateDir string) int {
	dir, err := state.ResolveDir(stateDir)
	if err != nil {
		return 0
	}
	records, err := state.ListRecords(dir)
	if err != nil {
		return 0
	}
	count := 0
	for _, record := range records {
		if state.ProcessAlive(record) {
			count++
		}
	}
	return count
}

// pickerActiveTunnels flattens live KindTunnel records into the
// picker's "Active Tunnels" projection. Only records whose daemon
// process is still alive are surfaced — orphan records (daemon
// crashed without writing EndedAt) stay invisible here to keep the
// home page focused on actionable items. Use `ssherpa forward list`
// to see orphans.
func pickerActiveTunnels(stateDir string) []ui.ActiveTunnelItem {
	dir, err := state.ResolveDir(stateDir)
	if err != nil {
		return nil
	}
	records, err := state.ListRecords(dir)
	if err != nil {
		return nil
	}
	var out []ui.ActiveTunnelItem
	now := time.Now()
	for _, r := range records {
		if r.Kind != state.KindTunnel {
			continue
		}
		if r.EndedAt != nil {
			continue
		}
		if !state.ProcessAlive(r) {
			continue
		}
		out = append(out, ui.ActiveTunnelItem{
			SessionID:   r.ID,
			Title:       activeTunnelTitle(r),
			Description: activeTunnelDescription(r, now),
		})
	}
	return out
}

func pickerActiveProxies(stateDir string) []ui.ActiveTunnelItem {
	dir, err := state.ResolveDir(stateDir)
	if err != nil {
		return nil
	}
	records, err := state.ListRecords(dir)
	if err != nil {
		return nil
	}
	var out []ui.ActiveTunnelItem
	now := time.Now()
	for _, r := range records {
		if r.Kind != state.KindProxy || r.EndedAt != nil || !state.ProcessAlive(r) {
			continue
		}
		out = append(out, ui.ActiveTunnelItem{
			SessionID:   r.ID,
			Title:       activeProxyTitle(r),
			Description: activeProxyDescription(r, now),
		})
	}
	return out
}

// activeTunnelTitle picks the most recognizable label for a live
// tunnel row: saved alias if the launch came from the catalog,
// otherwise the SSH destination alias, falling back to a short
// suffix of the session ID for truly anonymous tunnels.
func activeTunnelTitle(r state.SessionRecord) string {
	if r.Forward != nil && r.Forward.SavedAlias != "" {
		return r.Forward.SavedAlias
	}
	if r.TargetAlias != "" {
		return r.TargetAlias
	}
	if len(r.ID) > 12 {
		return "session " + r.ID[len(r.ID)-12:]
	}
	return r.ID
}

// activeTunnelDescription renders the operator-facing one-liner:
// `LOCAL -> REMOTE · up DURATION`. Compact so the home page row stays
// scannable; process IDs stay in details/status views.
func activeTunnelDescription(r state.SessionRecord, now time.Time) string {
	parts := []string{}
	if r.Forward != nil {
		parts = append(parts, formatEndpointBindOrLoopback(r.Forward.LocalBind, r.Forward.LocalPort)+" -> "+
			formatEndpointBindOrLoopback(r.Forward.RemoteHost, r.Forward.RemotePort))
	}
	uptime := now.Sub(r.StartedAt)
	if uptime > 0 {
		parts = append(parts, "up "+humanShortDuration(uptime))
	}
	if len(parts) == 0 {
		return "running"
	}
	return strings.Join(parts, " · ")
}

func activeProxyTitle(r state.SessionRecord) string {
	if r.Proxy != nil && r.Proxy.SavedAlias != "" {
		return r.Proxy.SavedAlias
	}
	if r.TargetAlias != "" {
		return r.TargetAlias
	}
	if len(r.ID) > 12 {
		return "session " + r.ID[len(r.ID)-12:]
	}
	return r.ID
}

func activeProxyDescription(r state.SessionRecord, now time.Time) string {
	parts := []string{}
	if r.Proxy != nil {
		parts = append(parts, "SOCKS "+formatEndpointBindOrLoopback(r.Proxy.Bind, r.Proxy.Port))
	}
	uptime := now.Sub(r.StartedAt)
	if uptime > 0 {
		parts = append(parts, "up "+humanShortDuration(uptime))
	}
	if len(parts) == 0 {
		return "running"
	}
	return strings.Join(parts, " · ")
}

func formatEndpointBindOrLoopback(bind string, port int) string {
	if bind == "" {
		bind = "127.0.0.1"
	}
	if bind == "127.0.0.1" {
		return fmt.Sprintf(":%d", port)
	}
	if strings.Contains(bind, ":") {
		return fmt.Sprintf("[%s]:%d", bind, port)
	}
	return fmt.Sprintf("%s:%d", bind, port)
}

// humanShortDuration is the picker-row equivalent of
// forward_management.humanDuration — kept private here so it can
// stay short (no "Xd Yh" composite) for narrow home-page rows.
func humanShortDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours())/24)
	}
}

// pickerSavedForwards flattens the StoredForward catalog into the
// picker's lightweight projection. Failures are silent — the home
// page should always render, even if the forwards directory is
// missing or unreadable.
func activeSavedNames(items []ui.ActiveTunnelItem) map[string]bool {
	names := make(map[string]bool, len(items))
	for _, item := range items {
		if item.Title != "" {
			names[item.Title] = true
		}
	}
	return names
}

func pickerSavedForwards(stateDir string, active map[string]bool) []ui.SavedForwardItem {
	dir, err := state.ResolveDir(stateDir)
	if err != nil {
		return nil
	}
	forwards, err := state.ListForwards(dir)
	if err != nil || len(forwards) == 0 {
		return nil
	}
	out := make([]ui.SavedForwardItem, 0, len(forwards))
	for _, f := range forwards {
		if active[f.Name] {
			continue
		}
		desc := formatEndpointBindOrLoopback(f.LocalBind, f.LocalPort) + " -> " + formatEndpointBindOrLoopback(f.RemoteHost, f.RemotePort)
		out = append(out, ui.SavedForwardItem{
			Name:        f.Name,
			Description: desc,
			Detail:      savedForwardDetail(f),
		})
	}
	return out
}

func pickerSavedProxies(stateDir string, active map[string]bool) []ui.SavedForwardItem {
	dir, err := state.ResolveDir(stateDir)
	if err != nil {
		return nil
	}
	proxies, err := state.ListProxies(dir)
	if err != nil || len(proxies) == 0 {
		return nil
	}
	out := make([]ui.SavedForwardItem, 0, len(proxies))
	for _, p := range proxies {
		if active[p.Name] {
			continue
		}
		desc := "SOCKS " + formatEndpointBindOrLoopback(p.Bind, p.Port)
		out = append(out, ui.SavedForwardItem{Name: p.Name, Description: desc, Detail: savedProxyDetail(p)})
	}
	return out
}

func savedForwardDetail(f state.StoredForward) string {
	parts := []string{fmt.Sprintf("alias %s", f.SSHAlias)}
	parts = append(parts, fmt.Sprintf("%s:%d -> %s:%d", defaultString(f.LocalBind, "127.0.0.1"), f.LocalPort, f.RemoteHost, f.RemotePort))
	if f.Through != "" {
		parts = append(parts, "via "+f.Through)
	}
	if f.Description != "" {
		parts = append(parts, f.Description)
	}
	return strings.Join(parts, " · ")
}

func savedProxyDetail(p state.StoredProxy) string {
	parts := []string{fmt.Sprintf("alias %s", p.SSHAlias), proxySavedListener(p)}
	if p.Description != "" {
		parts = append(parts, p.Description)
	}
	return strings.Join(parts, " · ")
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

// jsonSchemaVersion is stamped into top-level --json envelopes so
// scripts can detect future shape changes. Changes within 1.x are
// additive only; the number moves when a field changes meaning.
const jsonSchemaVersion = 1

type listOutput struct {
	SchemaVersion int                    `json:"schema_version"`
	Config        graphSummary           `json:"config"`
	Aliases       []hostlist.Alias       `json:"aliases"`
	Diagnostics   []sshconfig.Diagnostic `json:"diagnostics,omitempty"`
}

type showOutput struct {
	SchemaVersion int                    `json:"schema_version"`
	Config        graphSummary           `json:"config"`
	Alias         *hostlist.Alias        `json:"alias"`
	Diagnostics   []sshconfig.Diagnostic `json:"diagnostics,omitempty"`
}

func runList(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, listUsage)
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
			SchemaVersion: jsonSchemaVersion,
			Config:        summarizeGraph(graph),
			Aliases:       inventory.Aliases,
			Diagnostics:   inventory.Diagnostics,
		})
		return 0
	}

	if len(inventory.Aliases) == 0 {
		// A missing or unreadable config degrades to diagnostics and an
		// empty inventory; bare exit 0 with no output reads as a hang or
		// a bug. Keep the exit code (scripts depend on it) but say what
		// happened and what to do next.
		if flags.Filter != "" || flags.User != "" {
			fmt.Fprintln(stderr, "ssherpa: no hosts matched the current filter")
		} else {
			// Hosts with User git (github.com et al.) are hidden by
			// default, so "no hosts" can mean "only git hosts" — point
			// at the toggle rather than telling that user to add hosts
			// they already have.
			fmt.Fprintf(stderr, "ssherpa: no hosts found in %s; run \"ssherpa add\" to create one (hosts with User git are hidden by default; set SSHERPA_IGNORE_USER_GIT=0 to show them)\n", graph.RootPath)
		}
		return 0
	}

	for _, alias := range inventory.Aliases {
		fmt.Fprintf(stdout, "%s\t%s\n", alias.Name, displayAlias(alias))
	}
	return 0
}

func runShow(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, showUsage)
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
			SchemaVersion: jsonSchemaVersion,
			Config:        summarizeGraph(graph),
			Alias:         alias,
			Diagnostics:   inventory.Diagnostics,
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

// runHelp implements `ssherpa help [COMMAND]`. With no topic it prints
// the global overview; with one topic it prints that command's usage
// block — the same text `ssherpa COMMAND --help` prints.
func runHelp(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 0
	}
	if len(args) > 1 {
		fmt.Fprintf(stderr, "ssherpa: help accepts at most one command: %s\n", strings.Join(args, " "))
		return 1
	}
	topic := args[0]
	if topic == "--help" || topic == "-h" {
		fmt.Fprint(stdout, helpUsage)
		return 0
	}
	topicUsage, ok := helpTopicUsage(topic)
	if !ok {
		fmt.Fprintf(stderr, "ssherpa: unknown help topic %q; run \"ssherpa help\" for the command list\n", topic)
		return 1
	}
	fmt.Fprint(stdout, topicUsage)
	return 0
}

// helpTopicUsage maps a dispatched command name (or alias) to its
// usage block. Every command in the Run switch has an entry, plus
// "connect" for the no-command picker/ssh mode.
func helpTopicUsage(name string) (string, bool) {
	switch name {
	case "connect":
		return connectUsage, true
	case "add":
		return addUsage, true
	case "edit":
		return editUsage, true
	case "jump":
		return jumpUsage, true
	case "proxy":
		return proxyUsage, true
	case "forward":
		return forwardUsage, true
	case "send":
		return sendUsage, true
	case "receive", "recv":
		return receiveUsage, true
	case "check":
		return checkUsage, true
	case "incoming":
		return incomingUsage, true
	case "list":
		return listUsage, true
	case "show":
		return showUsage, true
	case "authkeys":
		return authkeysUsage, true
	case "theme":
		return themeUsage, true
	case "session":
		return sessionUsage, true
	case "version":
		return versionUsage, true
	case "help":
		return helpUsage, true
	default:
		return "", false
	}
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
