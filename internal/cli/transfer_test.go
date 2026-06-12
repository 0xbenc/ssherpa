package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
)

// writeDirAwareFakeSFTP drops a fake sftp binary (same pattern as
// writeFakeSFTP in cli_test.go) whose listings model /srv containing the
// directory "app", which in turn contains payload.txt and a file
// literally named "download". `get` batches write "partial" into their
// local target and exit with getExit, simulating an interrupted real
// download (sftp truncates/writes the local target before failing).
func writeDirAwareFakeSFTP(t *testing.T, getExit int) (string, string) {
	t.Helper()
	dir := t.TempDir()
	batchLog := filepath.Join(dir, "batch.log")
	path := filepath.Join(dir, "fake-sftp")
	script := "#!/bin/sh\n" +
		"batch=$(cat)\n" +
		"printf '%s\\n' \"$batch\" >> " + shellQuote(batchLog) + "\n" +
		"case \"$batch\" in\n" +
		"  *'cd /srv/app'*'ls -la'*) printf '%s\\n' 'Remote working directory: /srv/app' '-rw-r--r-- 1 root root 120 May 29 10:11 payload.txt' '-rw-r--r-- 1 root root 64 May 29 10:12 download' ;;\n" +
		"  *'cd /srv'*'ls -la'*) printf '%s\\n' 'Remote working directory: /srv' 'drwxr-xr-x 2 root root 4096 May 29 10:10 app' ;;\n" +
		"  get*) target=$(printf '%s\\n' \"$batch\" | awk '{print $3; exit}'); printf 'partial' > \"$target\"; exit " + strconv.Itoa(getExit) + " ;;\n" +
		"  *) printf '%s\\n' 'fake sftp ok' ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake sftp: %v", err)
	}
	return path, batchLog
}

