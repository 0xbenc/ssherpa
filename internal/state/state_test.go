package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWriteReadAndListRecords(t *testing.T) {
	dir := t.TempDir()
	startedA := time.Date(2026, 5, 24, 10, 0, 0, 0, time.UTC)
	startedB := time.Date(2026, 5, 24, 11, 0, 0, 0, time.UTC)

	first := SessionRecord{
		ID:               "first",
		TargetAlias:      "prod",
		Route:            []string{"prod"},
		StartedAt:        startedA,
		LocalPID:         100,
		SSHPID:           101,
		RunnerMode:       "supervised",
		RemoteHost:       "prod.example.com",
		RemoteCWD:        "/srv/app",
		RemotePrompt:     RemotePromptPrompt,
		DisconnectReason: "latency unhealthy for 30s",
		Events: []SessionEvent{{
			Time:            startedA.Add(time.Minute),
			Type:            "latency_disconnect",
			Message:         "latency unhealthy for 30s",
			LatencyMillis:   5000,
			ThresholdMillis: 2000,
		}},
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
	if got.DisconnectReason != first.DisconnectReason || len(got.Events) != 1 || got.Events[0].Type != "latency_disconnect" {
		t.Fatalf("record health fields = %#v, want disconnect event", got)
	}
	if got.RemoteHost != first.RemoteHost || got.RemoteCWD != first.RemoteCWD || got.RemotePrompt != first.RemotePrompt {
		t.Fatalf("record remote fields = %#v, want observed remote state", got)
	}

	records, err := ListRecords(dir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 2 || records[0].ID != "second" || records[1].ID != "first" {
		t.Fatalf("records sorted newest-first = %#v", records)
	}
}

func TestProcessAlive(t *testing.T) {
	cases := []struct {
		name string
		rec  SessionRecord
		want bool
	}{
		{
			name: "ended record never alive",
			rec:  SessionRecord{LocalPID: os.Getpid(), EndedAt: ptrTime(time.Now())},
			want: false,
		},
		{
			name: "zero local pid never alive",
			rec:  SessionRecord{LocalPID: 0},
			want: false,
		},
		{
			name: "negative local pid never alive",
			rec:  SessionRecord{LocalPID: -1},
			want: false,
		},
		{
			name: "current process is alive",
			rec:  SessionRecord{LocalPID: os.Getpid()},
			want: true,
		},
		{
			name: "remote mirror never alive locally",
			rec:  SessionRecord{LocalPID: os.Getpid(), RemoteMirror: true},
			want: false,
		},
		{
			name: "obviously-dead pid is not alive",
			rec:  SessionRecord{LocalPID: 1 << 20}, // very unlikely to exist
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ProcessAlive(c.rec)
			if got != c.want {
				t.Fatalf("ProcessAlive(%+v) = %v, want %v", c.rec, got, c.want)
			}
		})
	}
}

func TestCleanupStaleRemoteMirrorsClosesOldOpenMirrors(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC)
	ended := now.Add(-time.Minute)
	parentExit := 0
	records := []SessionRecord{
		{ID: "missing-parent-child", ParentID: "missing", RemoteMirror: true, StartedAt: now.Add(-4 * time.Minute), RunnerMode: "supervised"},
		{ID: "ended-parent", StartedAt: now.Add(-3 * time.Minute), EndedAt: &ended, ExitCode: &parentExit, RunnerMode: "supervised"},
		{ID: "ended-parent-child", ParentID: "ended-parent", RemoteMirror: true, StartedAt: now.Add(-2 * time.Minute), RunnerMode: "supervised"},
		{ID: "live-parent", StartedAt: now.Add(-time.Minute), LocalPID: os.Getpid(), RunnerMode: "supervised"},
		{ID: "live-parent-child", ParentID: "live-parent", RemoteMirror: true, StartedAt: now, RunnerMode: "supervised"},
	}
	for _, record := range records {
		if err := WriteRecord(dir, record); err != nil {
			t.Fatalf("WriteRecord(%s): %v", record.ID, err)
		}
	}

	result, err := CleanupStaleRemoteMirrors(dir, now)
	if err != nil {
		t.Fatalf("CleanupStaleRemoteMirrors returned error: %v", err)
	}
	if len(result.RemoteMirrors) != 2 {
		t.Fatalf("cleanup result = %#v, want two closed stale mirrors", result)
	}
	for _, id := range []string{"missing-parent-child", "ended-parent-child"} {
		record, err := ReadRecord(dir, id)
		if err != nil {
			t.Fatalf("ReadRecord(%s): %v", id, err)
		}
		if record.EndedAt == nil || record.DisconnectReason != "stale_remote_mirror_cleanup" {
			t.Fatalf("%s = %#v, want stale mirror finalized", id, record)
		}
		if len(record.Events) == 0 || record.Events[len(record.Events)-1].Type != "stale_remote_mirror_cleanup" {
			t.Fatalf("%s events = %#v, want cleanup event", id, record.Events)
		}
	}
	live, err := ReadRecord(dir, "live-parent-child")
	if err != nil {
		t.Fatalf("ReadRecord(live-parent-child): %v", err)
	}
	if live.EndedAt != nil {
		t.Fatalf("live-parent-child = %#v, want still open while parent process is alive", live)
	}
}

