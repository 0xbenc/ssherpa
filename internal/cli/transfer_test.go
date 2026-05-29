package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalFilePickerItemsSortsDirectoriesBeforeFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "z-dir"), 0o700); err != nil {
		t.Fatalf("mkdir z-dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a-dir"), 0o700); err != nil {
		t.Fatalf("mkdir a-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "z.txt"), []byte("z"), 0o600); err != nil {
		t.Fatalf("write z.txt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	items, err := localFilePickerItems(dir)
	if err != nil {
		t.Fatalf("localFilePickerItems returned error: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("items = %#v, want parent plus four entries", items)
	}
	wantTitles := []string{"..", "a-dir" + string(os.PathSeparator), "z-dir" + string(os.PathSeparator), "a.txt", "z.txt"}
	for i, want := range wantTitles {
		if items[i].Title != want {
			t.Fatalf("items[%d].Title = %q, want %q; items=%#v", i, items[i].Title, want, items)
		}
	}
	if items[1].Kind != filePickerDir || items[3].Kind != filePickerFile {
		t.Fatalf("item kinds = %#v, want directories before files", items)
	}
}

func TestHumanBytes(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{0, "0 B"},
		{12, "12 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{10 * 1024, "10 KB"},
		{2 * 1024 * 1024, "2.0 MB"},
	}
	for _, tt := range tests {
		if got := humanBytes(tt.size); got != tt.want {
			t.Fatalf("humanBytes(%d) = %q, want %q", tt.size, got, tt.want)
		}
	}
}

func TestParseRemoteDirectoryListing(t *testing.T) {
	output := strings.Join([]string{
		"Remote working directory: /srv/app",
		"drwxr-xr-x    2 deploy deploy      4096 May 29 10:10 logs",
		"-rw-r--r--    1 deploy deploy       120 May 29 10:11 app.txt",
		"drwxr-xr-x    2 deploy deploy      4096 May 29 10:12 config files",
		"drwxr-xr-x    2 deploy deploy      4096 May 29 10:13 .",
		"drwxr-xr-x    2 deploy deploy      4096 May 29 10:14 ..",
	}, "\n")

	listing := parseRemoteDirectoryListing(output)
	if listing.CWD != "/srv/app" {
		t.Fatalf("CWD = %q, want /srv/app", listing.CWD)
	}
	want := []string{"config files", "logs"}
	if len(listing.Dirs) != len(want) {
		t.Fatalf("Dirs = %#v, want %#v", listing.Dirs, want)
	}
	for i := range want {
		if listing.Dirs[i] != want[i] {
			t.Fatalf("Dirs = %#v, want %#v", listing.Dirs, want)
		}
	}
}

func TestRemoteDirectoryPickerItems(t *testing.T) {
	items := remoteDirectoryPickerItems("/srv/app", []string{"logs"})
	if len(items) != 3 {
		t.Fatalf("items = %#v, want here, parent, child", items)
	}
	if items[0].Kind != remotePickerHere || items[0].Token != "/srv/app" {
		t.Fatalf("here item = %#v", items[0])
	}
	if items[1].Kind != remotePickerUp || items[1].Token != "/srv" {
		t.Fatalf("parent item = %#v", items[1])
	}
	if items[2].Kind != remotePickerDir || items[2].Token != "/srv/app/logs" {
		t.Fatalf("dir item = %#v", items[2])
	}
}

func TestRemotePathHelpers(t *testing.T) {
	tests := []struct {
		dir        string
		name       string
		wantJoin   string
		wantParent string
	}{
		{"/", "tmp", "/tmp", "/"},
		{"/srv/app", "logs", "/srv/app/logs", "/srv"},
		{".", "logs", "logs", ".."},
		{"nested/path", "logs", "nested/path/logs", "nested"},
	}
	for _, tt := range tests {
		if got := remoteJoin(tt.dir, tt.name); got != tt.wantJoin {
			t.Fatalf("remoteJoin(%q, %q) = %q, want %q", tt.dir, tt.name, got, tt.wantJoin)
		}
		if got := remoteParent(tt.dir); got != tt.wantParent {
			t.Fatalf("remoteParent(%q) = %q, want %q", tt.dir, got, tt.wantParent)
		}
	}
}
