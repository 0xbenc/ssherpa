package session

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/inband"
	"github.com/0xbenc/ssherpa/internal/sessionview"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/ssherpa/internal/transcript"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

const (
	RunnerModeSupervised   = "supervised"
	OverlayHotkey          = byte(0x1e)
	OverlayHotkeyName      = "Ctrl-^"
	ComposerHotkey         = byte(0x07)
	ComposerHotkeyName     = "Ctrl-G"
	ComposerSendHotkey     = byte(0x07)
	ComposerSendHotkeyName = "Ctrl-G"

	// EscapeRopeReason marks a session that was torn down by the escape rope
	// (the "disconnect every layer" action in the session overlay). It is
	// recorded as the session's DisconnectReason.
	EscapeRopeReason = "escape_rope"
	// EscapeRopeExitCode is the exit code RunSupervised returns when the escape
	// rope is pulled, so a wrapper can distinguish a deliberate bail-out from a
	// normal logout or an ssh error (255). The value is outside the usual
	// shell/signal ranges and ssherpa's own 0/1/255 codes.
	EscapeRopeExitCode = 120
	// InterruptReason marks a startup connection attempt that was locally
	// cancelled with Ctrl+C before the SSH child produced output.
	InterruptReason = "interrupt"
	// InterruptExitCode mirrors the conventional shell status for SIGINT.
	InterruptExitCode = 130
)

type Metadata struct {
	TargetAlias string
	Hops        []string
	Route       []string
	// Kind tags the recorded session by its high-level shape (e.g.
	// state.KindInteractive, state.KindTunnel, or state.KindProxy). Leave empty for a
	// default interactive session; the session-map renderer treats an
	// empty Kind as interactive for backward compatibility with records
	// written before this field existed.
	Kind string
	// Forward, when non-nil, captures the runtime port-forward spec
	// for a tunnel-kind session. It is copied onto the session record
	// so the management commands (`ssherpa forward list/status/stop`)
	// can read local/remote/through without re-parsing SSHArgv.
	Forward *state.ForwardSpec
	// Proxy, when non-nil, captures the runtime SOCKS proxy spec
	// for a proxy-kind session.
	Proxy *state.ProxySpec
}

type Options struct {
	StateDir       string
	Stdin          *os.File
	Stdout         io.Writer
	Stderr         io.Writer
	Env            []string
	Now            func() time.Time
	Watchdog       WatchdogOptions
	Composer       ComposerOptions
	Overlay        OverlayOptions
	Reconnect      ReconnectOptions
	Theme          termstyle.Theme
	ThemeName      string
	ThemeFile      string
	NoColor        bool
	NoRecord       bool
	RecordMaxBytes int64
	SSHerpaVersion string
	// Detached runs the supervisor in non-interactive daemon mode:
	// no PTY raw mode, no copyInput goroutine, no overlay/composer.
	// forwardSignals stays installed so `ssherpa forward stop` can
	// SIGHUP the daemon and have it tear the ssh client down cleanly.
	// Set by Phase 2b's `forward --background` flow.
	Detached bool
	// RecordID, when non-empty, overrides the auto-generated session
	// ID. Used by the daemon parent process to pre-assign an ID
	// before forking, so it can print the ID + open a log file at
	// <stateDir>/sessions/<id>.log before the child writes its first
	// record.
	RecordID string
}

// ReconnectOptions controls the retry-with-backoff behavior used when a
// supervised tunnel session (Kind == state.KindTunnel) loses its
// underlying SSH process. Interactive sessions ignore these — they are
// one-shot regardless of Reconnect settings.
//
// Default policy (DefaultReconnect()):
//   - 100 attempts cap (safety belt against a misconfigured tunnel
//     churning forever; pass MaxAttempts == 0 explicitly to opt into
//     unlimited).
//   - Capped exponential backoff: 1s, 2s, 4s, 8s, 16s, 32s, 60s, 60s, …
//   - Give up immediately on ssh exit code 1 (bind failure /
//     ExitOnForwardFailure trigger) and on spawn failures — these are
//     not transient.
type ReconnectOptions struct {
	// Enabled gates the retry loop entirely. False = single attempt
	// regardless of failure mode. True = retry per the policy below.
	Enabled bool
	// MaxAttempts caps the number of spawn attempts. 0 means
	// unlimited; any positive value bounds the loop.
	MaxAttempts int
	// InitialBackoff is the wait between attempt 1 and attempt 2.
	// Each subsequent retry doubles the wait, capped at MaxBackoff.
	InitialBackoff time.Duration
	// MaxBackoff caps the exponentially-grown wait.
	MaxBackoff time.Duration
	// Multiplier is the per-attempt growth factor. Defaults to 2.0
	// when zero.
	Multiplier float64
}

const (
	DefaultReconnectMaxAttempts    = 100
	DefaultReconnectInitialBackoff = 1 * time.Second
	DefaultReconnectMaxBackoff     = 60 * time.Second
	DefaultReconnectMultiplier     = 2.0
)

// DefaultReconnect returns a ReconnectOptions populated with the
// Phase 2a defaults. Disabled by default — callers must opt in (the
// `forward` runner does this automatically when Metadata.Kind ==
// state.KindTunnel).
func DefaultReconnect() ReconnectOptions {
	return ReconnectOptions{
		Enabled:        false,
		MaxAttempts:    DefaultReconnectMaxAttempts,
		InitialBackoff: DefaultReconnectInitialBackoff,
		MaxBackoff:     DefaultReconnectMaxBackoff,
		Multiplier:     DefaultReconnectMultiplier,
	}
}

type ComposerOptions struct {
	Disabled   bool
	Hotkey     byte
	HotkeyName string
}

func (c ComposerOptions) enabled() bool {
	return !c.Disabled
}

func (c ComposerOptions) hotkey() byte {
	if c.Hotkey == 0 {
		return ComposerHotkey
	}
	return c.Hotkey
}

func (c ComposerOptions) hotkeyName() string {
	if c.HotkeyName == "" {
		return ComposerHotkeyName
	}
	return c.HotkeyName
}

type OverlayTransferRequest struct {
	Direction    string
	SessionID    string
	StateDir     string
	TargetAlias  string
	Hops         []string
	Route        []string
	ControlPath  string
	RemoteHost   string
	RemoteCWD    string
	RemotePrompt string
	InbandSend   InbandSendFunc
}

type OverlayTransferFunc func(OverlayTransferRequest) int

type InbandSendRequest struct {
	LocalPath  string
	RemotePath string
	MaxBytes   int64
}

type InbandSendResult struct {
	LocalPath  string
	RemotePath string
	Size       int64
	SHA256     string
}

type InbandSendFunc func(InbandSendRequest) (InbandSendResult, error)

type OverlayOptions struct {
	Send    OverlayTransferFunc
	Receive OverlayTransferFunc
	// Key overrides the overlay hotkey byte. Zero means OverlayHotkey.
	Key byte
	// KeyName is the display label matching Key. Empty means
	// OverlayHotkeyName.
	KeyName string
}

func (o OverlayOptions) hotkey() byte {
	if o.Key == 0 {
		return OverlayHotkey
	}
	return o.Key
}

func (o OverlayOptions) hotkeyName() string {
	if o.KeyName == "" {
		return OverlayHotkeyName
	}
	return o.KeyName
}

type WatchdogOptions struct {
	WarnThreshold   time.Duration
	DisconnectAfter time.Duration
	Interval        time.Duration
	ProbeTimeout    time.Duration
	ProbeCommand    sshcmd.Command
	RunProbe        ProbeRunner
}

type ProbeResult struct {
	Duration time.Duration
	Err      error
}

type ProbeRunner func(context.Context, sshcmd.Command) ProbeResult

func (w WatchdogOptions) Enabled() bool {
	return w.WarnThreshold > 0
}