func ptrTime(value time.Time) *time.Time { return &value }

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

func TestPruneRecordsDeletesByFilenameNotInternalID(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "state")
	sessions := SessionsDir(dir)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)

	// The file the old code would have deleted: RecordPath resolved
	// from the JSON-internal id, which escapes sessions/ entirely.
	victim := RecordPath(dir, "../../escape")
	if filepath.Dir(victim) != root {
		t.Fatalf("test setup: victim %q not directly under root %q", victim, root)
	}
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}
	evil := `{"id":"../../escape","started_at":"2026-05-01T00:00:00Z","ended_at":"2026-05-01T01:00:00Z","local_pid":0,"runner_mode":"supervised","state_version":1}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "evil.json"), []byte(evil), 0o600); err != nil {
		t.Fatalf("write evil record: %v", err)
	}

	result, err := PruneRecords(dir, 7*24*time.Hour, now, false)
	if err != nil {
		t.Fatalf("PruneRecords: %v", err)
	}
	if len(result.Records) != 1 || result.Records[0].ID != "../../escape" {
		t.Fatalf("result = %#v, want the tampered record reported", result)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Fatalf("file outside sessions/ was deleted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sessions, "evil.json")); !os.IsNotExist(err) {
		t.Fatalf("the file that was read still exists, err=%v", err)
	}
}

func TestPruneRecordsRemovesTranscriptArtifacts(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	oldEnd := now.Add(-8 * 24 * time.Hour)
	recentEnd := now.Add(-time.Hour)
	exitCode := 0
	records := []SessionRecord{
		{ID: "old", StartedAt: oldEnd.Add(-time.Hour), EndedAt: &oldEnd, ExitCode: &exitCode, RunnerMode: "supervised"},
		{ID: "recent", StartedAt: recentEnd.Add(-time.Hour), EndedAt: &recentEnd, ExitCode: &exitCode, RunnerMode: "supervised"},
	}
	for _, record := range records {
		if err := WriteRecord(dir, record); err != nil {
			t.Fatalf("WriteRecord(%s): %v", record.ID, err)
		}
	}
	sessions := SessionsDir(dir)
	oldCast := filepath.Join(sessions, "old.cast")
	oldLog := filepath.Join(sessions, "old.log")
	recentCast := filepath.Join(sessions, "recent.cast")
	for _, path := range []string{oldCast, oldLog, recentCast} {
		if err := os.WriteFile(path, []byte("artifact"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	dryRun, err := PruneRecords(dir, 7*24*time.Hour, now, true)
	if err != nil {
		t.Fatalf("PruneRecords dry-run: %v", err)
	}
	if !reflect.DeepEqual(dryRun.Artifacts, []string{oldCast, oldLog}) {
		t.Fatalf("dry-run artifacts = %#v, want old.cast and old.log", dryRun.Artifacts)
	}
	for _, path := range []string{oldCast, oldLog, recentCast} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("dry-run removed %s: %v", path, err)
		}
	}

	applied, err := PruneRecords(dir, 7*24*time.Hour, now, false)
	if err != nil {
		t.Fatalf("PruneRecords apply: %v", err)
	}
	if !reflect.DeepEqual(applied.Artifacts, []string{oldCast, oldLog}) {
		t.Fatalf("applied artifacts = %#v, want old.cast and old.log", applied.Artifacts)
	}
	for _, path := range []string{RecordPath(dir, "old"), oldCast, oldLog} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("%s still exists, err=%v", path, err)
		}
	}
	if _, err := os.Stat(recentCast); err != nil {
		t.Fatalf("kept record's transcript was removed: %v", err)
	}
	if _, err := os.Stat(RecordPath(dir, "recent")); err != nil {
		t.Fatalf("kept record was removed: %v", err)
	}
}

func TestListRecordsSkipsCorruptFiles(t *testing.T) {
	dir := t.TempDir()
	if err := WriteRecord(dir, SessionRecord{ID: "good", StartedAt: time.Now().UTC(), RunnerMode: "supervised"}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	corrupt := filepath.Join(SessionsDir(dir), "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	records, skipped, err := ListRecordsDetailed(dir)
	if err != nil {
		t.Fatalf("ListRecordsDetailed: %v", err)
	}
	if len(records) != 1 || records[0].ID != "good" {
		t.Fatalf("records = %#v, want only good", records)
	}
	if len(skipped) != 1 || skipped[0].Path != corrupt || skipped[0].Reason == "" {
		t.Fatalf("skipped = %#v, want corrupt.json with reason", skipped)
	}

	plain, err := ListRecords(dir)
	if err != nil {
		t.Fatalf("ListRecords with one corrupt file: %v", err)
	}
	if len(plain) != 1 || plain[0].ID != "good" {
		t.Fatalf("ListRecords = %#v, want only good", plain)
	}
}

func TestFutureStateVersionIsSkippedNeverRewritten(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	if err := WriteRecord(dir, SessionRecord{ID: "good", StartedAt: now, RunnerMode: "supervised"}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	futurePath := RecordPath(dir, "future")
	futureBody := `{"id":"future","started_at":"2026-05-01T00:00:00Z","ended_at":"2026-05-01T01:00:00Z","local_pid":0,"runner_mode":"supervised","state_version":2,"from_the_future":true}` + "\n"
	if err := os.WriteFile(futurePath, []byte(futureBody), 0o600); err != nil {
		t.Fatalf("write future record: %v", err)
	}

	if _, err := ReadRecord(dir, "future"); !errors.Is(err, ErrFutureStateVersion) {
		t.Fatalf("ReadRecord(future) = %v, want ErrFutureStateVersion", err)
	}

	records, skipped, err := ListRecordsDetailed(dir)
	if err != nil {
		t.Fatalf("ListRecordsDetailed: %v", err)
	}
	if len(records) != 1 || records[0].ID != "good" {
		t.Fatalf("records = %#v, want only good", records)
	}
	if len(skipped) != 1 || skipped[0].Path != futurePath || !strings.Contains(skipped[0].Reason, "state_version 2") {
		t.Fatalf("skipped = %#v, want future record with version reason", skipped)
	}

	if err := WriteRecord(dir, SessionRecord{ID: "future", StartedAt: now, RunnerMode: "supervised"}); !errors.Is(err, ErrFutureStateVersion) {
		t.Fatalf("WriteRecord over future file = %v, want ErrFutureStateVersion", err)
	}
	// The prune pass must leave the future-format file alone even
	// though its ended_at is ancient.
	if _, err := PruneRecords(dir, 7*24*time.Hour, now, false); err != nil {
		t.Fatalf("PruneRecords: %v", err)
	}
	data, err := os.ReadFile(futurePath)
	if err != nil {
		t.Fatalf("future file was removed or rewritten: %v", err)
	}
	if string(data) != futureBody {
		t.Fatalf("future file bytes changed:\n%s", data)
	}
}

func TestMissingStateVersionReadsAsCurrent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(SessionsDir(dir), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"id":"legacy","started_at":"2026-05-01T00:00:00Z","local_pid":0,"runner_mode":"supervised"}` + "\n"
	if err := os.WriteFile(RecordPath(dir, "legacy"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}
	record, err := ReadRecord(dir, "legacy")
	if err != nil {
		t.Fatalf("ReadRecord(legacy) = %v, want pre-state_version file to read as v1", err)
	}
	if record.ID != "legacy" {
		t.Fatalf("record = %#v", record)
	}
	records, skipped, err := ListRecordsDetailed(dir)
	if err != nil || len(records) != 1 || len(skipped) != 0 {
		t.Fatalf("ListRecordsDetailed = %#v, %#v, %v; want legacy listed, none skipped", records, skipped, err)
	}
}

