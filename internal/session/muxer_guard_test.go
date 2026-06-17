package session

import (
	"bytes"
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
)

// ttyID is the (ino, rdev) identity a fake pts node reports.
type ttyID struct{ ino, rdev uint64 }

// guardStep is one poll: the attached client ttys, the pts nodes that
// exist this tick, and the action observe() must return. unknown forces an
// OK=false probe (query failed this tick).
type guardStep struct {
	clients []string
	ttys    map[string]ttyID
	unknown bool
	want    muxerAction
}

func runGuardSteps(t *testing.T, steps []guardStep) {
	t.Helper()
	g := &muxerGuardState{}
	for i, step := range steps {
		ttys := step.ttys
		stat := func(path string) (uint64, uint64, bool) {
			id, ok := ttys[path]
			if !ok {
				return 0, 0, false
			}
			return id.ino, id.rdev, true
		}
		got := g.observe(muxerAttachState{Clients: step.clients, OK: !step.unknown}, stat)
		if got != step.want {
			t.Fatalf("step %d: observe = %v, want %v (state %+v)", i, got, step.want, g)
		}
	}
}

func TestMuxerGuardStateMachine(t *testing.T) {
	pts2 := map[string]ttyID{"/dev/pts/2": {ino: 10, rdev: 5}}
	gone := map[string]ttyID{}

	t.Run("deliberate detach spares and stays sticky after later logout", func(t *testing.T) {
		runGuardSteps(t, []guardStep{
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone}, // arm
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone}, // attached
			{clients: nil, ttys: pts2, want: muxerActionNone},                    // detached, pts alive (1)
			{clients: nil, ttys: pts2, want: muxerActionDetached},                // detached confirmed -> protected
			{clients: nil, ttys: gone, want: muxerActionNone},                    // later logout: still spared
			{clients: nil, ttys: gone, want: muxerActionNone},
		})
	})

	t.Run("drop while attached tears down", func(t *testing.T) {
		runGuardSteps(t, []guardStep{
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone}, // arm
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone}, // attached
			{clients: nil, ttys: gone, want: muxerActionNone},                    // pts gone (1)
			{clients: nil, ttys: gone, want: muxerActionTeardown},                // pts gone (2) -> teardown
		})
	})

	t.Run("drop with pts lingering one tick is not mislatched as detach", func(t *testing.T) {
		runGuardSteps(t, []guardStep{
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone}, // arm
			{clients: nil, ttys: pts2, want: muxerActionNone},                    // pts lingers (detachRun=1)
			{clients: nil, ttys: gone, want: muxerActionNone},                    // pts gone (detach reset, dropRun=1)
			{clients: nil, ttys: gone, want: muxerActionTeardown},                // dropRun=2 -> teardown
		})
	})

	t.Run("never attached never tears down", func(t *testing.T) {
		runGuardSteps(t, []guardStep{
			{clients: nil, ttys: gone, want: muxerActionNone},
			{clients: nil, ttys: gone, want: muxerActionNone},
			{clients: nil, ttys: gone, want: muxerActionNone},
		})
	})

	t.Run("unknown probe takes no action and neither advances nor resets streaks", func(t *testing.T) {
		runGuardSteps(t, []guardStep{
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone}, // arm
			{clients: nil, ttys: gone, want: muxerActionNone},                    // dropRun=1
			{unknown: true, want: muxerActionNone},                               // unknown: streak held at 1
			{clients: nil, ttys: gone, want: muxerActionTeardown},                // dropRun=2 -> teardown
		})
	})

	t.Run("reused pts (same rdev, different ino) is a drop not a detach", func(t *testing.T) {
		reused := map[string]ttyID{"/dev/pts/2": {ino: 99, rdev: 5}} // same device, fresh inode
		runGuardSteps(t, []guardStep{
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone}, // arm (ino 10)
			{clients: nil, ttys: reused, want: muxerActionNone},                  // reused -> identity mismatch -> drop(1)
			{clients: nil, ttys: reused, want: muxerActionTeardown},              // drop(2) -> teardown
		})
	})

	t.Run("multi-client: pinned user drop detected though peer stays attached", func(t *testing.T) {
		bothTTYs := map[string]ttyID{"/dev/pts/2": {ino: 10, rdev: 5}, "/dev/pts/9": {ino: 20, rdev: 9}}
		peerOnly := map[string]ttyID{"/dev/pts/9": {ino: 20, rdev: 9}}
		runGuardSteps(t, []guardStep{
			{clients: []string{"/dev/pts/2"}, ttys: pts2, want: muxerActionNone},                   // arm user pts2
			{clients: []string{"/dev/pts/2", "/dev/pts/9"}, ttys: bothTTYs, want: muxerActionNone}, // peer joins
			{clients: []string{"/dev/pts/9"}, ttys: peerOnly, want: muxerActionNone},               // user drops (pts2 gone) (1)
			{clients: []string{"/dev/pts/9"}, ttys: peerOnly, want: muxerActionTeardown},           // (2) -> teardown
		})
	})

	t.Run("arm skips an unstatable client and pins the next", func(t *testing.T) {
		runGuardSteps(t, []guardStep{
			// First client is not a valid pts (bogus), second is -> pin the second.
			{clients: []string{"/dev/bogus", "/dev/pts/2"}, ttys: pts2, want: muxerActionNone},
			{clients: nil, ttys: gone, want: muxerActionNone},
			{clients: nil, ttys: gone, want: muxerActionTeardown},
		})
	})
}