func RunSupervised(command sshcmd.Command, metadata Metadata, opts Options) int {
	stderr := writerOrDiscard(opts.Stderr)
	stdout := writerOrDiscard(opts.Stdout)
	stdin := opts.Stdin
	if stdin == nil {
		stdin = os.Stdin
	}

	if len(command.Argv) == 0 {
		fmt.Fprintln(stderr, "ssherpa: empty SSH command")
		return 1
	}

	stateDir, err := state.ResolveDir(opts.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}

	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	env := opts.Env
	if env == nil {
		env = os.Environ()
	}
	theme, err := resolveSessionTheme(opts, env)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	record := buildRecord(command, metadata, now(), env, opts.RecordID)
	identity, identityErr := state.EnsureMachineIdentity(stateDir, opts.SSHerpaVersion, now().UTC())
	if identityErr != nil {
		fmt.Fprintf(stderr, "ssherpa: machine identity unavailable: %v\n", identityErr)
	} else {
		origin := state.RecordingOriginForIdentity(identity, opts.SSHerpaVersion)
		record.RecordedBy = &origin
	}

	guard := newTerminalGuard()
	var recordMu sync.Mutex
	// A panic in the supervisor's own goroutine must never leave the
	// user's terminal in raw mode or strand the session record open.
	// Restore + finalize, then re-panic so the bug stays loudly
	// visible. TryLock: if the panic escaped a recordMu-held section,
	// taking the lock again would deadlock the unwind.
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		guard.restore()
		if recordMu.TryLock() {
			endedAt := now().UTC()
			record.DisconnectReason = panicDisconnectReason
			record.Events = append(record.Events, state.SessionEvent{
				Time:    endedAt,
				Type:    panicDisconnectReason,
				Message: fmt.Sprintf("supervisor panicked: %v", r),
			})
			if record.EndedAt == nil {
				record.EndedAt = &endedAt
			}
			_ = state.WriteRecord(stateDir, record)
			recordMu.Unlock()
		}
		panic(r)
	}()

	if controlPath, ok, err := prepareControlMaster(stateDir, record.ID, metadata, env); err != nil {
		fmt.Fprintf(stderr, "ssherpa: prepare SSH control socket: %v\n", err)
		return 1
	} else if ok {
		controlled := sshcmd.WithControlMaster(command, controlPath)
		if !sameStrings(controlled.Argv, command.Argv) {
			command = controlled
			record.SSHArgv = append([]string(nil), command.Argv...)
			record.ControlPath = controlPath
			destination := metadata.TargetAlias
			sshBinary := command.Argv[0]
			defer func() {
				exitControlMaster(sshBinary, env, controlPath, destination)
				_ = os.Remove(controlPath)
			}()
		}
	}
	overlayBase := overlayTransferRequestFromRecord("", stateDir, record)
	recorder := newSessionRecorder(sessionRecorderOptions{
		Disabled: opts.NoRecord,
		StateDir: stateDir,
		Command:  command,
		Env:      env,
		MaxBytes: opts.RecordMaxBytes,
		Record:   &record,
		RecordMu: &recordMu,
		Now:      now,
	})
	defer func() {
		if err := recorder.Close(now().UTC()); err != nil {
			fmt.Fprintf(stderr, "ssherpa: close transcript: %v\n", err)
		}
	}()

	// Detached mode has no PTY consumer — skip raw mode entirely.
	// makeRawIfTerminal handles a non-tty stdin gracefully too, but
	// explicitly skipping makes the daemon's intent obvious. The guard
	// owns the restore function: overlay transfers re-arm it from the
	// input goroutine while the supervisor's defer (and the panic
	// handlers) read it, so it must be mutex-protected, not a bare
	// closure variable.
	suspendTerminal := func(fn func()) {
		fn()
	}
	terminalInput := false
	if !opts.Detached {
		terminalInput = stdin != nil && term.IsTerminal(stdin.Fd())
		restore, err := makeRawIfTerminal(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: put terminal in raw mode: %v\n", err)
			return 1
		}
		guard.arm(restore)
		defer guard.restore()
		suspendTerminal = func(fn func()) {
			guard.restore()
			defer func() {
				restore, err := makeRawIfTerminal(stdin)
				if err != nil {
					fmt.Fprintf(stderr, "ssherpa: restore raw terminal mode: %v\n", err)
					return
				}
				guard.arm(restore)
			}()
			fn()
		}
	}

	// The supervisor swaps these per attempt as the reconnect loop
	// rotates the ssh process. copyInput captures ptmxRef once at
	// start and writes through whichever ptmx is current.
	ptmxRefShared := newPtmxRef()
	procRefShared := newProcRef()

	// pullRope tears down this (outermost) supervised session by
	// signaling the current ssh client; the remote sshd then HUPs the
	// next layer down, collapsing every nested session from the top.
	// With the reconnect loop, pullRope reads procRefShared to reach
	// whichever attempt is live — and ropePulledCh wakes the loop out
	// of a mid-backoff sleep.
	ropeCtx, ropeCancel := context.WithCancel(context.Background())
	defer ropeCancel()
	interruptCtx, interruptCancel := context.WithCancel(context.Background())
	defer interruptCancel()
	var ropePulled atomic.Bool
	var interrupted atomic.Bool
	ropePulledCh := make(chan struct{})
	interruptedCh := make(chan struct{})
	pullRope := func() {
		if !ropePulled.CompareAndSwap(false, true) {
			return
		}
		close(ropePulledCh)
		withLock(&recordMu, func() {
			record.DisconnectReason = EscapeRopeReason
			record.Events = append(record.Events, state.SessionEvent{
				Time:    now().UTC(),
				Type:    EscapeRopeReason,
				Message: "escape rope pulled; disconnecting all downstream sessions",
			})
			_ = state.WriteRecord(stateDir, record)
		})
		proc := procRefShared.get()
		if proc == nil {
			// Mid-backoff: no live process. The retry loop wakes on
			// ropePulledCh below and exits without restarting.
			return
		}
		// SIGHUP the whole process group, not just the ssh client PID:
		// under a PTY the child is a session leader (pgid == pid), so
		// a wrapper such as `kitten ssh` and the ssh it forks share
		// the group and both must die or we leak an orphaned
		// connection. Force-kill the group after a short grace;
		// cancel that timer once the supervisor has actually returned.
		signalSessionGroup(proc, syscall.SIGHUP)
		go func(p *os.Process) {
			timer := time.NewTimer(escapeRopeKillGrace)
			defer timer.Stop()
			select {
			case <-ropeCtx.Done():
			case <-timer.C:
				signalSessionGroup(p, syscall.SIGKILL)
			}
		}(proc)
	}
	interruptLocal := func() {
		if !interrupted.CompareAndSwap(false, true) {
			return
		}
		close(interruptedCh)
		withLock(&recordMu, func() {
			record.DisconnectReason = InterruptReason
			record.Events = append(record.Events, state.SessionEvent{
				Time:    now().UTC(),
				Type:    InterruptReason,
				Message: "connection attempt interrupted locally with Ctrl+C",
			})
			_ = state.WriteRecord(stateDir, record)
		})
		proc := procRefShared.get()
		if proc == nil {
			return
		}
		signalSessionGroup(proc, syscall.SIGINT)
		go func(p *os.Process) {
			timer := time.NewTimer(localInterruptKillGrace)
			defer timer.Stop()
			select {
			case <-interruptCtx.Done():
			case <-timer.C:
				signalSessionGroup(p, syscall.SIGKILL)
			}
		}(proc)
	}

	// failSupervisor is the child-goroutine panic handler: a panic in
	// copyInput/copyOutput/the watchdog/the signal forwarder must not
	// crash the whole process with the terminal still in raw mode. It
	// restores the terminal, finalizes the panic reason on the record,
	// tears down the current ssh attempt, and wakes the retry loop so
	// RunSupervised returns through its normal finalization path.
	// TryLock mirrors the top-level recover: the panicking goroutine
	// may have been holding recordMu.
	var panicked atomic.Bool
	panickedCh := make(chan struct{})
	failSupervisor := func(origin string, value any) {
		guard.restore()
		if !panicked.CompareAndSwap(false, true) {
			return
		}
		if recordMu.TryLock() {
			record.DisconnectReason = panicDisconnectReason
			record.Events = append(record.Events, state.SessionEvent{
				Time:    now().UTC(),
				Type:    panicDisconnectReason,
				Message: fmt.Sprintf("internal panic in %s: %v", origin, value),
			})
			_ = state.WriteRecord(stateDir, record)
			recordMu.Unlock()
		}
		fmt.Fprintf(stderr, "\r\nssherpa: internal error: %s panicked: %v\r\n", origin, value)
		close(panickedCh)
		proc := procRefShared.get()
		if proc == nil {
			return
		}
		signalSessionGroup(proc, syscall.SIGHUP)
		go func(p *os.Process) {
			timer := time.NewTimer(escapeRopeKillGrace)
			defer timer.Stop()
			select {
			case <-ropeCtx.Done():
			case <-timer.C:
				signalSessionGroup(p, syscall.SIGKILL)
			}
		}(proc)
	}

	// Lifetime signal coverage. forwardSignals is installed only while
	// an attempt's ssh process is alive, so without this handler a
	// SIGTERM/SIGHUP/SIGQUIT during the reconnect backoff sleep would
	// kill the supervisor with no finalization — `forward stop` would
	// report success and leave a permanently "running" record. pullRope
	// is idempotent, so overlapping with the per-attempt forwarder is
	// harmless, and closing ropePulledCh wakes the backoff sleep.
	lifetimeSignals := make(chan os.Signal, 4)
	signal.Notify(lifetimeSignals, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	lifetimeSignalsDone := make(chan struct{})
	go func() {
		for {
			select {
			case <-lifetimeSignalsDone:
				return
			case sig := <-lifetimeSignals:
				if sig != nil {
					pullRope()
				}
			}
		}
	}()
	defer func() {
		signal.Stop(lifetimeSignals)
		close(lifetimeSignalsDone)
	}()

	output := &lockedWriter{w: stdout}
	tap := &outputTap{}
	startupInterruptible := &atomic.Bool{}
	inputDone := make(chan struct{})
	defer close(inputDone)
	var inputStarted atomic.Bool
	startInput := func() {
		if !inputStarted.CompareAndSwap(false, true) {
			return
		}
		go func() {
			defer recoverSupervisorPanic("session input loop", failSupervisor)
			copyInput(ptmxRefShared, stdin, output, tap, stateDir, record.ID, overlayBase, opts.Composer, opts.Overlay, theme, pullRope, recorder, suspendTerminal, terminalInput, startupInterruptible, interruptLocal, inputDone)
		}()
	}
	if opts.Detached {
		// In detached mode there is no stdin to forward and no
		// overlay to render. Replace startInput with a no-op so the
		// onPtmxReady callback is harmless on every attempt.
		startInput = func() {}
	}

	var lastWaitErr error
	for attempt := 1; ; attempt++ {
		if ropePulled.Load() || interrupted.Load() || panicked.Load() {
			break
		}
		ac := attemptContext{
			command:              command,
			stateDir:             stateDir,
			record:               &record,
			recordMu:             &recordMu,
			env:                  env,
			stdin:                stdin,
			ptmxRef:              ptmxRefShared,
			procRef:              procRefShared,
			output:               output,
			outputTap:            tap,
			transcript:           recorder,
			startupInterruptible: startupInterruptible,
			watchdog:             opts.Watchdog,
			stderr:               stderr,
			now:                  now,
			onPtmxReady:          startInput,
			pullRope:             pullRope,
			onPanic:              failSupervisor,
		}
		waitErr := attemptOnce(ac)
		lastWaitErr = waitErr
		if attempt == 1 && isSpawnFailure(waitErr) {
			// Couldn't even start the first SSH process — surface the
			// raw spawn error like the pre-reconnect supervisor did.
			fmt.Fprintf(stderr, "ssherpa: run %s: %v\n", sshcmd.QuoteArgv(command.Argv), waitErr)
			return 1
		}
		if ropePulled.Load() || interrupted.Load() || panicked.Load() {
			break
		}
		if waitErr == nil {
			// Clean exit — the reconnect retry loop is for *failed*
			// attempts; a successful one ends the supervisor.
			break
		}
		if !shouldRetry(metadata.Kind, opts.Reconnect, waitErr, attempt) {
			if attempt > 1 {
				withLock(&recordMu, func() {
					record.Events = append(record.Events, state.SessionEvent{
						Time:    now().UTC(),
						Type:    "reconnect_gave_up",
						Message: fmt.Sprintf("attempt %d: %v", attempt, waitErr),
					})
					_ = state.WriteRecord(stateDir, record)
				})
			}
			break
		}
		backoff := computeBackoff(attempt, opts.Reconnect)
		withLock(&recordMu, func() {
			record.Events = append(record.Events, state.SessionEvent{
				Time:    now().UTC(),
				Type:    "reconnect_scheduled",
				Message: fmt.Sprintf("attempt %d failed: %v; retrying in %s", attempt, waitErr, backoff),
			})
			if record.Forward != nil {
				record.Forward.RetryCount = attempt
			}
			if record.Proxy != nil {
				record.Proxy.RetryCount = attempt
			}
			_ = state.WriteRecord(stateDir, record)
		})
		recorder.WriteMarker(now().UTC(), fmt.Sprintf("reconnect scheduled after attempt %d failed: %v", attempt, waitErr))
		fmt.Fprintf(stderr, "\r\nssherpa: session attempt %d ended (%v); retrying in %s\r\n", attempt, waitErr, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ropePulledCh:
			timer.Stop()
		case <-interruptedCh:
			timer.Stop()
		case <-panickedCh:
			timer.Stop()
		case <-timer.C:
		}
	}

	exitCode := exitCodeFromError(lastWaitErr)
	if ropePulled.Load() {
		exitCode = EscapeRopeExitCode
		fmt.Fprint(stderr, "\r\nssherpa: escape rope pulled — disconnecting all downstream sessions\r\n")
	} else if interrupted.Load() {
		exitCode = InterruptExitCode
		fmt.Fprint(stderr, "\r\nssherpa: connection attempt interrupted\r\n")
	} else if panicked.Load() {
		// failSupervisor already reported the panic on stderr.
		exitCode = 1
	}
	endedAt := now().UTC()
	var recordForTelemetry state.SessionRecord
	withLock(&recordMu, func() {
		record.EndedAt = &endedAt
		record.ExitCode = &exitCode
		err = state.WriteRecord(stateDir, record)
		recordForTelemetry = record
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: update session record: %v\n", err)
		if exitCode == 0 {
			return 1
		}
	}
	if err == nil {
		finalizeRemoteMirrors(stateDir, recordForTelemetry, endedAt, exitCode)
		emitSessionTelemetry(output, recordForTelemetry, env)
	}
	return exitCode
}

// isSpawnFailure reports whether an error from attemptOnce came from
// the process never starting (pty.Start failed) rather than from a
// completed Wait. Wait failures wrap *exec.ExitError; spawn failures
// surface the underlying os/exec error directly.
func isSpawnFailure(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	return !errors.As(err, &exitErr)
}

func resolveSessionTheme(opts Options, env []string) (termstyle.Theme, error) {
	if !opts.Theme.IsZero() {
		return opts.Theme.WithNoColor(opts.Theme.NoColor || opts.NoColor), nil
	}
	return termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:    opts.ThemeName,
		File:    opts.ThemeFile,
		NoColor: opts.NoColor,
		Env:     env,
	})
}

