package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/transcript"
	"github.com/creack/pty"
)

func TestRunSupervisedRecordsSessionLifecycle(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "printf 'hello from pty'; exit 7"}},
		Metadata{
			TargetAlias: "prod",
			Hops:        []string{"bastion"},
			Route:       []string{"bastion", "prod"},
		},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Now:      fixedClock(),
		},
	)

	if code != 7 {
		t.Fatalf("RunSupervised returned %d, want 7; stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if !strings.Contains(stdout.String(), "hello from pty") {
		t.Fatalf("stdout = %q, want pty output", stdout.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.Status() != "exited" || record.ExitCode == nil || *record.ExitCode != 7 {
		t.Fatalf("record status/exit = %#v", record)
	}
	if record.SSHPID == 0 || record.LocalPID == 0 {
		t.Fatalf("record pids = %#v", record)
	}
	if record.TargetAlias != "prod" || !reflect.DeepEqual(record.Hops, []string{"bastion"}) || !reflect.DeepEqual(record.Route, []string{"bastion", "prod"}) {
		t.Fatalf("record route metadata = %#v", record)
	}
	if got := strings.Join(record.SSHArgv, "\x00"); got != "sh\x00-c\x00printf 'hello from pty'; exit 7" {
		t.Fatalf("record ssh argv = %#v", record.SSHArgv)
	}
	if record.Transcript != nil {
		t.Fatalf("record.Transcript = %#v, want nil until recording is started from the overlay", record.Transcript)
	}
	if _, err := os.Stat(transcript.Path(stateDir, record.ID)); !os.IsNotExist(err) {
		t.Fatalf("transcript file stat err = %v, want not exist until recording is started from the overlay", err)
	}
}

func TestRunSupervisedAddsControlMasterForSSHCommands(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	sshPath := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nprintf 'fake ssh\\n'\nexit 0\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	code := RunSupervised(
		sshcmd.Command{Argv: []string{sshPath, "prod"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.ControlPath == "" {
		t.Fatalf("record ControlPath is empty; record = %#v", record)
	}
	got := strings.Join(record.SSHArgv, "\x00")
	if !strings.Contains(got, "\x00-o\x00ControlMaster=auto\x00-o\x00ControlPath="+record.ControlPath+"\x00-o\x00ControlPersist=10m\x00prod") {
		t.Fatalf("record ssh argv = %#v, want ControlMaster options before target", record.SSHArgv)
	}
}

func TestRunSupervisedPropagatesSessionEnvironment(t *testing.T) {
	var stdout bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "session-env.txt")
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "printf '%s/%s/%s' \"$SSHERPA_SESSION_ID\" \"$SSHERPA_DEPTH\" \"$SSHERPA_ROUTE\" > \"$OUT\""}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Env: []string{
				"PATH=" + os.Getenv("PATH"),
				"SSHERPA_SESSION_ID=parent",
				"SSHERPA_DEPTH=2",
				"SSHERPA_ROUTE=laptop",
				"OUT=" + outPath,
			},
			Now: fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0", code)
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	want := records[0].ID + "/3/laptop,prod"
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read env output: %v; stdout=%q", err, stdout.String())
	}
	if string(got) != want {
		t.Fatalf("env output = %q, want %q; stdout=%q", string(got), want, stdout.String())
	}
}

func TestRunSupervisedRecordsRemoteCWDAndPromptState(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `printf '\033]7;file://deep.example.com/var/www/app\007\033]133;B\033\\ready'`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ready") {
		t.Fatalf("stdout = %q, want remote stream preserved", stdout.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.RemoteHost != "deep.example.com" || record.RemoteCWD != "/var/www/app" || record.RemotePrompt != RemotePromptPrompt {
		t.Fatalf("remote state = host %q cwd %q prompt %q, want observed OSC state", record.RemoteHost, record.RemoteCWD, record.RemotePrompt)
	}
}

func TestRunSupervisedRecordsRunningPromptState(t *testing.T) {
	var stdout bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `printf '\033]133;C\007busy'`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0", code)
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].RemotePrompt != RemotePromptRunning {
		t.Fatalf("records = %#v, want running prompt state", records)
	}
}

func TestRunSupervisedMirrorsRemoteDescendantTelemetry(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	child := state.SessionRecord{
		ID:          "child",
		TargetAlias: "prod",
		Route:       []string{"prod"},
		StartedAt:   fixedClock()(),
		RunnerMode:  RunnerModeSupervised,
	}
	payload, ok := sessionTelemetryFrame(child)
	if !ok {
		t.Fatalf("sessionTelemetryFrame returned !ok")
	}
	mirrorPath := state.RecordPath(stateDir, child.ID)

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `printf '%s' "$SSHERPA_TEST_TELEMETRY"; i=0; while [ "$i" -lt 200 ] && [ ! -f "$SSHERPA_TEST_MIRROR_PATH" ]; do i=$((i + 1)); sleep 0.01; done`}},
		Metadata{TargetAlias: "bastion", Route: []string{"bastion"}},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env: []string{
				"PATH=" + os.Getenv("PATH"),
				"SSHERPA_TEST_TELEMETRY=" + string(payload),
				"SSHERPA_TEST_MIRROR_PATH=" + mirrorPath,
			},
			Now:      fixedClock(),
			RecordID: "parent",
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "ssherpa-session") {
		t.Fatalf("stdout leaked telemetry frame: %q", stdout.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %#v, want parent and mirrored child", records)
	}
	var mirrored state.SessionRecord
	for _, record := range records {
		if record.ID == "child" {
			mirrored = record
		}
	}
	if !mirrored.RemoteMirror || mirrored.ParentID != "parent" || mirrored.TargetAlias != "prod" || mirrored.Depth != 1 {
		t.Fatalf("mirrored record = %#v, want remote mirror child under parent", mirrored)
	}
	if !reflect.DeepEqual(mirrored.Route, []string{"bastion", "prod"}) {
		t.Fatalf("mirrored route = %#v, want parent route plus child target", mirrored.Route)
	}
	if mirrored.LocalPID != 0 || mirrored.SSHPID != 0 || state.ProcessAlive(mirrored) {
		t.Fatalf("mirrored pids/process = %#v, want non-local process", mirrored)
	}
	if mirrored.EndedAt == nil || mirrored.ExitCode == nil || *mirrored.ExitCode != 0 {
		t.Fatalf("mirrored finalization = %#v, want finalized with parent exit", mirrored)
	}
}

