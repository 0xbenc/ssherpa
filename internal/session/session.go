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
)

type Metadata struct {
	TargetAlias string
	Hops        []string
	Route       []string
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
	Theme     termstyle.Theme
	ThemeName string
	ThemeFile string
	NoColor   bool
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
	record := buildRecord(command, metadata, now(), env)
	var recordMu sync.Mutex

	proc := exec.Command(command.Argv[0], command.Argv[1:]...)
	proc.Env = sessionEnv(env, record)

	restore, err := makeRawIfTerminal(stdin)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: put terminal in raw mode: %v\n", err)
		return 1
	}
	defer restore()

	ptmx, err := pty.Start(proc)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: run %s: %v\n", sshcmd.QuoteArgv(command.Argv), err)
		return 1
	}
	defer ptmx.Close()

	record.SSHPID = proc.Process.Pid
	if err := writeRecordLocked(stateDir, &record, &recordMu); err != nil {
		_ = proc.Process.Kill()
		_ = proc.Wait()
		fmt.Fprintf(stderr, "ssherpa: write session record: %v\n", err)
		return 1
	}

	output := &lockedWriter{w: stdout}
	done := make(chan struct{})
	outputDone := make(chan struct{})
	go copyInput(ptmx, stdin, output, stateDir, record.ID, opts.Composer, theme, done)
	go func() {
		_, _ = io.Copy(output, ptmx)
		close(outputDone)
	}()
	stopWatchdog := startLatencyWatchdog(latencyWatchdogConfig{
		Options:  opts.Watchdog,
		StateDir: stateDir,
		Record:   &record,
		RecordMu: &recordMu,
		Stderr:   stderr,
		Process:  proc.Process,
		Now:      now,
	})

	stopSignals := forwardSignals(stdin, ptmx, proc)
	waitErr := proc.Wait()
	close(done)
	stopWatchdog()
	stopSignals()
	_ = ptmx.Close()
	<-outputDone

	exitCode := exitCodeFromError(waitErr)
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

func buildRecord(command sshcmd.Command, metadata Metadata, started time.Time, env []string) state.SessionRecord {
	parentID, depth, inheritedRoute := state.InheritedMetadataFromEnv(env, "")
	route := append([]string(nil), inheritedRoute...)
	if len(metadata.Route) > 0 {
		route = append(route, metadata.Route...)
	} else if metadata.TargetAlias != "" {
		route = append(route, metadata.TargetAlias)
	}

	return state.SessionRecord{
		ID:           state.NewSessionID(started),
		ParentID:     parentID,
		Depth:        depth,
		Route:        route,
		TargetAlias:  metadata.TargetAlias,
		Hops:         append([]string(nil), metadata.Hops...),
		SSHArgv:      append([]string(nil), command.Argv...),
		StartedAt:    started.UTC(),
		LocalPID:     os.Getpid(),
		RunnerMode:   RunnerModeSupervised,
		StateVersion: state.StateVersion,
	}
}

func writeRecordLocked(stateDir string, record *state.SessionRecord, mu *sync.Mutex) error {
	mu.Lock()
	defer mu.Unlock()
	return state.WriteRecord(stateDir, *record)
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

func copyInput(ptmx *os.File, stdin *os.File, output *lockedWriter, stateDir string, currentID string, composer ComposerOptions, theme termstyle.Theme, done <-chan struct{}) {
	if stdin == nil {
		return
	}
	var buf [1]byte
	for {
		n, err := stdin.Read(buf[:])
		if n > 0 {
			switch {
			case buf[0] == OverlayHotkey:
				showSessionOverlay(stdin, output, stateDir, currentID, theme)
			case composer.enabled() && buf[0] == composer.hotkey():
				showComposer(stdin, output, ptmx, composer, theme)
			default:
				_, _ = ptmx.Write(buf[:n])
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

func showSessionOverlay(stdin *os.File, output *lockedWriter, stateDir string, currentID string, theme termstyle.Theme) {
	output.mu.Lock()
	defer output.mu.Unlock()

	frame := drawSessionOverlay(output.w, stdin, stateDir, currentID, theme)
	defer clearSessionOverlay(output.w, frame)

	var buf [1]byte
	for {
		n, err := stdin.Read(buf[:])
		if err != nil {
			return
		}
		if n == 0 {
			continue
		}
		switch buf[0] {
		case OverlayHotkey, 'q', 'Q', '\r', '\n', 0x1b, 0x03:
			return
		case 'r', 'R':
			clearSessionOverlay(output.w, frame)
			frame = drawSessionOverlay(output.w, stdin, stateDir, currentID, theme)
		}
	}
}

func drawSessionOverlay(w io.Writer, stdin *os.File, stateDir string, currentID string, theme termstyle.Theme) overlayFrame {
	width, height, terminalOutput := overlaySize(stdin)
	lines := sessionOverlayLines(stateDir, currentID)
	lines = styleOverlayLines(lines, theme)
	lines = append(lines, "", overlayHelp(fmt.Sprintf("%s/q/Esc close   r refresh   local only", OverlayHotkeyName), theme))

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
)

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
	signal.Notify(sigCh, syscall.SIGWINCH, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigCh:
				if sig == nil {
					continue
				}
				if sig == syscall.SIGWINCH {
					resizePTY(stdin, ptmx)
					continue
				}
				if proc.Process != nil {
					_ = proc.Process.Signal(sig)
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