// TestConfirmTransferSafetySendDirectoryDestination pins the send
// overwrite gate for directory destinations: the check must target the
// real final file path (dir + local basename), refusing when that file
// exists and proceeding without a prompt when it does not.
func TestConfirmTransferSafetySendDirectoryDestination(t *testing.T) {
	fakeSFTP, _ := writeDirAwareFakeSFTP(t, 0)

	tests := []struct {
		name        string
		localName   string
		wantProceed bool
		wantCode    int
		wantStderr  string
	}{
		{
			name:        "existing file inside directory refused",
			localName:   "payload.txt",
			wantProceed: false,
			wantCode:    1,
			wantStderr:  "/srv/app/payload.txt already exists",
		},
		{
			name:        "fresh file inside directory proceeds",
			localName:   "fresh.txt",
			wantProceed: true,
			wantCode:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := filepath.Join(t.TempDir(), tt.localName)
			if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
				t.Fatalf("write local payload: %v", err)
			}
			transfer := sshcmd.SFTPTransfer{
				Direction:  sshcmd.SFTPTransferSend,
				Alias:      "prod",
				LocalPath:  local,
				RemotePath: "/srv/app",
			}
			var stderr bytes.Buffer

			proceed, code := confirmTransferSafety(transferFlags{}, transfer, sshcmd.Command{Argv: []string{fakeSFTP}}, &stderr)

			if proceed != tt.wantProceed || code != tt.wantCode {
				t.Fatalf("confirmTransferSafety = (%v, %d), want (%v, %d); stderr=%q", proceed, code, tt.wantProceed, tt.wantCode, stderr.String())
			}
			if tt.wantStderr != "" {
				assertContains(t, stderr.String(), tt.wantStderr)
			} else if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

// TestRemotePathInfoResolvesFileNamedDownload is the regression for the
// remoteBase fallback collision: a remote file literally named
// "download" used to be reported as "cannot resolve remote path name".
func TestRemotePathInfoResolvesFileNamedDownload(t *testing.T) {
	fakeSFTP, _ := writeDirAwareFakeSFTP(t, 0)

	info, err := remotePathInfo(sshcmd.Command{Argv: []string{fakeSFTP}}, "/srv/app/download")

	if err != nil {
		t.Fatalf("remotePathInfo returned error: %v", err)
	}
	if !info.Exists || info.IsDir {
		t.Fatalf("info = %+v, want existing file", info)
	}
}

// TestReceiveDownloadsToTempAndPreservesOriginalOnFailure pins the
// temp+rename receive contract: an interrupted get must leave the
// confirmed-overwrite original untouched and clean up its temp file.
func TestReceiveDownloadsToTempAndPreservesOriginalOnFailure(t *testing.T) {
	fakeSFTP, batchLog := writeDirAwareFakeSFTP(t, 1)
	destDir := t.TempDir()
	final := filepath.Join(destDir, "payload.txt")
	if err := os.WriteFile(final, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}
	flags := transferFlags{
		SFTPBinary: fakeSFTP,
		LocalPath:  final,
		RemotePath: "/srv/app/payload.txt",
		Force:      true,
	}
	var stdout, stderr bytes.Buffer

	code, _, _ := executeSFTPTransfer(sshcmd.SFTPTransferReceive, flags, hostlist.Alias{Name: "prod"}, &stdout, &stderr)

	if code == 0 {
		t.Fatalf("code = 0, want failure; stderr=%q", stderr.String())
	}
	if got := readFile(t, final); got != "original" {
		t.Fatalf("original = %q after failed download, want untouched", got)
	}
	assertNoPartFiles(t, destDir)
	assertContains(t, readFile(t, batchLog), "get /srv/app/payload.txt "+filepath.Join(destDir, ".payload.txt.ssherpa."))
}

func TestReceiveRenamesTempIntoPlaceOnSuccess(t *testing.T) {
	fakeSFTP, batchLog := writeDirAwareFakeSFTP(t, 0)
	destDir := t.TempDir()
	final := filepath.Join(destDir, "payload.txt")
	if err := os.WriteFile(final, []byte("original"), 0o600); err != nil {
		t.Fatalf("write original: %v", err)
	}
	flags := transferFlags{
		SFTPBinary: fakeSFTP,
		LocalPath:  final,
		RemotePath: "/srv/app/payload.txt",
		Force:      true,
	}
	var stdout, stderr bytes.Buffer

	code, transfer, attempted := executeSFTPTransfer(sshcmd.SFTPTransferReceive, flags, hostlist.Alias{Name: "prod"}, &stdout, &stderr)

	if code != 0 || !attempted {
		t.Fatalf("code = %d attempted = %v, want success; stderr=%q", code, attempted, stderr.String())
	}
	if got := readFile(t, final); got != "partial" {
		t.Fatalf("final = %q, want downloaded content renamed into place", got)
	}
	if transfer.LocalPath != final {
		t.Fatalf("transfer.LocalPath = %q, want final path %q", transfer.LocalPath, final)
	}
	assertNoPartFiles(t, destDir)
	assertContains(t, readFile(t, batchLog), "get /srv/app/payload.txt "+filepath.Join(destDir, ".payload.txt.ssherpa."))
}

// TestExecuteSFTPTransferRejectsControlCharacterPaths pins that CR/LF
// and friends are refused before any sftp process runs, pointing the
// user at the in-band transport.
func TestExecuteSFTPTransferRejectsControlCharacterPaths(t *testing.T) {
	fakeSFTP, batchLog := writeDirAwareFakeSFTP(t, 0)
	local := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local payload: %v", err)
	}
	flags := transferFlags{
		SFTPBinary: fakeSFTP,
		LocalPath:  local,
		RemotePath: "/srv/evil\nname",
	}
	var stdout, stderr bytes.Buffer

	code, _, attempted := executeSFTPTransfer(sshcmd.SFTPTransferSend, flags, hostlist.Alias{Name: "prod"}, &stdout, &stderr)

	if code != 1 || attempted {
		t.Fatalf("code = %d attempted = %v, want validation failure", code, attempted)
	}
	assertContains(t, stderr.String(), "control characters")
	assertContains(t, stderr.String(), "in-band")
	if _, err := os.Stat(batchLog); !os.IsNotExist(err) {
		t.Fatalf("sftp ran despite control-character path (batch log present, err=%v)", err)
	}
}

func assertNoPartFiles(t *testing.T, dir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.part"))
	if err != nil {
		t.Fatalf("glob part files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("leftover temp downloads: %v", matches)
	}
}

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

func TestLocalDirectoryPickerItemsOmitsFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "z-dir"), 0o700); err != nil {
		t.Fatalf("mkdir z-dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(dir, "a-dir"), 0o700); err != nil {
		t.Fatalf("mkdir a-dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o600); err != nil {
		t.Fatalf("write a.txt: %v", err)
	}

	items, err := localDirectoryPickerItems(dir)
	if err != nil {
		t.Fatalf("localDirectoryPickerItems returned error: %v", err)
	}
	wantTitles := []string{"Use this folder", "..", "a-dir" + string(os.PathSeparator), "z-dir" + string(os.PathSeparator)}
	if len(items) != len(wantTitles) {
		t.Fatalf("items = %#v, want %d entries", items, len(wantTitles))
	}
	for i, want := range wantTitles {
		if items[i].Title != want {
			t.Fatalf("items[%d].Title = %q, want %q; items=%#v", i, items[i].Title, want, items)
		}
	}
	if items[0].Kind != filePickerHere || items[2].Kind != filePickerDir {
		t.Fatalf("item kinds = %#v, want here then directories", items)
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

func TestParseRemoteListingIncludesFilesAndDirectories(t *testing.T) {
	output := strings.Join([]string{
		"Remote working directory: /srv/app",
		"drwxr-xr-x    2 deploy deploy      4096 May 29 10:10 logs",
		"-rw-r--r--    1 deploy deploy       120 May 29 10:11 app.txt",
		"-rw-r--r--    1 deploy deploy       240 May 29 10:12 config file.txt",
		"lrwxrwxrwx    1 deploy deploy        11 May 29 10:13 latest -> app.txt",
	}, "\n")

	listing := parseRemoteListing(output)
	if listing.CWD != "/srv/app" {
		t.Fatalf("CWD = %q, want /srv/app", listing.CWD)
	}
	want := []remoteEntry{
		{Name: "logs", IsDir: true, Size: "4096"},
		{Name: "app.txt", IsDir: false, Size: "120"},
		{Name: "config file.txt", IsDir: false, Size: "240"},
		{Name: "latest", IsDir: false, Size: "11"},
	}
	if len(listing.Entries) != len(want) {
		t.Fatalf("Entries = %#v, want %#v", listing.Entries, want)
	}
	for i := range want {
		if listing.Entries[i] != want[i] {
			t.Fatalf("Entries = %#v, want %#v", listing.Entries, want)
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

func TestRemoteFilePickerItems(t *testing.T) {
	items := remoteFilePickerItems("/srv/app", []remoteEntry{
		{Name: "logs", IsDir: true, Size: "4096"},
		{Name: "app.txt", Size: "120"},
	})
	if len(items) != 3 {
		t.Fatalf("items = %#v, want parent, dir, file", items)
	}
	if items[0].Kind != remotePickerUp || items[0].Token != "/srv" {
		t.Fatalf("parent item = %#v", items[0])
	}
	if items[1].Kind != remotePickerDir || items[1].Token != "/srv/app/logs" {
		t.Fatalf("dir item = %#v", items[1])
	}
	if items[2].Kind != remotePickerFile || items[2].Token != "/srv/app/app.txt" || items[2].Description != "120 B" {
		t.Fatalf("file item = %#v", items[2])
	}
}

func TestRemotePathHelpers(t *testing.T) {
	tests := []struct {
		dir           string
		name          string
		wantJoin      string
		wantParent    string
		wantBase      string
		wantLocalName string
	}{
		{"/", "tmp", "/tmp", "/", "", "download"},
		{"/srv/app", "logs", "/srv/app/logs", "/srv", "app", "app"},
		{".", "logs", "logs", "..", "", "download"},
		{"nested/path", "logs", "nested/path/logs", "nested", "path", "path"},
		// A remote file literally named "download" must behave like any
		// other basename: it used to collide with the internal fallback
		// sentinel and made the path untransferable.
		{"/srv/download", "x", "/srv/download/x", "/srv", "download", "download"},
	}
	for _, tt := range tests {
		if got := remoteJoin(tt.dir, tt.name); got != tt.wantJoin {
			t.Fatalf("remoteJoin(%q, %q) = %q, want %q", tt.dir, tt.name, got, tt.wantJoin)
		}
		if got := remoteParent(tt.dir); got != tt.wantParent {
			t.Fatalf("remoteParent(%q) = %q, want %q", tt.dir, got, tt.wantParent)
		}
		if got := remoteBase(tt.dir); got != tt.wantBase {
			t.Fatalf("remoteBase(%q) = %q, want %q", tt.dir, got, tt.wantBase)
		}
		if got := localNameForRemote(tt.dir); got != tt.wantLocalName {
			t.Fatalf("localNameForRemote(%q) = %q, want %q", tt.dir, got, tt.wantLocalName)
		}
	}
}
