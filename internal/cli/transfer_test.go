package cli

import (
	"os"
	"path/filepath"
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