func buildRecord(command sshcmd.Command, metadata Metadata, started time.Time, env []string, recordIDOverride string) state.SessionRecord {
	parentID, depth, inheritedRoute := state.InheritedMetadataFromEnv(env, "")
	route := append([]string(nil), inheritedRoute...)
	if len(metadata.Route) > 0 {
		route = append(route, metadata.Route...)
	} else if metadata.TargetAlias != "" {
		route = append(route, metadata.TargetAlias)
	}
	originHost := state.LocalOriginHost(env)

	var forward *state.ForwardSpec
	if metadata.Forward != nil {
		copyFwd := *metadata.Forward
		forward = &copyFwd
	}
	var proxy *state.ProxySpec
	if metadata.Proxy != nil {
		copyProxy := *metadata.Proxy
		proxy = &copyProxy
	}
	id := strings.TrimSpace(recordIDOverride)
	if id == "" {
		id = state.NewSessionID(started)
	}
	return state.SessionRecord{
		ID:           id,
		ParentID:     parentID,
		Depth:        depth,
		Route:        route,
		OriginHost:   originHost,
		TargetAlias:  metadata.TargetAlias,
		Hops:         append([]string(nil), metadata.Hops...),
		SSHArgv:      append([]string(nil), command.Argv...),
		Kind:         metadata.Kind,
		Forward:      forward,
		Proxy:        proxy,
		StartedAt:    started.UTC(),
		LocalPID:     os.Getpid(),
		RunnerMode:   RunnerModeSupervised,
		StateVersion: state.StateVersion,
	}
}

func prepareControlMaster(stateDir string, recordID string, metadata Metadata, env []string) (string, bool, error) {
	if strings.TrimSpace(recordID) == "" || !interactiveSessionKind(metadata.Kind) {
		return "", false, nil
	}
	dir, err := controlMasterDir(env)
	if err != nil {
		return "", false, err
	}
	sum := sha1.Sum([]byte(stateDir + "\x00" + recordID))
	path := filepath.Join(dir, hex.EncodeToString(sum[:8])+".sock")
	return path, true, nil
}

// controlMasterDir picks the directory holding SSH control sockets.
// A user-private runtime dir (XDG_RUNTIME_DIR, mirroring
// internal/incoming) is preferred; on the world-writable os.TempDir()
// fallback the path is predictable, so an existing dir is verified —
// a /tmp/ssherpa-<uid> pre-created by another user (or left
// group/other-accessible) must not be able to capture authenticated
// control sockets.
func controlMasterDir(env []string) (string, error) {
	if value := strings.TrimSpace(sessionEnvValue(env, "XDG_RUNTIME_DIR")); value != "" {
		dir := filepath.Join(value, "ssherpa", "cm")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
		return dir, nil
	}
	base := filepath.Join(os.TempDir(), fmt.Sprintf("ssherpa-%d", os.Getuid()))
	dir := filepath.Join(base, "cm")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, path := range []string{base, dir} {
		if err := verifyPrivateDir(path); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// verifyPrivateDir refuses a control-socket directory that is a
// symlink, not a directory, or owned by another user, and tightens
// group/other permission bits on a directory we do own. MkdirAll
// happily reuses an attacker-precreated path, so existence alone
// proves nothing on a shared /tmp.
func verifyPrivateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("control socket directory %s is not a directory", path)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("control socket directory %s: cannot verify ownership", path)
	}
	if int(stat.Uid) != os.Getuid() {
		return fmt.Errorf("control socket directory %s is owned by uid %d, not the current user", path, stat.Uid)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("control socket directory %s is group/other accessible and cannot be tightened: %w", path, err)
		}
	}
	return nil
}

// controlMasterExitTimeout bounds the best-effort `ssh -O exit` at
// session teardown. Talking to a local unix socket is fast; a wedged
// master must not delay the user's return to their shell.
const controlMasterExitTimeout = 2 * time.Second

// exitControlMaster asks the multiplexing master listening on
// controlPath to exit before the socket is unlinked. Unlinking a live
// socket only orphans the authenticated master for the full
// ControlPersist window (10m), during which it keeps holding any
// forwarded ports. Best-effort: a dead or never-started master is the
// common, fine case.
func exitControlMaster(sshBinary string, env []string, controlPath string, destination string) {
	sshBinary = strings.TrimSpace(sshBinary)
	controlPath = strings.TrimSpace(controlPath)
	destination = strings.TrimSpace(destination)
	if sshBinary == "" || controlPath == "" || destination == "" {
		return
	}
	if _, err := os.Stat(controlPath); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), controlMasterExitTimeout)
	defer cancel()
	// Use the same ssh binary and environment that created the master:
	// a custom --ssh-binary/SSHERPA_SSH_BINARY may not be on PATH (and a
	// bare "ssh" might be absent or a different build), in which case
	// `-O exit` would silently miss the real master and leave it holding
	// forwarded ports for the full ControlPersist window.
	cmd := exec.CommandContext(ctx, sshBinary, "-O", "exit", "-o", "ControlPath="+controlPath, destination)
	cmd.Env = env
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run()
}

func interactiveSessionKind(kind string) bool {
	kind = strings.TrimSpace(kind)
	return kind == "" || kind == state.KindInteractive
}

func sessionEnv(env []string, record state.SessionRecord) []string {
	return withEnv(env, state.EnvForRecord(record))
}

func withEnv(env []string, updates []string) []string {
	result := append([]string(nil), env...)
	for _, update := range updates {
		key, _, ok := strings.Cut(update, "=")
		if !ok {
			continue
		}
		replaced := false
		prefix := key + "="
		for i, item := range result {
			if strings.HasPrefix(item, prefix) {
				result[i] = update
				replaced = true
			}
		}
		if !replaced {
			result = append(result, update)
		}
	}
	return result
}

// panicDisconnectReason marks a session torn down because the
// supervisor (or one of its goroutines) panicked. Recorded as the
// session's DisconnectReason so the map shows why the session died.
const panicDisconnectReason = "panic"

// terminalGuard owns the raw-mode restore function. The supervisor
// goroutine arms it, overlay transfers re-arm it from the input
// goroutine after a suspend, and panic handlers on any goroutine may
// fire it — so access is mutex-protected and restore runs the armed
// function exactly once per arm.
type terminalGuard struct {
	mu sync.Mutex
	fn func()
}

func newTerminalGuard() *terminalGuard {
	return &terminalGuard{}
}

func (g *terminalGuard) arm(restore func()) {
	g.mu.Lock()
	g.fn = restore
	g.mu.Unlock()
}

