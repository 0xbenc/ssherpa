package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/session"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/transcript"
	"github.com/0xbenc/ssherpa/internal/ui"
)

const (
	defaultProxyBind   = "127.0.0.1"
	defaultProxyPort   = 1080
	defaultForwardBind = "127.0.0.1"
)

type jumpFlags struct {
	connectFlags
	Destination   string
	Hops          []string
	RouteProvided bool
}

type proxyFlags struct {
	connectFlags
	Bind             string
	Port             int
	PortSet          bool
	Background       bool
	savedFromCatalog string
}

type forwardFlags struct {
	connectFlags
	LocalBind  string
	LocalPort  int
	LocalSet   bool
	RemoteHost string
	RemotePort int
	RemoteSet  bool
	Through    string

	// Reconnect knobs (Phase 2a). All are optional — DefaultReconnect()
	// supplies sensible values, and the tunnel-kind gate inside the
	// supervisor decides whether reconnect runs at all.
	NoReconnect             bool
	ReconnectMaxAttempts    int
	ReconnectMaxAttemptsSet bool
	ReconnectInitialBackoff time.Duration
	ReconnectMaxBackoff     time.Duration

	// Background (Phase 2b) detaches the supervisor: the parent
	// process spawns a child via os.StartProcess with
	// SysProcAttr.Setsid, prints the child PID + session ID, and
	// exits. The child runs in detached mode (no PTY raw mode, no
	// stdin loop) but otherwise uses the same RunSupervised retry
	// loop.
	Background bool

	// savedFromCatalog is set internally when the catalog lookup
	// (Phase 2e) populated --local/--remote/--through defaults from
	// a StoredForward of this name. It is NOT a user-settable flag;
	// runForwardWith uses it to stamp Forward.SavedAlias on the
	// resulting session record so `forward list` can show which
	// named tunnel the row belongs to.
	savedFromCatalog string
}

type connectOptions struct {
	Print          bool
	Supervise      bool
	StateDir       string
	Config         string
	SSHBinary      string
	Watchdog       session.WatchdogOptions
	Composer       session.ComposerOptions
	OverlayKey     byte
	OverlayKeyName string
	Reconnect      session.ReconnectOptions
	MuxerGuard     session.MuxerGuardSettings
	NoColor        bool
	ThemeName      string
	ThemeFile      string
	NoRecord       bool
	RecordMaxBytes int64
	// Detached and RecordID flow through to session.Options for the
	// `forward --background` daemon path. Other commands leave these
	// at their zero values and get the interactive supervisor.
	Detached bool
	RecordID string
}

func (flags connectFlags) connectOptions(probe sshcmd.Command) connectOptions {
	return connectOptions{
		Print:     flags.Print,
		Supervise: !flags.Direct,
		StateDir:  flags.StateDir,
		Config:    flags.Config,
		SSHBinary: flags.SSHBinary,
		Watchdog: session.WatchdogOptions{
			WarnThreshold:   flags.LatencyWarn,
			DisconnectAfter: flags.LatencyDisconnect,
			ProbeCommand:    probe,
		},
		Composer: session.ComposerOptions{
			Disabled:   flags.NoComposer,
			Hotkey:     flags.ComposerKey,
			HotkeyName: flags.ComposerKeyName,
		},
		OverlayKey:     flags.OverlayKey,
		OverlayKeyName: flags.OverlayKeyName,
		MuxerGuard:     muxerGuardSettings(flags.NoMuxerGuard),
		NoColor:        flags.NoColor,
		ThemeName:      flags.ThemeName,
		ThemeFile:      flags.ThemeFile,
		NoRecord:       flags.NoRecord,
		RecordMaxBytes: flags.RecordMaxBytes,
	}
}

func runJump(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseJumpFlags(args, stderr)
	if !ok {
		return 1
	}
	if !validateLatencyFlags(flags.connectFlags, stderr) {
		return 1
	}
	if !validateComposerFlags(flags.connectFlags, stderr) {
		return 1
	}
	if !validateThemeFlags(flags.connectFlags, stderr) {
		return 1
	}
	if flags.JSON {
		fmt.Fprintln(stderr, "ssherpa: --json is not supported for jump")
		return 1
	}

	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	destination, hops, ok, code := resolveJumpRoute(flags, inventory, stderr)
	if !ok {
		return code
	}

	if err := sshcmd.ValidateJumpRoute(destination, hops); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	base := resolveSSHCommand(flags.connectFlags)
	cmd := sshcmd.BuildJump(base, destination, hops, flags.SSHArgs)
	metadata := session.Metadata{
		TargetAlias: destination,
		Hops:        hops,
		Route:       append(append([]string(nil), hops...), destination),
	}
	return printOrRunSSH(cmd, flags.connectOptions(sshcmd.BuildProbe(base, metadata.TargetAlias, metadata.Hops)), metadata, stdout, stderr)
}

func runProxy(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runProxyList(args[1:], stdout, stderr)
		case "status":
			return runProxyStatus(args[1:], stdout, stderr)
		case "stop":
			return runProxyStop(args[1:], stdout, stderr)
		case "saved":
			return runProxySaved(args[1:], stdout, stderr)
		}
	}
	return runProxyWith(args, false, "", stdout, stderr)
}