func TestSelectExitCode(t *testing.T) {
	tests := []struct {
		name                 string
		base                 int
		rope, intr, mux, pan bool
		wantCode             int
		wantKind             teardownKind
	}{
		{name: "clean", base: 0, wantCode: 0, wantKind: teardownNone},
		{name: "error base", base: 3, wantCode: 3, wantKind: teardownNone},
		{name: "rope", rope: true, wantCode: EscapeRopeExitCode, wantKind: teardownRope},
		{name: "interrupt", intr: true, wantCode: InterruptExitCode, wantKind: teardownInterrupt},
		{name: "muxer", mux: true, wantCode: MuxerUpstreamLostExitCode, wantKind: teardownMuxer},
		{name: "panic", pan: true, wantCode: 1, wantKind: teardownPanic},
		{name: "rope beats interrupt", rope: true, intr: true, wantCode: EscapeRopeExitCode, wantKind: teardownRope},
		{name: "rope beats muxer", rope: true, mux: true, wantCode: EscapeRopeExitCode, wantKind: teardownRope},
		{name: "interrupt beats muxer", intr: true, mux: true, wantCode: InterruptExitCode, wantKind: teardownInterrupt},
		{name: "muxer beats panic", mux: true, pan: true, wantCode: MuxerUpstreamLostExitCode, wantKind: teardownMuxer},
		{name: "rope beats everything", rope: true, intr: true, mux: true, pan: true, wantCode: EscapeRopeExitCode, wantKind: teardownRope},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, kind := selectExitCode(tt.base, tt.rope, tt.intr, tt.mux, tt.pan)
			if code != tt.wantCode || kind != tt.wantKind {
				t.Fatalf("selectExitCode = (%d,%v), want (%d,%v)", code, kind, tt.wantCode, tt.wantKind)
			}
		})
	}
}

