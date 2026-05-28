package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/0xbenc/ssherpa/internal/fsutil"
)

const (
	StateVersion = 1
	DefaultPrune = 7 * 24 * time.Hour

	// KindInteractive marks a normal interactive SSH session. It is the
	// implicit kind for any record without a Kind field set, so existing
	// records on disk continue to read correctly.
	KindInteractive = "interactive"
	// KindTunnel marks a non-interactive port-forward session (e.g. one
	// started by `ssherpa forward`). The session-map overlay and list
	// renderers tag these so an operator can tell a tunnel apart from a
	// shell at a glance.
	KindTunnel = "tunnel"
)

type SessionRecord struct {
	ID               string         `json:"id"`
	ParentID         string         `json:"parent_id,omitempty"`
	Depth            int            `json:"depth"`
	Route            []string       `json:"route,omitempty"`
	TargetAlias      string         `json:"target_alias,omitempty"`
	Hops             []string       `json:"hops,omitempty"`
	SSHArgv          []string       `json:"ssh_argv,omitempty"`
	Kind             string         `json:"kind,omitempty"`
	Forward          *ForwardSpec   `json:"forward,omitempty"`
	StartedAt        time.Time      `json:"started_at"`
	EndedAt          *time.Time     `json:"ended_at,omitempty"`
	LocalPID         int            `json:"local_pid"`
	SSHPID           int            `json:"ssh_pid,omitempty"`
	ExitCode         *int           `json:"exit_code,omitempty"`
	RunnerMode       string         `json:"runner_mode"`
	Events           []SessionEvent `json:"events,omitempty"`
	DisconnectReason string         `json:"disconnect_reason,omitempty"`
	StateVersion     int            `json:"state_version"`
}

// ForwardSpec captures the runtime shape of a port-forward tunnel
// session. It is set on SessionRecord.Forward when Kind == KindTunnel
// so the management commands (`ssherpa forward list/status/stop`) and
// the session-map overlay can show the tunnel's endpoints without
// re-parsing SSHArgv. The struct is intentionally additive — the
// json omitempty on every field keeps backward compatibility with
// records written before the field existed.
type ForwardSpec struct {
	LocalBind  string `json:"local_bind,omitempty"`
	LocalPort  int    `json:"local_port,omitempty"`
	RemoteHost string `json:"remote_host,omitempty"`
	RemotePort int    `json:"remote_port,omitempty"`
	Through    string `json:"through,omitempty"`
	// SavedAlias is the catalog name when the forward was launched
	// from a persisted "saved forward" rather than ad-hoc CLI args.
	// Phase 2e populates this; earlier phases leave it empty.
	SavedAlias string `json:"saved_alias,omitempty"`
	// Detached is true when the session is running under the
	// background daemon supervisor (Phase 2b) rather than the
	// foreground supervised PTY.
	Detached bool `json:"detached,omitempty"`
	// RetryCount is incremented each time the underlying ssh
	// process is restarted by the reconnect loop. 0 means the
	// initial spawn is still running (or the session exited
	// without a restart).
	RetryCount int `json:"retry_count,omitempty"`
}

func (r SessionRecord) Status() string {
	if r.EndedAt == nil {
		return "active"
	}
	return "exited"
}

// ProcessAlive reports whether the LocalPID in this record still
// names a live process. Used by the forward-management commands to
// distinguish "active" (record open AND daemon PID responds to
// signal 0) from "orphan" (record open but PID gone — the daemon
// crashed without writing EndedAt). syscall.Kill with signal 0
// performs an existence check without delivering a signal; it
// returns nil iff the process exists and the caller can signal it.
// Records with EndedAt set or LocalPID == 0 always return false.
func ProcessAlive(record SessionRecord) bool {
	if record.EndedAt != nil {
		return false
	}
	if record.LocalPID <= 0 {
		return false
	}
	return syscall.Kill(record.LocalPID, syscall.Signal(0)) == nil
}