func runProxyWith(args []string, detached bool, recordID string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseProxyFlags(args, stderr)
	if !ok {
		return 1
	}
	if !validateLatencyFlags(flags.connectFlags, stderr) {
		return 1
	}
	if !validateComposerFlags(flags.connectFlags, stderr) {
		return 1
	}
	if !validateThemeFlags(flags.connectFlags, stderr) {
		return 1
	}
	if flags.JSON {
		fmt.Fprintln(stderr, "ssherpa: --json is not supported for proxy")
		return 1
	}
	if flags.Select != "" && !flags.PortSet {
		if stateDir, err := state.ResolveDir(flags.StateDir); err == nil {
			if saved, err := state.ReadProxy(stateDir, flags.Select); err == nil {
				applyProxyCatalogDefaults(&flags, saved)
				flags.savedFromCatalog = saved.Name
				touchProxyLastLaunched(stateDir, saved)
			}
		}
	}
	// Security gate: by now flags.Select is the destination handed to the
	// argv builder — either typed on the CLI or loaded from a saved-proxy
	// catalog entry (whose JSON could have been tampered with). A
	// dash-prefixed name would be parsed by OpenSSH as an option.
	if flags.Select != "" {
		if err := sshcmd.ValidateDestination(flags.Select); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
	}
	if flags.Background && flags.Print {
		fmt.Fprintln(stderr, "ssherpa: --background and --print are mutually exclusive")
		return 1
	}
	if flags.Background && flags.Direct {
		fmt.Fprintln(stderr, "ssherpa: --background requires supervised mode; remove --direct")
		return 1
	}

	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	if !flags.PortSet && flags.Select == "" {
		port, ok := promptProxyPort(stderr)
		if !ok {
			return 1
		}
		flags.Port = port
	}

	alias, ok, code := resolveProxyAlias(flags, inventory, stderr)
	if !ok {
		return code
	}

	if err := sshcmd.ValidateProxy(alias.Name, flags.Bind, flags.Port); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	base := resolveSSHCommand(flags.connectFlags)
	cmd := sshcmd.BuildProxy(base, alias.Name, flags.Bind, flags.Port, flags.SSHArgs)
	if flags.Background && !detached {
		if !validateSSHCommandBinary(sshcmd.WithSessionEnvForwarding(sshcmd.WithConnectTimeout(cmd, sshcmd.DefaultConnectTimeoutSeconds)), flags.SSHBinary, stderr) {
			return 1
		}
		return daemonizeProxy(args, flags, stdout, stderr)
	}

	metadata := session.Metadata{
		TargetAlias: alias.Name,
		Route:       []string{alias.Name},
		Kind:        state.KindProxy,
		Proxy: &state.ProxySpec{
			Bind:       flags.Bind,
			Port:       flags.Port,
			SavedAlias: flags.savedFromCatalog,
			Detached:   detached,
		},
	}
	opts := flags.connectOptions(sshcmd.BuildProbe(base, metadata.TargetAlias, metadata.Hops))
	opts.Reconnect = session.DefaultReconnect()
	opts.Detached = detached
	opts.RecordID = recordID
	return printOrRunSSH(cmd, opts, metadata, stdout, stderr)
}

func runForward(args []string, stdout io.Writer, stderr io.Writer) int {
	// Management subcommands (Phase 2c) take precedence over the
	// alias/launch path. `ssherpa forward list/status/stop ...` route
	// here. A user who has an SSH alias literally named "list" can
	// disambiguate with `ssherpa forward --select list ...`.
	if len(args) > 0 {
		switch args[0] {
		case "list":
			return runForwardList(args[1:], stdout, stderr)
		case "status":
			return runForwardStatus(args[1:], stdout, stderr)
		case "stop":
			return runForwardStop(args[1:], stdout, stderr)
		case "saved":
			return runForwardSaved(args[1:], stdout, stderr)
		}
	}
	return runForwardWith(args, false, "", stdout, stderr)
}

// runForwardWith is the shared body of `forward` invocations. The
// foreground entry point (runForward) calls with detached=false; the
// hidden --__supervisor child path (runSupervisorChild) calls with
// detached=true and a pre-assigned recordID. The single body keeps
// flag parsing and validation identical across both paths.
func runForwardWith(args []string, detached bool, recordID string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseForwardFlags(args, stderr)
	if !ok {
		return 1
	}
	if !validateLatencyFlags(flags.connectFlags, stderr) {
		return 1
	}
	if !validateComposerFlags(flags.connectFlags, stderr) {
		return 1
	}
	if !validateThemeFlags(flags.connectFlags, stderr) {
		return 1
	}
	if flags.JSON {
		fmt.Fprintln(stderr, "ssherpa: --json is not supported for forward")
		return 1
	}
	// Phase 2e: if --select matches a saved-forward name AND neither
	// --local nor --remote is set on the CLI, populate the forward
	// spec from the catalog. CLI args always win — explicit local /
	// remote / through values override the catalog defaults — and an
	// unmatched name silently falls through to the standard "alias
	// not found" path below.
	if flags.Select != "" && !flags.LocalSet && !flags.RemoteSet {
		if stateDir, err := state.ResolveDir(flags.StateDir); err == nil {
			if saved, err := state.ReadForward(stateDir, flags.Select); err == nil {
				applyCatalogDefaults(&flags, saved)
				// Record that this launch came from the catalog so the
				// session record's Forward.SavedAlias reflects it.
				flags.savedFromCatalog = saved.Name
				touchForwardLastLaunched(stateDir, saved)
			}
		}
	}
	// Security gate: flags.Select and flags.Through are the destinations
	// handed to the argv builder — typed on the CLI or loaded from a
	// saved-forward catalog entry (whose JSON could have been tampered
	// with). A dash-prefixed name would be parsed by OpenSSH as an option.
	if flags.Select != "" {
		if err := sshcmd.ValidateDestination(flags.Select); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
	}
	if flags.Through != "" {
		if err := sshcmd.ValidateDestination(flags.Through); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
	}
	if !flags.LocalSet {
		fmt.Fprintln(stderr, "ssherpa: forward requires --local PORT or --local BIND:PORT")
		return 1
	}
	if !flags.RemoteSet {
		fmt.Fprintln(stderr, "ssherpa: forward requires --remote HOST:PORT")
		return 1
	}
	if flags.Background && flags.Print {
		fmt.Fprintln(stderr, "ssherpa: --background and --print are mutually exclusive")
		return 1
	}
	if flags.Background && flags.Direct {
		fmt.Fprintln(stderr, "ssherpa: --background requires supervised mode; remove --direct")
		return 1
	}

	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	alias, ok, code := resolveForwardAlias(flags, inventory, stderr)
	if !ok {
		return code
	}

	if flags.Through != "" && findAlias(inventory.Aliases, flags.Through) == nil {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", flags.Through)
		return 2
	}

	if err := sshcmd.ValidateForward(alias.Name, flags.LocalBind, flags.LocalPort, flags.RemoteHost, flags.RemotePort, flags.Through); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	// Parent process for --background: validation has passed, now fork
	// the child supervisor and exit. The child invocation re-enters
	// this function via the --__supervisor dispatch with detached=true.
	base := resolveSSHCommand(flags.connectFlags)
	cmd := sshcmd.BuildForward(base, alias.Name, flags.LocalBind, flags.LocalPort, flags.RemoteHost, flags.RemotePort, flags.Through, flags.SSHArgs)
	if flags.Background && !detached {
		if !validateSSHCommandBinary(sshcmd.WithSessionEnvForwarding(sshcmd.WithConnectTimeout(cmd, sshcmd.DefaultConnectTimeoutSeconds)), flags.SSHBinary, stderr) {
			return 1
		}
		return daemonizeForward(args, flags, stdout, stderr)
	}

	route := []string{alias.Name}
	var hops []string
	if flags.Through != "" {
		hops = []string{flags.Through}
		route = []string{flags.Through, alias.Name}
	}
	metadata := session.Metadata{
		TargetAlias: alias.Name,
		Hops:        hops,
		Route:       route,
		Kind:        state.KindTunnel,
		Forward: &state.ForwardSpec{
			LocalBind:  flags.LocalBind,
			LocalPort:  flags.LocalPort,
			RemoteHost: flags.RemoteHost,
			RemotePort: flags.RemotePort,
			Through:    flags.Through,
			SavedAlias: flags.savedFromCatalog,
			Detached:   detached,
		},
	}
	opts := flags.connectOptions(sshcmd.BuildProbe(base, metadata.TargetAlias, metadata.Hops))
	opts.Reconnect = forwardReconnectOptions(flags)
	opts.Detached = detached
	opts.RecordID = recordID
	return printOrRunSSH(cmd, opts, metadata, stdout, stderr)
}