func TestWriteRecordDoesNotResurrectClosedRecord(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if err := WriteRecord(dir, SessionRecord{ID: "rmw", StartedAt: started, LocalPID: 100, RunnerMode: "supervised"}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}

	// Writer A loads the open record…
	stale, err := ReadRecord(dir, "rmw")
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	// …writer B finalizes it…
	finalized, err := ReadRecord(dir, "rmw")
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	ended := started.Add(time.Hour)
	code := 7
	finalized.EndedAt = &ended
	finalized.ExitCode = &code
	finalized.DisconnectReason = "remote closed"
	if err := WriteRecord(dir, finalized); err != nil {
		t.Fatalf("WriteRecord(finalized): %v", err)
	}
	// …and writer A saves its stale open copy with a metadata update.
	stale.RemoteCWD = "/srv"
	if err := WriteRecord(dir, stale); err != nil {
		t.Fatalf("WriteRecord(stale): %v", err)
	}

	got, err := ReadRecord(dir, "rmw")
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("EndedAt = %v, want preserved %v", got.EndedAt, ended)
	}
	if got.ExitCode == nil || *got.ExitCode != 7 {
		t.Fatalf("ExitCode = %v, want preserved 7", got.ExitCode)
	}
	if got.DisconnectReason != "remote closed" {
		t.Fatalf("DisconnectReason = %q, want preserved", got.DisconnectReason)
	}
	if got.RemoteCWD != "/srv" {
		t.Fatalf("RemoteCWD = %q, want the stale writer's metadata update kept", got.RemoteCWD)
	}

	// Explicitly-set fields still win over the on-disk values.
	explicit := stale
	code9 := 9
	explicit.ExitCode = &code9
	explicit.DisconnectReason = "manual"
	if err := WriteRecord(dir, explicit); err != nil {
		t.Fatalf("WriteRecord(explicit): %v", err)
	}
	got, err = ReadRecord(dir, "rmw")
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) {
		t.Fatalf("EndedAt = %v, want still preserved %v", got.EndedAt, ended)
	}
	if got.ExitCode == nil || *got.ExitCode != 9 || got.DisconnectReason != "manual" {
		t.Fatalf("explicit fields lost: exit=%v reason=%q", got.ExitCode, got.DisconnectReason)
	}
}

