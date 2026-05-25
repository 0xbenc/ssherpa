package cli

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

const sessionUsage = `Usage:
  ssherpa session list [--json] [--state-dir PATH]
  ssherpa session show SESSION_ID [--json] [--state-dir PATH]
  ssherpa session prune [--older-than DURATION] [--dry-run] [--state-dir PATH]
`

type sessionFlags struct {
	JSON      bool
	StateDir  string
	OlderThan time.Duration
	DryRun    bool
	Rest      []string
}

func runSession(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || hasHelpFlag(args) {
		fmt.Fprint(stdout, sessionUsage)
		return 0
	}

	switch args[0] {
	case "list":
		return runSessionList(args[1:], stdout, stderr)
	case "show":
		return runSessionShow(args[1:], stdout, stderr)
	case "prune":
		return runSessionPrune(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown session command %q\n", args[0])
		return 1
	}
}

func runSessionList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session list does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list sessions: %v\n", err)
		return 1
	}

	if flags.JSON {
		writeJSON(stdout, records)
		return 0
	}
	for _, record := range records {
		fmt.Fprintf(stdout, "%s\t%s\tdepth=%d\ttarget=%s\troute=%s\tstarted=%s\n",
			record.ID,
			record.Status(),
			record.Depth,
			defaultString(record.TargetAlias, "-"),
			formatRoute(record.Route),
			record.StartedAt.Local().Format(time.RFC3339),
		)
	}
	return 0
}

func runSessionShow(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 1 {
		fmt.Fprintln(stderr, "ssherpa: session show requires exactly one session id")
		return 1
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	record, err := state.ReadRecord(stateDir, flags.Rest[0])
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: show session: %v\n", err)
		return 2
	}

	if flags.JSON {
		writeJSON(stdout, record)
		return 0
	}
	printSessionRecord(stdout, record)
	return 0
}

func runSessionPrune(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, true)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session prune does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}
	if flags.OlderThan == 0 {
		flags.OlderThan = state.DefaultPrune
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	result, err := state.PruneRecords(stateDir, flags.OlderThan, time.Now(), flags.DryRun)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: prune sessions: %v\n", err)
		return 1
	}

	if flags.JSON {
		writeJSON(stdout, result)
		return 0
	}
	prefix := "removed"
	if flags.DryRun {
		prefix = "would remove"
	}
	fmt.Fprintf(stdout, "%s %d session record(s)\n", prefix, len(result.Records))
	for _, record := range result.Records {
		fmt.Fprintf(stdout, "%s\tended=%s\ttarget=%s\n",
			record.ID,
			formatOptionalTime(record.EndedAt),
			defaultString(record.TargetAlias, "-"),
		)
	}
	return 0
}

func parseSessionFlags(args []string, stderr io.Writer, allowPruneFlags bool) (sessionFlags, bool) {
	flags := sessionFlags{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			flags.Rest = append(flags.Rest, args[i+1:]...)
			return flags, true
		case arg == "--json":
			flags.JSON = true
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--older-than" && allowPruneFlags:
			value, ok := nextArg(args, &i, stderr, "--older-than")
			if !ok {
				return flags, false
			}
			olderThan, ok := parseDuration(value, stderr, "--older-than")
			if !ok {
				return flags, false
			}
			flags.OlderThan = olderThan
		case strings.HasPrefix(arg, "--older-than=") && allowPruneFlags:
			olderThan, ok := parseDuration(strings.TrimPrefix(arg, "--older-than="), stderr, "--older-than")
			if !ok {
				return flags, false
			}
			flags.OlderThan = olderThan
		case arg == "--dry-run" && allowPruneFlags:
			flags.DryRun = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown session flag %q\n", arg)
			return flags, false
		default:
			flags.Rest = append(flags.Rest, arg)
		}
	}
	return flags, true
}

func parseDuration(value string, stderr io.Writer, flag string) (time.Duration, bool) {
	duration, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || duration <= 0 {
		fmt.Fprintf(stderr, "ssherpa: %s must be a positive duration like 168h or 30m\n", flag)
		return 0, false
	}
	return duration, true
}

func printSessionRecord(stdout io.Writer, record state.SessionRecord) {
	fmt.Fprintf(stdout, "id:\t%s\n", record.ID)
	fmt.Fprintf(stdout, "status:\t%s\n", record.Status())
	fmt.Fprintf(stdout, "target:\t%s\n", defaultString(record.TargetAlias, "-"))
	fmt.Fprintf(stdout, "depth:\t%d\n", record.Depth)
	fmt.Fprintf(stdout, "route:\t%s\n", formatRoute(record.Route))
	fmt.Fprintf(stdout, "hops:\t%s\n", formatRoute(record.Hops))
	fmt.Fprintf(stdout, "started:\t%s\n", record.StartedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(stdout, "ended:\t%s\n", formatOptionalTime(record.EndedAt))
	if record.ExitCode != nil {
		fmt.Fprintf(stdout, "exit_code:\t%d\n", *record.ExitCode)
	}
	fmt.Fprintf(stdout, "local_pid:\t%d\n", record.LocalPID)
	fmt.Fprintf(stdout, "ssh_pid:\t%d\n", record.SSHPID)
	fmt.Fprintf(stdout, "runner:\t%s\n", record.RunnerMode)
	fmt.Fprintf(stdout, "argv:\t%s\n", strings.Join(record.SSHArgv, " "))
}

func formatRoute(route []string) string {
	if len(route) == 0 {
		return "-"
	}
	return strings.Join(route, " -> ")
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Local().Format(time.RFC3339)
}
