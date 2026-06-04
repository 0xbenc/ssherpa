package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/authkeys"
	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/ui"
)

func TestAuthkeysMenuItems(t *testing.T) {
	items := authkeysMenuItems()
	if len(items) != 5 {
		t.Fatalf("len(items) = %d, want 5", len(items))
	}
	want := []struct {
		token string
		group string
		badge string
		kind  ui.ItemKind
	}{
		{"add", "Add Keys", "add", ui.ItemAuthkeys},
		{"merge", "Add Keys", "merge", ui.ItemAuthkeys},
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
	if !strings.Contains(items[3].Action, "fingerprint") {
		t.Fatalf("delete action = %q", items[3].Action)
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
