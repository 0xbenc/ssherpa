package session

import (
	"bytes"
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
