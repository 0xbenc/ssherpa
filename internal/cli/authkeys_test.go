package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/authkeys"
	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/ui"
)

func TestAuthkeysMenuItems(t *testing.T) {
	items := authkeysMenuItems()
	if len(items) != 8 {
		t.Fatalf("len(items) = %d, want 8", len(items))
	}
	want := []struct {
		token string
		group string
		badge string
		kind  ui.ItemKind
	}{
		{"view", "Inspect", "view", ui.ItemAuthkeys},
		{"add", "Add Keys", "add", ui.ItemAuthkeys},
		{"merge", "Add Keys", "merge", ui.ItemAuthkeys},
		{"seed", "Remote", "seed", ui.ItemAuthkeys},
		{"revoke", "Remote", "revoke", ui.ItemConfirmDelete},
		{"replace", "Overwrite", "repl", ui.ItemConfirmDelete},
		{"delete", "Remove", "delete", ui.ItemConfirmDelete},
		{"back", "Navigation", "back", ui.ItemKind("back")},
	}
	for i, w := range want {
		item := items[i]
		if item.Token != w.token || item.Group != w.group || item.Badge != w.badge || item.Kind != w.kind {
			t.Fatalf("item %d = %+v, want token %q group %q badge %q kind %q", i, item, w.token, w.group, w.badge, w.kind)
		}
	}
	if !strings.Contains(items[0].Action, "read-only") {
		t.Fatalf("view action = %q", items[0].Action)
	}
	if !strings.Contains(items[3].Action, "selected SSH hosts") {
		t.Fatalf("seed action = %q", items[3].Action)
	}
	if !strings.Contains(items[4].Action, "Remove one") {
		t.Fatalf("revoke action = %q", items[4].Action)
	}
	if !strings.Contains(items[6].Action, "fingerprint") {
		t.Fatalf("delete action = %q", items[6].Action)
	}
}

func TestAuthkeysDirectoryBrowserOptionsUseTransferPickerShape(t *testing.T) {
	opts := authkeysDirectoryBrowserOptions(nil, "SSHERPA AUTHKEYS REPLACE SOURCE", "/home/test/keys")
	if opts.Title != "SSHERPA AUTHKEYS REPLACE SOURCE" || opts.Mode != "local-folder" {
		t.Fatalf("title/mode = %q / %q", opts.Title, opts.Mode)
	}
	if opts.LocationLabel != "LOCAL" || opts.Location != "/home/test/keys" {
		t.Fatalf("location = %q / %q", opts.LocationLabel, opts.Location)
	}
	if strings.Join(opts.Steps, "\x00") != "action\x00directory\x00confirm" || opts.CurrentStep != 1 {
		t.Fatalf("steps/current = %#v / %d", opts.Steps, opts.CurrentStep)
	}
	if !strings.Contains(opts.Footer, "open/use") {
		t.Fatalf("footer = %q", opts.Footer)
	}
}

func TestPrintAuthkeysReportIncludesMutationDetails(t *testing.T) {
	var stdout bytes.Buffer
	plan := authkeys.Plan{
		Path:   "/home/test/.ssh/authorized_keys",
		Action: "replaced",
		Target: "/home/test/keys",
		Keys: []authkeys.AuthorizedKey{{
			Type:    "ssh-ed25519",
			Blob:    "abc123",
			Comment: "alice@example",
		}},
		Stats: authkeys.ImportStats{
			Valid:   1,
			Added:   1,
			Invalid: 1,
		},
		Diagnostics: []authkeys.Diagnostic{{Severity: "warning", Message: "bad key"}},
		NotFound:    []string{"SHA256:missing"},
	}
	result := fsutil.WriteResult{
		Changed:    true,
		BackupPath: "/home/test/.ssh/authorized_keys.ssherpa-backup.1",
	}

	printAuthkeysReport(&stdout, plan, result)

	text := stdout.String()
	for _, want := range []string{
		"[report] authorized_keys",
		"action      replaced",
		"path        /home/test/.ssh/authorized_keys",
		"target      /home/test/keys",
		"changed     yes",
		"dry-run     no",
		"keys        1",
		"stats       valid=1 added=1 deleted=0 invalid=1 duplicate=0 already-present=0 ignored=0",
		"not-found   SHA256:missing",
		"warnings    1",
		"backup      /home/test/.ssh/authorized_keys.ssherpa-backup.1",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report missing %q:\n%s", want, text)
		}
	}
}

