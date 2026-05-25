package session

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/charmbracelet/x/term"
	"github.com/creack/pty"
)

const RunnerModeSupervised = "supervised"

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

	done := make(chan struct{})
	outputDone := make(chan struct{})
	go copyInput(ptmx, stdin, done)
	go func() {
		_, _ = io.Copy(stdout, ptmx)
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

func copyInput(ptmx *os.File, stdin *os.File, done <-chan struct{}) {
	if stdin == nil {
		return
	}
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(ptmx, stdin)
		close(copyDone)
	}()
	select {
	case <-done:
	case <-copyDone:
	}
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