func parseJumpFlags(args []string, stderr io.Writer) (jumpFlags, bool) {
	flags := jumpFlags{}

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
		case arg == "--dest" || arg == "--destination":
			value, ok := nextArg(args, &i, stderr, arg)
			if !ok {
				return flags, false
			}
			flags.Destination = value
			flags.RouteProvided = true
		case strings.HasPrefix(arg, "--dest="):
			flags.Destination = strings.TrimPrefix(arg, "--dest=")
			flags.RouteProvided = true
		case strings.HasPrefix(arg, "--destination="):
			flags.Destination = strings.TrimPrefix(arg, "--destination=")
			flags.RouteProvided = true
		case arg == "--hop":
			value, ok := nextArg(args, &i, stderr, "--hop")
			if !ok {
				return flags, false
			}
			flags.Hops = append(flags.Hops, splitHopArg(value)...)
			flags.RouteProvided = true
		case strings.HasPrefix(arg, "--hop="):
			flags.Hops = append(flags.Hops, splitHopArg(strings.TrimPrefix(arg, "--hop="))...)
			flags.RouteProvided = true
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
			fmt.Fprintf(stderr, "ssherpa: unknown jump flag %q\n", arg)
			return flags, false
		default:
			if flags.Destination == "" {
				flags.Destination = arg
			} else {
				flags.Hops = append(flags.Hops, arg)
			}
			flags.RouteProvided = true
		}
	}

	return flags, true
}

func parseProxyFlags(args []string, stderr io.Writer) (proxyFlags, bool) {
	flags := proxyFlags{
		Bind: defaultProxyBind,
		Port: defaultProxyPort,
	}

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
		case arg == "--bind":
			value, ok := nextArg(args, &i, stderr, "--bind")
			if !ok {
				return flags, false
			}
			flags.Bind = value
		case strings.HasPrefix(arg, "--bind="):
			flags.Bind = strings.TrimPrefix(arg, "--bind=")
		case arg == "--port":
			value, ok := nextArg(args, &i, stderr, "--port")
			if !ok {
				return flags, false
			}
			port, ok := parseProxyPort(value, stderr)
			if !ok {
				return flags, false
			}
			flags.Port = port
			flags.PortSet = true
		case strings.HasPrefix(arg, "--port="):
			port, ok := parseProxyPort(strings.TrimPrefix(arg, "--port="), stderr)
			if !ok {
				return flags, false
			}
			flags.Port = port
			flags.PortSet = true
		case arg == "--background":
			flags.Background = true
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
			fmt.Fprintf(stderr, "ssherpa: unknown proxy flag %q\n", arg)
			return flags, false
		default:
			if flags.Select != "" {
				fmt.Fprintf(stderr, "ssherpa: proxy accepts only one alias before --: %s\n", arg)
				return flags, false
			}
			flags.Select = arg
		}
	}

	return flags, true
}