func (g *terminalGuard) restore() {
	g.mu.Lock()
	fn := g.fn
	g.fn = nil
	g.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// recoverSupervisorPanic is the deferred recover wrapper for the
// supervisor's child goroutines (input/output loops, watchdog, signal
// forwarder). A panic there would otherwise crash the whole process
// with the terminal still in raw mode. onPanic is RunSupervised's
// failSupervisor; callers without one (direct attemptOnce use in
// tests) get the panic re-raised so it cannot be silently swallowed.
func recoverSupervisorPanic(origin string, onPanic func(string, any)) {
	r := recover()
	if r == nil {
		return
	}
	if onPanic == nil {
		panic(r)
	}
	onPanic(origin, r)
}

func makeRawIfTerminal(stdin *os.File) (func(), error) {
	if stdin == nil || !term.IsTerminal(stdin.Fd()) {
		return func() {}, nil
	}
	state, err := term.MakeRaw(stdin.Fd())
	if err != nil {
		return nil, err
	}
	return func() {
		_ = term.Restore(stdin.Fd(), state)
	}, nil
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(data)
}

type outputTap struct {
	mu     sync.Mutex
	active *activeOutputTap
}

type activeOutputTap struct {
	ch       chan []byte
	suppress bool
}

func (t *outputTap) start(suppress bool) (<-chan []byte, func()) {
	if t == nil {
		ch := make(chan []byte)
		close(ch)
		return ch, func() {}
	}
	active := &activeOutputTap{ch: make(chan []byte, 1024), suppress: suppress}
	t.mu.Lock()
	t.active = active
	t.mu.Unlock()
	stop := func() {
		t.mu.Lock()
		if t.active == active {
			t.active = nil
		}
		t.mu.Unlock()
	}
	return active.ch, stop
}

func (t *outputTap) observe(data []byte) bool {
	if t == nil || len(data) == 0 {
		return false
	}
	t.mu.Lock()
	active := t.active
	t.mu.Unlock()
	if active == nil {
		return false
	}
	copied := append([]byte(nil), data...)
	select {
	case active.ch <- copied:
	default:
	}
	return active.suppress
}

type overlayFrame struct {
	terminal bool
	startRow int
	lines    int
}

func copyInput(ptmxRef *ptmxRef, stdin *os.File, output *lockedWriter, tap *outputTap, stateDir string, currentID string, overlayBase OverlayTransferRequest, composer ComposerOptions, overlay OverlayOptions, theme termstyle.Theme, pullRope func(), recorder *sessionRecorder, suspendTerminal func(func()), localInterrupts bool, startupInterruptible *atomic.Bool, interruptLocal func(), done <-chan struct{}) {
	if stdin == nil {
		return
	}
	var buf [1]byte
	for {
		n, err := stdin.Read(buf[:])
		if n > 0 {
			switch {
			case localInterrupts && buf[0] == 0x03 && startupInterruptible != nil && startupInterruptible.Load() && interruptLocal != nil:
				interruptLocal()
			case buf[0] == overlay.hotkey():
				showSessionOverlay(ptmxRef, stdin, output, tap, stateDir, currentID, overlayBase, overlay, theme, pullRope, recorder, suspendTerminal, time.Now())
			case composer.enabled() && buf[0] == composer.hotkey():
				if ptmx := ptmxRef.get(); ptmx != nil {
					showComposer(stdin, output, ptmx, composer, theme)
				}
			default:
				_, _ = ptmxRef.write(buf[:n])
			}
		}
		if err != nil {
			return
		}
		select {
		case <-done:
			return
		default:
		}
	}
}

func showSessionOverlay(ptmxRef *ptmxRef, stdin *os.File, output *lockedWriter, tap *outputTap, stateDir string, currentID string, overlayBase OverlayTransferRequest, overlay OverlayOptions, theme termstyle.Theme, pullRope func(), recorder *sessionRecorder, suspendTerminal func(func()), openedAt time.Time) {
	output.mu.Lock()
	_, stopOverlayTap := tap.start(true)
	unlocked := false
	unlock := func() {
		if unlocked {
			return
		}
		stopOverlayTap()
		output.mu.Unlock()
		unlocked = true
	}
	defer unlock()

	frame := drawSessionOverlay(output.w, stdin, stateDir, currentID, overlay, theme, recorder)
	clearAndReturn := func() {
		clearSessionOverlay(output.w, frame)
	}

	pull := func() {
		if pullRope != nil {
			pullRope()
		}
	}

	// Panic-tap state: rapid repeated hotkey presses pull the rope immediately.
	taps := 1
	lastTap := openedAt
	confirming := false

	var buf [1]byte
	for {
		n, err := stdin.Read(buf[:])
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		key := buf[0]

		if confirming {
			// Deliberate path: a second uppercase X confirms; anything else
			// cancels back to the session map.
			if key == 'X' {
				pull()
				clearAndReturn()
				return
			}
			confirming = false
			clearSessionOverlay(output.w, frame)
			frame = drawSessionOverlay(output.w, stdin, stateDir, currentID, overlay, theme, recorder)
			continue
		}

		if key == overlay.hotkey() {
			if time.Since(lastTap) <= escapeRopePanicWindow {
				taps++
				lastTap = time.Now()
				if taps >= escapeRopePanicTaps {
					pull()
					clearAndReturn()
					return
				}
				continue
			}
			clearAndReturn()
			return // a single, settled hotkey press closes the overlay
		}
		// Any other key breaks a panic-tap streak.
		lastTap = time.Time{}

		switch key {
		case 'q', 'Q', '\r', '\n', 0x1b, 0x03:
			clearAndReturn()
			return
		case 'X':
			// Escape rope: confirm before tearing down every layer.
			confirming = true
			clearSessionOverlay(output.w, frame)
			frame = drawEscapeConfirm(output.w, stdin, theme)
		case 'r', 'R':
			clearSessionOverlay(output.w, frame)
			frame = drawSessionOverlay(output.w, stdin, stateDir, currentID, overlay, theme, recorder)
		case 't', 'T':
			clearSessionOverlay(output.w, frame)
			if recorder == nil {
				frame = drawOverlayNotice(output.w, stdin, theme, "ssherpa recording", "recording is not available for this session")
				continue
			}
			message, err := recorder.Toggle()
			if err != nil {
				frame = drawOverlayNotice(output.w, stdin, theme, "ssherpa recording", err.Error())
				continue
			}
			frame = drawOverlayNotice(output.w, stdin, theme, "ssherpa recording", message)
		case 's', 'S':
			if overlay.Send == nil {
				clearSessionOverlay(output.w, frame)
				frame = drawOverlayNotice(output.w, stdin, theme, "ssherpa send", "send is not available for this session")
				continue
			}
			clearSessionOverlay(output.w, frame)
			req := overlayTransferRequest("send", stateDir, currentID, overlayBase)
			req.InbandSend = newInbandSendFunc(ptmxRef, tap)
			unlock()
			runOverlayTransfer(overlay.Send, req, suspendTerminal)
			return
		case 'v', 'V':
			if overlay.Receive == nil {
				clearSessionOverlay(output.w, frame)
				frame = drawOverlayNotice(output.w, stdin, theme, "ssherpa receive", "receive is not available for this session")
				continue
			}
			clearSessionOverlay(output.w, frame)
			unlock()
			runOverlayTransfer(overlay.Receive, overlayTransferRequest("receive", stateDir, currentID, overlayBase), suspendTerminal)
			return
		}
	}
}

func runOverlayTransfer(fn OverlayTransferFunc, req OverlayTransferRequest, suspendTerminal func(func())) {
	if suspendTerminal == nil {
		_ = fn(req)
		return
	}
	suspendTerminal(func() {
		_ = fn(req)
	})
}

// In-band driver wait windows. Package-level so the deterministic
// driver tests can shrink them; production code never overrides them.
var (
	inbandProbeTimeout    = 5 * time.Second
	inbandReadyTimeout    = 5 * time.Second
	inbandCompleteTimeout = 30 * time.Second
)

func newInbandSendFunc(ptmxRef *ptmxRef, tap *outputTap) InbandSendFunc {
	return func(req InbandSendRequest) (InbandSendResult, error) {
		localPath := strings.TrimSpace(req.LocalPath)
		remotePath := strings.TrimSpace(req.RemotePath)
		if localPath == "" {
			return InbandSendResult{}, errors.New("local path is required")
		}
		if remotePath == "" {
			return InbandSendResult{}, errors.New("remote path is required")
		}
		file, err := os.Open(localPath)
		if err != nil {
			return InbandSendResult{}, fmt.Errorf("open local file: %w", err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			return InbandSendResult{}, fmt.Errorf("stat local file: %w", err)
		}
		if info.IsDir() {
			return InbandSendResult{}, fmt.Errorf("local path %s is a directory", localPath)
		}
		plan, payload, err := inband.NewSendPlanFromReader(remotePath, state.NewSessionID(time.Now().UTC()), file, req.MaxBytes)
		if err != nil {
			return InbandSendResult{}, err
		}
		encoded := make([]byte, base64.StdEncoding.EncodedLen(len(payload)))
		base64.StdEncoding.Encode(encoded, payload)

		ptmx := ptmxRef.get()
		if ptmx == nil {
			return InbandSendResult{}, errors.New("session PTY is not available")
		}
		output, stopTap := tap.start(true)
		defer stopTap()

		writeLine := func(line string) error {
			_, err := ptmx.Write([]byte(line + "\n"))
			return err
		}
		if err := writeLine(plan.ProbeCommand); err != nil {
			return InbandSendResult{}, fmt.Errorf("write capability probe: %w", err)
		}
		probe, err := waitForInbandOutput(output, inbandProbeTimeout, func(text string) (bool, error) {
			switch {
			case strings.Contains(text, inband.ProbePrefix+" ok"):
				return true, nil
			case strings.Contains(text, inband.ProbePrefix+" fail"):
				return false, errors.New("remote shell lacks base64, checksum, or stty support")
			default:
				return false, nil
			}
		})
		if err != nil {
			return InbandSendResult{}, fmt.Errorf("capability probe failed: %w", err)
		}
		if !probe {
			return InbandSendResult{}, errors.New("capability probe did not complete")
		}

		if err := writeLine(plan.ReceiverCommand); err != nil {
			return InbandSendResult{}, fmt.Errorf("write receiver command: %w", err)
		}
		ready, err := waitForInbandOutput(output, inbandReadyTimeout, func(text string) (bool, error) {
			return strings.Contains(text, inband.ReadyPrefix), nil
		})
		if err != nil {
			_, _ = ptmx.Write([]byte{0x03})
			_ = writeLine(plan.ResetCommand)
			return InbandSendResult{}, fmt.Errorf("receiver did not become ready: %w", err)
		}
		if !ready {
			_, _ = ptmx.Write([]byte{0x03})
			_ = writeLine(plan.ResetCommand)
			return InbandSendResult{}, errors.New("receiver did not become ready")
		}
		for len(encoded) > 0 {
			n := min(len(encoded), 4096)
			if _, err := ptmx.Write(encoded[:n]); err != nil {
				_, _ = ptmx.Write([]byte{0x03})
				_ = writeLine(plan.ResetCommand)
				return InbandSendResult{}, fmt.Errorf("stream payload: %w", err)
			}
			encoded = encoded[n:]
		}
		// The receiver reads exactly Base64Length bytes. A trailing newline is
		// harmless once the remote PTY is truly non-canonical, and it flushes
		// BSD/macOS PTYs if canonical buffering is still in effect.
		if _, err := ptmx.Write([]byte("\n")); err != nil {
			_, _ = ptmx.Write([]byte{0x03})
			_ = writeLine(plan.ResetCommand)
			return InbandSendResult{}, fmt.Errorf("flush payload: %w", err)
		}
		done, err := waitForInbandOutput(output, inbandCompleteTimeout, func(text string) (bool, error) {
			ok, parseErr := inband.ParseCompletion(text, plan.SHA256)
			if parseErr == nil && ok {
				return true, nil
			}
			if parseErr != nil && !errors.Is(parseErr, inband.ErrNoCompletion) {
				return false, parseErr
			}
			return false, nil
		})
		if err != nil {
			_, _ = ptmx.Write([]byte{0x03})
			_ = writeLine(plan.ResetCommand)
			return InbandSendResult{}, fmt.Errorf("in-band transfer failed: %w", err)
		}
		if !done {
			_, _ = ptmx.Write([]byte{0x03})
			_ = writeLine(plan.ResetCommand)
			return InbandSendResult{}, errors.New("in-band transfer did not complete")
		}
		return InbandSendResult{
			LocalPath:  localPath,
			RemotePath: remotePath,
			Size:       plan.Size,
			SHA256:     plan.SHA256,
		}, nil
	}
}

func waitForInbandOutput(ch <-chan []byte, timeout time.Duration, done func(string) (bool, error)) (bool, error) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var b strings.Builder
	for {
		select {
		case chunk, ok := <-ch:
			if !ok {
				return false, errors.New("output tap closed")
			}
			b.Write(chunk)
			if b.Len() > 64*1024 {
				text := b.String()
				b.Reset()
				if len(text) > 32*1024 {
					b.WriteString(text[len(text)-32*1024:])
				}
			}
			okDone, err := done(b.String())
			if err != nil || okDone {
				return okDone, err
			}
		case <-timer.C:
			return false, errors.New("timed out waiting for remote response")
		}
	}
}

func overlayTransferRequest(direction string, stateDir string, currentID string, fallback OverlayTransferRequest) OverlayTransferRequest {
	req := fallback
	req.Direction = direction
	req.SessionID = currentID
	req.StateDir = stateDir
	record, err := state.ReadRecord(stateDir, currentID)
	if err != nil {
		return req
	}
	next := overlayTransferRequestFromRecord(direction, stateDir, record)
	next.InbandSend = fallback.InbandSend
	return next
}

func overlayTransferRequestFromRecord(direction string, stateDir string, record state.SessionRecord) OverlayTransferRequest {
	req := OverlayTransferRequest{
		Direction:    direction,
		SessionID:    record.ID,
		StateDir:     stateDir,
		TargetAlias:  record.TargetAlias,
		Hops:         append([]string(nil), record.Hops...),
		Route:        append([]string(nil), record.Route...),
		ControlPath:  record.ControlPath,
		RemoteHost:   record.RemoteHost,
		RemoteCWD:    record.RemoteCWD,
		RemotePrompt: record.RemotePrompt,
	}
	return req
}

func sameStrings(a []string, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func drawEscapeConfirm(w io.Writer, stdin *os.File, theme termstyle.Theme) overlayFrame {
	lines := []string{
		overlayTitle("ssherpa escape rope", theme),
		theme.Style(termstyle.RoleWarning, "Disconnect ALL nested sessions and return to the outermost shell?"),
		overlayHelp("press X to confirm   any other key cancels", theme),
	}
	return drawBottomFrame(w, stdin, lines)
}

func drawOverlayNotice(w io.Writer, stdin *os.File, theme termstyle.Theme, title string, message string) overlayFrame {
	lines := []string{
		overlayTitle(title, theme),
		theme.Style(termstyle.RoleWarning, message),
		overlayHelp("press any key to return", theme),
	}
	return drawBottomFrame(w, stdin, lines)
}

// drawBottomFrame renders lines pinned to the bottom of the terminal, saving
// and hiding the cursor like the other overlays. When stdout is not a terminal
// it falls back to plain inline printing.
func drawBottomFrame(w io.Writer, stdin *os.File, lines []string) overlayFrame {
	width, height, terminalOutput := overlaySize(stdin)
	if !terminalOutput {
		fmt.Fprintln(w)
		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
		return overlayFrame{}
	}
	visibleLines := min(len(lines), max(3, height-2))
	startRow := max(1, height-visibleLines+1)
	fmt.Fprint(w, "\x1b7\x1b[?25l")
	for i := 0; i < visibleLines; i++ {
		row := startRow + i
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K%s", row, truncateOverlayLine(lines[i], width))
	}
	return overlayFrame{terminal: true, startRow: startRow, lines: visibleLines}
}

func drawSessionOverlay(w io.Writer, stdin *os.File, stateDir string, currentID string, overlay OverlayOptions, theme termstyle.Theme, recorder *sessionRecorder) overlayFrame {
	width, height, terminalOutput := overlaySize(stdin)
	actions := []string{fmt.Sprintf("%s/q/Esc close", overlay.hotkeyName()), "r refresh"}
	if recorder != nil {
		actions = append(actions, recorder.HelpAction())
	}
	if overlay.Send != nil {
		actions = append(actions, "s send")
	}
	if overlay.Receive != nil {
		actions = append(actions, "v receive")
	}
	actions = append(actions, "X escape rope", fmt.Sprintf("%sx3 panic", overlay.hotkeyName()), "local only")
	help := strings.Join(actions, "   ")
	lines := sessionOverlayLines(stateDir, currentID, theme, width, height, help)

	if !terminalOutput {
		fmt.Fprintln(w)
		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
		return overlayFrame{}
	}

	visibleLines := min(len(lines), max(6, height-2))
	startRow := max(1, height-visibleLines+1)
	fmt.Fprint(w, "\x1b7\x1b[?25l")
	for i := 0; i < visibleLines; i++ {
		row := startRow + i
		line := truncateOverlayLine(lines[i], width)
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K%s", row, line)
	}
	return overlayFrame{terminal: true, startRow: startRow, lines: visibleLines}
}

func clearSessionOverlay(w io.Writer, frame overlayFrame) {
	if !frame.terminal {
		fmt.Fprintln(w, "----- ssherpa overlay closed -----")
		return
	}
	for i := 0; i < frame.lines; i++ {
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K", frame.startRow+i)
	}
	fmt.Fprint(w, "\x1b8\x1b[?25h")
}

func showComposer(stdin *os.File, output *lockedWriter, ptmx *os.File, composer ComposerOptions, theme termstyle.Theme) {
	output.mu.Lock()
	defer output.mu.Unlock()

	var buffer []byte
	frame := drawComposer(output.w, stdin, buffer, composer, theme)
	sent := false
	defer func() {
		clearComposer(output.w, frame, sent)
	}()

	var buf [1]byte
	for {
		n, err := stdin.Read(buf[:])
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}

		key := buf[0]
		switch key {
		case '\r', '\n':
			_, _ = ptmx.Write(append(append([]byte(nil), buffer...), '\n'))
			sent = true
			return
		case ComposerSendHotkey:
			_, _ = ptmx.Write(buffer)
			sent = true
			return
		case 0x1b, 0x03:
			return
		case 0x15:
			buffer = buffer[:0]
		case 0x7f, 0x08:
			if len(buffer) > 0 {
				buffer = buffer[:len(buffer)-1]
			}
		default:
			if isComposerPrintable(key) {
				buffer = append(buffer, key)
			}
		}

		if frame.terminal {
			clearTerminalFrame(output.w, frame)
			frame = drawComposer(output.w, stdin, buffer, composer, theme)
		}
	}
}

func drawComposer(w io.Writer, stdin *os.File, buffer []byte, composer ComposerOptions, theme termstyle.Theme) overlayFrame {
	width, height, terminalOutput := overlaySize(stdin)
	display := string(buffer)
	if display == "" {
		display = "<empty>"
	}
	lines := []string{
		overlayTitle("ssherpa composer", theme),
		overlayField("buffer", display, theme),
		overlayHelp(fmt.Sprintf("Enter send+newline   %s send   Esc cancel   Backspace edit   Ctrl-U clear", ComposerSendHotkeyName), theme),
	}
	if composer.hotkeyName() != ComposerHotkeyName {
		lines = append(lines, overlayField("hotkey", composer.hotkeyName(), theme))
	}

	if !terminalOutput {
		fmt.Fprintln(w)
		fmt.Fprintln(w, overlayTitle("ssherpa composer", theme))
		for _, line := range lines {
			fmt.Fprintln(w, line)
		}
		return overlayFrame{}
	}

	visibleLines := min(len(lines), max(3, height-2))
	startRow := max(1, height-visibleLines+1)
	fmt.Fprint(w, "\x1b7\x1b[?25l")
	for i := 0; i < visibleLines; i++ {
		row := startRow + i
		line := truncateOverlayLine(lines[i], width)
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K%s", row, line)
	}
	return overlayFrame{terminal: true, startRow: startRow, lines: visibleLines}
}

func clearComposer(w io.Writer, frame overlayFrame, sent bool) {
	if !frame.terminal {
		if sent {
			fmt.Fprintln(w, "----- ssherpa composer sent -----")
		} else {
			fmt.Fprintln(w, "----- ssherpa composer cancelled -----")
		}
		return
	}
	clearTerminalFrame(w, frame)
}

func clearTerminalFrame(w io.Writer, frame overlayFrame) {
	for i := 0; i < frame.lines; i++ {
		fmt.Fprintf(w, "\x1b[%d;1H\x1b[2K", frame.startRow+i)
	}
	fmt.Fprint(w, "\x1b8\x1b[?25h")
}

func isComposerPrintable(key byte) bool {
	return key == '\t' || (key >= 0x20 && key <= 0x7e)
}

func overlayTitle(value string, theme termstyle.Theme) string {
	return theme.Style(termstyle.RoleTitle, value)
}

func overlayField(label string, value string, theme termstyle.Theme) string {
	return theme.Style(termstyle.RoleAccent, label+":") + " " + theme.Style(termstyle.RoleForeground, value)
}

func overlayHelp(value string, theme termstyle.Theme) string {
	return theme.Style(termstyle.RoleMuted, value)
}

func sessionOverlayLines(stateDir string, currentID string, theme termstyle.Theme, width int, height int, help string) []string {
	records, err := state.ListRecords(stateDir)
	if err != nil {
		lines := []string{
			overlayTitle("ssherpa session map", theme),
			overlayField("state", stateDir, theme),
			theme.Style(termstyle.RoleDanger, "error: "+err.Error()),
			overlayHelp(help, theme),
		}
		return lines
	}
	view := sessionview.MapView(sessionview.ViewOptions{
		Title:    "ssherpa session map",
		StateDir: stateDir,
		Records:  records,
		Map:      sessionview.MapOptions{CurrentID: currentID},
		Theme:    theme,
		Width:    width,
		Height:   height - 1,
		Help:     help,
	})
	return strings.Split(view.Content, "\n")
}

const (
	defaultLatencyProbeInterval = 10 * time.Second
	defaultLatencyProbeTimeout  = 10 * time.Second
	latencyKillGrace            = 2 * time.Second
	// escapeRopeKillGrace is short: pulling the rope means "get me out now", so
	// we escalate from SIGHUP to SIGKILL quickly to guarantee a prompt local
	// return even if the ssh client ignores the hangup.
	escapeRopeKillGrace = 750 * time.Millisecond
	// localInterruptKillGrace mirrors the escape rope escalation window for
	// Ctrl+C during a startup attempt. SIGINT is tried first so a normal ssh
	// client can clean up; SIGKILL follows quickly so a blocked connect cannot
	// leave ssherpa wedged in raw mode.
	localInterruptKillGrace = 750 * time.Millisecond
	// Mashing the overlay hotkey escapeRopePanicTaps times within
	// escapeRopePanicWindow of each press pulls the rope immediately, skipping
	// the confirm step — a blind panic exit for when a layer is wedged and you
	// cannot read the overlay. A single, settled press still just closes it.
	escapeRopePanicWindow = 400 * time.Millisecond
	escapeRopePanicTaps   = 3
	// ptyOutputDrainGrace lets the PTY reader consume the child's final
	// buffered output after Wait observes process exit. Linux runners can
	// otherwise lose very short final writes if the master is closed first.
	ptyOutputDrainGrace = 100 * time.Millisecond
)

// signalSessionGroup signals the child's whole process group when possible,
// falling back to the single process. Under a PTY the supervised child is a
// session leader (its pgid equals its pid), so a negative pid reaches the child
// and anything it forked (e.g. the ssh under a `kitten ssh` wrapper).
func signalSessionGroup(proc *os.Process, sig syscall.Signal) {
	if proc == nil {
		return
	}
	if err := syscall.Kill(-proc.Pid, sig); err != nil {
		_ = proc.Signal(sig)
	}
}

type latencyWatchdogConfig struct {
	Options  WatchdogOptions
	StateDir string
	Record   *state.SessionRecord
	RecordMu *sync.Mutex
	Stderr   io.Writer
	Process  *os.Process
	Now      func() time.Time
	// OnPanic receives panics from the watchdog goroutine (see
	// recoverSupervisorPanic); nil re-raises them.
	OnPanic func(string, any)
}

func startLatencyWatchdog(cfg latencyWatchdogConfig) func() {
	if !cfg.Options.Enabled() {
		return func() {}
	}
	if len(cfg.Options.ProbeCommand.Argv) == 0 {
		fmt.Fprintln(cfg.Stderr, "ssherpa: latency watchdog disabled: no sidecar probe command")
		return func() {}
	}
	if cfg.Process == nil {
		fmt.Fprintln(cfg.Stderr, "ssherpa: latency watchdog disabled: no supervised process")
		return func() {}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer recoverSupervisorPanic("latency watchdog", cfg.OnPanic)
		runLatencyWatchdog(ctx, cfg)
	}()
	return func() {
		cancel()
		<-done
	}
}

func runLatencyWatchdog(ctx context.Context, cfg latencyWatchdogConfig) {
	runner := cfg.Options.RunProbe
	if runner == nil {
		runner = runSidecarProbe
	}
	interval := cfg.Options.Interval
	if interval <= 0 {
		interval = defaultLatencyProbeInterval
	}
	timeout := cfg.Options.ProbeTimeout
	if timeout <= 0 {
		timeout = defaultLatencyProbeTimeout
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	var unhealthySince time.Time
	warningActive := false
	disconnected := false
	for {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		started := time.Now()
		result := runner(probeCtx, cfg.Options.ProbeCommand)
		probeErr := probeCtx.Err()
		cancel()
		if result.Duration <= 0 {
			result.Duration = time.Since(started)
		}
		if ctx.Err() != nil {
			return
		}
		if probeErr != nil && result.Err == nil {
			result.Err = probeErr
		}

		sampleTime := now().UTC()
		unhealthy, message := latencyProbeUnhealthy(result, cfg.Options.WarnThreshold)
		if unhealthy {
			if unhealthySince.IsZero() {
				unhealthySince = sampleTime
			}
			if !warningActive {
				writeLatencyEvent(cfg, state.SessionEvent{
					Time:            sampleTime,
					Type:            "latency_warning",
					Message:         message,
					LatencyMillis:   durationMillis(result.Duration),
					ThresholdMillis: durationMillis(cfg.Options.WarnThreshold),
				}, "")
				fmt.Fprintf(cfg.Stderr, "\nssherpa: latency warning: %s\n", message)
				warningActive = true
			}
			if cfg.Options.DisconnectAfter > 0 && !disconnected && sampleTime.Sub(unhealthySince) >= cfg.Options.DisconnectAfter {
				reason := fmt.Sprintf("latency unhealthy for %s; disconnect threshold %s", sampleTime.Sub(unhealthySince).Round(time.Millisecond), cfg.Options.DisconnectAfter)
				writeLatencyEvent(cfg, state.SessionEvent{
					Time:            sampleTime,
					Type:            "latency_disconnect",
					Message:         reason,
					LatencyMillis:   durationMillis(result.Duration),
					ThresholdMillis: durationMillis(cfg.Options.WarnThreshold),
				}, reason)
				fmt.Fprintf(cfg.Stderr, "\nssherpa: latency disconnect: %s\n", reason)
				_ = cfg.Process.Signal(syscall.SIGTERM)
				scheduleForceKill(ctx, cfg.Process)
				disconnected = true
				return
			}
		} else if warningActive {
			writeLatencyEvent(cfg, state.SessionEvent{
				Time:            sampleTime,
				Type:            "latency_recovered",
				Message:         fmt.Sprintf("sidecar probe recovered in %s", result.Duration.Round(time.Millisecond)),
				LatencyMillis:   durationMillis(result.Duration),
				ThresholdMillis: durationMillis(cfg.Options.WarnThreshold),
			}, "")
			fmt.Fprintf(cfg.Stderr, "\nssherpa: latency recovered: sidecar probe completed in %s\n", result.Duration.Round(time.Millisecond))
			unhealthySince = time.Time{}
			warningActive = false
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func runSidecarProbe(ctx context.Context, command sshcmd.Command) ProbeResult {
	started := time.Now()
	proc := exec.CommandContext(ctx, command.Argv[0], command.Argv[1:]...)
	proc.Stdin = nil
	proc.Stdout = io.Discard
	proc.Stderr = io.Discard
	err := proc.Run()
	return ProbeResult{Duration: time.Since(started), Err: err}
}

func latencyProbeUnhealthy(result ProbeResult, threshold time.Duration) (bool, string) {
	if result.Err != nil {
		return true, fmt.Sprintf("sidecar probe failed after %s: %v", result.Duration.Round(time.Millisecond), result.Err)
	}
	if result.Duration > threshold {
		return true, fmt.Sprintf("sidecar probe took %s; threshold %s", result.Duration.Round(time.Millisecond), threshold)
	}
	return false, ""
}

func writeLatencyEvent(cfg latencyWatchdogConfig, event state.SessionEvent, disconnectReason string) {
	cfg.RecordMu.Lock()
	cfg.Record.Events = append(cfg.Record.Events, event)
	if disconnectReason != "" {
		cfg.Record.DisconnectReason = disconnectReason
	}
	err := state.WriteRecord(cfg.StateDir, *cfg.Record)
	cfg.RecordMu.Unlock()
	if err != nil {
		fmt.Fprintf(cfg.Stderr, "\nssherpa: update latency event: %v\n", err)
	}
}

func scheduleForceKill(ctx context.Context, process *os.Process) {
	go func() {
		timer := time.NewTimer(latencyKillGrace)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			_ = process.Kill()
		}
	}()
}

func durationMillis(value time.Duration) int64 {
	return value.Round(time.Millisecond).Milliseconds()
}

func overlaySize(stdin *os.File) (width int, height int, ok bool) {
	if stdin == nil || !term.IsTerminal(stdin.Fd()) {
		return 88, 24, false
	}
	width, height, err := term.GetSize(stdin.Fd())
	if err != nil || width <= 0 || height <= 0 {
		return 88, 24, true
	}
	return width, height, true
}

func truncateOverlayLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if termstyle.VisibleWidth(value) <= width {
		return value
	}
	value = termstyle.Strip(value)
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "~"
}

func forwardSignals(stdin *os.File, ptmx *os.File, proc *exec.Cmd, pullRope func(), onPanic func(string, any)) func() {
	resizePTY(stdin, ptmx)

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
		defer recoverSupervisorPanic("signal forwarder", onPanic)
		for {
			select {
			case sig := <-sigCh:
				if sig == nil {
					continue
				}
				switch sig {
				case syscall.SIGWINCH:
					resizePTY(stdin, ptmx)
				case syscall.SIGINT:
					if proc.Process != nil {
						_ = proc.Process.Signal(sig)
					}
				case syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT:
					// External termination request — for foreground
					// supervisors this is the user's terminal driver, for
					// the daemon it's `ssherpa forward stop` calling
					// syscall.Kill. Route through pullRope so the
					// supervisor marks ropePulled and the retry loop
					// breaks out *without* respawning ssh. Without this,
					// ssh's own signal handler catches SIGHUP and exits
					// cleanly with code 255, which shouldRetry would
					// (correctly, for a real network drop) treat as
					// transient — so the daemon would just keep spawning
					// new ssh processes against the user's wishes.
					if pullRope != nil {
						pullRope()
					} else if proc.Process != nil {
						signalSessionGroup(proc.Process, syscall.SIGHUP)
						go func() {
							timer := time.NewTimer(escapeRopeKillGrace)
							defer timer.Stop()
							select {
							case <-done:
							case <-timer.C:
								signalSessionGroup(proc.Process, syscall.SIGKILL)
							}
						}()
					}
				}
			case <-done:
				return
			}
		}
	}()

	return func() {
		signal.Stop(sigCh)
		close(done)
	}
}

func resizePTY(stdin *os.File, ptmx *os.File) {
	if stdin == nil || ptmx == nil || !term.IsTerminal(stdin.Fd()) {
		return
	}
	_ = pty.InheritSize(stdin, ptmx)
}

func exitCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		if code >= 0 {
			return code
		}
		return 1
	}
	return 1
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// ptmxRef is a mutex-protected pointer to the current PTY master.
// copyInput captures the ref once at supervisor start, then each retry
// attempt swaps the underlying ptmx via set(). Writes during the
// reconnect transition (when ref is nil) silently no-op so the input
// loop never blocks.
type ptmxRef struct {
	mu sync.RWMutex
	f  *os.File
}

func newPtmxRef() *ptmxRef { return &ptmxRef{} }

func (r *ptmxRef) set(f *os.File) {
	r.mu.Lock()
	r.f = f
	r.mu.Unlock()
}

func (r *ptmxRef) get() *os.File {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.f
}

func (r *ptmxRef) write(p []byte) (int, error) {
	r.mu.RLock()
	f := r.f
	r.mu.RUnlock()
	if f == nil {
		// Mid-reconnect: drop the bytes rather than block. A tunnel
		// session has nothing to type at anyway (-N suppresses the
		// remote shell); an interactive session would only see this
		// if the supervisor were retrying, which we don't do today.
		return len(p), nil
	}
	return f.Write(p)
}

// procRef holds the current attempt's *os.Process so that signal-aware
// callers (pullRope) can reach the live ssh client even after the
// reconnect loop has swapped it.
type procRef struct {
	mu sync.RWMutex
	p  *os.Process
}

func newProcRef() *procRef { return &procRef{} }

func (r *procRef) set(p *os.Process) {
	r.mu.Lock()
	r.p = p
	r.mu.Unlock()
}

func (r *procRef) get() *os.Process {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.p
}

// attemptContext is the per-attempt parameter bundle handed to
// attemptOnce by the supervisor loop. Everything in here is either
// per-attempt scratch (proc, ptmx) or a long-lived pointer that the
// shared state (record, refs) writes through.
type attemptContext struct {
	command    sshcmd.Command
	stateDir   string
	record     *state.SessionRecord
	recordMu   *sync.Mutex
	env        []string
	stdin      *os.File
	ptmxRef    *ptmxRef
	procRef    *procRef
	output     *lockedWriter
	outputTap  *outputTap
	transcript *sessionRecorder
	// startupInterruptible is true while the current child attempt has not
	// emitted any PTY output. copyInput uses that window to treat Ctrl+C as a
	// local abort instead of forwarding it to an ssh client blocked in connect.
	startupInterruptible *atomic.Bool
	watchdog             WatchdogOptions
	stderr               io.Writer
	now                  func() time.Time
	// onPtmxReady, if non-nil, is invoked once per attempt after ptmxRef
	// points at the freshly-started PTY and the active record has been
	// written. The supervisor uses this to start the long-lived copyInput
	// goroutine on the first attempt; opening the input path after the
	// record write keeps an immediate overlay from racing ahead of the
	// current session's state file.
	onPtmxReady func()
	// pullRope, if non-nil, is the supervisor's escape rope handle.
	// forwardSignals invokes it on external SIGTERM/SIGHUP/SIGQUIT
	// so the retry loop's ropePulled check trips and the daemon
	// doesn't immediately respawn ssh after the kill.
	pullRope func()
	// onPanic, if non-nil, receives panics recovered in the attempt's
	// child goroutines (output loop, watchdog, signal forwarder). The
	// supervisor wires its failSupervisor here.
	onPanic func(string, any)
}

type sessionRecorderOptions struct {
	Disabled bool
	StateDir string
	Command  sshcmd.Command
	Env      []string
	MaxBytes int64
	Record   *state.SessionRecord
	RecordMu *sync.Mutex
	Now      func() time.Time
}

// transcriptWriter is the subset of *transcript.Writer the recorder
// drives; tests substitute a close-failing implementation to pin the
// persist-before-propagate contract of sessionRecorder.Close.
type transcriptWriter interface {
	WriteOutput(at time.Time, data []byte)
	WriteMarker(at time.Time, message string)
	Close(ended time.Time) (state.TranscriptSpec, error)
	StopReason() string
}

type sessionRecorder struct {
	mu       sync.Mutex
	disabled bool
	active   bool
	writer   transcriptWriter
	stateDir string
	command  sshcmd.Command
	env      []string
	maxBytes int64
	record   *state.SessionRecord
	recordMu *sync.Mutex
	now      func() time.Time
}

func newSessionRecorder(opts sessionRecorderOptions) *sessionRecorder {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &sessionRecorder{
		disabled: opts.Disabled,
		stateDir: opts.StateDir,
		command:  opts.Command,
		env:      append([]string(nil), opts.Env...),
		maxBytes: opts.MaxBytes,
		record:   opts.Record,
		recordMu: opts.RecordMu,
		now:      now,
	}
}

func (r *sessionRecorder) Toggle() (string, error) {
	if r == nil {
		return "", errors.New("recording is not available for this session")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.disabled {
		return "", errors.New("recording is disabled for this session")
	}
	if r.writer == nil {
		return r.startLocked()
	}
	if r.active {
		r.active = false
		r.writer.WriteMarker(r.now().UTC(), "recording paused from session overlay")
		return "recording paused", nil
	}
	r.active = true
	r.writer.WriteMarker(r.now().UTC(), "recording resumed from session overlay")
	return "recording resumed", nil
}

func (r *sessionRecorder) HelpAction() string {
	if r == nil || r.disabled {
		return "T recording off"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.writer == nil:
		return "T start recording"
	case r.active:
		return "T pause recording"
	default:
		return "T resume recording"
	}
}

func (r *sessionRecorder) WriteOutput(at time.Time, data []byte) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active || r.writer == nil {
		return
	}
	r.writer.WriteOutput(at, data)
}

func (r *sessionRecorder) WriteMarker(at time.Time, message string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writer == nil {
		return
	}
	r.writer.WriteMarker(at, message)
}

func (r *sessionRecorder) Close(ended time.Time) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	writer := r.writer
	r.active = false
	r.writer = nil
	r.mu.Unlock()
	if writer == nil {
		return nil
	}
	spec, err := writer.Close(ended)
	// Persist why recording stopped early (size limit, write error) so
	// the record explains a transcript that ends before the session
	// did — and persist before propagating any close error: a failing
	// disk is exactly the scenario in which stop_reason and ended_at
	// must not be lost (Close still returns a usable snapshot alongside
	// its error).
	spec.StopReason = writer.StopReason()
	r.updateRecord(spec)
	return err
}

