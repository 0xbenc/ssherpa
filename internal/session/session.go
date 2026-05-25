package session

import (
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
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

const (
	RunnerModeSupervised = "supervised"
	OverlayHotkey        = byte(0x1d)
	OverlayHotkeyName    = "Ctrl-]"
)

type Metadata struct {
	TargetAlias string
	Hops        []string
	Route       []string
}

type Options struct {
	StateDir string
	Stdin    *os.File
	Stdout   io.Writer
	Stderr   io.Writer
	Env      []string
	Now      func() time.Time
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
	record := buildRecord(command, metadata, now(), env)

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
	if err := state.WriteRecord(stateDir, record); err != nil {
		_ = proc.Process.Kill()
		_ = proc.Wait()
		fmt.Fprintf(stderr, "ssherpa: write session record: %v\n", err)
		return 1
	}

	output := &lockedWriter{w: stdout}
	done := make(chan struct{})
	outputDone := make(chan struct{})
	go copyInput(ptmx, stdin, output, stateDir, record.ID, done)
	go func() {
		_, _ = io.Copy(output, ptmx)
		close(outputDone)
	}()

	stopSignals := forwardSignals(stdin, ptmx, proc)
	waitErr := proc.Wait()
	close(done)
	stopSignals()
	_ = ptmx.Close()
	<-outputDone

	exitCode := exitCodeFromError(waitErr)
	endedAt := now().UTC()
	record.EndedAt = &endedAt
	record.ExitCode = &exitCode
	if err := state.WriteRecord(stateDir, record); err != nil {
		fmt.Fprintf(stderr, "ssherpa: update session record: %v\n", err)
		if exitCode == 0 {
			return 1
		}
	}
	return exitCode
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

func copyInput(ptmx *os.File, stdin *os.File, output *lockedWriter, stateDir string, currentID string, done <-chan struct{}) {
	if stdin == nil {
		return
	}
	var buf [1]byte
	for {
		n, err := stdin.Read(buf[:])
		if n > 0 {
			if buf[0] == OverlayHotkey {
				showSessionOverlay(stdin, output, stateDir, currentID)
			} else {
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

func showSessionOverlay(stdin *os.File, output *lockedWriter, stateDir string, currentID string) {
	output.mu.Lock()
	defer output.mu.Unlock()

	frame := drawSessionOverlay(output.w, stdin, stateDir, currentID)
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
			frame = drawSessionOverlay(output.w, stdin, stateDir, currentID)
		}
	}
}

func drawSessionOverlay(w io.Writer, stdin *os.File, stateDir string, currentID string) overlayFrame {
	width, height, terminalOutput := overlaySize(stdin)
	lines := sessionOverlayLines(stateDir, currentID)
	lines = append(lines, "", fmt.Sprintf("%s/q/Esc close   r refresh   local only", OverlayHotkeyName))

	if !terminalOutput {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "----- ssherpa session overlay -----")
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

func sessionOverlayLines(stateDir string, currentID string) []string {
	records, err := state.ListRecords(stateDir)
	if err != nil {
		return []string{
			"ssherpa session map (local overlay)",
			"state: " + stateDir,
			"error: " + err.Error(),
		}
	}
	lines := sessionview.MapLines(stateDir, records, currentID)
	if len(lines) > 0 {
		lines[0] = "ssherpa session map (local overlay)"
	}
	return lines
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