func parseForwardFlags(args []string, stderr io.Writer) (forwardFlags, bool) {
	flags := forwardFlags{}

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
		case arg == "--local":
			value, ok := nextArg(args, &i, stderr, "--local")
			if !ok {
				return flags, false
			}
			bind, port, ok := parseForwardLocal(value, stderr)
			if !ok {
				return flags, false
			}
			flags.LocalBind = bind
			flags.LocalPort = port
			flags.LocalSet = true
		case strings.HasPrefix(arg, "--local="):
			bind, port, ok := parseForwardLocal(strings.TrimPrefix(arg, "--local="), stderr)
			if !ok {
				return flags, false
			}
			flags.LocalBind = bind
			flags.LocalPort = port
			flags.LocalSet = true
		case arg == "--remote":
			value, ok := nextArg(args, &i, stderr, "--remote")
			if !ok {
				return flags, false
			}
			host, port, ok := parseForwardRemote(value, stderr)
			if !ok {
				return flags, false
			}
			flags.RemoteHost = host
			flags.RemotePort = port
			flags.RemoteSet = true
		case strings.HasPrefix(arg, "--remote="):
			host, port, ok := parseForwardRemote(strings.TrimPrefix(arg, "--remote="), stderr)
			if !ok {
				return flags, false
			}
			flags.RemoteHost = host
			flags.RemotePort = port
			flags.RemoteSet = true
		case arg == "--through":
			value, ok := nextArg(args, &i, stderr, "--through")
			if !ok {
				return flags, false
			}
			flags.Through = value
		case strings.HasPrefix(arg, "--through="):
			flags.Through = strings.TrimPrefix(arg, "--through=")
		case arg == "--background":
			flags.Background = true
		case arg == "--no-reconnect":
			flags.NoReconnect = true
		case arg == "--reconnect-max":
			value, ok := nextArg(args, &i, stderr, "--reconnect-max")
			if !ok {
				return flags, false
			}
			n, ok := parseReconnectMax(value, stderr)
			if !ok {
				return flags, false
			}
			flags.ReconnectMaxAttempts = n
			flags.ReconnectMaxAttemptsSet = true
		case strings.HasPrefix(arg, "--reconnect-max="):
			n, ok := parseReconnectMax(strings.TrimPrefix(arg, "--reconnect-max="), stderr)
			if !ok {
				return flags, false
			}
			flags.ReconnectMaxAttempts = n
			flags.ReconnectMaxAttemptsSet = true
		case arg == "--reconnect-backoff":
			value, ok := nextArg(args, &i, stderr, "--reconnect-backoff")
			if !ok {
				return flags, false
			}
			d, ok := parseDuration(value, stderr, "--reconnect-backoff")
			if !ok {
				return flags, false
			}
			flags.ReconnectInitialBackoff = d
		case strings.HasPrefix(arg, "--reconnect-backoff="):
			d, ok := parseDuration(strings.TrimPrefix(arg, "--reconnect-backoff="), stderr, "--reconnect-backoff")
			if !ok {
				return flags, false
			}
			flags.ReconnectInitialBackoff = d
		case arg == "--reconnect-max-backoff":
			value, ok := nextArg(args, &i, stderr, "--reconnect-max-backoff")
			if !ok {
				return flags, false
			}
			d, ok := parseDuration(value, stderr, "--reconnect-max-backoff")
			if !ok {
				return flags, false
			}
			flags.ReconnectMaxBackoff = d
		case strings.HasPrefix(arg, "--reconnect-max-backoff="):
			d, ok := parseDuration(strings.TrimPrefix(arg, "--reconnect-max-backoff="), stderr, "--reconnect-max-backoff")
			if !ok {
				return flags, false
			}
			flags.ReconnectMaxBackoff = d
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
			fmt.Fprintf(stderr, "ssherpa: unknown forward flag %q\n", arg)
			return flags, false
		default:
			if flags.Select != "" {
				fmt.Fprintf(stderr, "ssherpa: forward accepts only one alias before --: %s\n", arg)
				return flags, false
			}
			flags.Select = arg
		}
	}

	return flags, true
}

func resolveJumpRoute(flags jumpFlags, inventory hostlist.Inventory, stderr io.Writer) (string, []string, bool, int) {
	if flags.RouteProvided {
		if flags.Destination == "" || len(flags.Hops) == 0 {
			fmt.Fprintln(stderr, "ssherpa: jump requires --dest and at least one --hop when route flags are used")
			return "", nil, false, 1
		}
		// Security gate before the inventory lookup: a dash-prefixed
		// --dest or --hop must fail with the injection rationale, not
		// a generic "alias not found".
		for _, name := range append([]string{flags.Destination}, flags.Hops...) {
			if err := sshcmd.ValidateDestination(name); err != nil {
				fmt.Fprintf(stderr, "ssherpa: %v\n", err)
				return "", nil, false, 1
			}
		}
		if err := sshcmd.ValidateJumpRoute(flags.Destination, flags.Hops); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return "", nil, false, 1
		}
		if missing := missingAliases(inventory.Aliases, append([]string{flags.Destination}, flags.Hops...)); len(missing) > 0 {
			fmt.Fprintf(stderr, "ssherpa: alias not found: %s\n", strings.Join(missing, ", "))
			return "", nil, false, 2
		}
		return flags.Destination, append([]string(nil), flags.Hops...), true, 0
	}

	return pickJumpRoute(inventory.Aliases, flags.NoColor, flags.ThemeName, flags.ThemeFile, stderr)
}