func (r *sessionRecorder) startLocked() (string, error) {
	started := r.now().UTC()
	writer, spec, err := transcript.OpenWriter(transcript.WriterOptions{
		Path:    transcript.Path(r.stateDir, r.record.ID),
		Started: started,
		Header: transcript.Header{
			Width:   120,
			Height:  40,
			Command: sshcmd.QuoteArgv(r.command.Argv),
			Title:   defaultTranscriptTitle(r.record.TargetAlias),
			Env:     transcriptEnv(r.env),
		},
		MaxBytes: r.maxBytes,
	})
	if err != nil {
		return "", err
	}
	r.writer = writer
	r.active = true
	writer.WriteMarker(started, "recording started from session overlay")
	r.updateRecord(spec)
	return "recording started", nil
}

func (r *sessionRecorder) updateRecord(spec state.TranscriptSpec) {
	if r.record == nil || r.recordMu == nil {
		return
	}
	withLock(r.recordMu, func() {
		r.record.Transcript = &spec
		_ = state.WriteRecord(r.stateDir, *r.record)
	})
}

// withLock runs fn while holding mu, releasing it via defer. Using a
// deferred unlock (rather than an inline Lock/.../Unlock) means a panic
// inside fn releases the lock during unwind, so the deferred
// recorder.Close and the top-level panic recovery — which both need
// this same mutex to finalize the record — cannot deadlock the
// terminal-restore path.
func withLock(mu *sync.Mutex, fn func()) {
	mu.Lock()
	defer mu.Unlock()
	fn()
}