// TestRunSupervisedMuxerGuardTearsDownOnUpstreamDrop drives a real
// supervised session inside a (simulated) tmux pane and proves that when
// the upstream link drops while attached, the guard SIGHUPs the ssh child,
// finalizes the record with MuxerUpstreamLostReason, and returns the
// distinct exit code — without any real tmux.
func TestRunSupervisedMuxerGuardTearsDownOnUpstreamDrop(t *testing.T) {
	stateDir := t.TempDir()
	stdin := devNull(t)
	defer stdin.Close()
	var stdout, stderr bytes.Buffer
	fake, probe := armThenDrop(false) // pts dies on the drop -> upstream lost

	code := runSupervisedWithin(t, 3*time.Second,
		sshcmd.Command{Argv: []string{"sh", "-c", "sleep 30"}},
		Metadata{TargetAlias: "deep"},
		muxerGuardOptions(stateDir, stdin, &stdout, &stderr, true,
			MuxerGuardSettings{Interval: 5 * time.Millisecond, Probe: probe, Stat: fake.stat}),
	)

	if code != MuxerUpstreamLostExitCode {
		t.Fatalf("exit code = %d, want %d; stderr=%q", code, MuxerUpstreamLostExitCode, stderr.String())
	}
	rec := localRecord(t, stateDir)
	if rec.DisconnectReason != MuxerUpstreamLostReason {
		t.Fatalf("disconnect reason = %q, want %q", rec.DisconnectReason, MuxerUpstreamLostReason)
	}
	if rec.Muxer == nil || rec.Muxer.Type != "tmux" {
		t.Fatalf("record Muxer = %#v, want type tmux", rec.Muxer)
	}
	if rec.EndedAt == nil || rec.ExitCode == nil || *rec.ExitCode != MuxerUpstreamLostExitCode {
		t.Fatalf("record not finalized as expected: EndedAt=%v ExitCode=%v", rec.EndedAt, rec.ExitCode)
	}
}

// TestRunSupervisedMuxerGuardForceTearsDownWithoutAncestry proves the
// SSHERPA_MUXER_GUARD=force path engages the kill path end-to-end even with
// no ssherpa ancestry (no SSHERPA_SESSION_ID), tearing down on a drop.
func TestRunSupervisedMuxerGuardForceTearsDownWithoutAncestry(t *testing.T) {
	stateDir := t.TempDir()
	stdin := devNull(t)
	defer stdin.Close()
	var stdout, stderr bytes.Buffer
	fake, probe := armThenDrop(false)

	code := runSupervisedWithin(t, 3*time.Second,
		sshcmd.Command{Argv: []string{"sh", "-c", "sleep 30"}},
		Metadata{TargetAlias: "deep"},
		muxerGuardOptions(stateDir, stdin, &stdout, &stderr, false, // NOT nested
			MuxerGuardSettings{Interval: 5 * time.Millisecond, Force: true, Probe: probe, Stat: fake.stat}),
	)

	if code != MuxerUpstreamLostExitCode {
		t.Fatalf("force mode should tear down without ancestry: code=%d, want %d; stderr=%q", code, MuxerUpstreamLostExitCode, stderr.String())
	}
	if rec := localRecord(t, stateDir); rec.DisconnectReason != MuxerUpstreamLostReason {
		t.Fatalf("disconnect reason = %q, want %q", rec.DisconnectReason, MuxerUpstreamLostReason)
	}
}

// TestRunSupervisedMuxerGuardSparesDeliberateDetach proves the guard does
// NOT tear down a session whose client deliberately detached (the pts stays
// alive): the session runs to its own clean exit and is flagged detached.
func TestRunSupervisedMuxerGuardSparesDeliberateDetach(t *testing.T) {
	stateDir := t.TempDir()
	stdin := devNull(t)
	defer stdin.Close()
	var stdout, stderr bytes.Buffer
	fake, probe := armThenDrop(true) // pts stays alive -> deliberate detach

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "sleep 0.4"}}, // self-exits; must NOT be torn down first
		Metadata{TargetAlias: "deep"},
		muxerGuardOptions(stateDir, stdin, &stdout, &stderr, true,
			MuxerGuardSettings{Interval: 5 * time.Millisecond, Probe: probe, Stat: fake.stat}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (spared); stderr=%q", code, stderr.String())
	}
	rec := localRecord(t, stateDir)
	if rec.DisconnectReason != "" {
		t.Fatalf("disconnect reason = %q, want empty (not torn down)", rec.DisconnectReason)
	}
	if rec.Muxer == nil || !rec.Muxer.Detached {
		t.Fatalf("record Muxer = %#v, want Detached=true", rec.Muxer)
	}
}

