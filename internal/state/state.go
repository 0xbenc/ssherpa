package state

import (
	"crypto/rand"
	"crypto/sha256"
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
	StateVersion          = 1
	IdentitySchemaVersion = 1
	DefaultPrune          = 7 * 24 * time.Hour

	// KindInteractive marks a normal interactive SSH session. It is the
	// implicit kind for any record without a Kind field set, so existing
	// records on disk continue to read correctly.
	KindInteractive = "interactive"
	// KindTunnel marks a non-interactive port-forward session (e.g. one
	// started by `ssherpa forward`). The session-map overlay and list
	// renderers tag these so an operator can tell a tunnel apart from a
	// shell at a glance.
	KindTunnel = "tunnel"
	// KindProxy marks a non-interactive SOCKS proxy session (e.g. one
	// started by `ssherpa proxy`). It shares the supervised/background
	// lifecycle with tunnels but carries a ProxySpec instead of a
	// ForwardSpec.
	KindProxy = "proxy"

	RemotePromptPrompt      = "prompt"
	RemotePromptRunning     = "running"
	RemotePromptPromptStart = "prompt_start"
)

type SessionRecord struct {
	ID               string           `json:"id"`
	ParentID         string           `json:"parent_id,omitempty"`
	Depth            int              `json:"depth"`
	Route            []string         `json:"route,omitempty"`
	OriginHost       string           `json:"origin_host,omitempty"`
	TargetAlias      string           `json:"target_alias,omitempty"`
	Hops             []string         `json:"hops,omitempty"`
	SSHArgv          []string         `json:"ssh_argv,omitempty"`
	ControlPath      string           `json:"control_path,omitempty"`
	Kind             string           `json:"kind,omitempty"`
	Forward          *ForwardSpec     `json:"forward,omitempty"`
	Proxy            *ProxySpec       `json:"proxy,omitempty"`
	Transcript       *TranscriptSpec  `json:"transcript,omitempty"`
	RecordedBy       *RecordingOrigin `json:"recorded_by,omitempty"`
	Import           *ImportSpec      `json:"import,omitempty"`
	RemoteHost       string           `json:"remote_host,omitempty"`
	RemoteCWD        string           `json:"remote_cwd,omitempty"`
	RemotePrompt     string           `json:"remote_prompt,omitempty"`
	StartedAt        time.Time        `json:"started_at"`
	EndedAt          *time.Time       `json:"ended_at,omitempty"`
	LocalPID         int              `json:"local_pid"`
	SSHPID           int              `json:"ssh_pid,omitempty"`
	ExitCode         *int             `json:"exit_code,omitempty"`
	RunnerMode       string           `json:"runner_mode"`
	Events           []SessionEvent   `json:"events,omitempty"`
	DisconnectReason string           `json:"disconnect_reason,omitempty"`
	StateVersion     int              `json:"state_version"`
	Inherited        bool             `json:"inherited,omitempty"`
	RemoteMirror     bool             `json:"remote_mirror,omitempty"`
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

// ProxySpec captures the runtime shape of a SOCKS proxy session.
// It is set on SessionRecord.Proxy when Kind == KindProxy so
// management commands and the session map can show the listener
// without re-parsing SSHArgv.
type ProxySpec struct {
	Bind       string `json:"bind,omitempty"`
	Port       int    `json:"port"`
	SavedAlias string `json:"saved_alias,omitempty"`
	Detached   bool   `json:"detached,omitempty"`
	RetryCount int    `json:"retry_count,omitempty"`
}

type TranscriptSpec struct {
	Path      string     `json:"path,omitempty"`
	Format    string     `json:"format"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Bytes     int64      `json:"bytes"`
	Frames    int64      `json:"frames"`
	MaxBytes  int64      `json:"max_bytes,omitempty"`
	Truncated bool       `json:"truncated,omitempty"`
	Input     bool       `json:"input,omitempty"`
}

type RecordingOrigin struct {
	MachineID      string `json:"machine_id,omitempty"`
	IdentitySchema int    `json:"identity_schema,omitempty"`
	SSHerpaVersion string `json:"ssherpa_version,omitempty"`
}

type ImportSpec struct {
	ImportedAt      time.Time `json:"imported_at"`
	ImportedBy      string    `json:"imported_by,omitempty"`
	SourceMachineID string    `json:"source_machine_id,omitempty"`
	SourceSessionID string    `json:"source_session_id,omitempty"`
	OriginClass     string    `json:"origin_class"`
	BundleSHA256    string    `json:"bundle_sha256,omitempty"`
}

type MachineIdentity struct {
	SchemaVersion    int       `json:"schema_version"`
	MachineID        string    `json:"machine_id"`
	CreatedAt        time.Time `json:"created_at"`
	CreatedByVersion string    `json:"created_by_version,omitempty"`
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
	if record.RemoteMirror {
		return false
	}
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

type CleanupResult struct {
	RemoteMirrors []SessionRecord `json:"remote_mirrors"`
}

type SessionNode struct {
	Record   SessionRecord `json:"record"`
	Children []SessionNode `json:"children,omitempty"`
}

func CleanupStaleRemoteMirrors(dir string, now time.Time) (CleanupResult, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	records, err := ListRecords(dir)
	if err != nil {
		return CleanupResult{}, err
	}
	byID := map[string]SessionRecord{}
	for _, record := range records {
		if record.ID != "" {
			byID[record.ID] = record
		}
	}
	result := CleanupResult{}
	for _, record := range records {
		if !staleRemoteMirror(record, byID) {
			continue
		}
		endedAt := now.UTC()
		exitCode := 0
		record.EndedAt = &endedAt
		record.ExitCode = &exitCode
		record.DisconnectReason = "stale_remote_mirror_cleanup"
		record.Events = append(record.Events, SessionEvent{
			Time:    endedAt,
			Type:    "stale_remote_mirror_cleanup",
			Message: "remote mirror was closed because its local parent session is no longer active",
		})
		if err := WriteRecord(dir, record); err != nil {
			return result, err
		}
		result.RemoteMirrors = append(result.RemoteMirrors, record)
		byID[record.ID] = record
	}
	return result, nil
}

func staleRemoteMirror(record SessionRecord, byID map[string]SessionRecord) bool {
	return staleRemoteMirrorSeen(record, byID, map[string]bool{})
}

func staleRemoteMirrorSeen(record SessionRecord, byID map[string]SessionRecord, seen map[string]bool) bool {
	if !record.RemoteMirror || record.EndedAt != nil {
		return false
	}
	if record.ID != "" {
		if seen[record.ID] {
			return true
		}
		seen[record.ID] = true
	}
	parentID := strings.TrimSpace(record.ParentID)
	if parentID == "" || parentID == record.ID {
		return true
	}
	parent, ok := byID[parentID]
	if !ok {
		return true
	}
	if parent.EndedAt != nil {
		return true
	}
	if parent.RemoteMirror {
		return staleRemoteMirrorSeen(parent, byID, seen)
	}
	return !ProcessAlive(parent)
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

func IdentityPath(dir string) string {
	return filepath.Join(filepath.Clean(dir), "identity.json")
}

func EnsureMachineIdentity(dir string, ssherpaVersion string, now time.Time) (MachineIdentity, error) {
	existing, err := ReadMachineIdentity(dir)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return MachineIdentity{}, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	id, err := newUUID()
	if err != nil {
		return MachineIdentity{}, err
	}
	identity := MachineIdentity{
		SchemaVersion:    IdentitySchemaVersion,
		MachineID:        id,
		CreatedAt:        now.UTC(),
		CreatedByVersion: strings.TrimSpace(ssherpaVersion),
	}
	if identity.CreatedByVersion == "" {
		identity.CreatedByVersion = "unknown"
	}
	if err := WriteMachineIdentity(dir, identity); err != nil {
		return MachineIdentity{}, err
	}
	return identity, nil
}

func ReadMachineIdentity(dir string) (MachineIdentity, error) {
	data, err := os.ReadFile(IdentityPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return MachineIdentity{}, os.ErrNotExist
		}
		return MachineIdentity{}, fmt.Errorf("read machine identity: %w", err)
	}
	var identity MachineIdentity
	if err := json.Unmarshal(data, &identity); err != nil {
		return MachineIdentity{}, fmt.Errorf("parse machine identity: %w", err)
	}
	if strings.TrimSpace(identity.MachineID) == "" {
		return MachineIdentity{}, errors.New("machine identity is missing machine_id")
	}
	return identity, nil
}

func WriteMachineIdentity(dir string, identity MachineIdentity) error {
	if strings.TrimSpace(identity.MachineID) == "" {
		return errors.New("machine identity requires machine_id")
	}
	if identity.SchemaVersion <= 0 {
		identity.SchemaVersion = IdentitySchemaVersion
	}
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = time.Now().UTC()
	}
	data, err := json.MarshalIndent(identity, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal machine identity: %w", err)
	}
	data = append(data, '\n')
	_, err = fsutil.AtomicWriteFile(IdentityPath(dir), data, fsutil.WriteOptions{Mode: 0o600})
	if err != nil {
		return fmt.Errorf("write machine identity: %w", err)
	}
	return nil
}

func RecordingOriginForIdentity(identity MachineIdentity, ssherpaVersion string) RecordingOrigin {
	version := strings.TrimSpace(ssherpaVersion)
	if version == "" {
		version = identity.CreatedByVersion
	}
	if version == "" {
		version = "unknown"
	}
	return RecordingOrigin{
		MachineID:      identity.MachineID,
		IdentitySchema: identity.SchemaVersion,
		SSHerpaVersion: version,
	}
}

func OriginClass(local MachineIdentity, sourceMachineID string) string {
	sourceMachineID = strings.TrimSpace(sourceMachineID)
	if sourceMachineID == "" {
		return "imported_unknown"
	}
	if strings.TrimSpace(local.MachineID) != "" && sourceMachineID == local.MachineID {
		return "imported_self"
	}
	return "imported_other"
}

func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return strings.Join([]string{
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	}, "-"), nil
}

func EnvForRecord(record SessionRecord) []string {
	route := strings.Join(record.Route, ",")
	return []string{
		"SSHERPA_SESSION_ID=" + record.ID,
		"SSHERPA_PARENT_SESSION_ID=" + record.ParentID,
		fmt.Sprintf("SSHERPA_DEPTH=%d", record.Depth),
		"SSHERPA_ROUTE=" + route,
		"SSHERPA_ORIGIN_HOST=" + record.OriginHost,
	}
}

func InheritedMetadata(target string) (parentID string, depth int, route []string) {
	return InheritedMetadataFromEnv(os.Environ(), target)
}

func OriginHostFromEnv(env []string) string {
	return strings.TrimSpace(envValue(env, "SSHERPA_ORIGIN_HOST"))
}

func LocalOriginHost(env []string) string {
	if origin := OriginHostFromEnv(env); origin != "" {
		return origin
	}
	if label := strings.TrimSpace(envValue(env, "SSHERPA_HOST_LABEL")); label != "" {
		return label
	}
	if hostname, err := os.Hostname(); err == nil {
		return strings.TrimSpace(hostname)
	}
	return ""
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