func TestFinalizeRemoteMirrorsClosesDescendants(t *testing.T) {
	stateDir := t.TempDir()
	parent := state.SessionRecord{ID: "parent", TargetAlias: "bastion"}
	child := state.SessionRecord{ID: "child", ParentID: "parent", RemoteMirror: true, TargetAlias: "prod"}
	grandchild := state.SessionRecord{ID: "grandchild", ParentID: "child", RemoteMirror: true, TargetAlias: "db"}
	unrelated := state.SessionRecord{ID: "unrelated", ParentID: "other", RemoteMirror: true, TargetAlias: "other"}
	for _, record := range []state.SessionRecord{parent, child, grandchild, unrelated} {
		if err := state.WriteRecord(stateDir, record); err != nil {
			t.Fatalf("WriteRecord(%s): %v", record.ID, err)
		}
	}

	endedAt := fixedClock()().UTC()
	finalizeRemoteMirrors(stateDir, parent, endedAt, 120)

	for _, id := range []string{"child", "grandchild"} {
		record, err := state.ReadRecord(stateDir, id)
		if err != nil {
			t.Fatalf("ReadRecord(%s): %v", id, err)
		}
		if record.EndedAt == nil || !record.EndedAt.Equal(endedAt) || record.ExitCode == nil || *record.ExitCode != 120 {
			t.Fatalf("%s finalization = %#v, want ended with parent exit", id, record)
		}
	}
	record, err := state.ReadRecord(stateDir, "unrelated")
	if err != nil {
		t.Fatalf("ReadRecord(unrelated): %v", err)
	}
	if record.EndedAt != nil {
		t.Fatalf("unrelated mirror ended = %#v, want active", record)
	}
}

func TestRemoteMirrorRecordRejectsUnrelatedTelemetry(t *testing.T) {
	parent := state.SessionRecord{ID: "parent", Route: []string{"bastion"}, OriginHost: "laptop"}
	child := state.SessionRecord{
		ID:         "child",
		ParentID:   "other",
		OriginHost: "other-laptop",
		Route:      []string{"unrelated", "prod"},
	}

	if got, _, ok := remoteMirrorRecord(parent, child); ok {
		t.Fatalf("remoteMirrorRecord = %#v, true; want rejected unrelated telemetry", got)
	}
}

func TestRemoteMirrorRecordUsesParentOriginForParentlessTelemetry(t *testing.T) {
	parent := state.SessionRecord{
		ID:          "parent",
		Depth:       0,
		OriginHost:  "Bens-Mac-mini",
		TargetAlias: "ben-6900hx-daily",
		Route:       []string{"ben-6900hx-daily"},
	}
	child := state.SessionRecord{
		ID:          "child",
		OriginHost:  "pop-os",
		TargetAlias: "mdw0-vms-tailscale",
		Route:       []string{"mdw0-vms-tailscale"},
	}

	got, backfilled, ok := remoteMirrorRecord(parent, child)
	if !ok {
		t.Fatalf("remoteMirrorRecord rejected parentless telemetry")
	}
	if !backfilled {
		t.Fatalf("remoteMirrorRecord did not report the parentless child as backfilled")
	}
	if got.OriginHost != "Bens-Mac-mini" {
		t.Fatalf("OriginHost = %q, want parent origin", got.OriginHost)
	}
	if !reflect.DeepEqual(got.Route, []string{"ben-6900hx-daily", "mdw0-vms-tailscale"}) {
		t.Fatalf("Route = %#v, want parent alias plus child target", got.Route)
	}
	if got.ParentID != "parent" || got.Depth != 1 || !got.RemoteMirror {
		t.Fatalf("mirror metadata = %#v, want attached remote mirror", got)
	}
}

// TestRunSupervisedDetachedMode validates Phase 2b's daemon path at
// the session level: with Options.Detached the supervisor skips PTY
// raw mode, never starts copyInput, accepts a pre-assigned RecordID,
// still writes the lifecycle record, and surfaces the child's exit
// code. The Forward.Detached flag round-trips via Metadata.
func TestRunSupervisedDetachedMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	stateDir := t.TempDir()
	preassignedID := "detached-test-id-0001"

	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		// A trivially-fast child so the test runs in <100ms.
		sshcmd.Command{Argv: []string{"sh", "-c", "printf 'detached output'; exit 0"}},
		Metadata{
			TargetAlias: "pgbox",
			Route:       []string{"pgbox"},
			Kind:        state.KindTunnel,
			Forward: &state.ForwardSpec{
				LocalBind:  "127.0.0.1",
				LocalPort:  5432,
				RemoteHost: "127.0.0.1",
				RemotePort: 5432,
				Detached:   true,
			},
		},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Now:      fixedClock(),
			Detached: true,
			RecordID: preassignedID,
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised(detached) returned %d, want 0; stderr=%q", code, stderr.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.ID != preassignedID {
		t.Fatalf("record.ID = %q, want pre-assigned %q", rec.ID, preassignedID)
	}
	if rec.Kind != state.KindTunnel {
		t.Fatalf("record.Kind = %q, want tunnel", rec.Kind)
	}
	if rec.Forward == nil || !rec.Forward.Detached {
		t.Fatalf("record.Forward.Detached not set: %+v", rec.Forward)
	}
	if rec.EndedAt == nil {
		t.Fatalf("record.EndedAt not set; supervisor didn't finalize")
	}
	if rec.ExitCode == nil || *rec.ExitCode != 0 {
		t.Fatalf("record.ExitCode = %v, want 0", rec.ExitCode)
	}
}

// TestForwardSignalsCallsPullRopeOnExternalTerminate is the regression
// guard for the bug where `ssherpa forward stop` would SIGHUP the
// daemon but the daemon would respawn ssh anyway. Root cause: ssh
// catches SIGHUP and exits cleanly with code 255, which shouldRetry
// (correctly, for a real network drop) treated as transient — so the
// retry loop kept going without the supervisor knowing the human asked
// for a stop. The fix routes SIGHUP/SIGTERM/SIGQUIT through pullRope
// so the retry loop's ropePulled check breaks out.
//
// The test sends SIGHUP to its own process (forwardSignals subscribes
// process-wide via signal.Notify, so the signal reaches its handler),
// then asserts the supplied pullRope callback was invoked.
func TestForwardSignalsCallsPullRopeOnExternalTerminate(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_, _ = cmd.Process.Wait()
	})

	pulled := make(chan struct{}, 1)
	pullRope := func() {
		select {
		case pulled <- struct{}{}:
		default:
		}
	}

	stop := forwardSignals(nil, nil, cmd, pullRope, nil)
	defer stop()

	// Send SIGHUP to our own pid; signal.Notify subscribed in
	// forwardSignals routes it to the handler goroutine.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatalf("self SIGHUP: %v", err)
	}

	select {
	case <-pulled:
		// expected — fix works
	case <-time.After(2 * time.Second):
		t.Fatalf("pullRope was not invoked within 2s after SIGHUP — daemon would have respawned ssh")
	}
}

