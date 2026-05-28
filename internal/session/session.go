package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/sessionview"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

const (
	RunnerModeSupervised   = "supervised"
	OverlayHotkey          = byte(0x1d)
	OverlayHotkeyName      = "Ctrl-]"
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
)

type Metadata struct {
	TargetAlias string
	Hops        []string
	Route       []string
	// Kind tags the recorded session by its high-level shape (e.g.
	// state.KindInteractive or state.KindTunnel). Leave empty for a
	// default interactive session; the session-map renderer treats an
	// empty Kind as interactive for backward compatibility with records
	// written before this field existed.
	Kind string
	// Forward, when non-nil, captures the runtime port-forward spec
	// for a tunnel-kind session. It is copied onto the session record
	// so the management commands (`ssherpa forward list/status/stop`)
	// can read local/remote/through without re-parsing SSHArgv.
	Forward *state.ForwardSpec
}

type Options struct {
	StateDir  string
	Stdin     *os.File
	Stdout    io.Writer
	Stderr    io.Writer
	Env       []string
	Now       func() time.Time
	Watchdog  WatchdogOptions
	Composer  ComposerOptions
	Reconnect ReconnectOptions
	Theme     termstyle.Theme
	ThemeName string
	ThemeFile string
	NoColor   bool
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
	var recordMu sync.Mutex

	// Detached mode has no PTY consumer — skip raw mode entirely.
	// makeRawIfTerminal handles a non-tty stdin gracefully too, but
	// explicitly skipping makes the daemon's intent obvious.
	if !opts.Detached {
		restore, err := makeRawIfTerminal(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: put terminal in raw mode: %v\n", err)
			return 1
		}
		defer restore()
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
	// of a mid-backoff sleep. See docs/escape-rope.md.
	ropeCtx, ropeCancel := context.WithCancel(context.Background())
	defer ropeCancel()
	var ropePulled atomic.Bool
	ropePulledCh := make(chan struct{})
	pullRope := func() {
		if !ropePulled.CompareAndSwap(false, true) {
			return
		}
		close(ropePulledCh)
		recordMu.Lock()
		record.DisconnectReason = EscapeRopeReason
		record.Events = append(record.Events, state.SessionEvent{
			Time:    now().UTC(),
			Type:    EscapeRopeReason,
			Message: "escape rope pulled; disconnecting all downstream sessions",
		})
		_ = state.WriteRecord(stateDir, record)
		recordMu.Unlock()
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

	output := &lockedWriter{w: stdout}
	inputDone := make(chan struct{})
	defer close(inputDone)
	var inputStarted atomic.Bool
	startInput := func() {
		if !inputStarted.CompareAndSwap(false, true) {
			return
		}
		go copyInput(ptmxRefShared, stdin, output, stateDir, record.ID, opts.Composer, theme, pullRope, inputDone)
	}
	if opts.Detached {
		// In detached mode there is no stdin to forward and no
		// overlay to render. Replace startInput with a no-op so the
		// onPtmxReady callback is harmless on every attempt.
		startInput = func() {}
	}

	var lastWaitErr error
	for attempt := 1; ; attempt++ {
		if ropePulled.Load() {
			break
		}
		ac := attemptContext{
			command:     command,
			stateDir:    stateDir,
			record:      &record,
			recordMu:    &recordMu,
			env:         env,
			stdin:       stdin,
			ptmxRef:     ptmxRefShared,
			procRef:     procRefShared,
			output:      output,
			watchdog:    opts.Watchdog,
			stderr:      stderr,
			now:         now,
			onPtmxReady: startInput,
		}
		waitErr := attemptOnce(ac)
		lastWaitErr = waitErr
		if attempt == 1 && isSpawnFailure(waitErr) {
			// Couldn't even start the first SSH process — surface the
			// raw spawn error like the pre-reconnect supervisor did.
			fmt.Fprintf(stderr, "ssherpa: run %s: %v\n", sshcmd.QuoteArgv(command.Argv), waitErr)
			return 1
		}
		if ropePulled.Load() {
			break
		}
		if waitErr == nil {
			// Clean exit — the reconnect retry loop is for *failed*
			// attempts; a successful one ends the supervisor.
			break
		}
		if !shouldRetry(metadata.Kind, opts.Reconnect, waitErr, attempt) {
			if attempt > 1 {
				recordMu.Lock()
				record.Events = append(record.Events, state.SessionEvent{
					Time:    now().UTC(),
					Type:    "reconnect_gave_up",
					Message: fmt.Sprintf("attempt %d: %v", attempt, waitErr),
				})
				_ = state.WriteRecord(stateDir, record)
				recordMu.Unlock()
			}
			break
		}
		backoff := computeBackoff(attempt, opts.Reconnect)
		recordMu.Lock()
		record.Events = append(record.Events, state.SessionEvent{
			Time:    now().UTC(),
			Type:    "reconnect_scheduled",
			Message: fmt.Sprintf("attempt %d failed: %v; retrying in %s", attempt, waitErr, backoff),
		})
		if record.Forward != nil {
			record.Forward.RetryCount = attempt
		}
		_ = state.WriteRecord(stateDir, record)
		recordMu.Unlock()
		fmt.Fprintf(stderr, "\r\nssherpa: tunnel attempt %d ended (%v); retrying in %s\r\n", attempt, waitErr, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ropePulledCh:
			timer.Stop()
		case <-timer.C:
		}
	}

	exitCode := exitCodeFromError(lastWaitErr)
	if ropePulled.Load() {
		exitCode = EscapeRopeExitCode
		fmt.Fprint(stderr, "\r\nssherpa: escape rope pulled — disconnecting all downstream sessions\r\n")
	}
	endedAt := now().UTC()
	recordMu.Lock()
	record.EndedAt = &endedAt
	record.ExitCode = &exitCode
	err = state.WriteRecord(stateDir, record)
	recordMu.Unlock()
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: update session record: %v\n", err)
		if exitCode == 0 {
			return 1
		}
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

	var forward *state.ForwardSpec
	if metadata.Forward != nil {
		copyFwd := *metadata.Forward
		forward = &copyFwd
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
		TargetAlias:  metadata.TargetAlias,
		Hops:         append([]string(nil), metadata.Hops...),
		SSHArgv:      append([]string(nil), command.Argv...),
		Kind:         metadata.Kind,
		Forward:      forward,
		StartedAt:    started.UTC(),
		LocalPID:     os.Getpid(),
		RunnerMode:   RunnerModeSupervised,
		StateVersion: state.StateVersion,
	}
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

type overlayFrame struct {
	terminal bool
	startRow int
	lines    int
}

func copyInput(ptmxRef *ptmxRef, stdin *os.File, output *lockedWriter, stateDir string, currentID string, composer ComposerOptions, theme termstyle.Theme, pullRope func(), done <-chan struct{}) {
	if stdin == nil {
		return
	}
	var buf [1]byte
	for {
		n, err := stdin.Read(buf[:])
		if n > 0 {
			switch {
			case buf[0] == OverlayHotkey:
				showSessionOverlay(stdin, output, stateDir, currentID, theme, pullRope, time.Now())
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

func showSessionOverlay(stdin *os.File, output *lockedWriter, stateDir string, currentID string, theme termstyle.Theme, pullRope func(), openedAt time.Time) {
	output.mu.Lock()
	defer output.mu.Unlock()

	frame := drawSessionOverlay(output.w, stdin, stateDir, currentID, theme)
	defer func() { clearSessionOverlay(output.w, frame) }()

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
				return
			}
			confirming = false
			clearSessionOverlay(output.w, frame)
			frame = drawSessionOverlay(output.w, stdin, stateDir, currentID, theme)
			continue
		}

		if key == OverlayHotkey {
			if time.Since(lastTap) <= escapeRopePanicWindow {
				taps++
				lastTap = time.Now()
				if taps >= escapeRopePanicTaps {
					pull()
					return
				}
				continue
			}
			return // a single, settled hotkey press closes the overlay
		}
		// Any other key breaks a panic-tap streak.
		lastTap = time.Time{}

		switch key {
		case 'q', 'Q', '\r', '\n', 0x1b, 0x03:
			return
		case 'X':
			// Escape rope: confirm before tearing down every layer.
			confirming = true
			clearSessionOverlay(output.w, frame)
			frame = drawEscapeConfirm(output.w, stdin, theme)
		case 'r', 'R':
			clearSessionOverlay(output.w, frame)
			frame = drawSessionOverlay(output.w, stdin, stateDir, currentID, theme)
		}
	}
}

func drawEscapeConfirm(w io.Writer, stdin *os.File, theme termstyle.Theme) overlayFrame {
	lines := []string{
		overlayTitle("ssherpa escape rope", theme),
		theme.Style(termstyle.RoleWarning, "Disconnect ALL nested sessions and return to the outermost shell?"),
		overlayHelp("press X to confirm   any other key cancels", theme),
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

func drawSessionOverlay(w io.Writer, stdin *os.File, stateDir string, currentID string, theme termstyle.Theme) overlayFrame {
	width, height, terminalOutput := overlaySize(stdin)
	lines := sessionOverlayLines(stateDir, currentID)
	lines = styleOverlayLines(lines, theme)
	lines = append(lines, "", overlayHelp(fmt.Sprintf("%s/q/Esc close   r refresh   X escape rope (quit all layers, confirm)   %sx3 panic   local only", OverlayHotkeyName, OverlayHotkeyName), theme))

	if !terminalOutput {
		fmt.Fprintln(w)
		fmt.Fprintln(w, overlayTitle("ssherpa session overlay", theme))
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

func styleOverlayLines(lines []string, theme termstyle.Theme) []string {
	styled := append([]string(nil), lines...)
	for i, line := range styled {
		switch {
		case i == 0:
			styled[i] = overlayTitle(line, theme)
		case strings.HasPrefix(line, "state:"):
			styled[i] = theme.Style(termstyle.RoleMuted, line)
		case strings.HasPrefix(line, "active:"):
			styled[i] = theme.Style(termstyle.RoleSuccess, line)
		case strings.Contains(line, "[active]"):
			styled[i] = theme.Style(termstyle.RoleSuccess, line)
		case strings.Contains(line, "[exit"):
			styled[i] = theme.Style(termstyle.RoleMuted, line)
		case strings.Contains(line, "current"):
			styled[i] = theme.Style(termstyle.RolePrimary, line)
		}
	}
	return styled
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

func sessionOverlayLines(stateDir string, currentID string) []string {
	records, err := state.ListRecords(stateDir)
	if err != nil {
		return []string{
			"ssherpa session map (local overlay)",
			"state: " + stateDir,
			"error: " + err.Error(),
		}
	}
	lines := sessionview.MapLinesWithOptions(stateDir, records, sessionview.MapOptions{CurrentID: currentID})
	if len(lines) > 0 {
		lines[0] = "ssherpa session map (local overlay)"
	}
	return lines
}

const (
	defaultLatencyProbeInterval = 10 * time.Second
	defaultLatencyProbeTimeout  = 10 * time.Second
	latencyKillGrace            = 2 * time.Second
	// escapeRopeKillGrace is short: pulling the rope means "get me out now", so
	// we escalate from SIGHUP to SIGKILL quickly to guarantee a prompt local
	// return even if the ssh client ignores the hangup.
	escapeRopeKillGrace = 750 * time.Millisecond
	// Mashing the overlay hotkey escapeRopePanicTaps times within
	// escapeRopePanicWindow of each press pulls the rope immediately, skipping
	// the confirm step — a blind panic exit for when a layer is wedged and you
	// cannot read the overlay. A single, settled press still just closes it.
	escapeRopePanicWindow = 400 * time.Millisecond
	escapeRopePanicTaps   = 3
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

func forwardSignals(stdin *os.File, ptmx *os.File, proc *exec.Cmd) func() {
	resizePTY(stdin, ptmx)

	sigCh := make(chan os.Signal, 8)
	signal.Notify(sigCh, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	done := make(chan struct{})
	go func() {
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
					// The supervisor was asked to terminate. Tear down the whole
					// child process group and guarantee we exit so the deferred
					// terminal restore actually runs — SIGQUIT in particular would
					// otherwise terminate ssherpa with the tty stuck in raw mode.
					if proc.Process != nil {
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
	command  sshcmd.Command
	stateDir string
	record   *state.SessionRecord
	recordMu *sync.Mutex
	env      []string
	stdin    *os.File
	ptmxRef  *ptmxRef
	procRef  *procRef
	output   *lockedWriter
	watchdog WatchdogOptions
	stderr   io.Writer
	now      func() time.Time
	// onPtmxReady, if non-nil, is invoked once per attempt right
	// after ptmxRef has been set to the freshly-started PTY. The
	// supervisor uses this to start the long-lived copyInput
	// goroutine on the first attempt — before then a fixture-driven
	// test's stdin bytes could race ahead of the first set and get
	// dropped by ptmxRef.write's nil fallback.
	onPtmxReady func()
}

// attemptOnce spawns the SSH process once under a fresh PTY, swaps the
// shared refs so the input loop and escape rope can reach it, waits
// for the process to exit, and tears down its per-attempt goroutines
// (watchdog, signal forwarder, output reader). Returns proc.Wait()'s
// error verbatim — the caller decides whether to retry.
func attemptOnce(ac attemptContext) error {
	proc := exec.Command(ac.command.Argv[0], ac.command.Argv[1:]...)
	proc.Env = sessionEnv(ac.env, *ac.record)

	ptmx, err := pty.Start(proc)
	if err != nil {
		return err
	}
	ac.ptmxRef.set(ptmx)
	ac.procRef.set(proc.Process)
	defer func() {
		ac.procRef.set(nil)
		ac.ptmxRef.set(nil)
	}()
	if ac.onPtmxReady != nil {
		ac.onPtmxReady()
	}

	ac.recordMu.Lock()
	ac.record.SSHPID = proc.Process.Pid
	writeErr := state.WriteRecord(ac.stateDir, *ac.record)
	ac.recordMu.Unlock()
	if writeErr != nil {
		_ = proc.Process.Kill()
		_ = proc.Wait()
		_ = ptmx.Close()
		return fmt.Errorf("write session record: %w", writeErr)
	}

	outputDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(ac.output, ptmx)
		close(outputDone)
	}()

	stopWatchdog := startLatencyWatchdog(latencyWatchdogConfig{
		Options:  ac.watchdog,
		StateDir: ac.stateDir,
		Record:   ac.record,
		RecordMu: ac.recordMu,
		Stderr:   ac.stderr,
		Process:  proc.Process,
		Now:      ac.now,
	})
	stopSignals := forwardSignals(ac.stdin, ptmx, proc)

	waitErr := proc.Wait()
	stopWatchdog()
	stopSignals()
	// Close ptmx explicitly before waiting for the output-copy goroutine
	// to drain — io.Copy(ac.output, ptmx) only returns when its source
	// hits EOF, and a still-open ptmx never does.
	_ = ptmx.Close()
	<-outputDone
	return waitErr
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
	if kind != state.KindTunnel {
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
