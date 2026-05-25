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
	"time"

	"github.com/0xbenc/ssherpa/internal/fsutil"
)

const (
	StateVersion = 1
	DefaultPrune = 7 * 24 * time.Hour
)

type SessionRecord struct {
	ID           string     `json:"id"`
	ParentID     string     `json:"parent_id,omitempty"`
	Depth        int        `json:"depth"`
	Route        []string   `json:"route,omitempty"`
	TargetAlias  string     `json:"target_alias,omitempty"`
	Hops         []string   `json:"hops,omitempty"`
	SSHArgv      []string   `json:"ssh_argv,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	LocalPID     int        `json:"local_pid"`
	SSHPID       int        `json:"ssh_pid,omitempty"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	RunnerMode   string     `json:"runner_mode"`
	StateVersion int        `json:"state_version"`
}

func (r SessionRecord) Status() string {
	if r.EndedAt == nil {
		return "active"
	}
	return "exited"
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
			return nil, nil
		}
		return nil, fmt.Errorf("read sessions directory: %w", err)
	}

	var records []SessionRecord
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

func SessionsDir(dir string) string {
	return filepath.Join(filepath.Clean(dir), "sessions")
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