// TestRunSupervisedMuxerGuardDisabledWhenNotNested proves the guard never
// engages without ssherpa ancestry (no SSHERPA_SESSION_ID). The injected
// probe WOULD arm-then-drop and tear down if the guard ran, so removing the
// ancestry gate flips this session to exit 121 and fails the test.
func TestRunSupervisedMuxerGuardDisabledWhenNotNested(t *testing.T) {
	stateDir := t.TempDir()
	stdin := devNull(t)
	defer stdin.Close()
	var stdout, stderr bytes.Buffer
	fake, probe := armThenDrop(false) // would tear down if the guard ever started

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "sleep 0.5"}}, // self-exits when the guard is (correctly) disabled
		Metadata{TargetAlias: "deep"},
		muxerGuardOptions(stateDir, stdin, &stdout, &stderr, false, // NOT nested -> gate fails
			MuxerGuardSettings{Interval: 5 * time.Millisecond, Probe: probe, Stat: fake.stat}),
	)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (guard must not engage without ancestry); stderr=%q", code, stderr.String())
	}
	rec := localRecord(t, stateDir)
	if rec.DisconnectReason != "" {
		t.Fatalf("disconnect reason = %q, want empty (guard disabled)", rec.DisconnectReason)
	}
	// Still tagged for display even though the kill path was disabled.
	if rec.Muxer == nil || rec.Muxer.Type != "tmux" {
		t.Fatalf("record Muxer = %#v, want type tmux (detect-only)", rec.Muxer)
	}
}

// devNull opens /dev/null as a non-terminal stdin for a supervised session.
func devNull(t *testing.T) *os.File {
	t.Helper()
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	return f
}

// muxerGuardOptions builds Options for an interactive session simulated to
// run inside tmux, optionally nested under an ssherpa parent (which sets a
// non-empty ParentID and so satisfies the ancestry gate).
func muxerGuardOptions(stateDir string, stdin *os.File, stdout, stderr *bytes.Buffer, nested bool, settings MuxerGuardSettings) Options {
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"TMUX=/tmp/tmux-1000/default,5166,0",
		"TMUX_PANE=%0",
	}
	if nested {
		env = append(env, "SSHERPA_SESSION_ID=parent-123")
	}
	return Options{StateDir: stateDir, Stdin: stdin, Stdout: stdout, Stderr: stderr, Env: env, MuxerGuard: settings}
}

// armThenDrop returns a fake whose probe reports one attached client (so the
// guard arms), then on the third poll drops it. keepPTSAlive=false removes
// the pinned login pts (a real upstream drop -> teardown); keepPTSAlive=true
// leaves it (a deliberate detach -> spare).
func armThenDrop(keepPTSAlive bool) (*fakeTmuxContext, muxerAttachProbe) {
	fake := &fakeTmuxContext{}
	fake.setClients("/dev/pts/2")
	fake.setTTY("/dev/pts/2", 10, 5)
	var calls atomic.Int32
	probe := func(ctx context.Context) muxerAttachState {
		if calls.Add(1) == 3 {
			fake.setClients()
			if !keepPTSAlive {
				fake.removeTTY("/dev/pts/2")
			}
		}
		return fake.probe(ctx)
	}
	return fake, probe
}

// runSupervisedWithin runs RunSupervised in a goroutine and fails fast if it
// does not return within timeout, so a guard regression that fails to tear
// down a long-lived child surfaces promptly instead of waiting out the sleep.
func runSupervisedWithin(t *testing.T, timeout time.Duration, cmd sshcmd.Command, meta Metadata, opts Options) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- RunSupervised(cmd, meta, opts) }()
	select {
	case code := <-done:
		return code
	case <-time.After(timeout):
		t.Fatalf("RunSupervised did not return within %s; guard likely failed to tear down", timeout)
		return -1
	}
}

// localRecord returns the single local (non-mirror) session record.
func localRecord(t *testing.T, stateDir string) state.SessionRecord {
	t.Helper()
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	for _, rec := range records {
		if !rec.RemoteMirror {
			return rec
		}
	}
	t.Fatalf("no local record found among %d records", len(records))
	return state.SessionRecord{}
}