// attemptOnce spawns the SSH process once under a fresh PTY, swaps the
// shared refs so the input loop and escape rope can reach it, waits
// for the process to exit, and tears down its per-attempt goroutines
// (watchdog, signal forwarder, output reader). Returns proc.Wait()'s
// error verbatim — the caller decides whether to retry.
func attemptOnce(ac attemptContext) error {
	proc := exec.Command(ac.command.Argv[0], ac.command.Argv[1:]...)
	proc.Env = sessionEnv(ac.env, *ac.record)
	if ac.startupInterruptible != nil {
		ac.startupInterruptible.Store(true)
	}

	ptmx, err := pty.Start(proc)
	if err != nil {
		if ac.startupInterruptible != nil {
			ac.startupInterruptible.Store(false)
		}
		return err
	}
	// Close the PTY master on every exit path. The fast path below used
	// to return without closing — one leaked master fd per reconnect
	// attempt (regression from a112f3b). Double-close on the drain path
	// is harmless (ErrClosed, ignored).
	defer func() { _ = ptmx.Close() }()
	ac.ptmxRef.set(ptmx)
	ac.procRef.set(proc.Process)
	defer func() {
		if ac.startupInterruptible != nil {
			ac.startupInterruptible.Store(false)
		}
		ac.procRef.set(nil)
		ac.ptmxRef.set(nil)
	}()

	ac.recordMu.Lock()
	ac.record.SSHPID = proc.Process.Pid
	writeErr := state.WriteRecord(ac.stateDir, *ac.record)
	recordForTelemetry := *ac.record
	ac.recordMu.Unlock()
	if writeErr != nil {
		_ = proc.Process.Kill()
		_ = proc.Wait()
		return fmt.Errorf("write session record: %w", writeErr)
	}
	emitSessionTelemetry(ac.output, recordForTelemetry, ac.env)

	if ac.onPtmxReady != nil {
		ac.onPtmxReady()
	}

	outputDone := make(chan struct{})
	go func() {
		defer close(outputDone)
		defer recoverSupervisorPanic("session output loop", ac.onPanic)
		copyOutput(ac, ptmx)
	}()

	stopWatchdog := startLatencyWatchdog(latencyWatchdogConfig{
		Options:  ac.watchdog,
		StateDir: ac.stateDir,
		Record:   ac.record,
		RecordMu: ac.recordMu,
		Stderr:   ac.stderr,
		Process:  proc.Process,
		Now:      ac.now,
		OnPanic:  ac.onPanic,
	})
	stopSignals := forwardSignals(ac.stdin, ptmx, proc, ac.pullRope, ac.onPanic)

	waitErr := proc.Wait()
	stopWatchdog()
	stopSignals()
	select {
	case <-outputDone:
		return waitErr
	case <-time.After(ptyOutputDrainGrace):
	}
	// Close ptmx explicitly before waiting for the output-copy goroutine if it
	// did not finish during the drain grace. The reader only returns when its
	// source hits EOF, and a still-open ptmx can otherwise block forever.
	_ = ptmx.Close()
	<-outputDone
	return waitErr
}

