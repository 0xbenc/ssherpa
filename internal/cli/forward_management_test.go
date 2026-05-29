package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/ui"
)

// seedTunnelRecord writes a SessionRecord with Kind=tunnel for the
// forward-management tests, defaulting fields that don't matter to
// each individual case. Returns the resulting record so tests can
// reference its generated ID.
func seedTunnelRecord(t *testing.T, stateDir string, rec state.SessionRecord) state.SessionRecord {
	t.Helper()
	if rec.ID == "" {
		rec.ID = state.NewSessionID(time.Now())
	}
	if rec.Kind == "" {
		rec.Kind = state.KindTunnel
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = time.Now().Add(-5 * time.Minute)
	}
	if rec.RunnerMode == "" {
		rec.RunnerMode = "supervised"
	}
	if err := state.WriteRecord(stateDir, rec); err != nil {
		t.Fatalf("WriteRecord: %v", err)
	}
	return rec
}

func TestRunForwardListShowsOnlyTunnels(t *testing.T) {
	stateDir := t.TempDir()
	exit := 0

	// Two tunnels (one active-looking, one exited) and one interactive
	// — list should show only the tunnels.
	tunnel1 := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:          "tunnel-active-id",
		TargetAlias: "pgbox",
		StartedAt:   time.Now().Add(-2 * time.Hour),
		LocalPID:    os.Getpid(), // alive
		Forward: &state.ForwardSpec{
			LocalBind: "127.0.0.1", LocalPort: 5432,
			RemoteHost: "127.0.0.1", RemotePort: 5432,
		},
	})
	endedAt := time.Now().Add(-30 * time.Minute)
	tunnel2 := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:          "tunnel-exited-id",
		TargetAlias: "redisbox",
		StartedAt:   time.Now().Add(-3 * time.Hour),
		EndedAt:     &endedAt,
		ExitCode:    &exit,
		Forward: &state.ForwardSpec{
			LocalBind: "127.0.0.1", LocalPort: 6379,
			RemoteHost: "127.0.0.1", RemotePort: 6379,
			Through: "bastion",
		},
	})
	interactive := state.SessionRecord{
		ID:          "interactive-id",
		Kind:        state.KindInteractive,
		TargetAlias: "shell",
		StartedAt:   time.Now().Add(-1 * time.Hour),
		LocalPID:    os.Getpid(),
		RunnerMode:  "supervised",
	}
	if err := state.WriteRecord(stateDir, interactive); err != nil {
		t.Fatalf("WriteRecord interactive: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"forward", "list", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	assertContains(t, out, tunnel1.ID)
	assertContains(t, out, tunnel2.ID)
	assertContains(t, out, "pgbox")
	assertContains(t, out, "redisbox")
	if strings.Contains(out, interactive.ID) {
		t.Fatalf("interactive session leaked into forward list:\n%s", out)
	}
}

func TestRunForwardListEmpty(t *testing.T) {
	stateDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := Run([]string{"forward", "list", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "No tunnel sessions recorded.")
}

func TestRunForwardListJSON(t *testing.T) {
	stateDir := t.TempDir()
	tunnel := seedTunnelRecord(t, stateDir, state.SessionRecord{
		TargetAlias: "pgbox",
		LocalPID:    os.Getpid(),
		Forward: &state.ForwardSpec{
			LocalBind: "127.0.0.1", LocalPort: 5432,
			RemoteHost: "127.0.0.1", RemotePort: 5432,
		},
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"forward", "list", "--state-dir", stateDir, "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}
	var entries []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		t.Fatalf("json.Unmarshal: %v; stdout=%q", err, stdout.String())
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0]["id"] != tunnel.ID {
		t.Fatalf("entries[0].id = %v, want %s", entries[0]["id"], tunnel.ID)
	}
	if entries[0]["status"] != "active" {
		t.Fatalf("entries[0].status = %v, want \"active\"", entries[0]["status"])
	}
}

func TestRunForwardStatusByID(t *testing.T) {
	stateDir := t.TempDir()
	exit := 0
	endedAt := time.Now().Add(-10 * time.Minute)
	tunnel := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:          "status-test-id",
		TargetAlias: "pgbox",
		StartedAt:   time.Now().Add(-2 * time.Hour),
		EndedAt:     &endedAt,
		ExitCode:    &exit,
		LocalPID:    12345,
		SSHPID:      12346,
		Forward: &state.ForwardSpec{
			LocalBind: "127.0.0.1", LocalPort: 5432,
			RemoteHost: "127.0.0.1", RemotePort: 5432,
			Through:  "bastion",
			Detached: true,
		},
		Events: []state.SessionEvent{
			{Time: time.Now().Add(-1 * time.Hour), Type: "reconnect_attempt", Message: "attempt 1"},
		},
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"forward", "status", "--state-dir", stateDir, tunnel.ID}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	assertContains(t, out, "session     "+tunnel.ID)
	assertContains(t, out, "status      exited")
	assertContains(t, out, "target      pgbox")
	assertContains(t, out, "local       127.0.0.1:5432")
	assertContains(t, out, "remote      127.0.0.1:5432")
	assertContains(t, out, "through     bastion")
	assertContains(t, out, "reconnect_attempt")
}

func TestRunForwardStatusUnknownID(t *testing.T) {
	stateDir := t.TempDir()
	var stderr bytes.Buffer
	code := Run([]string{"forward", "status", "--state-dir", stateDir, "no-such-id"}, nil, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "no tunnel session matches")
}

func TestRunForwardStopAlreadyExited(t *testing.T) {
	stateDir := t.TempDir()
	exit := 0
	endedAt := time.Now().Add(-30 * time.Second)
	tunnel := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:       "already-stopped-id",
		EndedAt:  &endedAt,
		ExitCode: &exit,
		LocalPID: 99999,
		Forward:  &state.ForwardSpec{LocalPort: 5432, RemotePort: 5432, RemoteHost: "127.0.0.1"},
	})

	var stdout, stderr bytes.Buffer
	code := Run([]string{"forward", "stop", "--state-dir", stateDir, tunnel.ID}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "already exited")
}