func pickJumpRoute(aliases []hostlist.Alias, noColor bool, themeName string, themeFile string, stderr io.Writer) (string, []string, bool, int) {
	if len(aliases) == 0 {
		fmt.Fprintln(stderr, "[skipped] no aliases available for jump")
		return "", nil, false, 0
	}
	if len(aliases) < 2 {
		fmt.Fprintln(stderr, "[skipped] not enough distinct hosts for a jump")
		return "", nil, false, 0
	}

	dest, ok, err := pickAlias(aliases, noColor, themeName, themeFile, "Jump: pick destination", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return "", nil, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] jump cancelled (destination)")
		return "", nil, false, 0
	}

	firstChoices := aliasesExcluding(aliases, dest.Name, nil)
	firstHop, ok, err := pickAlias(firstChoices, noColor, themeName, themeFile, "Jump: pick first hop", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return "", nil, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] jump cancelled (first hop)")
		return "", nil, false, 0
	}

	hops := []string{firstHop.Name}
	for {
		choices := aliasesExcluding(aliases, dest.Name, hops)
		if len(choices) == 0 {
			break
		}

		choice, ok, err := ui.ChooseJumpHop(context.Background(), choices, ui.JumpHopChooserOptions{
			Input:        os.Stdin,
			Output:       stderr,
			NoAltScreen:  envBool("SSHERPA_NO_ALT_SCREEN"),
			NoColor:      noColor,
			ThemeName:    themeName,
			ThemeFile:    themeFile,
			Destination:  dest.Name,
			Hops:         hops,
			RouteSummary: routeSummary(dest.Name, hops),
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
			return "", nil, false, 1
		}
		if !ok {
			fmt.Fprintln(stderr, "[skipped] jump cancelled (additional hops)")
			return "", nil, false, 0
		}
		if choice.Done {
			break
		}
		hops = append(hops, choice.Alias)
	}

	return dest.Name, hops, true, 0
}

func resolveProxyAlias(flags proxyFlags, inventory hostlist.Inventory, stderr io.Writer) (hostlist.Alias, bool, int) {
	if flags.Select != "" {
		alias := findAlias(inventory.Aliases, flags.Select)
		if alias == nil {
			fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", flags.Select)
			return hostlist.Alias{}, false, 2
		}
		return *alias, true, 0
	}

	alias, ok, err := pickAlias(inventory.Aliases, flags.NoColor, flags.ThemeName, flags.ThemeFile, "Proxy: pick host", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return hostlist.Alias{}, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] proxy cancelled")
		return hostlist.Alias{}, false, 0
	}
	return alias, true, 0
}

func resolveForwardAlias(flags forwardFlags, inventory hostlist.Inventory, stderr io.Writer) (hostlist.Alias, bool, int) {
	if flags.Select != "" {
		alias := findAlias(inventory.Aliases, flags.Select)
		if alias == nil {
			fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", flags.Select)
			return hostlist.Alias{}, false, 2
		}
		return *alias, true, 0
	}

	alias, ok, err := pickAlias(inventory.Aliases, flags.NoColor, flags.ThemeName, flags.ThemeFile, "Forward: pick host", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return hostlist.Alias{}, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] forward cancelled")
		return hostlist.Alias{}, false, 0
	}
	return alias, true, 0
}

func pickAlias(aliases []hostlist.Alias, noColor bool, themeName string, themeFile string, title string, stderr io.Writer) (hostlist.Alias, bool, error) {
	if len(aliases) == 0 {
		return hostlist.Alias{}, false, nil
	}
	token, ok, err := ui.ChooseHost(context.Background(), aliases, ui.HostChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     noColor,
		ThemeName:   themeName,
		ThemeFile:   themeFile,
		Title:       title,
		Mode:        hostChooserMode(title),
		Steps:       hostChooserSteps(title),
		CurrentStep: hostChooserCurrentStep(title),
	})
	if err != nil || !ok {
		return hostlist.Alias{}, ok, err
	}
	alias := findAlias(aliases, token)
	if alias == nil {
		return hostlist.Alias{}, false, fmt.Errorf("selected alias %q disappeared", token)
	}
	return *alias, true, nil
}

func hostChooserMode(title string) string {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "jump: pick destination":
		return "choose jump destination"
	case "jump: pick first hop":
		return "choose first hop"
	case "proxy: pick host":
		return "choose SOCKS proxy host"
	case "forward: pick host":
		return "choose tunnel host"
	case "transfer: pick host":
		return "choose transfer host"
	case "send file: pick target":
		return "choose send target"
	case "receive file: pick source":
		return "choose receive source"
	case "check: pick host":
		return "choose host to check"
	default:
		return "pick host"
	}
}

func hostChooserSteps(title string) []string {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "jump: pick destination", "jump: pick first hop":
		return []string{"destination", "first hop", "extra hops", "run"}
	case "proxy: pick host":
		return []string{"host", "port", "run"}
	case "forward: pick host":
		return []string{"host", "local", "remote", "run"}
	case "transfer: pick host":
		return []string{"host", "paths", "confirm", "complete"}
	case "send file: pick target":
		return []string{"local", "target", "remote", "complete"}
	case "receive file: pick source":
		return []string{"source", "remote", "local", "complete"}
	case "check: pick host":
		return []string{"scope", "host", "results"}
	default:
		return nil
	}
}

func hostChooserCurrentStep(title string) int {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "jump: pick first hop":
		return 1
	case "send file: pick target":
		return 1
	case "check: pick host":
		return 1
	default:
		return 0
	}
}

func promptProxyPort(stderr io.Writer) (int, bool) {
	value, ok, err := promptText(stderr, "Start SOCKS proxy", "port", strconv.Itoa(defaultProxyPort), func(value string) error {
		_, err := parsePortValue(value, "proxy port")
		return err
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read proxy port: %v\n", err)
		return 0, false
	}
	if !ok {
		return 0, false
	}
	return parseProxyPort(value, stderr)
}

func parseProxyPort(value string, stderr io.Writer) (int, bool) {
	port, err := parsePortValue(value, "proxy port")
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 0, false
	}
	return port, true
}

func parsePortValue(value string, label string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be an integer from 1 to 65535", label)
	}
	return port, nil
}

// parseReconnectMax accepts a non-negative integer. 0 means unlimited
// attempts; positive N caps at N attempts. Negative values are
// rejected — they don't have a meaningful interpretation.
func parseReconnectMax(value string, stderr io.Writer) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || n < 0 {
		fmt.Fprintln(stderr, "ssherpa: --reconnect-max must be a non-negative integer (0 = unlimited)")
		return 0, false
	}
	return n, true
}

