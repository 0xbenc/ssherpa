package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
)

// Phase 2c — forward management subcommands.
//
//   ssherpa forward list   [--json] [--state-dir PATH]
//   ssherpa forward status <id-or-name> [--json] [--state-dir PATH]
//   ssherpa forward stop   <id-or-name> [--yes] [--state-dir PATH]
//
// All three are kind-filtered wrappers around the existing session
// state machinery (state.ListRecords / ReadRecord / ProcessAlive).
// `forward stop` resolves its argument as either a session ID or a
// saved-alias name — Phase 2e populates Forward.SavedAlias; for now
// the resolver falls through to ID-only matching.

const (
	// forwardStopWait caps how long `forward stop` polls the session
	// record waiting for the daemon to finalize EndedAt. The
	// supervisor's own kill-grace is 750ms (escapeRopeKillGrace);
	// 2s is enough headroom for the SIGHUP→SIGKILL escalation plus
	// the final record write.
	forwardStopWait = 2 * time.Second
)

// forwardManagementFlags is the shared flag bag for list/status/stop.
// Each subcommand parser fills the subset it cares about.
type forwardManagementFlags struct {
	StateDir string
	JSON     bool
	Yes      bool
	// Args holds the positional arguments after flags are consumed
	// (status / stop both take a session ID or saved-alias name).
	Args []string
}

// parseForwardManagementFlags is a tiny hand-rolled parser mirroring
// the rest of the CLI's style. It accepts only the flags listed and
// rejects unknown options so a typo doesn't get silently swallowed.
func parseForwardManagementFlags(args []string, stderr io.Writer, accept map[string]bool) (forwardManagementFlags, bool) {
	out := forwardManagementFlags{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			out.Args = append(out.Args, args[i+1:]...)
			return out, true
		case arg == "--json" && accept["--json"]:
			out.JSON = true
		case arg == "--yes" && accept["--yes"]:
			out.Yes = true
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return out, false
			}
			out.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			out.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--help" || arg == "-h":
			return out, false
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown flag %q\n", arg)
			return out, false
		default:
			out.Args = append(out.Args, arg)
		}
	}
	return out, true
}

// forwardListEntry is a flattened view of a tunnel SessionRecord that
// the list/status JSON output uses. Plain ListRecords records contain
// fields irrelevant to forward management (StateVersion, raw SSHArgv);
// the flat shape is what scripts wrapping ssherpa will want.
type forwardListEntry struct {
	ID         string             `json:"id"`
	Status     string             `json:"status"`
	Target     string             `json:"target,omitempty"`
	SavedAlias string             `json:"saved_alias,omitempty"`
	Local      string             `json:"local,omitempty"`
	Remote     string             `json:"remote,omitempty"`
	Through    string             `json:"through,omitempty"`
	Detached   bool               `json:"detached,omitempty"`
	StartedAt  time.Time          `json:"started_at"`
	EndedAt    *time.Time         `json:"ended_at,omitempty"`
	Uptime     string             `json:"uptime,omitempty"`
	Retries    int                `json:"retries"`
	LocalPID   int                `json:"local_pid,omitempty"`
	SSHPID     int                `json:"ssh_pid,omitempty"`
	Forward    *state.ForwardSpec `json:"forward,omitempty"`
}

func runForwardList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardManagementFlags(args, stderr, map[string]bool{"--json": true})
	if !ok {
		return 1
	}
	if len(flags.Args) > 0 {
		fmt.Fprintf(stderr, "ssherpa: forward list does not accept positional arguments: %s\n", strings.Join(flags.Args, " "))
		return 1
	}

	entries, code := loadForwardEntries(flags.StateDir, stderr)
	if code != 0 {
		return code
	}

	if flags.JSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(entries)
		return 0
	}

	if len(entries) == 0 {
		fmt.Fprintln(stdout, "No tunnel sessions recorded.")
		return 0
	}

	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tTARGET\tLOCAL\tREMOTE\tUPTIME\tRETRIES")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%d\n",
			e.ID,
			e.Status,
			displayTarget(e),
			e.Local,
			e.Remote,
			e.Uptime,
			e.Retries,
		)
	}
	_ = tw.Flush()
	return 0
}

