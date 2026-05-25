package session

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
