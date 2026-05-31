package session

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
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
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open dev null: %v", err)
	}
	defer stdin.Close()

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", "printf '%s/%s/%s' \"$SSHERPA_SESSION_ID\" \"$SSHERPA_DEPTH\" \"$SSHERPA_ROUTE\""}},
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
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want substring %q", stdout.String(), want)
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

	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-c", `printf '%s' "$SSHERPA_TEST_TELEMETRY"`}},
		Metadata{TargetAlias: "bastion", Route: []string{"bastion"}},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env: []string{
				"PATH=" + os.Getenv("PATH"),
				"SSHERPA_TEST_TELEMETRY=" + string(payload),
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

	if got, ok := remoteMirrorRecord(parent, child); ok {
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

	got, ok := remoteMirrorRecord(parent, child)
	if !ok {
		t.Fatalf("remoteMirrorRecord rejected parentless telemetry")
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

	stop := forwardSignals(nil, nil, cmd, pullRope)
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

func TestRunSupervisedOverlayInbandSendWritesRemoteFile(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	stateDir := t.TempDir()
	localPath := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(localPath, []byte("hello over pty"), 0o600); err != nil {
		t.Fatalf("write local payload: %v", err)
	}
	remotePath := filepath.Join(t.TempDir(), "remote payload.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin")
	input := append([]byte{OverlayHotkey, 's'}, []byte("exit\n")...)
	if err := os.WriteFile(stdinPath, input, 0o600); err != nil {
		t.Fatalf("write stdin fixture: %v", err)
	}
	stdin, err := os.Open(stdinPath)
	if err != nil {
		t.Fatalf("open stdin fixture: %v", err)
	}
	defer stdin.Close()

	results := make(chan InbandSendResult, 1)
	callbackErrs := make(chan string, 1)
	code := RunSupervised(
		sshcmd.Command{Argv: []string{"sh", "-i"}},
		Metadata{TargetAlias: "prod", Route: []string{"prod"}},
		Options{
			StateDir: stateDir,
			Stdin:    stdin,
			Stdout:   &stdout,
			Stderr:   &stderr,
			Env:      []string{"PATH=" + os.Getenv("PATH")},
			Now:      fixedClock(),
			Overlay: OverlayOptions{
				Send: func(req OverlayTransferRequest) int {
					if req.InbandSend == nil {
						callbackErrs <- "InbandSend is nil"
						return 1
					}
					result, err := req.InbandSend(InbandSendRequest{LocalPath: localPath, RemotePath: remotePath})
					if err != nil {
						callbackErrs <- "InbandSend returned error: " + err.Error()
						return 1
					}
					results <- result
					return 0
				},
			},
		},
	)

	if code != 0 {
		t.Fatalf("RunSupervised returned %d, want 0; stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	select {
	case err := <-callbackErrs:
		t.Fatalf("overlay callback error: %s; stderr=%q stdout=%q", err, stderr.String(), stdout.String())
	default:
	}
	got, err := os.ReadFile(remotePath)
	if err != nil {
		t.Fatalf("read remote payload: %v", err)
	}
	if string(got) != "hello over pty" {
		t.Fatalf("remote payload = %q", got)
	}
	select {
	case result := <-results:
		if result.RemotePath != remotePath || result.Size != int64(len("hello over pty")) || result.SHA256 == "" {
			t.Fatalf("result = %#v", result)
		}
	case <-time.After(time.Second):
		t.Fatalf("in-band result was not reported")
	}
	if strings.Contains(stdout.String(), "aGVsbG8") {
		t.Fatalf("stdout leaked base64 payload: %q", stdout.String())
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

func fixedClock() func() time.Time {
	current := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	return func() time.Time {
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