func runForwardStatus(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardManagementFlags(args, stderr, map[string]bool{"--json": true})
	if !ok {
		return 1
	}
	if len(flags.Args) != 1 {
		fmt.Fprintln(stderr, "ssherpa: forward status requires exactly one session ID or saved alias")
		return 1
	}
	target := flags.Args[0]

	stateDir, ok := resolveStateDirForManagement(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	record, ok := resolveForwardRecord(stateDir, target, stderr)
	if !ok {
		return 2
	}
	entry := flattenForwardRecord(record)

	if flags.JSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(entry)
		return 0
	}

	logPath := filepath.Join(state.SessionsDir(stateDir), record.ID+".log")
	fmt.Fprintf(stdout, "session     %s\n", record.ID)
	statusLine := entry.Status
	if entry.Detached {
		statusLine += " (detached"
		if entry.LocalPID > 0 {
			statusLine += fmt.Sprintf(", pid %d", entry.LocalPID)
		}
		statusLine += ")"
	}
	fmt.Fprintf(stdout, "status      %s\n", statusLine)
	if entry.SavedAlias != "" {
		fmt.Fprintf(stdout, "name        %s\n", entry.SavedAlias)
	}
	fmt.Fprintf(stdout, "target      %s\n", entry.Target)
	fmt.Fprintf(stdout, "started     %s\n", record.StartedAt.Local().Format(time.RFC3339))
	if record.EndedAt != nil {
		fmt.Fprintf(stdout, "ended       %s\n", record.EndedAt.Local().Format(time.RFC3339))
	} else if entry.Uptime != "" {
		fmt.Fprintf(stdout, "uptime      %s\n", entry.Uptime)
	}
	if entry.Local != "" {
		fmt.Fprintf(stdout, "local       %s\n", entry.Local)
	}
	if entry.Remote != "" {
		fmt.Fprintf(stdout, "remote      %s\n", entry.Remote)
	}
	if entry.Through != "" {
		fmt.Fprintf(stdout, "through     %s\n", entry.Through)
	}
	if entry.SSHPID > 0 {
		fmt.Fprintf(stdout, "ssh-pid     %d", entry.SSHPID)
		if entry.Retries > 0 {
			fmt.Fprintf(stdout, "  (after %d reconnect(s))", entry.Retries)
		}
		fmt.Fprintln(stdout)
	}
	if entry.Detached {
		fmt.Fprintf(stdout, "log         %s\n", logPath)
	}
	if len(record.Events) > 0 {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, "recent events:")
		// Last 10 in chronological order.
		from := 0
		if len(record.Events) > 10 {
			from = len(record.Events) - 10
		}
		for _, ev := range record.Events[from:] {
			msg := ev.Message
			if msg == "" {
				msg = "(no message)"
			}
			fmt.Fprintf(stdout, "  %s  %-20s  %s\n", ev.Time.Local().Format("15:04:05"), ev.Type, msg)
		}
	}
	return 0
}