func TestRunForwardStopSignalsLiveProcess(t *testing.T) {
	stateDir := t.TempDir()
	// Spawn `sleep` directly (no shell wrapper). SIGHUP's default
	// disposition is to terminate, so the stop command's signal kills
	// it within milliseconds. Using a subprocess (instead of signaling
	// the test process itself) keeps the test self-contained.
	sleep := exec.Command("sleep", "30")
	if err := sleep.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	// Make sure we don't leave it running if the test fails partway.
	t.Cleanup(func() {
		if sleep.Process != nil {
			_ = sleep.Process.Kill()
			_ = sleep.Wait()
		}
	})

	tunnel := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:       "stop-live-id",
		LocalPID: sleep.Process.Pid,
		Forward:  &state.ForwardSpec{Detached: true, LocalPort: 5432, RemotePort: 5432, RemoteHost: "127.0.0.1"},
	})

	// Drive `forward stop`. The poll-for-EndedAt loop won't observe
	// a finalization (no real supervisor is writing the record), so
	// it'll fall through to the "signaled but did not finalize"
	// branch — that's fine. The key invariant is that the SIGHUP
	// reached the live process and killed it.
	var stdout, stderr bytes.Buffer
	code := Run([]string{"forward", "stop", "--state-dir", stateDir, tunnel.ID}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}

	// The subprocess should exit within the poll window. Wait for
	// it to confirm SIGHUP landed.
	done := make(chan error, 1)
	go func() { done <- sleep.Wait() }()
	select {
	case err := <-done:
		// Exit 0 because of the HUP trap; any non-nil err means the
		// shell exited via a different signal — also acceptable.
		_ = err
	case <-time.After(3 * time.Second):
		t.Fatalf("sleep subprocess did not exit within 3s after SIGHUP; stdout=%q stderr=%q",
			stdout.String(), stderr.String())
	}
}

func TestRunForwardStopUnknownTarget(t *testing.T) {
	stateDir := t.TempDir()
	var stderr bytes.Buffer
	code := Run([]string{"forward", "stop", "--state-dir", stateDir, "no-such-id"}, nil, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "no tunnel session matches")
}

func TestRunForwardListRejectsPositionalArgs(t *testing.T) {
	stateDir := t.TempDir()
	var stderr bytes.Buffer
	code := Run([]string{"forward", "list", "--state-dir", stateDir, "extra-arg"}, nil, &stderr, BuildInfo{})
	if code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "does not accept positional arguments")
}

func TestRunForwardStatusRequiresTarget(t *testing.T) {
	stateDir := t.TempDir()
	var stderr bytes.Buffer
	code := Run([]string{"forward", "status", "--state-dir", stateDir}, nil, &stderr, BuildInfo{})
	if code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "requires exactly one")
}

