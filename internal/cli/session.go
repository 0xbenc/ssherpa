package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/sessionview"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

const sessionUsage = `Usage:
  ssherpa session list [--json] [--state-dir PATH]
  ssherpa session map [--json] [--all] [--state-dir PATH]
  ssherpa session show SESSION_ID [--json] [--state-dir PATH]
  ssherpa session stop-all [--json] [--state-dir PATH]
  ssherpa session prune [--older-than DURATION] [--dry-run] [--state-dir PATH]
`

type sessionMapOutput struct {
	StateDir string              `json:"state_dir"`
	Scope    string              `json:"scope"`
	Total    int                 `json:"total"`
	Active   int                 `json:"active"`
	Recorded int                 `json:"recorded"`
	Roots    []state.SessionNode `json:"roots"`
}

type sessionFlags struct {
	JSON      bool
	StateDir  string
	OlderThan time.Duration
	DryRun    bool
	All       bool
	Rest      []string
}

type sessionStopAllResult struct {
	Scanned  int                    `json:"scanned"`
	Matched  int                    `json:"matched"`
	Signaled int                    `json:"signaled"`
	Stopped  int                    `json:"stopped"`
	Pending  int                    `json:"pending"`
	Errors   int                    `json:"errors"`
	Records  []sessionStopAllRecord `json:"records"`
}