func runForwardStop(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardManagementFlags(args, stderr, map[string]bool{"--yes": true})
	if !ok {
		return 1
	}
	if len(flags.Args) != 1 {
		fmt.Fprintln(stderr, "ssherpa: forward stop requires exactly one session ID or saved alias")
		return 1
	}
	target := flags.Args[0]

	stateDir, ok := resolveStateDirForManagement(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	record, ok := resolveForwardRecord(stateDir, target, stderr)
	if !ok {
		return 2
	}
	if record.EndedAt != nil {
		fmt.Fprintf(stdout, "ssherpa: forward %s already exited at %s\n", record.ID, record.EndedAt.Local().Format(time.RFC3339))
		return 0
	}
	if record.LocalPID <= 0 {
		fmt.Fprintf(stderr, "ssherpa: forward %s has no LocalPID; cannot stop\n", record.ID)
		return 1
	}

	if err := syscall.Kill(record.LocalPID, syscall.SIGHUP); err != nil {
		fmt.Fprintf(stderr, "ssherpa: signal pid %d: %v\n", record.LocalPID, err)
		return 1
	}

	// Poll for the daemon to write EndedAt — its forwardSignals handler
	// escalates SIGHUP → SIGKILL after escapeRopeKillGrace, then
	// finalizes the record. Cap the wait so a hung daemon doesn't
	// block this command forever.
	deadline := time.Now().Add(forwardStopWait)
	for time.Now().Before(deadline) {
		rec, err := state.ReadRecord(stateDir, record.ID)
		if err == nil && rec.EndedAt != nil {
			record = rec
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if record.EndedAt == nil {
		fmt.Fprintf(stdout, "ssherpa: forward %s signaled (pid %d) but did not finalize within %s\n",
			record.ID, record.LocalPID, forwardStopWait)
		return 0
	}
	fmt.Fprintf(stdout, "ssherpa: forward %s stopped (pid %d, exit %d)\n",
		record.ID, record.LocalPID, derefExitCode(record.ExitCode))
	return 0
}

func loadForwardEntries(stateDirOverride string, stderr io.Writer) ([]forwardListEntry, int) {
	stateDir, ok := resolveStateDirForManagement(stateDirOverride, stderr)
	if !ok {
		return nil, 1
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list records: %v\n", err)
		return nil, 1
	}
	entries := make([]forwardListEntry, 0, len(records))
	for _, r := range records {
		if r.Kind != state.KindTunnel {
			continue
		}
		entries = append(entries, flattenForwardRecord(r))
	}
	// Most-recent first matches `ssherpa session list` ordering.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
	return entries, 0
}

// flattenForwardRecord projects a SessionRecord into the operator-facing
// shape — derives status (active/orphan/exited) via ProcessAlive,
// renders local/remote/uptime strings, and exposes the saved-alias name
// (Phase 2e) when present.
func flattenForwardRecord(record state.SessionRecord) forwardListEntry {
	entry := forwardListEntry{
		ID:        record.ID,
		Target:    record.TargetAlias,
		StartedAt: record.StartedAt,
		EndedAt:   record.EndedAt,
		LocalPID:  record.LocalPID,
		SSHPID:    record.SSHPID,
		Forward:   record.Forward,
	}
	if record.Forward != nil {
		entry.SavedAlias = record.Forward.SavedAlias
		entry.Local = formatEndpoint(record.Forward.LocalBind, record.Forward.LocalPort)
		entry.Remote = formatEndpoint(record.Forward.RemoteHost, record.Forward.RemotePort)
		entry.Through = record.Forward.Through
		entry.Detached = record.Forward.Detached
		entry.Retries = record.Forward.RetryCount
	}
	switch {
	case record.EndedAt != nil:
		entry.Status = "exited"
	case state.ProcessAlive(record):
		entry.Status = "active"
		entry.Uptime = humanDuration(time.Since(record.StartedAt))
	default:
		entry.Status = "orphan"
	}
	return entry
}

func resolveForwardRecord(stateDir string, target string, stderr io.Writer) (state.SessionRecord, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		fmt.Fprintln(stderr, "ssherpa: forward target cannot be empty")
		return state.SessionRecord{}, false
	}
	// Try exact session ID match first — cheap path, the ID is the
	// canonical reference and tests / scripts almost always pass it.
	if rec, err := state.ReadRecord(stateDir, target); err == nil {
		if rec.Kind != state.KindTunnel {
			fmt.Fprintf(stderr, "ssherpa: session %q is not a tunnel (Kind=%q)\n", target, rec.Kind)
			return state.SessionRecord{}, false
		}
		return rec, true
	}
	// Fall back to scanning for a SavedAlias match (Phase 2e). For
	// 2c there are no SavedAlias values on disk yet — this loop is a
	// no-op until 2e populates them — but wiring it in 2c keeps the
	// stop/status UX consistent the moment 2e lands.
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list records: %v\n", err)
		return state.SessionRecord{}, false
	}
	// Prefer active tunnels over exited ones with the same saved alias.
	var match *state.SessionRecord
	for i := range records {
		r := records[i]
		if r.Kind != state.KindTunnel {
			continue
		}
		if r.Forward == nil || r.Forward.SavedAlias != target {
			continue
		}
		if match == nil || (match.EndedAt != nil && r.EndedAt == nil) {
			match = &r
		}
	}
	if match != nil {
		return *match, true
	}
	fmt.Fprintf(stderr, "ssherpa: no tunnel session matches %q (tried session ID and saved alias)\n", target)
	return state.SessionRecord{}, false
}

func resolveStateDirForManagement(override string, stderr io.Writer) (string, bool) {
	stateDir, err := state.ResolveDir(override)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return "", false
	}
	return stateDir, true
}

func displayTarget(e forwardListEntry) string {
	if e.SavedAlias != "" {
		return e.SavedAlias
	}
	if e.Target != "" {
		return e.Target
	}
	return "-"
}

func formatEndpoint(host string, port int) string {
	if host == "" && port == 0 {
		return ""
	}
	if strings.Contains(host, ":") {
		// IPv6: bracket-quote per the SSH spec.
		return fmt.Sprintf("[%s]:%d", host, port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// humanDuration is a tiny "uptime" formatter — coarse enough that
// list output stays narrow but precise enough to be useful at a
// glance. Matches the style of `kubectl get pods` / `docker ps`.
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) - days*24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}

func derefExitCode(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}
