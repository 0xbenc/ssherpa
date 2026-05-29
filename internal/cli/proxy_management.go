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

type proxyListEntry struct {
	ID         string           `json:"id"`
	Status     string           `json:"status"`
	Target     string           `json:"target,omitempty"`
	SavedAlias string           `json:"saved_alias,omitempty"`
	Bind       string           `json:"bind,omitempty"`
	Port       int              `json:"port,omitempty"`
	Listener   string           `json:"listener,omitempty"`
	Detached   bool             `json:"detached,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	EndedAt    *time.Time       `json:"ended_at,omitempty"`
	Uptime     string           `json:"uptime,omitempty"`
	Retries    int              `json:"retries"`
	LocalPID   int              `json:"local_pid,omitempty"`
	SSHPID     int              `json:"ssh_pid,omitempty"`
	Proxy      *state.ProxySpec `json:"proxy,omitempty"`
}

func runProxyList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardManagementFlags(args, stderr, map[string]bool{"--json": true})
	if !ok {
		return 1
	}
	if len(flags.Args) > 0 {
		fmt.Fprintf(stderr, "ssherpa: proxy list does not accept positional arguments: %s\n", strings.Join(flags.Args, " "))
		return 1
	}
	entries, code := loadProxyEntries(flags.StateDir, stderr)
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
		fmt.Fprintln(stdout, "No proxy sessions recorded.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tTARGET\tLISTENER\tUPTIME\tRETRIES")
	for _, e := range entries {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%d\n", e.ID, e.Status, displayProxyTarget(e), e.Listener, e.Uptime, e.Retries)
	}
	_ = tw.Flush()
	return 0
}

func runProxyStatus(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardManagementFlags(args, stderr, map[string]bool{"--json": true})
	if !ok {
		return 1
	}
	if len(flags.Args) != 1 {
		fmt.Fprintln(stderr, "ssherpa: proxy status requires exactly one session ID or saved alias")
		return 1
	}
	stateDir, ok := resolveStateDirForManagement(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	record, ok := resolveProxyRecord(stateDir, flags.Args[0], stderr)
	if !ok {
		return 2
	}
	entry := flattenProxyRecord(record)
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
	fmt.Fprintf(stdout, "listener    %s\n", entry.Listener)
	fmt.Fprintf(stdout, "started     %s\n", record.StartedAt.Local().Format(time.RFC3339))
	if record.EndedAt != nil {
		fmt.Fprintf(stdout, "ended       %s\n", record.EndedAt.Local().Format(time.RFC3339))
	} else if entry.Uptime != "" {
		fmt.Fprintf(stdout, "uptime      %s\n", entry.Uptime)
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
	return 0
}

func runProxyStop(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardManagementFlags(args, stderr, map[string]bool{"--yes": true})
	if !ok {
		return 1
	}
	if len(flags.Args) != 1 {
		fmt.Fprintln(stderr, "ssherpa: proxy stop requires exactly one session ID or saved alias")
		return 1
	}
	stateDir, ok := resolveStateDirForManagement(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	record, ok := resolveProxyRecord(stateDir, flags.Args[0], stderr)
	if !ok {
		return 2
	}
	if record.EndedAt != nil {
		fmt.Fprintf(stdout, "ssherpa: proxy %s already exited at %s\n", record.ID, record.EndedAt.Local().Format(time.RFC3339))
		return 0
	}
	if record.LocalPID <= 0 {
		fmt.Fprintf(stderr, "ssherpa: proxy %s has no LocalPID; cannot stop\n", record.ID)
		return 1
	}
	if err := syscall.Kill(record.LocalPID, syscall.SIGHUP); err != nil {
		fmt.Fprintf(stderr, "ssherpa: signal pid %d: %v\n", record.LocalPID, err)
		return 1
	}
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
		fmt.Fprintf(stdout, "ssherpa: proxy %s signaled (pid %d) but did not finalize within %s\n", record.ID, record.LocalPID, forwardStopWait)
		return 0
	}
	fmt.Fprintf(stdout, "ssherpa: proxy %s stopped (pid %d, exit %d)\n", record.ID, record.LocalPID, derefExitCode(record.ExitCode))
	return 0
}

func loadProxyEntries(stateDirOverride string, stderr io.Writer) ([]proxyListEntry, int) {
	stateDir, ok := resolveStateDirForManagement(stateDirOverride, stderr)
	if !ok {
		return nil, 1
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list records: %v\n", err)
		return nil, 1
	}
	entries := make([]proxyListEntry, 0, len(records))
	for _, r := range records {
		if r.Kind != state.KindProxy {
			continue
		}
		entries = append(entries, flattenProxyRecord(r))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].StartedAt.After(entries[j].StartedAt)
	})
	return entries, 0
}

func flattenProxyRecord(record state.SessionRecord) proxyListEntry {
	entry := proxyListEntry{
		ID:        record.ID,
		Target:    record.TargetAlias,
		StartedAt: record.StartedAt,
		EndedAt:   record.EndedAt,
		LocalPID:  record.LocalPID,
		SSHPID:    record.SSHPID,
		Proxy:     record.Proxy,
	}
	if record.Proxy != nil {
		entry.SavedAlias = record.Proxy.SavedAlias
		entry.Bind = record.Proxy.Bind
		entry.Port = record.Proxy.Port
		entry.Listener = proxyListener(record.Proxy.Bind, record.Proxy.Port)
		entry.Detached = record.Proxy.Detached
		entry.Retries = record.Proxy.RetryCount
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

func resolveProxyRecord(stateDir string, target string, stderr io.Writer) (state.SessionRecord, bool) {
	target = strings.TrimSpace(target)
	if target == "" {
		fmt.Fprintln(stderr, "ssherpa: proxy target cannot be empty")
		return state.SessionRecord{}, false
	}
	if rec, err := state.ReadRecord(stateDir, target); err == nil {
		if rec.Kind != state.KindProxy {
			fmt.Fprintf(stderr, "ssherpa: session %q is not a proxy (Kind=%q)\n", target, rec.Kind)
			return state.SessionRecord{}, false
		}
		return rec, true
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list records: %v\n", err)
		return state.SessionRecord{}, false
	}
	var match *state.SessionRecord
	for i := range records {
		r := records[i]
		if r.Kind != state.KindProxy || r.Proxy == nil || r.Proxy.SavedAlias != target {
			continue
		}
		if match == nil || (match.EndedAt != nil && r.EndedAt == nil) {
			match = &r
		}
	}
	if match != nil {
		return *match, true
	}
	fmt.Fprintf(stderr, "ssherpa: no proxy session matches %q (tried session ID and saved alias)\n", target)
	return state.SessionRecord{}, false
}

func displayProxyTarget(e proxyListEntry) string {
	if e.SavedAlias != "" {
		return e.SavedAlias
	}
	if e.Target != "" {
		return e.Target
	}
	return "-"
}

func proxyListener(bind string, port int) string {
	if bind == "" {
		bind = defaultProxyBind
	}
	if strings.Contains(bind, ":") {
		return fmt.Sprintf("[%s]:%d", bind, port)
	}
	return fmt.Sprintf("%s:%d", bind, port)
}