// forwardReconnectOptions builds the ReconnectOptions for a `forward`
// invocation by starting from session.DefaultReconnect() (100 attempts,
// 1s→60s capped exponential) and overlaying any CLI-set knobs.
// Enabled defaults to true for tunnels; --no-reconnect flips it off.
func forwardReconnectOptions(flags forwardFlags) session.ReconnectOptions {
	opts := session.DefaultReconnect()
	opts.Enabled = !flags.NoReconnect
	if flags.ReconnectMaxAttemptsSet {
		opts.MaxAttempts = flags.ReconnectMaxAttempts
	}
	if flags.ReconnectInitialBackoff > 0 {
		opts.InitialBackoff = flags.ReconnectInitialBackoff
	}
	if flags.ReconnectMaxBackoff > 0 {
		opts.MaxBackoff = flags.ReconnectMaxBackoff
	}
	return opts
}

// parseForwardLocal wraps sshcmd.ParseForwardLocal with the CLI's
// stderr-emitting error reporting style. The actual parse logic
// lives in sshcmd so the TUI builder can validate input identically.
func parseForwardLocal(value string, stderr io.Writer) (string, int, bool) {
	bind, port, err := sshcmd.ParseForwardLocal(value)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", strings.Replace(err.Error(), "forward local", "--local", 1))
		return "", 0, false
	}
	return bind, port, true
}

// parseForwardRemote wraps sshcmd.ParseForwardRemote with the CLI's
// stderr-emitting error reporting style.
func parseForwardRemote(value string, stderr io.Writer) (string, int, bool) {
	host, port, err := sshcmd.ParseForwardRemote(value)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", strings.Replace(err.Error(), "forward remote", "--remote", 1))
		return "", 0, false
	}
	return host, port, true
}

func splitHopArg(value string) []string {
	parts := strings.Split(value, ",")
	hops := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			hops = append(hops, part)
		}
	}
	return hops
}

func missingAliases(aliases []hostlist.Alias, names []string) []string {
	var missing []string
	for _, name := range names {
		if findAlias(aliases, name) == nil {
			missing = append(missing, name)
		}
	}
	return uniqueStrings(missing)
}

func aliasesExcluding(aliases []hostlist.Alias, destination string, hops []string) []hostlist.Alias {
	excluded := map[string]bool{destination: true}
	for _, hop := range hops {
		excluded[hop] = true
	}

	filtered := make([]hostlist.Alias, 0, len(aliases))
	for _, alias := range aliases {
		if !excluded[alias.Name] {
			filtered = append(filtered, alias)
		}
	}
	return filtered
}

func routeSummary(destination string, hops []string) string {
	route := append([]string(nil), hops...)
	route = append(route, destination)
	return strings.Join(route, " -> ")
}

func resolveSSHCommand(flags connectFlags) sshcmd.Command {
	return sshcmd.Resolve(sshcmd.ResolveOptions{
		SSHBinary: flags.SSHBinary,
		NoKitty:   flags.NoKitty,
		Env:       sshcmd.Env(),
	})
}

func printOrRunSSH(cmd sshcmd.Command, options connectOptions, metadata session.Metadata, stdout io.Writer, stderr io.Writer) int {
	// Defense in depth for every launch path (connect/jump/proxy/forward):
	// destinations normally arrive here from the filtered inventory or an
	// upstream ValidateDestination gate, but a dash-prefixed target must
	// never be printed or spawned even if a future caller forgets one.
	if err := sshcmd.ValidateDestination(metadata.TargetAlias); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	cmd = sshcmd.WithConnectTimeout(cmd, sshcmd.DefaultConnectTimeoutSeconds)
	if options.Supervise {
		cmd = sshcmd.WithSessionEnvForwarding(cmd)
	}

	if options.Print {
		fmt.Fprintf(stdout, "[print] %s\n", sshcmd.QuoteArgv(cmd.Argv))
		return 0
	}

	if !validateSSHCommandBinary(cmd, options.SSHBinary, stderr) {
		return 1
	}

	if options.Supervise {
		fmt.Fprintf(stderr, "[supervise] %s\n", sshcmd.QuoteArgv(cmd.Argv))
		if options.Watchdog.Enabled() {
			fmt.Fprintf(stderr, "[latency] sidecar %s warn=%s", sshcmd.QuoteArgv(options.Watchdog.ProbeCommand.Argv), options.Watchdog.WarnThreshold)
			if options.Watchdog.DisconnectAfter > 0 {
				fmt.Fprintf(stderr, " disconnect-after=%s", options.Watchdog.DisconnectAfter)
			}
			fmt.Fprintln(stderr)
		}
		return session.RunSupervised(cmd, metadata, session.Options{
			StateDir:       options.StateDir,
			Stdin:          os.Stdin,
			Stdout:         stdout,
			Stderr:         stderr,
			Watchdog:       options.Watchdog,
			Composer:       options.Composer,
			Overlay:        overlayTransferOptions(options, metadata, stdout, stderr),
			Reconnect:      options.Reconnect,
			MuxerGuard:     options.MuxerGuard,
			ThemeName:      options.ThemeName,
			ThemeFile:      options.ThemeFile,
			NoColor:        options.NoColor,
			NoRecord:       options.NoRecord,
			RecordMaxBytes: options.RecordMaxBytes,
			Detached:       options.Detached,
			RecordID:       options.RecordID,
		})
	}

	fmt.Fprintf(stderr, "[exec] %s\n", sshcmd.QuoteArgv(cmd.Argv))
	return sshcmd.RunDirect(cmd, os.Stdin, stdout, stderr)
}

func connectFlagsAsJumpArgs(flags connectFlags) []string {
	return connectFlagsAsRouteArgs(flags)
}

func connectFlagsAsProxyArgs(flags connectFlags) []string {
	return connectFlagsAsRouteArgs(flags)
}

func connectFlagsAsForwardArgs(flags connectFlags) []string {
	return connectFlagsAsRouteArgs(flags)
}

