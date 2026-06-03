package incoming

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseWhoFiltersLocalTTYAndExtractsHost(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.Local)
	got := ParseWho(strings.Join([]string{
		"ben      pts/2        2026-06-03 10:15 (192.168.1.50)",
		"ben      tty1         2026-06-03 09:00",
		"alice    pts/10       Jun  2 23:59 (vpn.example.com)",
	}, "\n"), now)

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2; got=%+v", len(got), got)
	}
	if got[0].TTY != "pts/2" || got[0].ClientIP != "192.168.1.50" {
		t.Fatalf("got[0] = %+v, want pts/2 with client IP", got[0])
	}
	if got[1].TTY != "pts/10" || got[1].Host != "vpn.example.com" {
		t.Fatalf("got[1] = %+v, want host preserved", got[1])
	}
}

func TestMarkerFromEnvCapturesSSHerpaMetadata(t *testing.T) {
	now := time.Date(2026, 6, 3, 10, 0, 0, 0, time.UTC)
	got := MarkerFromEnv([]string{
		"USER=ben",
		"SSH_TTY=/dev/pts/2",
		"SSH_CLIENT=192.168.1.50 51234 22",
		"SSHERPA_SESSION_ID=session-1",
		"SSHERPA_PARENT_SESSION_ID=parent-1",
		"SSHERPA_DEPTH=3",
		"SSHERPA_ROUTE=laptop,bastion,prod",
		"SSHERPA_ORIGIN_HOST=laptop",
	}, 1234, now)

	if got.User != "ben" || got.TTY != "pts/2" || got.ClientIP != "192.168.1.50" {
		t.Fatalf("basic marker fields = %+v", got)
	}
	if got.SSHerpaSessionID != "session-1" || got.ParentSessionID != "parent-1" || got.Depth != 3 {
		t.Fatalf("ssherpa marker fields = %+v", got)
	}
	if strings.Join(got.Route, ",") != "laptop,bastion,prod" || got.OriginHost != "laptop" {
		t.Fatalf("route/origin = %+v", got)
	}
}

func TestListEnrichesWhoRowsWithLiveSSHerpaMarkers(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.Local)
	marker := Marker{
		User:             "ben",
		TTY:              "/dev/pts/2",
		SSHClient:        "192.168.1.50 51234 22",
		ClientIP:         "192.168.1.50",
		CreatedAt:        now.Add(-2 * time.Minute),
		MarkerPID:        444,
		ParentPID:        333,
		SSHerpaSessionID: "session-1",
		Depth:            2,
		Route:            []string{"laptop", "prod"},
		OriginHost:       "laptop",
	}
	if _, err := WriteMarker(marker, Options{RuntimeDir: dir, Now: func() time.Time { return now }}); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}

	got, err := List(Options{
		RuntimeDir: dir,
		Now:        func() time.Time { return now },
		RunWho: func() ([]byte, error) {
			return []byte("ben pts/2 2026-06-03 11:58 (192.168.1.50)\n"), nil
		},
		PidAlive: func(pid int) bool { return pid == 444 },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1; got=%+v", len(got), got)
	}
	if !got[0].SSHerpa || got[0].Kind != "ssherpa" || got[0].SSHerpaSessionID != "session-1" {
		t.Fatalf("got[0] = %+v, want ssherpa-enriched incoming session", got[0])
	}
	if got[0].MarkerPID != 444 || got[0].ParentPID != 333 || strings.Join(got[0].Route, ",") != "laptop,prod" {
		t.Fatalf("got[0] marker projection = %+v", got[0])
	}
}

func TestListIgnoresDeadOrMismatchedMarkers(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.Local)
	for _, marker := range []Marker{
		{
			User:             "ben",
			TTY:              "pts/2",
			ClientIP:         "192.168.1.50",
			CreatedAt:        now.Add(-time.Minute),
			MarkerPID:        444,
			SSHerpaSessionID: "dead",
		},
		{
			User:             "ben",
			TTY:              "pts/2",
			ClientIP:         "10.0.0.9",
			CreatedAt:        now.Add(-30 * time.Second),
			MarkerPID:        555,
			SSHerpaSessionID: "wrong-ip",
		},
	} {
		if _, err := WriteMarker(marker, Options{RuntimeDir: dir, Now: func() time.Time { return now }}); err != nil {
			t.Fatalf("WriteMarker: %v", err)
		}
	}

	got, err := List(Options{
		RuntimeDir: dir,
		Now:        func() time.Time { return now },
		RunWho: func() ([]byte, error) {
			return []byte("ben pts/2 2026-06-03 11:58 (192.168.1.50)\n"), nil
		},
		PidAlive: func(pid int) bool { return pid == 555 },
	})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].SSHerpa || got[0].Kind != "ssh" {
		t.Fatalf("got = %+v, want plain ssh row", got)
	}
}

func TestRuntimeDirHonorsEnvironment(t *testing.T) {
	dir, err := RuntimeDir(Options{Env: []string{"SSHERPA_INCOMING_DIR=/tmp/custom-incoming"}})
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	if dir != filepath.Clean("/tmp/custom-incoming") {
		t.Fatalf("dir = %q", dir)
	}

	dir, err = RuntimeDir(Options{Env: []string{"XDG_RUNTIME_DIR=/tmp/runtime"}})
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	if dir != filepath.Join("/tmp/runtime", "ssherpa", "incoming") {
		t.Fatalf("dir = %q", dir)
	}
}

func TestShellHook(t *testing.T) {
	hook, err := ShellHook("zsh")
	if err != nil {
		t.Fatalf("ShellHook zsh: %v", err)
	}
	if !strings.Contains(hook, "ssherpa incoming mark --watch-parent") || !strings.Contains(hook, "$SSH_TTY") {
		t.Fatalf("zsh hook = %q", hook)
	}

	fish, err := ShellHook("fish")
	if err != nil {
		t.Fatalf("ShellHook fish: %v", err)
	}
	if !strings.Contains(fish, "$fish_pid") {
		t.Fatalf("fish hook = %q", fish)
	}

	if _, err := ShellHook("csh"); err == nil {
		t.Fatalf("ShellHook csh returned nil error")
	}
}

func TestWriteMarkerCreatesPrivateJSON(t *testing.T) {
	dir := t.TempDir()
	path, err := WriteMarker(Marker{
		User:      "ben",
		TTY:       "/dev/pts/7",
		CreatedAt: time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC),
		MarkerPID: 123,
	}, Options{RuntimeDir: dir})
	if err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	if !strings.HasSuffix(path, "pts_7-123.json") {
		t.Fatalf("path = %q", path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat marker: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
}