func TestWriteRecordConcurrentStaleWritersKeepFinalization(t *testing.T) {
	dir := t.TempDir()
	started := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	if err := WriteRecord(dir, SessionRecord{ID: "race", StartedAt: started, LocalPID: 100, RunnerMode: "supervised"}); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	stale, err := ReadRecord(dir, "race")
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	ended := started.Add(time.Hour)
	code := 3
	finalized := stale
	finalized.EndedAt = &ended
	finalized.ExitCode = &code
	finalized.DisconnectReason = "done"
	if err := WriteRecord(dir, finalized); err != nil {
		t.Fatalf("WriteRecord(finalized): %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			copy := stale
			copy.RemoteCWD = fmt.Sprintf("/srv/%d", n)
			if err := WriteRecord(dir, copy); err != nil {
				t.Errorf("WriteRecord(stale %d): %v", n, err)
			}
		}(i)
	}
	wg.Wait()

	got, err := ReadRecord(dir, "race")
	if err != nil {
		t.Fatalf("ReadRecord: %v", err)
	}
	if got.EndedAt == nil || !got.EndedAt.Equal(ended) || got.ExitCode == nil || *got.ExitCode != 3 || got.DisconnectReason != "done" {
		t.Fatalf("finalization lost under concurrent stale writers: %#v", got)
	}
}

