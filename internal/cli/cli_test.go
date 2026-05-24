package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{"version"}, &stdout, &stderr, BuildInfo{
		Version: "1.2.3",
		Commit:  "abc123",
		Date:    "2026-05-24T23:59:00Z",
	})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	want := "ssherpa 1.2.3\ncommit: abc123\nbuilt: 2026-05-24T23:59:00Z\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunVersionDefaults(t *testing.T) {
	var stdout bytes.Buffer

	code := Run([]string{"--version"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}

	want := "ssherpa dev\ncommit: none\nbuilt: unknown\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunHelpForNoArgs(t *testing.T) {
	var stdout bytes.Buffer

	code := Run(nil, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "Usage:")
	assertContains(t, stdout.String(), "Phase 0:")
}

func TestRunHelpCommand(t *testing.T) {
	var stdout bytes.Buffer

	code := Run([]string{"help"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "Available Commands:")
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{"list"}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	assertContains(t, stderr.String(), `unknown command or flag "list"`)
	assertContains(t, stderr.String(), "Usage:")
}

func TestRunRejectsExtraVersionArgs(t *testing.T) {
	var stderr bytes.Buffer

	code := Run([]string{"version", "extra"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "version does not accept arguments: extra")
}

func assertContains(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want substring %q", got, want)
	}
}
