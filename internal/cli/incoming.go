package cli

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/incoming"
)

const incomingUsage = `Usage:
  ssherpa incoming list [--json] [--runtime-dir PATH]
  ssherpa incoming mark [--watch-parent PID] [--quiet] [--runtime-dir PATH]
  ssherpa incoming hook [--shell sh|bash|zsh|fish]
`

type incomingFlags struct {
	JSON       bool
	RuntimeDir string
	Quiet      bool
	WatchPID   int
	Shell      string
	Rest       []string
}

type incomingListOutput struct {
	RuntimeDir string             `json:"runtime_dir"`
	Count      int                `json:"count"`
	Sessions   []incoming.Session `json:"sessions"`
}

func runIncoming(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || hasHelpFlag(args) {
		fmt.Fprint(stdout, incomingUsage)
		return 0
	}
	switch args[0] {
	case "list":
		return runIncomingList(args[1:], stdout, stderr)
	case "mark":
		return runIncomingMark(args[1:], stdout, stderr)
	case "hook":
		return runIncomingHook(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown incoming command %q\n", args[0])
		return 1
	}
}

func runIncomingList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseIncomingFlags(args, stderr, "list")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: incoming list does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}
	opts := incoming.Options{RuntimeDir: flags.RuntimeDir, Env: os.Environ()}
	dir, err := incoming.RuntimeDir(opts)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve incoming runtime dir: %v\n", err)
		return 1
	}
	sessions, err := incoming.List(opts)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list incoming SSH sessions: %v\n", err)
		return 1
	}
	if flags.JSON {
		writeJSON(stdout, incomingListOutput{RuntimeDir: dir, Count: len(sessions), Sessions: sessions})
		return 0
	}
	if len(sessions) == 0 {
		fmt.Fprintln(stdout, "No incoming SSH sessions.")
		return 0
	}
	fmt.Fprintf(stdout, "Incoming SSH sessions\nruntime: %s\n", dir)
	for _, session := range sessions {
		fmt.Fprintf(stdout, "%s\t%s\t%s\t%s", session.User, session.TTY, defaultString(session.ClientIP, "-"), session.Kind)
		if session.SSHerpa {
			fmt.Fprintf(stdout, "\troute=%s", strings.Join(session.Route, ","))
			if session.OriginHost != "" {
				fmt.Fprintf(stdout, "\torigin=%s", session.OriginHost)
			}
		}
		fmt.Fprintln(stdout)
	}
	return 0
}

func runIncomingMark(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseIncomingFlags(args, stderr, "mark")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: incoming mark does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}
	opts := incoming.Options{RuntimeDir: flags.RuntimeDir, Env: os.Environ()}
	marker := incoming.MarkerFromEnv(os.Environ(), flags.WatchPID, time.Now())
	path, err := incoming.WriteMarker(marker, opts)
	if err != nil {
		if !flags.Quiet {
			fmt.Fprintf(stderr, "ssherpa: write incoming marker: %v\n", err)
		}
		return 1
	}
	if !flags.Quiet {
		fmt.Fprintf(stdout, "incoming marker: %s\n", path)
	}
	if flags.WatchPID <= 0 {
		return 0
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	defer signal.Stop(signals)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-signals:
			incoming.RemoveMarker(path)
			return 0
		case <-ticker.C:
			if !incomingParentAlive(flags.WatchPID) {
				incoming.RemoveMarker(path)
				return 0
			}
		}
	}
}

func runIncomingHook(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseIncomingFlags(args, stderr, "hook")
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: incoming hook does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}
	hook, err := incoming.ShellHook(flags.Shell)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	fmt.Fprint(stdout, hook)
	return 0
}

func parseIncomingFlags(args []string, stderr io.Writer, command string) (incomingFlags, bool) {
	var flags incomingFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			flags.Rest = append(flags.Rest, args[i+1:]...)
			return flags, true
		case arg == "--json" && command == "list":
			flags.JSON = true
		case arg == "--runtime-dir":
			value, ok := nextArg(args, &i, stderr, "--runtime-dir")
			if !ok {
				return flags, false
			}
			flags.RuntimeDir = value
		case strings.HasPrefix(arg, "--runtime-dir="):
			flags.RuntimeDir = strings.TrimPrefix(arg, "--runtime-dir=")
		case arg == "--quiet" && command == "mark":
			flags.Quiet = true
		case arg == "--watch-parent" && command == "mark":
			value, ok := nextArg(args, &i, stderr, "--watch-parent")
			if !ok {
				return flags, false
			}
			pid, ok := parseIncomingPID(value, stderr)
			if !ok {
				return flags, false
			}
			flags.WatchPID = pid
		case strings.HasPrefix(arg, "--watch-parent=") && command == "mark":
			pid, ok := parseIncomingPID(strings.TrimPrefix(arg, "--watch-parent="), stderr)
			if !ok {
				return flags, false
			}
			flags.WatchPID = pid
		case arg == "--shell" && command == "hook":
			value, ok := nextArg(args, &i, stderr, "--shell")
			if !ok {
				return flags, false
			}
			flags.Shell = value
		case strings.HasPrefix(arg, "--shell=") && command == "hook":
			flags.Shell = strings.TrimPrefix(arg, "--shell=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown incoming %s flag %q\n", command, arg)
			return flags, false
		default:
			flags.Rest = append(flags.Rest, arg)
		}
	}
	return flags, true
}

func parseIncomingPID(value string, stderr io.Writer) (int, bool) {
	pid, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || pid <= 0 {
		fmt.Fprintln(stderr, "ssherpa: --watch-parent must be a positive process id")
		return 0, false
	}
	return pid, true
}

func incomingParentAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, syscall.Signal(0))
	return err == nil || err == syscall.EPERM
}