type SessionEvent struct {
	Time            time.Time `json:"time"`
	Type            string    `json:"type"`
	Message         string    `json:"message,omitempty"`
	LatencyMillis   int64     `json:"latency_ms,omitempty"`
	ThresholdMillis int64     `json:"threshold_ms,omitempty"`
}

func NewSessionID(now time.Time) string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return now.UTC().Format("20060102T150405.000000000Z")
	}
	return now.UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(suffix[:])
}

func ResolveDir(override string) (string, error) {
	path := strings.TrimSpace(override)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("SSHERPA_STATE_DIR"))
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if runtime.GOOS == "darwin" {
			path = filepath.Join(home, "Library", "Application Support", "ssherpa")
		} else {
			path = filepath.Join(os.Getenv("XDG_STATE_HOME"), "ssherpa")
			if os.Getenv("XDG_STATE_HOME") == "" {
				path = filepath.Join(home, ".local", "state", "ssherpa")
			}
		}
	}
	return expandPath(path)
}

func WriteRecord(dir string, record SessionRecord) error {
	if err := validateID(record.ID); err != nil {
		return err
	}
	record.StateVersion = StateVersion
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal session record: %w", err)
	}
	data = append(data, '\n')
	_, err = fsutil.AtomicWriteFile(RecordPath(dir, record.ID), data, fsutil.WriteOptions{Mode: 0o600})
	if err != nil {
		return fmt.Errorf("write session record %s: %w", record.ID, err)
	}
	return nil
}

func ReadRecord(dir string, id string) (SessionRecord, error) {
	if err := validateID(id); err != nil {
		return SessionRecord{}, err
	}
	path := RecordPath(dir, id)
	data, err := os.ReadFile(path)
	if err != nil {
		return SessionRecord{}, fmt.Errorf("read session record %s: %w", id, err)
	}
	var record SessionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return SessionRecord{}, fmt.Errorf("parse session record %s: %w", id, err)
	}
	return record, nil
}

func ListRecords(dir string) ([]SessionRecord, error) {
	entries, err := os.ReadDir(SessionsDir(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []SessionRecord{}, nil
		}
		return nil, fmt.Errorf("read sessions directory: %w", err)
	}

	records := []SessionRecord{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		record, err := ReadRecord(dir, id)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i int, j int) bool {
		return records[i].StartedAt.After(records[j].StartedAt)
	})
	return records, nil
}

type PruneResult struct {
	Records []SessionRecord `json:"records"`
	DryRun  bool            `json:"dry_run"`
}

type SessionNode struct {
	Record   SessionRecord `json:"record"`
	Children []SessionNode `json:"children,omitempty"`
}