// runForwardBuilder is the home-page picker's Forward action: launches
// the TUI wizard, translates the resulting ForwardResult into a
// `ssherpa forward …` argv (or, for Save, writes the catalog entry
// directly), and runs the requested action. The boolean return is true
// when the caller should refresh and show the homepage again.
func runForwardBuilder(flags connectFlags, inventory hostlist.Inventory, stdout io.Writer, stderr io.Writer) (int, bool) {
	aliases := make([]ui.ForwardAlias, 0, len(inventory.Aliases))
	for _, a := range inventory.Aliases {
		aliases = append(aliases, ui.ForwardAlias{
			Name:        a.Name,
			Description: displayAlias(a),
		})
	}
	if len(aliases) == 0 {
		fmt.Fprintln(stderr, "[skipped] no aliases available for forward")
		return 0, true
	}

	result, ok, err := ui.BuildForward(context.Background(), ui.BuildForwardOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Aliases:     aliases,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: forward builder failed: %v\n", err)
		return 1, false
	}
	if !ok || result.Action == ui.ForwardActionCancel {
		fmt.Fprintln(stderr, "[skipped] forward builder cancelled")
		return 0, true
	}

	if result.Action == ui.ForwardActionSave {
		return saveForwardFromBuilder(flags, result, stdout, stderr), true
	}

	args := []string{
		"--select", result.Alias,
		"--local", result.LocalSpec(),
		"--remote", result.RemoteSpec(),
	}
	if result.Through != "" {
		args = append(args, "--through", result.Through)
	}
	switch result.Action {
	case ui.ForwardActionBackground:
		args = append(args, "--background")
	case ui.ForwardActionPrint:
		args = append(args, "--print")
	}
	args = append(args, connectFlagsAsForwardArgs(flags)...)
	return runForward(args, stdout, stderr), result.Action == ui.ForwardActionBackground
}

func runProxyBuilder(flags connectFlags, inventory hostlist.Inventory, stdout io.Writer, stderr io.Writer) (int, bool) {
	aliases := make([]ui.ForwardAlias, 0, len(inventory.Aliases))
	for _, a := range inventory.Aliases {
		aliases = append(aliases, ui.ForwardAlias{Name: a.Name, Description: displayAlias(a)})
	}
	if len(aliases) == 0 {
		fmt.Fprintln(stderr, "[skipped] no aliases available for proxy")
		return 0, true
	}
	result, ok, err := ui.BuildProxy(context.Background(), ui.BuildProxyOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Aliases:     aliases,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: proxy builder failed: %v\n", err)
		return 1, false
	}
	if !ok || result.Action == ui.ForwardActionCancel {
		fmt.Fprintln(stderr, "[skipped] proxy builder cancelled")
		return 0, true
	}
	if result.Action == ui.ForwardActionSave {
		return saveProxyFromBuilder(flags, result, stdout, stderr), true
	}
	args := []string{"--select", result.Alias, "--port", strconv.Itoa(result.Port)}
	if result.Bind != "" && result.Bind != defaultProxyBind {
		args = append(args, "--bind", result.Bind)
	}
	if result.Action == ui.ForwardActionBackground {
		args = append(args, "--background")
	} else if result.Action == ui.ForwardActionPrint {
		args = append(args, "--print")
	}
	args = append(args, connectFlagsAsProxyArgs(flags)...)
	return runProxy(args, stdout, stderr), result.Action == ui.ForwardActionBackground
}

// runForwardPreset handles a saved-forward row picked from the home
// page. The catalog still supplies the actual --local/--remote/--through
// values inside runForwardWith; this function only asks whether the
// preset should run actively in the foreground or as the detached
// background daemon.
func runForwardPreset(flags connectFlags, item ui.Item, stdout io.Writer, stderr io.Writer) (int, bool) {
	action, ok, err := ui.ChooseForwardLaunchAction(context.Background(), ui.ForwardActionOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Name:        item.Title,
		Description: item.Description,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: forward preset picker failed: %v\n", err)
		return 1, false
	}
	if !ok || action == ui.ForwardActionCancel {
		fmt.Fprintln(stderr, "[skipped] forward preset cancelled")
		return 0, true
	}
	return runForward(savedForwardLaunchArgs(flags, item.Token, action), stdout, stderr), action == ui.ForwardActionBackground
}

func runProxyPreset(flags connectFlags, item ui.Item, stdout io.Writer, stderr io.Writer) (int, bool) {
	action, ok, err := ui.ChooseForwardLaunchAction(context.Background(), ui.ForwardActionOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Name:        item.Title,
		Description: item.Description,
		KindLabel:   "SSHERPA PROXY PRESET",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: proxy preset picker failed: %v\n", err)
		return 1, false
	}
	if !ok || action == ui.ForwardActionCancel {
		fmt.Fprintln(stderr, "[skipped] proxy preset cancelled")
		return 0, true
	}
	return runProxy(savedProxyLaunchArgs(flags, item.Token, action), stdout, stderr), action == ui.ForwardActionBackground
}

func savedForwardLaunchArgs(flags connectFlags, name string, action ui.ForwardAction) []string {
	args := []string{"--select", name}
	if action == ui.ForwardActionBackground {
		args = append(args, "--background")
	}
	args = append(args, connectFlagsAsForwardArgs(flags)...)
	return args
}

func savedProxyLaunchArgs(flags connectFlags, name string, action ui.ForwardAction) []string {
	args := []string{"--select", name}
	if action == ui.ForwardActionBackground {
		args = append(args, "--background")
	}
	args = append(args, connectFlagsAsProxyArgs(flags)...)
	return args
}