func TestAuthkeysFingerprintItemsPreserveFingerprintAndSource(t *testing.T) {
	key, err := authkeys.ParsePublicKeyLine(`from="10.0.0.0/8" ` + testEd25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	key.Source = "/home/test/.ssh/authorized_keys"
	key.Line = 7
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	items := authkeysFingerprintItems([]authkeys.AuthorizedKey{key})
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	item := items[0]
	if item.Kind != ui.ItemConfirmDelete || item.Token != fp || item.Badge != "ed255" || item.Group != "Authorized Keys" {
		t.Fatalf("fingerprint item = %+v", item)
	}
	if item.Title != "alice@example" || !strings.Contains(item.Description, fp) {
		t.Fatalf("title/description = %q / %q", item.Title, item.Description)
	}
	for _, want := range []string{"/home/test/.ssh/authorized_keys:7", `options=from="10.0.0.0/8"`, "comment=alice@example"} {
		if !strings.Contains(item.Detail, want) {
			t.Fatalf("detail missing %q: %q", want, item.Detail)
		}
	}
}

func TestAuthkeysCurrentKeyItemsUseReadOnlyStyle(t *testing.T) {
	key, err := authkeys.ParsePublicKeyLine(`from="10.0.0.0/8" ` + testEd25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	key.Source = "/home/test/.ssh/authorized_keys"
	key.Line = 7
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	items := authkeysCurrentKeyItems([]authkeys.AuthorizedKey{key})
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	item := items[0]
	if item.Kind != ui.ItemAuthkeys || item.Token != fp || item.Badge != "ed255" || item.Group != "Current Keys" {
		t.Fatalf("current key item = %+v", item)
	}
	if item.Title != "alice@example" || !strings.Contains(item.Action, "details") {
		t.Fatalf("title/action = %q / %q", item.Title, item.Action)
	}
	if !strings.Contains(item.Detail, `options=from="10.0.0.0/8"`) {
		t.Fatalf("detail = %q", item.Detail)
	}
}

func TestAuthkeysKeyViewLinesIncludeFullEntry(t *testing.T) {
	key, err := authkeys.ParsePublicKeyLine(`from="10.0.0.0/8" ` + testEd25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	key.Source = "/home/test/.ssh/authorized_keys"
	key.Line = 7
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	text := strings.Join(authkeysKeyViewLines(key, fp), "\n")
	for _, want := range []string{
		"Fingerprint: " + fp,
		"Type: ssh-ed25519",
		"Source: /home/test/.ssh/authorized_keys:7",
		"Comment: alice@example",
		`Options: from="10.0.0.0/8"`,
		"Authorized key:",
		key.Render(),
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view lines missing %q:\n%s", want, text)
		}
	}
}

func TestRunAuthkeysAddRejectsControlCharOptions(t *testing.T) {
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "authorized_keys")
	injected := "command=\"echo a\nrm -rf /\" " + testEd25519Key

	code := Run([]string{"authkeys", "add", "--key", injected, "--path", path, "--yes"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "options cannot contain control characters")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("authorized_keys exists after rejected add, err=%v", err)
	}
}

func TestRunAuthkeysDeleteWithYesRefusesDuplicateBlobEntries(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "authorized_keys")
	contents := `from="10.0.0.0/8" ` + testEd25519Key + "\n" +
		`command="uptime",no-pty ` + testEd25519Key + "\n" +
		testECDSAKey + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	fp := mustTestFingerprint(t, testEd25519Key)

	code := Run([]string{"authkeys", "delete", "--fingerprint", fp, "--path", path, "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1; stdout = %q", code, stdout.String())
	}
	assertContains(t, stderr.String(), "fingerprint "+fp+" matches 2 authorized_keys entries")
	assertContains(t, stderr.String(), "pass --all-matching")
	assertContains(t, stderr.String(), `line 1: ssh-ed25519 options=from="10.0.0.0/8"`)
	assertContains(t, stderr.String(), `line 2: ssh-ed25519 options=command="uptime",no-pty`)
	if readFile(t, path) != contents {
		t.Fatalf("authorized_keys changed after refused delete: %q", readFile(t, path))
	}
}