func TestRunSupervisedCtrlCDuringStartupInterruptsLocally(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	userPTY, userTTY, err := pty.Open()
	if err != nil {
		t.Fatalf("open user pty: %v", err)
	}
	defer userPTY.Close()
	defer userTTY.Close()

	done := make(chan int, 1)
	go func() {
		done <- RunSupervised(
			sshcmd.Command{Argv: []string{"sleep", "30"}},
			Metadata{TargetAlias: "prod"},
			Options{
				StateDir: stateDir,
				Stdin:    userTTY,
				Stdout:   &stdout,
				Stderr:   &stderr,
				Env:      []string{"PATH=" + os.Getenv("PATH")},
				Now:      fixedClock(),
			},
		)
	}()

	waitForSessionRecordCount(t, stateDir, 1, 2*time.Second)
	if _, err := userPTY.Write([]byte{0x03}); err != nil {
		t.Fatalf("write Ctrl+C: %v", err)
	}

	var code int
	select {
	case code = <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("RunSupervised did not return after startup Ctrl+C; stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
	if code != InterruptExitCode {
		t.Fatalf("RunSupervised returned %d, want %d; stderr=%q", code, InterruptExitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "connection attempt interrupted") {
		t.Fatalf("stderr = %q, want interrupt notice", stderr.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].DisconnectReason != InterruptReason {
		t.Fatalf("records = %#v, want one interrupted record", records)
	}
	if records[0].ExitCode == nil || *records[0].ExitCode != InterruptExitCode {
		t.Fatalf("record exit = %#v, want %d", records[0].ExitCode, InterruptExitCode)
	}
	hasInterruptEvent := false
	for _, event := range records[0].Events {
		if event.Type == InterruptReason {
			hasInterruptEvent = true
		}
	}
	if !hasInterruptEvent {
		t.Fatalf("events = %#v, want interrupt event", records[0].Events)
	}
}

func TestRunSupervisedCtrlCAfterOutputReachesRemote(t *testing.T) {
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	userPTY, userTTY, err := pty.Open()
	if err != nil {
		t.Fatalf("open user pty: %v", err)
	}
	defer userPTY.Close()
	defer userTTY.Close()
	stdout := newNotifyingBuffer("ready")

	done := make(chan int, 1)
	go func() {
		done <- RunSupervised(
			sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo 2>/dev/null; printf ready; dd bs=1 count=1 of="$OUT" 2>/dev/null`}},
			Metadata{TargetAlias: "prod"},
			Options{
				StateDir: stateDir,
				Stdin:    userTTY,
				Stdout:   stdout,
				Stderr:   &stderr,
				Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
				Now:      fixedClock(),
			},
		)
	}()

	select {
	case <-stdout.notified:
	case <-time.After(3 * time.Second):
		t.Fatalf("child did not produce startup output; stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
	if _, err := userPTY.Write([]byte{0x03}); err != nil {
		t.Fatalf("write Ctrl+C: %v", err)
	}

	var code int
	select {
	case code = <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("RunSupervised did not return after forwarded Ctrl+C; stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input: %v", err)
	}
	if !bytes.Equal(got, []byte{0x03}) {
		t.Fatalf("remote input = %#v, want Ctrl+C byte", got)
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].DisconnectReason == InterruptReason {
		t.Fatalf("records = %#v, want normal forwarded Ctrl+C session", records)
	}
}

func TestRunSupervisedOverlayHotkeyDoesNotReachRemote(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	input := []byte{'a', OverlayHotkey, 'q', 'b'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=2 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if string(got) != "ab" {
		t.Fatalf("remote input = %q, want only non-hotkey bytes", string(got))
	}
	if !strings.Contains(stdout.String(), "ssherpa session map") {
		t.Fatalf("stdout = %q, want local overlay", stdout.String())
	}
}

func TestRunSupervisedCustomOverlayKeyOpensOverlayAndFreesDefault(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	// With the overlay rebound to Ctrl-] (0x1d), the default Ctrl-^ (0x1e)
	// must pass through to the remote and 0x1d must open the overlay.
	input := []byte{'a', OverlayHotkey, byte(0x1d), 'q', 'b'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=3 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Now:      fixedClock(),
			Overlay:  OverlayOptions{Key: 0x1d, KeyName: "Ctrl-]"},
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if string(got) != "a"+string(OverlayHotkey)+"b" {
		t.Fatalf("remote input = %q, want default hotkey to pass through", string(got))
	}
	if !strings.Contains(stdout.String(), "ssherpa session map") {
		t.Fatalf("stdout = %q, want local overlay", stdout.String())
	}
	if !strings.Contains(stdout.String(), "Ctrl-]/q/Esc close") {
		t.Fatalf("stdout = %q, want overlay help to show custom key name", stdout.String())
	}
}

func TestRunSupervisedOverlaySendActionDoesNotReachRemote(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	input := []byte{OverlayHotkey, 's', 'z'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	requests := make(chan OverlayTransferRequest, 1)
	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=1 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod", Hops: []string{"bastion"}, Route: []string{"bastion", "prod"}},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Now:      fixedClock(),
			Overlay: OverlayOptions{
				Send: func(req OverlayTransferRequest) int {
					requests <- req
					return 0
				},
			},
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if string(got) != "z" {
		t.Fatalf("remote input = %q, want only post-send byte", string(got))
	}
	select {
	case req := <-requests:
		if req.Direction != "send" || req.TargetAlias != "prod" || !reflect.DeepEqual(req.Hops, []string{"bastion"}) {
			t.Fatalf("request = %#v, want send to prod through bastion", req)
		}
	case <-time.After(time.Second):
		t.Fatalf("overlay send callback was not invoked")
	}
	if !strings.Contains(stdout.String(), "s send") {
		t.Fatalf("stdout = %q, want send action in overlay help", stdout.String())
	}
}

func TestRunSupervisedOverlayReceiveActionDoesNotReachRemote(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	input := []byte{OverlayHotkey, 'v', 'z'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	requests := make(chan OverlayTransferRequest, 1)
	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=1 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod", Hops: []string{"bastion"}, Route: []string{"bastion", "prod"}},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Now:      fixedClock(),
			Overlay: OverlayOptions{
				Receive: func(req OverlayTransferRequest) int {
					requests <- req
					return 0
				},
			},
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if string(got) != "z" {
		t.Fatalf("remote input = %q, want only post-receive byte", string(got))
	}
	select {
	case req := <-requests:
		if req.Direction != "receive" || req.TargetAlias != "prod" || !reflect.DeepEqual(req.Hops, []string{"bastion"}) {
			t.Fatalf("request = %#v, want receive from prod through bastion", req)
		}
	case <-time.After(time.Second):
		t.Fatalf("overlay receive callback was not invoked")
	}
	if !strings.Contains(stdout.String(), "v receive") {
		t.Fatalf("stdout = %q, want receive action in overlay help", stdout.String())
	}
}

func TestRunSupervisedRecordingStartsAndPausesFromOverlay(t *testing.T) {
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	userPTY, userTTY, err := pty.Open()
	if err != nil {
		t.Fatalf("open user pty: %v", err)
	}
	defer userPTY.Close()
	defer userTTY.Close()
	stdout := newNotifyingBuffer("PRE")

	done := make(chan int, 1)
	go func() {
		done <- RunSupervised(
			sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo 2>/dev/null; printf PRE; dd bs=1 count=1 of=/dev/null 2>/dev/null; printf ON; dd bs=1 count=1 of=/dev/null 2>/dev/null; printf OFF; dd bs=1 count=1 of=/dev/null 2>/dev/null; printf RESUMED`}},
			Metadata{TargetAlias: "prod"},
			Options{
				StateDir: stateDir,
				Stdin:    userTTY,
				Stdout:   stdout,
				Stderr:   &stderr,
				Env:      []string{"PATH=" + os.Getenv("PATH")},
				Now:      fixedClock(),
			},
		)
	}()

	waitForBufferContains(t, stdout, "PRE", 3*time.Second, func() string { return stderr.String() })
	if _, err := userPTY.Write([]byte{OverlayHotkey, 'T'}); err != nil {
		t.Fatalf("write start recording keys: %v", err)
	}
	waitForBufferContains(t, stdout, "recording started", 3*time.Second, func() string { return stderr.String() })
	if _, err := userPTY.Write([]byte{'q', 'a'}); err != nil {
		t.Fatalf("write first remote byte: %v", err)
	}
	waitForBufferContains(t, stdout, "ON", 3*time.Second, func() string { return stderr.String() })

	if _, err := userPTY.Write([]byte{OverlayHotkey, 'T'}); err != nil {
		t.Fatalf("write pause recording keys: %v", err)
	}
	waitForBufferContains(t, stdout, "recording paused", 3*time.Second, func() string { return stderr.String() })
	if _, err := userPTY.Write([]byte{'q', 'b'}); err != nil {
		t.Fatalf("write paused remote byte: %v", err)
	}
	waitForBufferContains(t, stdout, "OFF", 3*time.Second, func() string { return stderr.String() })

	if _, err := userPTY.Write([]byte{OverlayHotkey, 'T'}); err != nil {
		t.Fatalf("write resume recording keys: %v", err)
	}
	waitForBufferContains(t, stdout, "recording resumed", 3*time.Second, func() string { return stderr.String() })
	if _, err := userPTY.Write([]byte{'q', 'c'}); err != nil {
		t.Fatalf("write resumed remote byte: %v", err)
	}

	var code int
	select {
	case code = <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("RunSupervised did not return; stderr=%q stdout=%q", stderr.String(), stdout.String())
	}
	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.Transcript == nil {
		t.Fatalf("record.Transcript is nil, want overlay-started transcript")
	}
	recording, err := transcript.Read(transcript.PathForRecord(stateDir, record))
	if err != nil {
		t.Fatalf("read transcript: %v", err)
	}
	output := transcriptOutput(recording)
	if strings.Contains(output, "PRE") {
		t.Fatalf("transcript output = %q, want pre-start output omitted", output)
	}
	if strings.Contains(output, "OFF") {
		t.Fatalf("transcript output = %q, want paused output omitted", output)
	}
	if !strings.Contains(output, "ON") || !strings.Contains(output, "RESUMED") {
		t.Fatalf("transcript output = %q, want active output ON and RESUMED", output)
	}
}

func TestOutputTapStopDetachesWithoutClosingChannel(t *testing.T) {
	tap := &outputTap{}
	ch, stop := tap.start(true)
	if !tap.observe([]byte("one")) {
		t.Fatalf("observe returned false, want suppression")
	}
	select {
	case got := <-ch:
		if string(got) != "one" {
			t.Fatalf("tap chunk = %q, want one", got)
		}
	default:
		t.Fatalf("tap did not receive observed chunk")
	}

	stop()
	if tap.observe([]byte("two")) {
		t.Fatalf("observe returned true after stop")
	}
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatalf("stop closed tap channel; this can race with the PTY reader")
		}
	default:
	}
}

