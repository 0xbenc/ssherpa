package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWriteReadAndListRecords(t *testing.T) {
	dir := t.TempDir()
	startedA := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	startedB := time.Date(2026, 5, 24, 11, 0, 0, 0, time.UTC)

	first := SessionRecord{
		ID:          "first",
		TargetAlias: "prod",
		Route:       []string{"prod"},
		StartedAt:   startedA,
		LocalPID:    100,
		SSHPID:      101,
		RunnerMode:  "supervised",
	}
	second := SessionRecord{
		ID:          "second",
		TargetAlias: "db",
		Route:       []string{"bastion", "db"},
		StartedAt:   startedB,
		LocalPID:    200,
		SSHPID:      201,
		RunnerMode:  "supervised",
	}

	if err := WriteRecord(dir, first); err != nil {
		t.Fatalf("WriteRecord(first) returned error: %v", err)
	}
	if err := WriteRecord(dir, second); err != nil {
		t.Fatalf("WriteRecord(second) returned error: %v", err)
	}

	info, err := os.Stat(RecordPath(dir, first.ID))
	if err != nil {
		t.Fatalf("os.Stat returned error: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("record mode = %v, want 0600", got)
	}

	got, err := ReadRecord(dir, first.ID)
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if got.StateVersion != StateVersion || got.TargetAlias != first.TargetAlias || !reflect.DeepEqual(got.Route, first.Route) {
		t.Fatalf("record = %#v, want first record with state version", got)
	}

	records, err := ListRecords(dir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 2 || records[0].ID != "second" || records[1].ID != "first" {
		t.Fatalf("records sorted newest-first = %#v", records)
	}
}

func TestListRecordsMissingDirIsEmpty(t *testing.T) {
	records, err := ListRecords(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records = %#v, want empty", records)
	}
}

func TestPruneRecordsOnlyRemovesOldEndedSessions(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	oldEnd := now.Add(-8 * 24 * time.Hour)
	recentEnd := now.Add(-time.Hour)
	exitCode := 0

	records := []SessionRecord{
		{ID: "old", StartedAt: oldEnd.Add(-time.Hour), EndedAt: &oldEnd, ExitCode: &exitCode, RunnerMode: "supervised"},
		{ID: "recent", StartedAt: recentEnd.Add(-time.Hour), EndedAt: &recentEnd, ExitCode: &exitCode, RunnerMode: "supervised"},
		{ID: "active", StartedAt: now.Add(-2 * time.Hour), RunnerMode: "supervised"},
	}
	for _, record := range records {
		if err := WriteRecord(dir, record); err != nil {
			t.Fatalf("WriteRecord(%s) returned error: %v", record.ID, err)
		}
	}

	dryRun, err := PruneRecords(dir, 7*24*time.Hour, now, true)
	if err != nil {
		t.Fatalf("PruneRecords dry-run returned error: %v", err)
	}
	if !dryRun.DryRun || len(dryRun.Records) != 1 || dryRun.Records[0].ID != "old" {
		t.Fatalf("dryRun = %#v, want only old record", dryRun)
	}
	if _, err := os.Stat(RecordPath(dir, "old")); err != nil {
		t.Fatalf("old record removed during dry-run: %v", err)
	}

	applied, err := PruneRecords(dir, 7*24*time.Hour, now, false)
	if err != nil {
		t.Fatalf("PruneRecords apply returned error: %v", err)
	}
	if applied.DryRun || len(applied.Records) != 1 || applied.Records[0].ID != "old" {
		t.Fatalf("applied = %#v, want removed old record", applied)
	}
	if _, err := os.Stat(RecordPath(dir, "old")); !os.IsNotExist(err) {
		t.Fatalf("old record still exists, err=%v", err)
	}
	if _, err := os.Stat(RecordPath(dir, "recent")); err != nil {
		t.Fatalf("recent record missing: %v", err)
	}
	if _, err := os.Stat(RecordPath(dir, "active")); err != nil {
		t.Fatalf("active record missing: %v", err)
	}
}

func TestResolveDirHonorsOverrideAndEnv(t *testing.T) {
	override := filepath.Join(t.TempDir(), "override")
	got, err := ResolveDir(override)
	if err != nil {
		t.Fatalf("ResolveDir override returned error: %v", err)
	}
	if got != override {
		t.Fatalf("ResolveDir override = %q, want %q", got, override)
	}

	envDir := filepath.Join(t.TempDir(), "env")
	t.Setenv("SSHERPA_STATE_DIR", envDir)
	got, err = ResolveDir("")
	if err != nil {
		t.Fatalf("ResolveDir env returned error: %v", err)
	}
	if got != envDir {
		t.Fatalf("ResolveDir env = %q, want %q", got, envDir)
	}
}

func TestInheritedMetadata(t *testing.T) {
	t.Setenv("SSHERPA_SESSION_ID", "parent")
	t.Setenv("SSHERPA_DEPTH", "2")
	t.Setenv("SSHERPA_ROUTE", "laptop,bastion")

	parentID, depth, route := InheritedMetadata("prod")
	if parentID != "parent" || depth != 3 || !reflect.DeepEqual(route, []string{"laptop", "bastion", "prod"}) {
		t.Fatalf("metadata = %q %d %#v", parentID, depth, route)
	}
}

func TestRejectInvalidSessionID(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"../bad", " bad "} {
		record := SessionRecord{ID: id, StartedAt: time.Now()}
		if err := WriteRecord(dir, record); err == nil {
			t.Fatalf("WriteRecord accepted invalid session id %q", id)
		}
		if _, err := ReadRecord(dir, id); err == nil {
			t.Fatalf("ReadRecord accepted invalid session id %q", id)
		}
	}
}

func TestRecordJSONShape(t *testing.T) {
	started := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	record := SessionRecord{
		ID:           "shape",
		Depth:        1,
		Route:        []string{"bastion", "prod"},
		TargetAlias:  "prod",
		StartedAt:    started,
		LocalPID:     100,
		SSHPID:       101,
		RunnerMode:   "supervised",
		StateVersion: StateVersion,
	}

	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	for _, want := range []string{`"id":"shape"`, `"target_alias":"prod"`, `"ssh_pid":101`, `"state_version":1`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("json = %s, want substring %s", data, want)
		}
	}
}