func TestRunForwardSelectFromCatalog(t *testing.T) {
	stateDir := t.TempDir()
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	fakeSSH, logPath := writeFakeSSHFlaky(t, []int{0})

	saved := state.StoredForward{
		Name:       "pngwin-pg-tunnel",
		SSHAlias:   "pgbox",
		LocalBind:  "127.0.0.1",
		LocalPort:  5433,
		RemoteHost: "127.0.0.1",
		RemotePort: 5432,
	}
	if err := state.WriteForward(stateDir, saved); err != nil {
		t.Fatalf("seed forward: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"forward",
		"--state-dir", stateDir,
		"--select", "pngwin-pg-tunnel", // catalog name, not an SSH alias
		"--config", config,
		"--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(readFile(t, logPath))
	want := "-L 127.0.0.1:5433:127.0.0.1:5432 -N -o ExitOnForwardFailure=yes pgbox"
	if got != want {
		t.Fatalf("fake-ssh argv = %q, want %q", got, want)
	}

	// The session record's Forward.SavedAlias should reflect the
	// catalog name so `forward list` can show which named tunnel
	// the row belongs to.
	records, _ := state.ListRecords(stateDir)
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	if records[0].Forward == nil || records[0].Forward.SavedAlias != "pngwin-pg-tunnel" {
		t.Fatalf("record.Forward.SavedAlias = %v, want pngwin-pg-tunnel", records[0].Forward)
	}
}

func TestSavedForwardLaunchArgsCanBackgroundPreset(t *testing.T) {
	args := savedForwardLaunchArgs(connectFlags{
		inventoryFlags: inventoryFlags{Config: "/tmp/ssh_config"},
		StateDir:       "/tmp/ssherpa-state",
	}, "pngwin-pg-tunnel", ui.ForwardActionBackground)

	want := []string{
		"--select", "pngwin-pg-tunnel",
		"--background",
		"--config", "/tmp/ssh_config",
		"--state-dir", "/tmp/ssherpa-state",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args = %v, want %v", args, want)
	}
}

func TestRunForwardSelectFromCatalogCLIOverrides(t *testing.T) {
	// When --local AND --remote are explicit, the catalog is NOT
	// consulted — the user can override saved defaults ad-hoc.
	stateDir := t.TempDir()
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	fakeSSH, logPath := writeFakeSSHFlaky(t, []int{0})

	saved := state.StoredForward{
		Name: "pngwin-pg-tunnel", SSHAlias: "pgbox",
		LocalBind: "127.0.0.1", LocalPort: 5433,
		RemoteHost: "127.0.0.1", RemotePort: 5432,
	}
	_ = state.WriteForward(stateDir, saved)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"forward",
		"--state-dir", stateDir,
		"--select", "pgbox", // SSH alias, not catalog name
		"--local", "9999", "--remote", "127.0.0.1:5432",
		"--config", config, "--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}
	got := strings.TrimSpace(readFile(t, logPath))
	if !strings.Contains(got, "127.0.0.1:9999:127.0.0.1:5432") {
		t.Fatalf("CLI args did not override catalog: %q", got)
	}
}

func TestRunForwardSelectCatalogUpdatesLastLaunched(t *testing.T) {
	stateDir := t.TempDir()
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	fakeSSH, _ := writeFakeSSHFlaky(t, []int{0})

	saved := state.StoredForward{
		Name: "pngwin-pg-tunnel", SSHAlias: "pgbox",
		LocalBind: "127.0.0.1", LocalPort: 5433,
		RemoteHost: "127.0.0.1", RemotePort: 5432,
	}
	_ = state.WriteForward(stateDir, saved)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"forward",
		"--state-dir", stateDir,
		"--select", "pngwin-pg-tunnel",
		"--config", config, "--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}

	after, err := state.ReadForward(stateDir, "pngwin-pg-tunnel")
	if err != nil {
		t.Fatalf("ReadForward after launch: %v", err)
	}
	if after.LastLaunchedAt == nil {
		t.Fatalf("LastLaunchedAt not bumped after launch: %+v", after)
	}
}

func TestPickerActiveTunnelsFiltersToLiveTunnels(t *testing.T) {
	stateDir := t.TempDir()
	exit := 0
	endedAt := time.Now().Add(-1 * time.Minute)

	// Live tunnel — should appear.
	live := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:          "live-tunnel-id",
		TargetAlias: "pgbox",
		LocalPID:    os.Getpid(),
		StartedAt:   time.Now().Add(-3 * time.Minute),
		Forward: &state.ForwardSpec{
			LocalBind: "127.0.0.1", LocalPort: 5432,
			RemoteHost: "127.0.0.1", RemotePort: 5432,
			SavedAlias: "pngwin-pg-tunnel",
		},
	})

	// Exited tunnel — should be filtered out (EndedAt != nil).
	_ = seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:          "exited-tunnel-id",
		TargetAlias: "redisbox",
		LocalPID:    os.Getpid(),
		EndedAt:     &endedAt,
		ExitCode:    &exit,
		Forward:     &state.ForwardSpec{LocalPort: 6379, RemoteHost: "127.0.0.1", RemotePort: 6379},
	})

	// Orphan tunnel — EndedAt nil but PID gone. Should be filtered
	// out — the home page focuses on actionable items; orphans
	// surface in `ssherpa forward list` instead.
	_ = seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:          "orphan-tunnel-id",
		TargetAlias: "stalebox",
		LocalPID:    1 << 20, // very unlikely to be alive
		Forward:     &state.ForwardSpec{LocalPort: 1234, RemoteHost: "127.0.0.1", RemotePort: 1234},
	})

	// Interactive session — should be filtered out (Kind != tunnel).
	if err := state.WriteRecord(stateDir, state.SessionRecord{
		ID:          "interactive-id",
		Kind:        state.KindInteractive,
		TargetAlias: "shell",
		StartedAt:   time.Now().Add(-1 * time.Hour),
		LocalPID:    os.Getpid(),
		RunnerMode:  "supervised",
	}); err != nil {
		t.Fatalf("WriteRecord interactive: %v", err)
	}

	got := pickerActiveTunnels(stateDir)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got=%+v", len(got), got)
	}
	if got[0].SessionID != live.ID {
		t.Fatalf("got[0].SessionID = %q, want %q", got[0].SessionID, live.ID)
	}
	// Title prefers SavedAlias when set.
	if got[0].Title != "pngwin-pg-tunnel" {
		t.Fatalf("Title = %q, want 'pngwin-pg-tunnel' (saved alias preferred over TargetAlias)", got[0].Title)
	}
	// Description should include endpoints, uptime, and pid.
	if !strings.Contains(got[0].Description, "127.0.0.1:5432 -> 127.0.0.1:5432") {
		t.Fatalf("Description missing endpoints: %q", got[0].Description)
	}
	if !strings.Contains(got[0].Description, "pid ") {
		t.Fatalf("Description missing pid: %q", got[0].Description)
	}
	if !strings.Contains(got[0].Description, "up ") {
		t.Fatalf("Description missing uptime: %q", got[0].Description)
	}
}