func TestOverlayTransferRequestIncludesRemoteState(t *testing.T) {
	stateDir := t.TempDir()
	record := state.SessionRecord{
		ID:           "session-20260529T120000Z-abcdef",
		TargetAlias:  "prod",
		Hops:         []string{"bastion"},
		Route:        []string{"bastion", "prod"},
		ControlPath:  "/tmp/ssherpa.sock",
		RemoteHost:   "prod.example.com",
		RemoteCWD:    "/srv/app",
		RemotePrompt: state.RemotePromptPrompt,
	}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord returned error: %v", err)
	}

	req := overlayTransferRequest("send", stateDir, record.ID, OverlayTransferRequest{})
	if req.Direction != "send" || req.SessionID != record.ID || req.StateDir != stateDir {
		t.Fatalf("request identity = %#v", req)
	}
	if req.TargetAlias != record.TargetAlias || req.RemoteHost != record.RemoteHost || req.RemoteCWD != record.RemoteCWD || req.RemotePrompt != record.RemotePrompt {
		t.Fatalf("request remote state = %#v, want record values", req)
	}
	if req.ControlPath != record.ControlPath {
		t.Fatalf("request ControlPath = %q, want %q", req.ControlPath, record.ControlPath)
	}
	if !reflect.DeepEqual(req.Hops, record.Hops) || !reflect.DeepEqual(req.Route, record.Route) {
		t.Fatalf("request route = hops %#v route %#v, want %#v %#v", req.Hops, req.Route, record.Hops, record.Route)
	}
}