// saveForwardFromBuilder writes the wizard's Save action into the
// state catalog. The destination alias and ProxyJump live in
// ~/.ssh/config under the alias the user picked — we don't write a
// new Host block (that conflict caused the LocalForward-in-alias
// bug Phase 2e's two-layer split exists to avoid). The catalog
// entry just references the existing SSH alias by name.
func saveForwardFromBuilder(flags connectFlags, result ui.ForwardResult, stdout io.Writer, stderr io.Writer) int {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	spec := state.StoredForward{
		Name:       result.SavedName,
		SSHAlias:   result.Alias,
		LocalBind:  result.LocalBind,
		LocalPort:  result.LocalPort,
		RemoteHost: result.RemoteHost,
		RemotePort: result.RemotePort,
		Through:    result.Through,
	}
	if err := state.WriteForward(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: save forward: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: forward saved as %q\n", spec.Name)
	fmt.Fprintf(stdout, "  ssh alias:  %s\n", spec.SSHAlias)
	fmt.Fprintf(stdout, "  local:      %s\n", result.LocalSpec())
	fmt.Fprintf(stdout, "  remote:     %s\n", result.RemoteSpec())
	if spec.Through != "" {
		fmt.Fprintf(stdout, "  through:    %s\n", spec.Through)
	}
	fmt.Fprintf(stdout, "  launch:     ssherpa forward --select %s\n", spec.Name)
	fmt.Fprintf(stdout, "  daemonize:  ssherpa forward --select %s --background\n", spec.Name)
	return 0
}

func saveProxyFromBuilder(flags connectFlags, result ui.ProxyResult, stdout io.Writer, stderr io.Writer) int {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	spec := state.StoredProxy{
		Name:     result.SavedName,
		SSHAlias: result.Alias,
		Bind:     result.Bind,
		Port:     result.Port,
	}
	if err := state.WriteProxy(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: save proxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: proxy saved as %q\n", spec.Name)
	fmt.Fprintf(stdout, "  ssh alias:  %s\n", spec.SSHAlias)
	fmt.Fprintf(stdout, "  listener:   %s\n", result.ListenerSpec())
	fmt.Fprintf(stdout, "  launch:     ssherpa proxy --select %s\n", spec.Name)
	fmt.Fprintf(stdout, "  daemonize:  ssherpa proxy --select %s --background\n", spec.Name)
	return 0
}

// applyCatalogDefaults overlays a saved forward's spec onto flags
// when --local/--remote weren't set on the CLI. The user's explicit
// --through still wins if they passed one.
func applyCatalogDefaults(flags *forwardFlags, saved state.StoredForward) {
	flags.LocalBind = saved.LocalBind
	if flags.LocalBind == "" {
		flags.LocalBind = sshcmd.DefaultForwardBind
	}
	flags.LocalPort = saved.LocalPort
	flags.LocalSet = true
	flags.RemoteHost = saved.RemoteHost
	flags.RemotePort = saved.RemotePort
	flags.RemoteSet = true
	if flags.Through == "" {
		flags.Through = saved.Through
	}
	flags.Select = saved.SSHAlias
}

// touchForwardLastLaunched bumps the catalog entry's LastLaunchedAt
// timestamp. Best-effort — a failure here doesn't affect launching
// the tunnel, just leaves the catalog freshness stamp stale.
func touchForwardLastLaunched(stateDir string, saved state.StoredForward) {
	now := time.Now().UTC()
	saved.LastLaunchedAt = &now
	_ = state.WriteForward(stateDir, saved)
}

func applyProxyCatalogDefaults(flags *proxyFlags, saved state.StoredProxy) {
	flags.Bind = saved.Bind
	if flags.Bind == "" {
		flags.Bind = defaultProxyBind
	}
	flags.Port = saved.Port
	flags.PortSet = true
	flags.Select = saved.SSHAlias
}

func touchProxyLastLaunched(stateDir string, saved state.StoredProxy) {
	now := time.Now().UTC()
	saved.LastLaunchedAt = &now
	_ = state.WriteProxy(stateDir, saved)
}

func connectFlagsAsRouteArgs(flags connectFlags) []string {
	var args []string
	if flags.All {
		args = append(args, "--all")
	}
	if flags.Print {
		args = append(args, "--print")
	}
	if flags.Filter != "" {
		args = append(args, "--filter", flags.Filter)
	}
	if flags.User != "" {
		args = append(args, "--user", flags.User)
	}
	if flags.Config != "" {
		args = append(args, "--config", flags.Config)
	}
	if flags.SSHBinary != "" {
		args = append(args, "--ssh-binary", flags.SSHBinary)
	}
	if flags.Supervise && !flags.Direct {
		args = append(args, "--supervise")
	}
	if flags.Direct {
		args = append(args, "--direct")
	}
	if flags.StateDir != "" {
		args = append(args, "--state-dir", flags.StateDir)
	}
	if flags.LatencyWarn > 0 {
		args = append(args, "--latency-warn", flags.LatencyWarn.String())
	}
	if flags.LatencyDisconnect > 0 {
		args = append(args, "--latency-disconnect", flags.LatencyDisconnect.String())
	}
	if flags.ComposerKey != 0 {
		args = append(args, "--composer-key", flags.ComposerKeyName)
	}
	if flags.OverlayKey != 0 {
		args = append(args, "--overlay-key", flags.OverlayKeyName)
	}
	if flags.NoComposer {
		args = append(args, "--no-composer")
	}
	if flags.NoRecord {
		args = append(args, "--no-record")
	}
	if flags.RecordMaxBytes > 0 {
		args = append(args, "--record-max-bytes", strconv.FormatInt(flags.RecordMaxBytes, 10))
	}
	if flags.NoKitty {
		args = append(args, "--no-kitty")
	}
	if flags.NoColor {
		args = append(args, "--no-color")
	}
	if flags.ThemeFile != "" {
		args = append(args, "--theme-file", flags.ThemeFile)
	}
	if len(flags.SSHArgs) > 0 {
		args = append(args, "--")
		args = append(args, flags.SSHArgs...)
	}
	return args
}