func TestRunAuthkeysDeleteDryRunWithYesPreviewsDuplicates(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "authorized_keys")
	contents := `from="10.0.0.0/8" ` + testEd25519Key + "\n" +
		`command="uptime",no-pty ` + testEd25519Key + "\n" +
		testECDSAKey + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	fp := mustTestFingerprint(t, testEd25519Key)

	code := Run([]string{"authkeys", "delete", "--fingerprint", fp, "--path", path, "--dry-run", "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), `from="10.0.0.0/8"`)
	assertContains(t, stdout.String(), `command="uptime",no-pty`)
	if readFile(t, path) != contents {
		t.Fatalf("authorized_keys changed after dry-run: %q", readFile(t, path))
	}
}

func TestRunAuthkeysDeleteAllMatchingRemovesEveryMatchingEntry(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "authorized_keys")
	contents := `from="10.0.0.0/8" ` + testEd25519Key + "\n" +
		`command="uptime",no-pty ` + testEd25519Key + "\n" +
		testECDSAKey + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	fp := mustTestFingerprint(t, testEd25519Key)

	code := Run([]string{"authkeys", "delete", "--fingerprint", fp, "--path", path, "--all-matching", "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[removed]")
	got := readFile(t, path)
	if strings.Contains(got, "ssh-ed25519") || !strings.Contains(got, testECDSAKey) {
		t.Fatalf("authorized_keys = %q, want both matching entries removed", got)
	}
}

func TestRunAuthkeysDeleteInteractiveDuplicatesReachConfirmAndCancelByDefault(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "authorized_keys")
	contents := `from="10.0.0.0/8" ` + testEd25519Key + "\n" +
		`command="uptime",no-pty ` + testEd25519Key + "\n" +
		testECDSAKey + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	fp := mustTestFingerprint(t, testEd25519Key)
	t.Setenv("SSHERPA_NO_ALT_SCREEN", "1")
	swapStdinWithKeys(t, "\r")

	// Without --yes, duplicate matches must NOT be refused; the interactive
	// confirm (default No) decides, and a bare enter cancels.
	code := runCLIWithTimeout(t, []string{"authkeys", "delete", "--fingerprint", fp, "--path", path}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[skipped] authkeys delete cancelled")
	if strings.Contains(stderr.String(), "pass --all-matching") {
		t.Fatalf("interactive delete refused duplicates: %q", stderr.String())
	}
	if readFile(t, path) != contents {
		t.Fatalf("authorized_keys changed after cancelled delete: %q", readFile(t, path))
	}
}

func TestRunAuthkeysReplaceConfirmDefaultsToNo(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte(testEd25519Key+"\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	keysDir := filepath.Join(dir, "keys")
	if err := os.Mkdir(keysDir, 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, "ecdsa.pub"), []byte(testECDSAKey+"\n"), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}
	fake := writeFakeSSHKeygen(t, dir, 0)
	t.Setenv("SSHERPA_NO_ALT_SCREEN", "1")
	swapStdinWithKeys(t, "\r")

	code := runCLIWithTimeout(t, []string{"authkeys", "replace", "--from-dir", keysDir, "--path", path, "--ssh-keygen", fake}, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[skipped] authkeys replace cancelled")
	got := readFile(t, path)
	if !strings.Contains(got, testEd25519Key) || strings.Contains(got, testECDSAKey) {
		t.Fatalf("authorized_keys = %q, want original contents preserved", got)
	}
}

func TestAuthkeysDeleteDescriptionListsEveryEntry(t *testing.T) {
	first, err := authkeys.ParsePublicKeyLine(`from="10.0.0.0/8" ` + testEd25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	first.Line = 1
	second, err := authkeys.ParsePublicKeyLine(`command="uptime",no-pty ` + testEd25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	second.Line = 2
	plan := authkeys.Plan{
		Keys:  []authkeys.AuthorizedKey{first, second},
		Stats: authkeys.ImportStats{Deleted: 2},
	}

	got := authkeysDeleteDescription(plan, "/home/test/.ssh/authorized_keys")

	for _, want := range []string{
		"2 key(s) from /home/test/.ssh/authorized_keys",
		`- line 1: ssh-ed25519 options=from="10.0.0.0/8" comment=alice@example`,
		`- line 2: ssh-ed25519 options=command="uptime",no-pty comment=alice@example`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("description missing %q:\n%s", want, got)
		}
	}
}

func TestAuthkeysDuplicateDeleteGroupsDetectsSharedFingerprints(t *testing.T) {
	first, err := authkeys.ParsePublicKeyLine(`from="10.0.0.0/8" ` + testEd25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	second, err := authkeys.ParsePublicKeyLine(`command="uptime",no-pty ` + testEd25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	other, err := authkeys.ParsePublicKeyLine(testECDSAKey)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}

	groups := authkeysDuplicateDeleteGroups([]authkeys.AuthorizedKey{first, other, second})

	if len(groups) != 1 {
		t.Fatalf("groups = %#v, want one duplicate group", groups)
	}
	if groups[0].Fingerprint != mustTestFingerprint(t, testEd25519Key) || len(groups[0].Keys) != 2 {
		t.Fatalf("group = %#v, want both ed25519 entries", groups[0])
	}
	if groups[0].Keys[0].Options != `from="10.0.0.0/8"` || groups[0].Keys[1].Options != `command="uptime",no-pty` {
		t.Fatalf("group keys = %#v, want options preserved in order", groups[0].Keys)
	}
}

func mustTestFingerprint(t *testing.T, line string) string {
	t.Helper()
	key, err := authkeys.ParsePublicKeyLine(line)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine(%q): %v", line, err)
	}
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("SHA256Fingerprint: %v", err)
	}
	return fp
}

// swapStdinWithKeys replaces os.Stdin with a pipe pre-loaded with keypresses
// so interactive confirms can be driven from a test.
func swapStdinWithKeys(t *testing.T, keys string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(keys); err != nil {
		t.Fatalf("write stdin pipe: %v", err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = w.Close()
		_ = r.Close()
	})
}

func runCLIWithTimeout(t *testing.T, args []string, stdout *bytes.Buffer, stderr *bytes.Buffer) int {
	t.Helper()
	done := make(chan int, 1)
	go func() { done <- Run(args, stdout, stderr, BuildInfo{}) }()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatalf("command %v did not finish within budget", args)
		return -1
	}
}

func TestAuthkeysKeyBadgeAndCountLabel(t *testing.T) {
	cases := map[string]string{
		"ssh-ed25519":                "ed255",
		"sk-ssh-ed25519@openssh.com": "ed255",
		"ecdsa-sha2-nistp256":        "ecdsa",
		"ssh-rsa":                    "rsa",
		"weird-key":                  "key",
	}
	for input, want := range cases {
		if got := authkeysKeyBadge(input); got != want {
			t.Fatalf("authkeysKeyBadge(%q) = %q, want %q", input, got, want)
		}
	}
	if got, want := authkeysCountLabel(1, "key", "keys"), "1 key"; got != want {
		t.Fatalf("authkeysCountLabel singular = %q, want %q", got, want)
	}
	if got, want := authkeysCountLabel(2, "key", "keys"), "2 keys"; got != want {
		t.Fatalf("authkeysCountLabel plural = %q, want %q", got, want)
	}
}