func TestRunSupervisedEscapeRopeTearsDownSession(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	// Open the overlay, press X, then confirm with a second X. The child would
	// otherwise block for 30s, so a prompt exit proves the rope tore it down.
	input := []byte{OverlayHotkey, 'X', 'X'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	done := make(chan int, 1)
	go func() {
		done <- RunSupervised(
			sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo 2>/dev/null; sleep 30`}},
			Metadata{TargetAlias: "prod"},
			Options{
				StateDir: stateDir,
				Stdin:    stdin,
				Stdout:   &stdout,
				Stderr:   &stderr,
				Env:      []string{"PATH=" + os.Getenv("PATH")},
				Now:      fixedClock(),
			},
		)
	}()

	var code int
	select {
	case code = <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("RunSupervised did not return after escape rope; stderr=%q", stderr.String())
	}

	if code != EscapeRopeExitCode {
		t.Fatalf("RunSupervised returned %d, want escape rope code %d; stderr=%q", code, EscapeRopeExitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), "escape rope pulled") {
		t.Fatalf("stderr = %q, want escape rope notice", stderr.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	if records[0].DisconnectReason != EscapeRopeReason {
		t.Fatalf("disconnect reason = %q, want %q", records[0].DisconnectReason, EscapeRopeReason)
	}
	if records[0].Status() != "exited" {
		t.Fatalf("record status = %q, want exited", records[0].Status())
	}
	hasEvent := false
	for _, ev := range records[0].Events {
		if ev.Type == EscapeRopeReason {
			hasEvent = true
		}
	}
	if !hasEvent {
		t.Fatalf("events = %#v, want an %q event", records[0].Events, EscapeRopeReason)
	}
}

func TestRunSupervisedEscapeRopePanicTapTearsDownSession(t *testing.T) {
	var stdout, stderr bytes.Buffer
	stateDir := t.TempDir()
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	// Three rapid hotkey presses pull the rope immediately, no confirm. From a
	// regular file these reads are instant, i.e. well within the panic window.
	input := []byte{OverlayHotkey, OverlayHotkey, OverlayHotkey}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	done := make(chan int, 1)
	go func() {
		done <- RunSupervised(
			sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo 2>/dev/null; sleep 30`}},
			Metadata{TargetAlias: "prod"},
			Options{StateDir: stateDir, Stdin: stdin, Stdout: &stdout, Stderr: &stderr, Env: []string{"PATH=" + os.Getenv("PATH")}, Now: fixedClock()},
		)
	}()

	select {
	case code := <-done:
		if code != EscapeRopeExitCode {
			t.Fatalf("RunSupervised returned %d, want escape rope code %d; stderr=%q", code, EscapeRopeExitCode, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("RunSupervised did not return after panic tap; stderr=%q", stderr.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].DisconnectReason != EscapeRopeReason {
		t.Fatalf("records = %#v, want one with escape_rope reason", records)
	}
}

func TestRunSupervisedEscapeRopeConfirmCancelKeepsSession(t *testing.T) {
	var stdout, stderr bytes.Buffer
	stateDir := t.TempDir()
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	// Open overlay, X (arm), q (cancel the confirm), q (close overlay), then z
	// is forwarded to the child, which reads one byte and exits normally. The
	// rope must NOT fire.
	input := []byte{OverlayHotkey, 'X', 'q', 'q', 'z'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	done := make(chan int, 1)
	go func() {
		done <- RunSupervised(
			sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo 2>/dev/null; dd bs=1 count=1 of=/dev/null 2>/dev/null`}},
			Metadata{TargetAlias: "prod"},
			Options{StateDir: stateDir, Stdin: stdin, Stdout: &stdout, Stderr: &stderr, Env: []string{"PATH=" + os.Getenv("PATH")}, Now: fixedClock()},
		)
	}()

	select {
	case code := <-done:
		if code == EscapeRopeExitCode {
			t.Fatalf("RunSupervised returned escape rope code after cancel; want normal exit")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("RunSupervised did not return after confirm cancel; stderr=%q", stderr.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].DisconnectReason != "" {
		t.Fatalf("records = %#v, want one with no disconnect reason", records)
	}
}

func TestRunSupervisedComposerSendsBufferedLine(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	input := []byte{'a', ComposerHotkey, 'l', 's', '\r', 'b'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=5 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if string(got) != "als\nb" {
		t.Fatalf("remote input = %q, want composed line between normal bytes", string(got))
	}
	if !strings.Contains(stdout.String(), "ssherpa composer") {
		t.Fatalf("stdout = %q, want local composer", stdout.String())
	}
}

func TestRunSupervisedComposerCancelDoesNotSendBuffer(t *testing.T) {
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	input := []byte{ComposerHotkey, 'n', 'o', 0x1b, 'z'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=1 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if string(got) != "z" {
		t.Fatalf("remote input = %q, want only post-cancel byte", string(got))
	}
}

func TestRunSupervisedComposerCanBeDisabled(t *testing.T) {
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	input := []byte{ComposerHotkey, 'x'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=2 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Composer: ComposerOptions{Disabled: true},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if !bytes.Equal(got, input) {
		t.Fatalf("remote input = %#v, want raw composer hotkey and byte %#v", got, input)
	}
}

func TestRunSupervisedComposerSupportsCustomHotkeyAndSendWithoutNewline(t *testing.T) {
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	outPath := filepath.Join(t.TempDir(), "remote-input")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	customHotkey := byte(0x12)
	input := []byte{customHotkey, 'p', 'w', 'd', ComposerSendHotkey, 'q'}
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `stty raw -echo; dd bs=1 count=4 of="$OUT" 2>/dev/null`}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "OUT=" + outPath},
			Composer: ComposerOptions{Hotkey: customHotkey, HotkeyName: "Ctrl-R"},
			Now:      fixedClock(),
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read remote input fixture: %v", err)
	}
	if string(got) != "pwdq" {
		t.Fatalf("remote input = %q, want composed bytes without newline plus post-send byte", string(got))
	}
}

func TestRunSupervisedLatencyWarningRecordsEventOutsideRemoteStream(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "sleep 0.05; printf 'remote stream'"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Watchdog: WatchdogOptions{
				WarnThreshold: time.Millisecond,
				Interval:      time.Hour,
				ProbeCommand:  sshcmd.Command{Argv: []string{"ssh", "prod", "true"}},
				RunProbe: func(context.Context, sshcmd.Command) ProbeResult {
					return ProbeResult{Duration: 50 * time.Millisecond}
				},
			},
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "remote stream") {
		t.Fatalf("stdout = %q, want remote output", stdout.String())
	}
	if strings.Contains(stdout.String(), "latency warning") {
		t.Fatalf("stdout = %q, latency warning leaked into remote stream", stdout.String())
	}
	if !strings.Contains(stderr.String(), "latency warning") {
		t.Fatalf("stderr = %q, want local latency warning", stderr.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	if got := sessionEventTypes(records[0]); strings.Join(got, ",") != "latency_warning" {
		t.Fatalf("event types = %#v, want latency_warning", got)
	}
	if records[0].DisconnectReason != "" {
		t.Fatalf("disconnect reason = %q, want empty", records[0].DisconnectReason)
	}
}

func TestRunSupervisedLatencyWarningDoesNotDisconnectByDefault(t *testing.T) {
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "sleep 0.04; exit 0"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Watchdog: WatchdogOptions{
				WarnThreshold: time.Millisecond,
				Interval:      time.Hour,
				ProbeCommand:  sshcmd.Command{Argv: []string{"ssh", "prod", "true"}},
				RunProbe: func(context.Context, sshcmd.Command) ProbeResult {
					return ProbeResult{Duration: 50 * time.Millisecond}
				},
			},
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderr.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 || records[0].ExitCode == nil || *records[0].ExitCode != 0 {
		t.Fatalf("record lifecycle = %#v, want normal exit 0", records)
	}
	if records[0].DisconnectReason != "" {
		t.Fatalf("disconnect reason = %q, want empty", records[0].DisconnectReason)
	}
}

func TestRunSupervisedLatencyDisconnectRecordsReason(t *testing.T) {
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sleep", "5"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Watchdog: WatchdogOptions{
				WarnThreshold:   time.Millisecond,
				DisconnectAfter: 10 * time.Millisecond,
				Interval:        5 * time.Millisecond,
				ProbeCommand:    sshcmd.Command{Argv: []string{"ssh", "prod", "true"}},
				RunProbe: func(context.Context, sshcmd.Command) ProbeResult {
					return ProbeResult{Duration: 50 * time.Millisecond}
				},
			},
		},
	)

	if code == 0 {
		t.Fatalf("RunSupervised returned %d, want non-zero disconnect; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "latency disconnect") {
		t.Fatalf("stderr = %q, want latency disconnect notice", stderr.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	if !strings.Contains(records[0].DisconnectReason, "latency unhealthy") {
		t.Fatalf("disconnect reason = %q, want latency unhealthy", records[0].DisconnectReason)
	}
	if got := strings.Join(sessionEventTypes(records[0]), ","); got != "latency_warning,latency_disconnect" {
		t.Fatalf("event types = %q, want warning and disconnect", got)
	}
}

func TestRunSupervisedRejectsEmptyCommand(t *testing.T) {
	var stderr bytes.Buffer

	code := RunSupervised(sshcmd.Command{}, Metadata{}, Options{StateDir: t.TempDir(), Stderr: &stderr})

	if code != 1 {
		t.Fatalf("RunSupervised returned %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "empty SSH command") {
		t.Fatalf("stderr = %q, want empty command error", stderr.String())
	}
}

func TestRunSupervisedRejectsBadStateDir(t *testing.T) {
	var stderr bytes.Buffer
	filePath := filepath.Join(t.TempDir(), "state-file")
	if err := os.WriteFile(filePath, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "exit 0"}},
		Metadata{TargetAlias: "prod"},
		Options{StateDir: filePath, Stdin: stdin, Stderr: &stderr, Env: []string{"PATH=" + os.Getenv("PATH")}},
	)

	if code != 1 {
		t.Fatalf("RunSupervised returned %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "write session record") {
		t.Fatalf("stderr = %q, want state write error", stderr.String())
	}
}

// panicWriter panics on first write; used to drive a panic through the
// supervisor's own goroutine (telemetry emit) or a child goroutine
// (output copy), depending on where the first write lands.
type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) {
	panic("writer exploded")
}

// TestRunSupervisedPanicFinalizesRecordAndRepanics drives a panic
// through RunSupervised's own goroutine: SSHERPA_SESSION_ID marks the
// session as nested, so the supervisor emits telemetry to stdout from
// its own call stack at attempt start, and a panicking stdout writer
// unwinds the supervisor itself. The top-level recover must finalize
// the record with a panic disconnect reason and then re-panic so the
// bug stays visible.
func TestRunSupervisedPanicFinalizesRecordAndRepanics(t *testing.T) {
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("RunSupervised swallowed the panic instead of re-panicking")
		}
		records, err := state.ListRecords(stateDir)
		if err != nil {
			t.Fatalf("ListRecords returned error: %v", err)
		}
		if len(records) != 1 {
			t.Fatalf("records = %#v, want one", records)
		}
		record := records[0]
		if record.DisconnectReason != "panic" {
			t.Fatalf("DisconnectReason = %q, want panic", record.DisconnectReason)
		}
		if record.EndedAt == nil {
			t.Fatalf("record not finalized after panic: %#v", record)
		}
		if !strings.Contains(strings.Join(sessionEventTypes(record), ","), "panic") {
			t.Fatalf("events = %#v, want panic event", record.Events)
		}
	}()

	RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "exit 0"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   panicWriter{},
			Stderr:   io.Discard,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "SSHERPA_SESSION_ID=parent-id", "SSHERPA_DEPTH=0"},
			Now:      fixedClock(),
		},
	)
	t.Fatalf("RunSupervised returned instead of re-panicking")
}

// TestRunSupervisedChildGoroutinePanicFinalizesInsteadOfCrashing
// panics inside the latency watchdog goroutine. Before the recover
// wrappers this crashed the whole process with the terminal still in
// raw mode; now the supervisor must tear the session down, finalize
// the record with the panic reason, and return an error exit.
func TestRunSupervisedChildGoroutinePanicFinalizesInsteadOfCrashing(t *testing.T) {
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()
	stderr := newNotifyingBuffer("panicked")

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "sleep 5"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   io.Discard,
			Stderr:   stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Now:      fixedClock(),
			Watchdog: WatchdogOptions{
				WarnThreshold: time.Millisecond,
				Interval:      time.Hour,
				ProbeCommand:  sshcmd.Command{Argv: []string{"probe"}},
				RunProbe: func(context.Context, sshcmd.Command) ProbeResult {
					panic("probe exploded")
				},
			},
		},
	)

	if code != 1 {
		t.Fatalf("RunSupervised returned %d, want 1 after child-goroutine panic; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "latency watchdog panicked") {
		t.Fatalf("stderr = %q, want panic report", stderr.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.DisconnectReason != "panic" || record.EndedAt == nil {
		t.Fatalf("record = %#v, want finalized with panic reason", record)
	}
}

// TestRunSupervisedSignalDuringBackoffFinalizesRecord covers the
// reconnect backoff window: forwardSignals is only installed while an
// attempt's ssh process is alive, so a SIGTERM during the inter-attempt
// sleep used to kill the supervisor with no finalization (`forward
// stop` reported success and left a permanent orphan record).
func TestRunSupervisedSignalDuringBackoffFinalizesRecord(t *testing.T) {
	stateDir := t.TempDir()
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()
	stderr := newNotifyingBuffer("retrying in")

	codeCh := make(chan int, 1)
	go func() {
		codeCh <- RunSupervised(
			sshcmd.Command{Argv: []string{"sh", "-c", "exit 255"}},
			Metadata{TargetAlias: "tun", Kind: state.KindTunnel},
			Options{
				StateDir: stateDir,
				Stdin:    stdin,
				Stdout:   io.Discard,
				Stderr:   stderr,
				Env:      []string{"PATH=" + os.Getenv("PATH")},
				Now:      fixedClock(),
				Detached: true,
				Reconnect: ReconnectOptions{
					Enabled:        true,
					MaxAttempts:    5,
					InitialBackoff: 30 * time.Second,
					MaxBackoff:     30 * time.Second,
				},
			},
		)
	}()

	waitForBufferContains(t, stderr, "retrying in", 5*time.Second, nil)
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("self SIGTERM: %v", err)
	}

	select {
	case code := <-codeCh:
		if code != EscapeRopeExitCode {
			t.Fatalf("RunSupervised returned %d, want %d; stderr=%q", code, EscapeRopeExitCode, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("supervisor did not exit within 5s after SIGTERM during backoff")
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.EndedAt == nil || record.ExitCode == nil || *record.ExitCode != EscapeRopeExitCode {
		t.Fatalf("record = %#v, want finalized with escape-rope exit", record)
	}
	if record.DisconnectReason != EscapeRopeReason {
		t.Fatalf("DisconnectReason = %q, want %q", record.DisconnectReason, EscapeRopeReason)
	}
}

// TestRunSupervisedControlMasterTeardownIssuesControlExit checks that
// session teardown asks the multiplexing master to exit (ssh -O exit)
// before unlinking the socket. Unlinking a live socket orphans the
// authenticated master — and its held forward ports — for the full
// ControlPersist window.
func TestRunSupervisedControlMasterTeardownIssuesControlExit(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	binDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "control-exit-args")
	script := `#!/bin/sh
if [ "$1" = "-O" ]; then
  printf '%s\n' "$*" > "$SSHERPA_TEST_CM_MARKER"
  exit 0
fi
for arg in "$@"; do
  case "$arg" in
    ControlPath=*) : > "${arg#ControlPath=}" ;;
  esac
done
exit 0
`
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("SSHERPA_TEST_CM_MARKER", marker)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()
	var stdout bytes.Buffer
	var stderrBuf bytes.Buffer

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"ssh", "prod"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderrBuf,
			Env:      []string{"PATH=" + os.Getenv("PATH"), "SSHERPA_TEST_CM_MARKER=" + marker},
			Now:      fixedClock(),
		},
	)
	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderrBuf.String())
	}

	records, err := state.ListRecords(stateDir)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %#v err=%v, want one", records, err)
	}
	controlPath := records[0].ControlPath
	if controlPath == "" {
		t.Fatalf("record has no ControlPath: %#v", records[0])
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ssh -O exit was not invoked before socket removal: %v", err)
	}
	args := strings.TrimSpace(string(data))
	if !strings.Contains(args, "-O exit") || !strings.Contains(args, "ControlPath="+controlPath) || !strings.HasSuffix(args, "prod") {
		t.Fatalf("ssh -O exit args = %q, want -O exit with ControlPath and destination", args)
	}
	if _, err := os.Stat(controlPath); !os.IsNotExist(err) {
		t.Fatalf("control socket stat err = %v, want removed", err)
	}
}

func TestRunSupervisedControlMasterTeardownUsesResolvedBinary(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	// The custom ssh binary is a full path NOT on PATH, and its basename
	// must be "ssh" so WithControlMaster injects the multiplexing options.
	// A hardcoded bare "ssh" teardown could not find it.
	binDir := t.TempDir()
	customSSH := filepath.Join(binDir, "ssh")
	marker := filepath.Join(t.TempDir(), "control-exit-args")
	script := `#!/bin/sh
if [ "$1" = "-O" ]; then
  printf '%s\n' "$*" > "$SSHERPA_TEST_CM_MARKER"
  exit 0
fi
for arg in "$@"; do
  case "$arg" in
    ControlPath=*) : > "${arg#ControlPath=}" ;;
  esac
done
exit 0
`
	if err := os.WriteFile(customSSH, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	t.Setenv("SSHERPA_TEST_CM_MARKER", marker)
	// Deliberately do NOT add binDir to PATH: a bare "ssh" must not
	// resolve to this binary, so only a teardown using the resolved
	// full path can reach the master.
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()
	var stdout, stderrBuf bytes.Buffer

	code := RunSupervised(
		sshcmd.Command{Argv: []string{customSSH, "prod"}},
		Metadata{TargetAlias: "prod"},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderrBuf,
			Env:      []string{"PATH=/nonexistent", "SSHERPA_TEST_CM_MARKER=" + marker},
			Now:      fixedClock(),
		},
	)
	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q", code, stderrBuf.String())
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("ssh -O exit was not invoked via the resolved binary: %v", err)
	}
	if !strings.Contains(string(data), "-O exit") {
		t.Fatalf("ssh -O exit args = %q", strings.TrimSpace(string(data)))
	}
}

func TestPrepareControlMasterPrefersXDGRuntimeDir(t *testing.T) {
	runtimeDir := t.TempDir()
	path, ok, err := prepareControlMaster("/state", "rec-1", Metadata{}, []string{"XDG_RUNTIME_DIR=" + runtimeDir})
	if err != nil || !ok {
		t.Fatalf("prepareControlMaster = %q ok=%v err=%v, want socket path", path, ok, err)
	}
	wantDir := filepath.Join(runtimeDir, "ssherpa", "cm")
	if filepath.Dir(path) != wantDir {
		t.Fatalf("socket dir = %q, want %q", filepath.Dir(path), wantDir)
	}
	info, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("stat socket dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("socket dir perm = %o, want 0700", perm)
	}
}

func TestPrepareControlMasterTmpFallbackRefusesSymlinkedDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	base := filepath.Join(tmp, fmt.Sprintf("ssherpa-%d", os.Getuid()))
	if err := os.Symlink(t.TempDir(), base); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	_, _, err := prepareControlMaster("/state", "rec-1", Metadata{}, []string{"PATH=/usr/bin"})
	if err == nil {
		t.Fatalf("prepareControlMaster accepted a symlinked socket dir")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("error = %v, want symlink refusal", err)
	}
}

