package fsutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAtomicWriteFileCreatesBackupAndPreservesMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("Host old\n"), 0o640); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}

	result, err := AtomicWriteFile(path, []byte("Host new\n"), WriteOptions{
		Backup: true,
		Now: func() time.Time {
			return time.Date(2026, 5, 24, 19, 45, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("AtomicWriteFile returned error: %v", err)
	}
	if !result.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if result.BackupPath == "" {
		t.Fatalf("BackupPath is empty")
	}
	assertFile(t, path, "Host new\n")
	assertFile(t, result.BackupPath, "Host old\n")

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat returned error: %v", err)
	}
	if got := stat.Mode().Perm(); got != 0o640 {
		t.Fatalf("mode = %#o, want 0640", got)
	}
	if !strings.Contains(result.Diff, "-Host old") || !strings.Contains(result.Diff, "+Host new") {
		t.Fatalf("diff = %q, want old and new lines", result.Diff)
	}
}

func TestAtomicWriteFileDryRunDoesNotWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte("Host old\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}

	result, err := AtomicWriteFile(path, []byte("Host new\n"), WriteOptions{DryRun: true, Backup: true})
	if err != nil {
		t.Fatalf("AtomicWriteFile returned error: %v", err)
	}
	if !result.Changed || !result.DryRun {
		t.Fatalf("result = %#v, want changed dry-run", result)
	}
	if result.BackupPath != "" {
		t.Fatalf("BackupPath = %q, want empty for dry-run", result.BackupPath)
	}
	assertFile(t, path, "Host old\n")
}

func TestAtomicWriteFileCreatesParentAndNewFileMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "config")

	result, err := AtomicWriteFile(path, []byte("Host new\n"), WriteOptions{Backup: true})
	if err != nil {
		t.Fatalf("AtomicWriteFile returned error: %v", err)
	}
	if !result.Changed || result.BackupPath != "" {
		t.Fatalf("result = %#v, want changed without backup for new file", result)
	}
	assertFile(t, path, "Host new\n")

	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat returned error: %v", err)
	}
	if got := stat.Mode().Perm(); got != DefaultFileMode {
		t.Fatalf("mode = %#o, want %#o", got, os.FileMode(DefaultFileMode))
	}
}

func assertFile(t *testing.T, path string, want string) {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) returned error: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, string(data), want)
	}
}