func PruneRecords(dir string, olderThan time.Duration, now time.Time, dryRun bool) (PruneResult, error) {
	if olderThan <= 0 {
		return PruneResult{}, errors.New("older-than duration must be positive")
	}
	records, err := ListRecords(dir)
	if err != nil {
		return PruneResult{}, err
	}

	cutoff := now.Add(-olderThan)
	result := PruneResult{DryRun: dryRun}
	for _, record := range records {
		if record.EndedAt == nil || record.EndedAt.After(cutoff) {
			continue
		}
		result.Records = append(result.Records, record)
		if dryRun {
			continue
		}
		if err := os.Remove(RecordPath(dir, record.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return result, fmt.Errorf("remove session record %s: %w", record.ID, err)
		}
	}
	return result, nil
}

func BuildSessionForest(records []SessionRecord) []SessionNode {
	byID := map[string]SessionRecord{}
	for _, record := range records {
		if record.ID != "" {
			byID[record.ID] = record
		}
	}

	childrenByParent := map[string][]SessionRecord{}
	var roots []SessionRecord
	for _, record := range records {
		if record.ParentID == "" || record.ParentID == record.ID {
			roots = append(roots, record)
			continue
		}
		if _, ok := byID[record.ParentID]; !ok {
			roots = append(roots, record)
			continue
		}
		childrenByParent[record.ParentID] = append(childrenByParent[record.ParentID], record)
	}

	sortSessionRecords(roots)
	for parentID := range childrenByParent {
		sortSessionRecords(childrenByParent[parentID])
	}

	visited := map[string]bool{}
	forest := []SessionNode{}
	for _, root := range roots {
		if root.ID != "" && visited[root.ID] {
			continue
		}
		forest = append(forest, buildSessionNode(root, childrenByParent, visited, map[string]bool{}))
	}

	sorted := append([]SessionRecord(nil), records...)
	sortSessionRecords(sorted)
	for _, record := range sorted {
		if record.ID == "" || visited[record.ID] {
			continue
		}
		forest = append(forest, buildSessionNode(record, childrenByParent, visited, map[string]bool{}))
	}
	return forest
}

func SessionsDir(dir string) string {
	return filepath.Join(filepath.Clean(dir), "sessions")
}

func buildSessionNode(record SessionRecord, childrenByParent map[string][]SessionRecord, visited map[string]bool, ancestry map[string]bool) SessionNode {
	node := SessionNode{Record: record}
	if record.ID == "" || ancestry[record.ID] {
		return node
	}
	visited[record.ID] = true
	nextAncestry := map[string]bool{}
	for id, ok := range ancestry {
		nextAncestry[id] = ok
	}
	nextAncestry[record.ID] = true

	for _, child := range childrenByParent[record.ID] {
		if child.ID != "" && nextAncestry[child.ID] {
			continue
		}
		node.Children = append(node.Children, buildSessionNode(child, childrenByParent, visited, nextAncestry))
	}
	return node
}

func sortSessionRecords(records []SessionRecord) {
	sort.SliceStable(records, func(i int, j int) bool {
		if records[i].StartedAt.Equal(records[j].StartedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].StartedAt.Before(records[j].StartedAt)
	})
}

func RecordPath(dir string, id string) string {
	return filepath.Join(SessionsDir(dir), id+".json")
}

func EnvForRecord(record SessionRecord) []string {
	route := strings.Join(record.Route, ",")
	return []string{
		"SSHERPA_SESSION_ID=" + record.ID,
		"SSHERPA_PARENT_SESSION_ID=" + record.ParentID,
		fmt.Sprintf("SSHERPA_DEPTH=%d", record.Depth),
		"SSHERPA_ROUTE=" + route,
	}
}

func InheritedMetadata(target string) (parentID string, depth int, route []string) {
	return InheritedMetadataFromEnv(os.Environ(), target)
}

func InheritedMetadataFromEnv(env []string, target string) (parentID string, depth int, route []string) {
	parentID = strings.TrimSpace(envValue(env, "SSHERPA_SESSION_ID"))
	if value := strings.TrimSpace(envValue(env, "SSHERPA_DEPTH")); value != "" {
		var parsed int
		if _, err := fmt.Sscanf(value, "%d", &parsed); err == nil && parsed >= 0 {
			depth = parsed + 1
		}
	}
	if parentID != "" && depth == 0 {
		depth = 1
	}
	if value := strings.TrimSpace(envValue(env, "SSHERPA_ROUTE")); value != "" {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				route = append(route, part)
			}
		}
	}
	if target != "" {
		route = append(route, target)
	}
	return parentID, depth, route
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, item := range env {
		if strings.HasPrefix(item, prefix) {
			return strings.TrimPrefix(item, prefix)
		}
	}
	return ""
}

func expandPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve path %s: %w", path, err)
		}
		path = abs
	}
	return filepath.Clean(path), nil
}

func validateID(id string) error {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return errors.New("session id is required")
	}
	if id != trimmed {
		return fmt.Errorf("invalid session id %q", id)
	}
	if id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return fmt.Errorf("invalid session id %q", id)
	}
	return nil
}