func TestPrepareControlMasterTmpFallbackTightensLoosePermissions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	base := filepath.Join(tmp, fmt.Sprintf("ssherpa-%d", os.Getuid()))
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.Chmod(base, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	if _, _, err := prepareControlMaster("/state", "rec-1", Metadata{}, []string{"PATH=/usr/bin"}); err != nil {
		t.Fatalf("prepareControlMaster returned error: %v", err)
	}
	info, err := os.Stat(base)
	if err != nil {
		t.Fatalf("stat base dir: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("base dir perm = %o, want tightened to 0700", perm)
	}
}

// TestMirrorRemoteSessionRecordNotesAcceptEnvDegradationOnce: the
// telemetry-backfill branch firing with an empty parent_id is positive
// proof the remote sshd stripped SSHERPA_*; the parent record gets one
// (and only one) explanatory event.
func TestMirrorRemoteSessionRecordNotesAcceptEnvDegradationOnce(t *testing.T) {
	stateDir := t.TempDir()
	now := fixedClock()
	parent := state.SessionRecord{
		ID:           "parent",
		TargetAlias:  "prod",
		Route:        []string{"prod"},
		OriginHost:   "laptop",
		StartedAt:    now().UTC(),
		LocalPID:     os.Getpid(),
		RunnerMode:   RunnerModeSupervised,
		StateVersion: state.StateVersion,
	}
	var recordMu sync.Mutex
	ac := attemptContext{stateDir: stateDir, record: &parent, recordMu: &recordMu, now: now}

	mirrorRemoteSessionRecord(ac, state.SessionRecord{ID: "child-1", TargetAlias: "inner", StartedAt: now().UTC()})
	mirrorRemoteSessionRecord(ac, state.SessionRecord{ID: "child-2", TargetAlias: "inner-2", StartedAt: now().UTC()})

	count := 0
	for _, event := range parent.Events {
		if event.Type == "nested_metadata_blocked" {
			count++
			if !strings.Contains(event.Message, "AcceptEnv SSHERPA_*") {
				t.Fatalf("event message = %q, want AcceptEnv hint", event.Message)
			}
		}
	}
	if count != 1 {
		t.Fatalf("nested_metadata_blocked events = %d, want exactly one; events=%#v", count, parent.Events)
	}
	persisted, err := state.ReadRecord(stateDir, "parent")
	if err != nil {
		t.Fatalf("ReadRecord(parent) returned error: %v", err)
	}
	if !strings.Contains(strings.Join(sessionEventTypes(persisted), ","), "nested_metadata_blocked") {
		t.Fatalf("persisted parent events = %#v, want nested_metadata_blocked", persisted.Events)
	}

	// A descendant that did arrive with lineage intact must not trigger
	// the degradation note.
	otherDir := t.TempDir()
	healthyParent := state.SessionRecord{ID: "parent", StartedAt: now().UTC(), StateVersion: state.StateVersion}
	var otherMu sync.Mutex
	healthyAC := attemptContext{stateDir: otherDir, record: &healthyParent, recordMu: &otherMu, now: now}
	mirrorRemoteSessionRecord(healthyAC, state.SessionRecord{ID: "child", ParentID: "parent", StartedAt: now().UTC()})
	if len(healthyParent.Events) != 0 {
		t.Fatalf("healthy lineage produced events: %#v", healthyParent.Events)
	}
}

func TestSessionRecorderPersistsStopReason(t *testing.T) {
	stateDir := t.TempDir()
	now := fixedClock()
	record := state.SessionRecord{
		ID:           "stop-reason",
		StartedAt:    now().UTC(),
		LocalPID:     os.Getpid(),
		RunnerMode:   RunnerModeSupervised,
		StateVersion: state.StateVersion,
	}
	var recordMu sync.Mutex
	recorder := newSessionRecorder(sessionRecorderOptions{
		StateDir: stateDir,
		Command:  sshcmd.Command{Argv: []string{"ssh", "prod"}},
		MaxBytes: 256,
		Record:   &record,
		RecordMu: &recordMu,
		Now:      now,
	})

	if _, err := recorder.Toggle(); err != nil {
		t.Fatalf("Toggle returned error: %v", err)
	}
	recorder.WriteOutput(now().UTC(), bytes.Repeat([]byte("x"), 4096))
	if err := recorder.Close(now().UTC()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	persisted, err := state.ReadRecord(stateDir, "stop-reason")
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if persisted.Transcript == nil {
		t.Fatalf("record.Transcript = nil, want spec with stop reason")
	}
	if persisted.Transcript.StopReason != "size limit reached" {
		t.Fatalf("Transcript.StopReason = %q, want size limit reached", persisted.Transcript.StopReason)
	}
}

func TestTerminalGuardRestoreRunsOncePerArmAcrossGoroutines(t *testing.T) {
	var calls atomic.Int32
	guard := newTerminalGuard()
	guard.arm(func() { calls.Add(1) })

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			guard.restore()
		}()
	}
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("restore ran %d times, want once", got)
	}

	guard.arm(func() { calls.Add(1) })
	guard.restore()
	guard.restore()
	if got := calls.Load(); got != 2 {
		t.Fatalf("restore after re-arm ran %d total times, want 2", got)
	}
}