func TestCleanupReapsCrashedLocalSessions(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	deadPID := 1 << 20 // very unlikely to exist
	imported := &ImportSpec{ImportedAt: now, OriginClass: "imported_other"}
	records := []SessionRecord{
		{ID: "crashed-old", StartedAt: now.Add(-2 * time.Hour), LocalPID: deadPID, RunnerMode: "supervised"},
		{ID: "crashed-recent", StartedAt: now.Add(-time.Minute), LocalPID: deadPID, RunnerMode: "supervised"},
		{ID: "alive", StartedAt: now.Add(-2 * time.Hour), LocalPID: os.Getpid(), RunnerMode: "supervised"},
		{ID: "ssh-alive", StartedAt: now.Add(-2 * time.Hour), LocalPID: deadPID, SSHPID: os.Getpid(), RunnerMode: "supervised"},
		{ID: "imported-open", StartedAt: now.Add(-2 * time.Hour), LocalPID: deadPID, RunnerMode: "supervised", Import: imported},
		{ID: "no-pid", StartedAt: now.Add(-2 * time.Hour), RunnerMode: "incoming"},
	}
	for _, record := range records {
		if err := WriteRecord(dir, record); err != nil {
			t.Fatalf("WriteRecord(%s): %v", record.ID, err)
		}
	}

	result, err := CleanupStaleRemoteMirrors(dir, now)
	if err != nil {
		t.Fatalf("CleanupStaleRemoteMirrors: %v", err)
	}
	if len(result.LocalSessions) != 1 || result.LocalSessions[0].ID != "crashed-old" {
		t.Fatalf("LocalSessions = %#v, want only crashed-old", result.LocalSessions)
	}
	reaped, err := ReadRecord(dir, "crashed-old")
	if err != nil {
		t.Fatalf("ReadRecord(crashed-old): %v", err)
	}
	if reaped.EndedAt == nil || !reaped.EndedAt.Equal(now) {
		t.Fatalf("crashed-old EndedAt = %v, want %v", reaped.EndedAt, now)
	}
	if reaped.DisconnectReason != "stale_local_session_cleanup" {
		t.Fatalf("crashed-old DisconnectReason = %q", reaped.DisconnectReason)
	}
	if reaped.ExitCode != nil {
		t.Fatalf("crashed-old ExitCode = %v, want nil (real exit status unknowable)", *reaped.ExitCode)
	}
	if len(reaped.Events) == 0 || reaped.Events[len(reaped.Events)-1].Type != "stale_local_session_cleanup" {
		t.Fatalf("crashed-old events = %#v, want cleanup event", reaped.Events)
	}
	for _, id := range []string{"crashed-recent", "alive", "ssh-alive", "imported-open", "no-pid"} {
		record, err := ReadRecord(dir, id)
		if err != nil {
			t.Fatalf("ReadRecord(%s): %v", id, err)
		}
		if record.EndedAt != nil {
			t.Fatalf("%s was reaped: %#v", id, record)
		}
	}
}

func TestCleanupWritesBackToFileWithMismatchedID(t *testing.T) {
	dir := t.TempDir()
	sessions := SessionsDir(dir)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	// A stale-local-eligible record stored under mismatch.json whose
	// JSON-internal id names a different file. Finalization must land
	// in mismatch.json, not spawn other-id.json.
	record := fmt.Sprintf(`{"id":"other-id","started_at":%q,"local_pid":%d,"runner_mode":"supervised","state_version":1}`,
		now.Add(-2*time.Hour).Format(time.RFC3339), 1<<20) + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "mismatch.json"), []byte(record), 0o600); err != nil {
		t.Fatalf("write mismatch record: %v", err)
	}

	result, err := CleanupStaleRemoteMirrors(dir, now)
	if err != nil {
		t.Fatalf("CleanupStaleRemoteMirrors: %v", err)
	}
	if len(result.LocalSessions) != 1 || result.LocalSessions[0].ID != "other-id" {
		t.Fatalf("LocalSessions = %#v, want the mismatched record reaped", result.LocalSessions)
	}
	if _, err := os.Stat(filepath.Join(sessions, "other-id.json")); !os.IsNotExist(err) {
		t.Fatalf("cleanup spawned other-id.json from the internal id, err=%v", err)
	}
	data, err := os.ReadFile(filepath.Join(sessions, "mismatch.json"))
	if err != nil {
		t.Fatalf("read mismatch.json: %v", err)
	}
	if !strings.Contains(string(data), "stale_local_session_cleanup") {
		t.Fatalf("mismatch.json was not finalized in place: %s", data)
	}
}

