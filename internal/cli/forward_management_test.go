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