func copyOutput(ac attemptContext, ptmx *os.File) {
	tracker := newOSCTracker()
	buf := make([]byte, 32*1024)
	for {
		n, err := ptmx.Read(buf)
		if n > 0 {
			if ac.startupInterruptible != nil {
				ac.startupInterruptible.Store(false)
			}
			chunk := buf[:n]
			observed, clean := tracker.ObserveAndFilter(chunk)
			if len(clean) > 0 {
				suppress := false
				if ac.outputTap != nil {
					suppress = ac.outputTap.observe(clean)
				}
				if !suppress {
					_, _ = ac.output.Write(clean)
					if ac.transcript != nil {
						ac.transcript.WriteOutput(time.Now().UTC(), clean)
					}
				}
			}
			if observed.RemoteChanged {
				recordRemoteState(ac, observed.Remote)
			}
			for _, mirror := range observed.Mirrors {
				mirrorRemoteSessionRecord(ac, mirror)
			}
		}
		if err != nil {
			return
		}
	}
}

func transcriptEnv(env []string) map[string]string {
	out := map[string]string{}
	for _, key := range []string{"SHELL", "TERM", "USER", "LOGNAME"} {
		if value := strings.TrimSpace(sessionEnvValue(env, key)); value != "" {
			out[key] = value
		}
	}
	return out
}

