package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

// supervisorFlag is the hidden CLI prefix the parent process passes
// when re-execing itself as a detached supervisor. It is intentionally
// ugly so a curious user typing `ssherpa --` and tab-completing does
// not surface it; it is also not documented in `ssherpa help`.
const (
	supervisorFlag      = "--__supervisor"
	detachedIDFlag      = "--__detached-id"
	detachedStateFlag   = "--__detached-state-dir"
	detachedLogPathFlag = "--__detached-log-path"
)

var daemonRecordReadyTimeout = 2 * time.Second

// daemonStartProcess is the seam tests use to swap in a stub spawner.
// In production it re-execs ssherpa with the supervisor flags set; the
// returned PID is reported to the parent invocation's stdout so the
// user can `ssherpa forward stop <pid>` (or follow the log) without
// fishing for it.
var daemonStartProcess = func(name string, argv []string, attr *os.ProcAttr) (int, error) {
	proc, err := os.StartProcess(name, argv, attr)
	if err != nil {
		return 0, err
	}
	pid := proc.Pid
	if err := proc.Release(); err != nil {
		return pid, err
	}
	return pid, nil
}

// daemonizeForward is the parent-side of `ssherpa forward --background`.
// It validates state dir + log path, generates a session ID up-front so
// it can print it before the child writes the first record, then spawns
// the child with SysProcAttr.Setsid (detaching from the controlling
// TTY in one step on Linux + Darwin). The child argv is the original
// forward args minus `--background`, prefixed by the hidden supervisor
// flags so cli.Run's dispatch routes it to runSupervisorChild.
func daemonizeForward(originalArgs []string, flags forwardFlags, stdout io.Writer, stderr io.Writer) int {
	return daemonizeRoute("forward", originalArgs, flags.StateDir, stdout, stderr)
}

func daemonizeProxy(originalArgs []string, flags proxyFlags, stdout io.Writer, stderr io.Writer) int {
	return daemonizeRoute("proxy", originalArgs, flags.StateDir, stdout, stderr)
}

func daemonizeRoute(command string, originalArgs []string, stateDirOverride string, stdout io.Writer, stderr io.Writer) int {
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}

	// Reserve the session ID + log path before the fork so both the
	// parent's output and the child's record agree.
	sessionID := state.NewSessionID(time.Now())
	sessionsDir := state.SessionsDir(stateDir)
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "ssherpa: create sessions dir: %v\n", err)
		return 1
	}
	logPath := filepath.Join(sessionsDir, sessionID+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: open log file: %v\n", err)
		return 1
	}
	defer logFile.Close()

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: open %s: %v\n", os.DevNull, err)
		return 1
	}
	defer devnull.Close()

	self, err := os.Executable()
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve self executable: %v\n", err)
		return 1
	}

	childCommand := stripFlag(originalArgs, "--background")
	childArgs := []string{self, supervisorFlag,
		detachedIDFlag, sessionID,
		detachedStateFlag, stateDir,
		detachedLogPathFlag, logPath,
		command}
	childArgs = append(childArgs, childCommand...)

	pid, err := daemonStartProcess(self, childArgs, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{devnull, logFile, logFile},
		Sys:   &syscall.SysProcAttr{Setsid: true},
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: spawn detached supervisor: %v\n", err)
		return 1
	}

	waitForDetachedRecord(stateDir, sessionID, daemonRecordReadyTimeout)

	fmt.Fprintf(stdout, "ssherpa: %s detached\n", command)
	fmt.Fprintf(stdout, "  session id: %s\n", sessionID)
	fmt.Fprintf(stdout, "  daemon pid: %d\n", pid)
	fmt.Fprintf(stdout, "  log file:   %s\n", logPath)
	fmt.Fprintf(stdout, "  stop with:  ssherpa %s stop %s\n", command, sessionID)
	return 0
}

func waitForDetachedRecord(stateDir string, sessionID string, timeout time.Duration) bool {
	if timeout <= 0 {
		return false
	}
	deadline := time.Now().Add(timeout)
	for {
		if _, err := state.ReadRecord(stateDir, sessionID); err == nil {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// stripFlag returns args with the first occurrence of flag removed.
// Used to strip --background from the original forward args before
// they're handed to the detached child (the child must not re-enter
// the daemonize path).
func stripFlag(args []string, flag string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == flag {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// runSupervisorChild is the child-side entry point after the parent
// daemonized. It strips the hidden flags, makes the state dir override
// authoritative (env var alternative would race a user's $SSHERPA_STATE_DIR),
// then routes back into the relevant command body with detached=true so
// flag parsing and validation stay identical between foreground and
// background paths.
func runSupervisorChild(args []string, stdout io.Writer, stderr io.Writer) int {
	var recordID, stateDirOverride string
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case detachedIDFlag:
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "ssherpa: --__detached-id requires a value")
				return 1
			}
			recordID = args[i+1]
			i++
		case detachedStateFlag:
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "ssherpa: --__detached-state-dir requires a value")
				return 1
			}
			stateDirOverride = args[i+1]
			i++
		case detachedLogPathFlag:
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "ssherpa: --__detached-log-path requires a value")
				return 1
			}
			// Currently informational only — the parent opened it
			// and bound it to fds 1/2 of this child. Recorded here
			// for forward extension (a future `forward status` could
			// surface the log path even if the file got moved).
			_ = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	if recordID == "" {
		fmt.Fprintln(stderr, "ssherpa: --__supervisor requires --__detached-id")
		return 1
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "ssherpa: --__supervisor requires a subcommand")
		return 1
	}
	command := rest[0]
	commandArgs := rest[1:]
	if stateDirOverride != "" {
		// Inject as the first arg so the parent's resolved state dir
		// is authoritative even if the user's environment changes
		// between fork and exec (or if SSHERPA_STATE_DIR is set
		// inconsistently in the daemon's env).
		commandArgs = append([]string{"--state-dir", stateDirOverride}, commandArgs...)
	}
	switch command {
	case "forward":
		return runForwardWith(commandArgs, true, recordID, stdout, stderr)
	case "proxy":
		return runProxyWith(commandArgs, true, recordID, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: --__supervisor does not support %q\n", command)
		return 1
	}
}
