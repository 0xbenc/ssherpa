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