func TestPickerActiveTunnelsTitleFallsBackToTargetAlias(t *testing.T) {
	stateDir := t.TempDir()
	live := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:          "ad-hoc-id",
		TargetAlias: "ad-hoc-host",
		LocalPID:    os.Getpid(),
		Forward: &state.ForwardSpec{
			LocalBind: "127.0.0.1", LocalPort: 5432,
			RemoteHost: "127.0.0.1", RemotePort: 5432,
			// No SavedAlias — this is an ad-hoc tunnel.
		},
	})
	got := pickerActiveTunnels(stateDir)
	if len(got) != 1 || got[0].SessionID != live.ID {
		t.Fatalf("got = %+v, want one live entry", got)
	}
	if got[0].Title != "ad-hoc-host" {
		t.Fatalf("Title = %q, want 'ad-hoc-host' (fallback to TargetAlias)", got[0].Title)
	}
}

// TestRunForwardManagementLogPath verifies that a status query
// surfaces the expected log file path even when the log file
// doesn't exist yet (a status call against a freshly-seeded record).
func TestRunForwardStatusShowsLogPath(t *testing.T) {
	stateDir := t.TempDir()
	tunnel := seedTunnelRecord(t, stateDir, state.SessionRecord{
		ID:       "logpath-id",
		LocalPID: os.Getpid(),
		Forward:  &state.ForwardSpec{Detached: true, LocalPort: 5432, RemotePort: 5432, RemoteHost: "127.0.0.1"},
	})
	var stdout, stderr bytes.Buffer
	code := Run([]string{"forward", "status", "--state-dir", stateDir, tunnel.ID}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}
	expectLog := filepath.Join(state.SessionsDir(stateDir), tunnel.ID+".log")
	assertContains(t, stdout.String(), expectLog)
}
