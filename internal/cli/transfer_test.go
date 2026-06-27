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
	"github.com/0xbenc/termnav"
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
	assertFileMode(t, final, 0o600)
	assertNoPartFiles(t, destDir)
	assertContains(t, readFile(t, batchLog), "get /srv/app/payload.txt "+filepath.Join(destDir, ".payload.txt.ssherpa."))
}

// TestReceiveOverwritePreservesDestinationMode pins that the temp+rename
// dance does not silently tighten a confirmed overwrite to os.CreateTemp's
// 0600: the renamed file must carry the destination's prior mode.
func TestReceiveOverwritePreservesDestinationMode(t *testing.T) {
	fakeSFTP, _ := writeDirAwareFakeSFTP(t, 0)
	destDir := t.TempDir()
	final := filepath.Join(destDir, "payload.txt")
	if err := os.WriteFile(final, []byte("original"), 0o644); err != nil {
		t.Fatalf("write original: %v", err)
	}
	if err := os.Chmod(final, 0o644); err != nil {
		t.Fatalf("chmod original: %v", err)
	}
	flags := transferFlags{
		SFTPBinary: fakeSFTP,
		LocalPath:  final,
		RemotePath: "/srv/app/payload.txt",
		Force:      true,
	}
	var stdout, stderr bytes.Buffer

	code, _, _ := executeSFTPTransfer(sshcmd.SFTPTransferReceive, flags, hostlist.Alias{Name: "prod"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d, want success; stderr=%q", code, stderr.String())
	}
	if got := readFile(t, final); got != "partial" {
		t.Fatalf("final = %q, want downloaded content", got)
	}
	assertFileMode(t, final, 0o644)
}

// TestReceiveFreshDownloadGetsUmaskDefaultMode pins that a download to a
// path that did not exist before ends up with the same permissions a
// plain create would have produced (0666 minus the umask), not the 0600
// that os.CreateTemp gave the staging file.
func TestReceiveFreshDownloadGetsUmaskDefaultMode(t *testing.T) {
	fakeSFTP, _ := writeDirAwareFakeSFTP(t, 0)
	destDir := t.TempDir()
	final := filepath.Join(destDir, "fresh.txt")
	flags := transferFlags{
		SFTPBinary: fakeSFTP,
		LocalPath:  final,
		RemotePath: "/srv/app/payload.txt",
		Force:      true,
	}
	var stdout, stderr bytes.Buffer

	code, _, _ := executeSFTPTransfer(sshcmd.SFTPTransferReceive, flags, hostlist.Alias{Name: "prod"}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code = %d, want success; stderr=%q", code, stderr.String())
	}
	probePath := filepath.Join(t.TempDir(), "umask-probe")
	probe, err := os.OpenFile(probePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		t.Fatalf("create umask probe: %v", err)
	}
	probeInfo, err := probe.Stat()
	_ = probe.Close()
	if err != nil {
		t.Fatalf("stat umask probe: %v", err)
	}
	assertFileMode(t, final, probeInfo.Mode().Perm())
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %#o, want %#o", path, got, want)
	}
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

func TestLocalFileSourceSortsDirectoriesBeforeFiles(t *testing.T) {
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

	listing, err := localFileSource().ListSync(dir)
	if err != nil {
		t.Fatalf("localFileSource.ListSync returned error: %v", err)
	}
	rows := listing.Rows
	if len(rows) != 5 {
		t.Fatalf("rows = %#v, want parent plus four entries", rows)
	}
	wantTitles := []string{"..", "a-dir" + string(os.PathSeparator), "z-dir" + string(os.PathSeparator), "a.txt", "z.txt"}
	for i, want := range wantTitles {
		if rows[i].Title != want {
			t.Fatalf("rows[%d].Title = %q, want %q; rows=%#v", i, rows[i].Title, want, rows)
		}
	}
	if rows[1].Intent != termnav.IntentDescend || rows[3].Intent != termnav.IntentSelectLeaf {
		t.Fatalf("row intents = %#v, want directories before files", rows)
	}
}

func TestLocalDirSourceOmitsFiles(t *testing.T) {
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

	listing, err := localDirSource().ListSync(dir)
	if err != nil {
		t.Fatalf("localDirSource.ListSync returned error: %v", err)
	}
	rows := listing.Rows
	wantTitles := []string{"Use this folder", "..", "a-dir" + string(os.PathSeparator), "z-dir" + string(os.PathSeparator)}
	if len(rows) != len(wantTitles) {
		t.Fatalf("rows = %#v, want %d entries", rows, len(wantTitles))
	}
	for i, want := range wantTitles {
		if rows[i].Title != want {
			t.Fatalf("rows[%d].Title = %q, want %q; rows=%#v", i, rows[i].Title, want, rows)
		}
	}
	if rows[0].Intent != termnav.IntentUseContainer || rows[2].Intent != termnav.IntentDescend {
		t.Fatalf("row intents = %#v, want use then directories", rows)
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

func TestRemoteListingRowsFolderMode(t *testing.T) {
	// Folder picker: "Use this folder" + ".." + child directories only.
	listing := remoteListingRows("/srv/app", []remoteEntry{
		{Name: "logs", IsDir: true, Size: "4096"},
		{Name: "app.txt", Size: "120"},
	}, true, false, true)
	rows := listing.Rows
	if len(rows) != 3 {
		t.Fatalf("rows = %#v, want use, parent, dir", rows)
	}
	if rows[0].Intent != termnav.IntentUseContainer || rows[0].Token != "/srv/app" || rows[0].Kind != string(remotePickerHere) {
		t.Fatalf("use row = %#v", rows[0])
	}
	if rows[1].Intent != termnav.IntentAscend || rows[1].Token != "/srv" || rows[1].Kind != string(remotePickerUp) {
		t.Fatalf("parent row = %#v", rows[1])
	}
	if rows[2].Intent != termnav.IntentDescend || rows[2].Token != "/srv/app/logs" || rows[2].Kind != string(remotePickerDir) {
		t.Fatalf("dir row = %#v", rows[2])
	}
}

func TestRemoteListingRowsFileMode(t *testing.T) {
	// File picker: ".." + child directories + selectable files with descriptions.
	listing := remoteListingRows("/srv/app", []remoteEntry{
		{Name: "logs", IsDir: true, Size: "4096"},
		{Name: "app.txt", Size: "120"},
	}, false, true, false)
	rows := listing.Rows
	if len(rows) != 3 {
		t.Fatalf("rows = %#v, want parent, dir, file", rows)
	}
	if rows[0].Intent != termnav.IntentAscend || rows[0].Token != "/srv" {
		t.Fatalf("parent row = %#v", rows[0])
	}
	if rows[1].Intent != termnav.IntentDescend || rows[1].Token != "/srv/app/logs" {
		t.Fatalf("dir row = %#v", rows[1])
	}
	if rows[2].Intent != termnav.IntentSelectLeaf || !rows[2].Selectable || rows[2].Token != "/srv/app/app.txt" || rows[2].Description != "120 B" {
		t.Fatalf("file row = %#v", rows[2])
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