func TestPruneRecordsRemovesOrphanedArtifacts(t *testing.T) {
	dir := t.TempDir()
	sessions := SessionsDir(dir)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	now := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	old := now.Add(-8 * 24 * time.Hour)

	for _, name := range []string{"ghost.cast", "ghost.log", "fresh.cast"} {
		if err := os.WriteFile(filepath.Join(sessions, name), []byte("data"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, name := range []string{"ghost.cast", "ghost.log"} {
		if err := os.Chtimes(filepath.Join(sessions, name), old, old); err != nil {
			t.Fatalf("chtimes %s: %v", name, err)
		}
	}

	result, err := PruneRecords(dir, 7*24*time.Hour, now, false)
	if err != nil {
		t.Fatalf("PruneRecords: %v", err)
	}
	if len(result.Artifacts) != 2 {
		t.Fatalf("Artifacts = %#v, want the two ghost files", result.Artifacts)
	}
	for _, name := range []string{"ghost.cast", "ghost.log"} {
		if _, err := os.Stat(filepath.Join(sessions, name)); !os.IsNotExist(err) {
			t.Fatalf("%s survived prune, err=%v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(sessions, "fresh.cast")); err != nil {
		t.Fatalf("fresh.cast should survive (newer than cutoff): %v", err)
	}

	dry, err := PruneRecords(dir, 7*24*time.Hour, now, true)
	if err != nil {
		t.Fatalf("PruneRecords dry-run: %v", err)
	}
	if len(dry.Artifacts) != 0 {
		t.Fatalf("dry-run Artifacts = %#v, want none left", dry.Artifacts)
	}
}

func TestBuildSessionForest(t *testing.T) {
	now := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	records := []SessionRecord{
		{ID: "child-b", ParentID: "root", TargetAlias: "db", StartedAt: now.Add(2 * time.Minute)},
		{ID: "orphan", ParentID: "missing", TargetAlias: "orphan", StartedAt: now.Add(3 * time.Minute)},
		{ID: "root", TargetAlias: "prod", StartedAt: now},
		{ID: "child-a", ParentID: "root", TargetAlias: "web", StartedAt: now.Add(time.Minute)},
	}

	forest := BuildSessionForest(records)

	if len(forest) != 2 {
		t.Fatalf("forest = %#v, want two roots", forest)
	}
	if forest[0].Record.ID != "root" || forest[1].Record.ID != "orphan" {
		t.Fatalf("root order = %#v", forest)
	}
	children := forest[0].Children
	if len(children) != 2 || children[0].Record.ID != "child-a" || children[1].Record.ID != "child-b" {
		t.Fatalf("children = %#v, want started-at order", children)
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
	t.Setenv("SSHERPA_ORIGIN_HOST", "workstation")

	parentID, depth, route := InheritedMetadata("prod")
	if parentID != "parent" || depth != 3 || !reflect.DeepEqual(route, []string{"laptop", "bastion", "prod"}) {
		t.Fatalf("metadata = %q %d %#v", parentID, depth, route)
	}
	if got := OriginHostFromEnv(os.Environ()); got != "workstation" {
		t.Fatalf("OriginHostFromEnv = %q, want workstation", got)
	}
	if got := LocalOriginHost(os.Environ()); got != "workstation" {
		t.Fatalf("LocalOriginHost = %q, want workstation", got)
	}
}

func TestEnvForRecordIncludesOriginHost(t *testing.T) {
	env := EnvForRecord(SessionRecord{
		ID:         "child",
		ParentID:   "parent",
		Depth:      2,
		Route:      []string{"bastion", "prod"},
		OriginHost: "workstation",
	})

	got := strings.Join(env, "\n")
	for _, want := range []string{
		"SSHERPA_SESSION_ID=child",
		"SSHERPA_PARENT_SESSION_ID=parent",
		"SSHERPA_DEPTH=2",
		"SSHERPA_ROUTE=bastion,prod",
		"SSHERPA_ORIGIN_HOST=workstation",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("EnvForRecord missing %q in:\n%s", want, got)
		}
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