func waitForSessionRecordCount(t *testing.T, stateDir string, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last []state.SessionRecord
	for time.Now().Before(deadline) {
		records, err := state.ListRecords(stateDir)
		if err == nil {
			last = records
			if len(records) == want {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("session record count did not reach %d within %s; last=%#v", want, timeout, last)
}

type notifyingBuffer struct {
	mu       sync.Mutex
	buf      bytes.Buffer
	needle   string
	notified chan struct{}
	once     sync.Once
}

func newNotifyingBuffer(needle string) *notifyingBuffer {
	return &notifyingBuffer{needle: needle, notified: make(chan struct{})}
}

func (b *notifyingBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n, err := b.buf.Write(p)
	if strings.Contains(b.buf.String(), b.needle) {
		b.once.Do(func() { close(b.notified) })
	}
	return n, err
}

func (b *notifyingBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func waitForBufferContains(t *testing.T, b *notifyingBuffer, needle string, timeout time.Duration, detail func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(b.String(), needle) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	extra := ""
	if detail != nil {
		extra = "; " + detail()
	}
	t.Fatalf("buffer did not contain %q within %s%s; stdout=%q", needle, timeout, extra, b.String())
}

// fixedClock returns a deterministic test clock. Options.Now is called
// from several supervisor goroutines (watchdog, signal handlers), so
// the closure must be safe for concurrent use.
func fixedClock() func() time.Time {
	var mu sync.Mutex
	current := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(time.Second)
		return current
	}
}

func sessionEventTypes(record state.SessionRecord) []string {
	types := make([]string, 0, len(record.Events))
	for _, event := range record.Events {
		types = append(types, event.Type)
	}
	return types
}

// failingTranscriptWriter is a transcriptWriter whose Close fails the
// way a dying disk would, after having recorded a stop reason.
type failingTranscriptWriter struct {
	spec state.TranscriptSpec
	err  error
}

func (w *failingTranscriptWriter) WriteOutput(time.Time, []byte) {}
func (w *failingTranscriptWriter) WriteMarker(time.Time, string) {}
func (w *failingTranscriptWriter) StopReason() string            { return "write error: disk full" }
func (w *failingTranscriptWriter) Close(ended time.Time) (state.TranscriptSpec, error) {
	spec := w.spec
	endedUTC := ended.UTC()
	spec.EndedAt = &endedUTC
	return spec, w.err
}

// TestSessionRecorderClosePersistsRecordWhenWriterCloseFails pins the
// persist-before-propagate contract of sessionRecorder.Close: a close
// error (failing disk) is exactly the scenario in which stop_reason and
// ended_at must still reach the session record, and the error must
// still surface to the caller.
func TestSessionRecorderClosePersistsRecordWhenWriterCloseFails(t *testing.T) {
	stateDir := t.TempDir()
	record := state.SessionRecord{ID: "rec-close", TargetAlias: "prod", StartedAt: time.Now().UTC()}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("write record: %v", err)
	}
	closeErr := errors.New("close transcript: disk full")
	recorder := &sessionRecorder{
		stateDir: stateDir,
		record:   &record,
		recordMu: &sync.Mutex{},
		now:      time.Now,
		active:   true,
		writer: &failingTranscriptWriter{
			spec: state.TranscriptSpec{
				Path:      transcript.Path(stateDir, record.ID),
				Format:    transcript.FormatAsciicast,
				StartedAt: record.StartedAt,
			},
			err: closeErr,
		},
	}
	ended := time.Date(2026, 5, 24, 12, 30, 0, 0, time.UTC)

	err := recorder.Close(ended)

	if !errors.Is(err, closeErr) {
		t.Fatalf("Close error = %v, want propagated %v", err, closeErr)
	}
	stored, readErr := state.ReadRecord(stateDir, record.ID)
	if readErr != nil {
		t.Fatalf("read record: %v", readErr)
	}
	if stored.Transcript == nil {
		t.Fatalf("record.Transcript is nil after failing close, want persisted spec")
	}
	if stored.Transcript.StopReason != "write error: disk full" {
		t.Fatalf("StopReason = %q, want writer's stop reason persisted", stored.Transcript.StopReason)
	}
	if stored.Transcript.EndedAt == nil || !stored.Transcript.EndedAt.Equal(ended) {
		t.Fatalf("EndedAt = %v, want %v persisted despite the close error", stored.Transcript.EndedAt, ended)
	}
}

func transcriptOutput(recording transcript.Recording) string {
	var out strings.Builder
	for _, frame := range recording.Frames {
		if frame.Stream == "o" {
			out.WriteString(frame.Data)
		}
	}
	return out.String()
}