func defaultTranscriptTitle(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "ssherpa session"
	}
	return value
}

func emitSessionTelemetry(output io.Writer, record state.SessionRecord, env []string) {
	if output == nil || record.RemoteMirror || !shouldEmitSessionTelemetry(record, env) {
		return
	}
	payload, ok := sessionTelemetryOSC(record)
	if ok {
		_, _ = output.Write(payload)
	}
	frame, ok := sessionTelemetryFrame(record)
	if ok {
		_, _ = output.Write(frame)
	}
}

func shouldEmitSessionTelemetry(record state.SessionRecord, env []string) bool {
	if record.ParentID != "" {
		return true
	}
	return sessionEnvValue(env, "SSH_CONNECTION") != "" || sessionEnvValue(env, "SSH_TTY") != ""
}

func sessionEnvValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

// nestedMetadataBlockedEvent tags the once-per-session event recorded
// when a remote descendant reports itself with no parent id — positive
// proof the remote sshd stripped the SSHERPA_* lineage variables.
const nestedMetadataBlockedEvent = "nested_metadata_blocked"

func mirrorRemoteSessionRecord(ac attemptContext, record state.SessionRecord) {
	ac.recordMu.Lock()
	parent := *ac.record
	ac.recordMu.Unlock()
	record, backfilled, ok := remoteMirrorRecord(parent, record)
	if !ok {
		return
	}
	_ = state.WriteRecord(ac.stateDir, record)
	if backfilled {
		noteNestedMetadataBlocked(ac)
	}
}

// noteNestedMetadataBlocked records, once per session, that nested
// metadata never arrived on the remote side: the telemetry-backfill
// branch firing with an empty parent_id means the remote sshd rejected
// the SSHERPA_* environment (stock AcceptEnv allows none of it), so
// lineage under this session is reconstructed from telemetry instead
// of inherited env (§15.5). Surfacing it on the parent record lets the
// session map explain why remote-side lineage is flat.
func noteNestedMetadataBlocked(ac attemptContext) {
	now := ac.now
	if now == nil {
		now = time.Now
	}
	ac.recordMu.Lock()
	defer ac.recordMu.Unlock()
	for _, event := range ac.record.Events {
		if event.Type == nestedMetadataBlockedEvent {
			return
		}
	}
	ac.record.Events = append(ac.record.Events, state.SessionEvent{
		Time:    now().UTC(),
		Type:    nestedMetadataBlockedEvent,
		Message: "nested metadata blocked: remote sshd lacks AcceptEnv SSHERPA_*",
	})
	_ = state.WriteRecord(ac.stateDir, *ac.record)
}

func finalizeRemoteMirrors(stateDir string, parent state.SessionRecord, endedAt time.Time, exitCode int) {
	records, err := state.ListRecords(stateDir)
	if err != nil {
		return
	}
	finalized := map[string]bool{parent.ID: true}
	for {
		changed := false
		for _, record := range records {
			if !record.RemoteMirror || record.EndedAt != nil || !finalized[record.ParentID] {
				continue
			}
			record.EndedAt = &endedAt
			record.ExitCode = &exitCode
			if parent.DisconnectReason != "" && record.DisconnectReason == "" {
				record.DisconnectReason = parent.DisconnectReason
			}
			if err := state.WriteRecord(stateDir, record); err == nil {
				finalized[record.ID] = true
				changed = true
			}
		}
		if !changed {
			return
		}
		records, err = state.ListRecords(stateDir)
		if err != nil {
			return
		}
	}
}

// remoteMirrorRecord adapts a telemetry-reported descendant record for
// the local state dir. The second return reports whether the child
// arrived with no parent id and had its lineage backfilled from the
// parent — the detection signal that the remote sshd stripped the
// SSHERPA_* environment.
func remoteMirrorRecord(parent state.SessionRecord, child state.SessionRecord) (state.SessionRecord, bool, bool) {
	if child.ID == "" || child.ID == parent.ID || child.RemoteMirror {
		return state.SessionRecord{}, false, false
	}
	backfilled := false
	if child.ParentID == "" {
		backfilled = true
		child.ParentID = parent.ID
		child.Depth = parent.Depth + 1
		child.OriginHost = firstNonEmpty(parent.OriginHost, child.OriginHost)
		child.Route = appendRoute(parent.Route, child.Route, child.TargetAlias)
	} else if !isDescendantTelemetry(parent, child) {
		return state.SessionRecord{}, false, false
	}
	child.RemoteMirror = true
	child.Inherited = false
	child.LocalPID = 0
	child.SSHPID = 0
	if child.StateVersion == 0 {
		child.StateVersion = state.StateVersion
	}
	return child, backfilled, true
}

func isDescendantTelemetry(parent state.SessionRecord, child state.SessionRecord) bool {
	if child.ID == "" || child.ID == parent.ID || child.ParentID == "" {
		return false
	}
	if child.ParentID == parent.ID {
		return true
	}
	if len(parent.Route) == 0 || len(child.Route) <= len(parent.Route) {
		return false
	}
	for i, part := range parent.Route {
		if child.Route[i] != part {
			return false
		}
	}
	if parent.OriginHost != "" && child.OriginHost != "" && parent.OriginHost != child.OriginHost {
		return false
	}
	return true
}

func appendRoute(parentRoute []string, childRoute []string, fallbackTarget string) []string {
	route := append([]string(nil), parentRoute...)
	appendPart := func(part string) {
		part = strings.TrimSpace(part)
		if part == "" {
			return
		}
		if len(route) > 0 && route[len(route)-1] == part {
			return
		}
		route = append(route, part)
	}
	for _, part := range childRoute {
		appendPart(part)
	}
	if len(route) == len(parentRoute) {
		appendPart(fallbackTarget)
	}
	return route
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func recordRemoteState(ac attemptContext, observed remoteState) {
	ac.recordMu.Lock()
	if applyRemoteStateToRecord(ac.record, observed) {
		// Best effort: losing a live cwd/prompt update should not interrupt
		// the user's SSH stream.
		_ = state.WriteRecord(ac.stateDir, *ac.record)
	}
	ac.recordMu.Unlock()
}

// shouldRetry decides whether the supervisor's reconnect loop should
// run another attempt. Only tunnel-kind sessions retry; interactive
// sessions are always one-shot. Spawn failures and signaled deaths
// (escape rope, external SIGTERM) never retry. ssh exit code 1 — the
// signal for ExitOnForwardFailure (port in use locally) and host
// resolution failures — never retries because the next attempt will
// hit the same wall. Everything else (clean disconnect == 0, network
// error == 255, unknown codes) is treated as transient.
func shouldRetry(kind string, opts ReconnectOptions, waitErr error, attempt int) bool {
	if kind != state.KindTunnel && kind != state.KindProxy {
		return false
	}
	if !opts.Enabled {
		return false
	}
	if opts.MaxAttempts > 0 && attempt >= opts.MaxAttempts {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return false
	}
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return false
	}
	switch exitErr.ExitCode() {
	case 1:
		return false
	default:
		return true
	}
}

// computeBackoff returns the wait between attempt N and attempt N+1.
// Capped exponential growth starting at InitialBackoff, multiplied by
// Multiplier (default 2.0) per attempt, clamped at MaxBackoff. The loop
// stops growing once it would exceed the cap to avoid float overflow on
// very large attempt counts.
func computeBackoff(attempt int, opts ReconnectOptions) time.Duration {
	initial := opts.InitialBackoff
	if initial <= 0 {
		initial = DefaultReconnectInitialBackoff
	}
	maxBackoff := opts.MaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = DefaultReconnectMaxBackoff
	}
	mul := opts.Multiplier
	if mul <= 0 {
		mul = DefaultReconnectMultiplier
	}
	if attempt <= 1 {
		return initial
	}
	backoff := float64(initial)
	for i := 1; i < attempt; i++ {
		backoff *= mul
		if backoff >= float64(maxBackoff) {
			return maxBackoff
		}
	}
	return time.Duration(backoff)
}