type sessionStopAllRecord struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Target string `json:"target,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func runSession(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || hasHelpFlag(args) {
		fmt.Fprint(stdout, sessionUsage)
		return 0
	}

	switch args[0] {
	case "list":
		return runSessionList(args[1:], stdout, stderr)
	case "map":
		return runSessionMap(args[1:], stdout, stderr)
	case "show":
		return runSessionShow(args[1:], stdout, stderr)
	case "stop-all":
		return runSessionStopAll(args[1:], stdout, stderr)
	case "prune":
		return runSessionPrune(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown session command %q\n", args[0])
		return 1
	}
}

func runSessionList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, false)
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
	sessionview.WriteList(stdout, stateDir, records)
	return 0
}

func runSessionStopAll(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, false)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session stop-all does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
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

	result := stopAllLiveSessions(stateDir, records)
	if flags.JSON {
		writeJSON(stdout, result)
		return boolToCode(result.Errors == 0)
	}
	if result.Matched == 0 {
		fmt.Fprintln(stdout, "No active tracked sessions to stop.")
		return 0
	}
	fmt.Fprintf(stdout, "ssherpa: stop-all signaled %d active session(s); stopped %d, pending %d, errors %d\n",
		result.Signaled, result.Stopped, result.Pending, result.Errors)
	for _, rec := range result.Records {
		label := defaultString(rec.Kind, state.KindInteractive)
		target := defaultString(rec.Target, "-")
		line := fmt.Sprintf("%s\t%s\t%s\tpid=%d\t%s", rec.ID, label, target, rec.PID, rec.Status)
		if rec.Error != "" {
			line += "\t" + rec.Error
		}
		fmt.Fprintln(stdout, line)
	}
	return boolToCode(result.Errors == 0)
}

func stopAllLiveSessions(stateDir string, records []state.SessionRecord) sessionStopAllResult {
	result := sessionStopAllResult{Scanned: len(records)}
	indexByID := map[string]int{}
	for _, record := range records {
		if !state.ProcessAlive(record) {
			continue
		}
		entry := sessionStopAllRecord{
			ID:     record.ID,
			Kind:   defaultString(record.Kind, state.KindInteractive),
			Target: record.TargetAlias,
			PID:    record.LocalPID,
			Status: "signaled",
		}
		result.Matched++
		if err := syscall.Kill(record.LocalPID, syscall.SIGHUP); err != nil {
			entry.Status = "error"
			entry.Error = err.Error()
			result.Errors++
		} else {
			result.Signaled++
		}
		indexByID[entry.ID] = len(result.Records)
		result.Records = append(result.Records, entry)
	}
	if result.Signaled == 0 {
		return result
	}

	deadline := time.Now().Add(forwardStopWait)
	for time.Now().Before(deadline) {
		allDone := true
		for _, entry := range result.Records {
			if entry.Status == "signaled" {
				allDone = false
				break
			}
		}
		if allDone {
			break
		}
		for id, index := range indexByID {
			if result.Records[index].Status != "signaled" {
				continue
			}
			record, err := state.ReadRecord(stateDir, id)
			if err != nil {
				continue
			}
			if record.EndedAt != nil || !state.ProcessAlive(record) {
				result.Records[index].Status = "stopped"
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	for _, entry := range result.Records {
		switch entry.Status {
		case "stopped":
			result.Stopped++
		case "signaled":
			result.Pending++
		}
	}
	return result
}

func boolToCode(ok bool) int {
	if ok {
		return 0
	}
	return 1
}

func runSessionMap(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, true)
	if !ok {
		return 1
	}
	if len(flags.Rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: session map does not accept positional arguments: %s\n", strings.Join(flags.Rest, " "))
		return 1
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: map sessions: %v\n", err)
		return 1
	}

	visible := records
	scope := "active"
	if !flags.All {
		visible = sessionview.ActiveRecords(records)
	} else {
		scope = "all"
	}
	roots := state.BuildSessionForest(visible)
	active, _ := sessionview.CountStatuses(records)
	if flags.JSON {
		writeJSON(stdout, sessionMapOutput{
			StateDir: stateDir,
			Scope:    scope,
			Total:    len(visible),
			Active:   active,
			Recorded: len(records),
			Roots:    roots,
		})
		return 0
	}
	sessionview.WriteMapWithOptions(stdout, stateDir, records, sessionview.MapOptions{IncludeExited: flags.All})
	return 0
}

func runSessionMapViewer(flags connectFlags, output io.Writer, stderr io.Writer) (int, bool) {
	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1, false
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: map sessions: %v\n", err)
		return 1, false
	}
	theme, err := termstyle.ResolveTheme(termstyle.ThemeOptions{
		Name:    flags.ThemeName,
		File:    flags.ThemeFile,
		NoColor: flags.NoColor,
		Env:     os.Environ(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1, false
	}
	err = sessionview.ShowMap(context.Background(), sessionview.ShowOptions{
		Input:       os.Stdin,
		Output:      output,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		View: sessionview.ViewOptions{
			Title:    "ssherpa session map",
			StateDir: stateDir,
			Records:  records,
			Map:      sessionview.MapOptions{},
			Theme:    theme.WithNoColor(theme.NoColor || flags.NoColor),
			Help:     "press any key to return",
		},
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: session map failed: %v\n", err)
		return 1, false
	}
	return 0, true
}

func runSessionShow(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseSessionFlags(args, stderr, false, false)
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
	flags, ok := parseSessionFlags(args, stderr, true, false)
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

func parseSessionFlags(args []string, stderr io.Writer, allowPruneFlags bool, allowAllFlag bool) (sessionFlags, bool) {
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
		case arg == "--all" && allowAllFlag:
			flags.All = true
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
	fmt.Fprintf(stdout, "route:\t%s\n", sessionview.FormatDisplayRoute(record.Route))
	fmt.Fprintf(stdout, "hops:\t%s\n", sessionview.FormatRoute(record.Hops))
	if forward := sessionview.ForwardSummary(record); forward != "" {
		fmt.Fprintf(stdout, "forward:\t%s\n", forward)
	}
	if proxy := sessionview.ProxySummary(record); proxy != "" {
		fmt.Fprintf(stdout, "proxy:\t%s\n", proxy)
	}
	fmt.Fprintf(stdout, "started:\t%s\n", record.StartedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(stdout, "ended:\t%s\n", formatOptionalTime(record.EndedAt))
	if record.ExitCode != nil {
		fmt.Fprintf(stdout, "exit_code:\t%d\n", *record.ExitCode)
	}
	if record.DisconnectReason != "" {
		fmt.Fprintf(stdout, "disconnect_reason:\t%s\n", record.DisconnectReason)
	}
	fmt.Fprintf(stdout, "local_pid:\t%d\n", record.LocalPID)
	fmt.Fprintf(stdout, "ssh_pid:\t%d\n", record.SSHPID)
	fmt.Fprintf(stdout, "runner:\t%s\n", record.RunnerMode)
	fmt.Fprintf(stdout, "argv:\t%s\n", strings.Join(record.SSHArgv, " "))
	if len(record.Events) > 0 {
		fmt.Fprintln(stdout, "events:")
		for _, event := range record.Events {
			fmt.Fprintf(stdout, "- %s\t%s", event.Time.Local().Format(time.RFC3339), event.Type)
			if event.LatencyMillis > 0 {
				fmt.Fprintf(stdout, "\tlatency=%dms", event.LatencyMillis)
			}
			if event.ThresholdMillis > 0 {
				fmt.Fprintf(stdout, "\tthreshold=%dms", event.ThresholdMillis)
			}
			if event.Message != "" {
				fmt.Fprintf(stdout, "\t%s", event.Message)
			}
			fmt.Fprintln(stdout)
		}
	}
}

func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Local().Format(time.RFC3339)
}
