package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshkeys"
)

func genTestKey(t *testing.T, path, passphrase, comment string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", path, "-N", passphrase, "-C", comment).CombinedOutput()
	if err != nil {
		t.Skipf("ssh-keygen generation unavailable here: %v: %s", err, out)
	}
}

func keyMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.Mode().Perm()
}

func TestKeyImportUnencrypted(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "backup_key")
	genTestKey(t, priv, "", "laptop")
	_ = os.Chmod(priv, 0o644) // loose-perm source, like a USB backup

	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "import", "--from", priv, "--yes"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("key import = %d; stderr=%s", code, stderr.String())
	}
	dest := filepath.Join(home, ".ssh", "backup_key")
	if keyMode(t, filepath.Join(home, ".ssh")) != 0o700 {
		t.Fatalf(".ssh dir mode = %o, want 0700", keyMode(t, filepath.Join(home, ".ssh")))
	}
	if keyMode(t, dest) != 0o600 {
		t.Fatalf("private mode = %o, want 0600", keyMode(t, dest))
	}
	if keyMode(t, dest+".pub") != 0o644 {
		t.Fatalf("public mode = %o, want 0644", keyMode(t, dest+".pub"))
	}
	// Source must be untouched.
	if keyMode(t, priv) != 0o644 {
		t.Fatalf("source perms changed to %o; import must not touch the source", keyMode(t, priv))
	}
}

func TestKeyImportEncryptedEnvPassphraseJSON(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "enc_key")
	genTestKey(t, priv, "s3cret", "enc")

	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSHERPA_KEY_PASSPHRASE", "s3cret")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "import", "--from", priv, "--yes", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("key import = %d; stderr=%s", code, stderr.String())
	}
	var out keyImportOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if !out.Imported || out.Type != "ssh-ed25519" || out.Fingerprint == "" {
		t.Fatalf("output = %#v", out)
	}
}

func TestKeyImportRejectsPublicKey(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "id")
	genTestKey(t, priv, "", "x")
	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "import", "--from", priv + ".pub", "--yes"}, &stdout, &stderr, BuildInfo{})
	if code == 0 {
		t.Fatalf("importing a .pub should fail; stderr=%s", stderr.String())
	}
}

func TestKeyGenerate(t *testing.T) {
	// Capability probe (skips on macOS CI where the agent socket path is too long).
	probe := filepath.Join(t.TempDir(), "probe")
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", probe, "-N", "").CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen generation unavailable: %v: %s", err, out)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "generate", "--name", "id_fresh", "--comment", "x", "--yes", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("key generate = %d; stderr=%s", code, stderr.String())
	}
	var out keyGenerateOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if !out.Generated || out.Type != "ed25519" || out.Fingerprint == "" {
		t.Fatalf("output = %#v", out)
	}
	if keyMode(t, filepath.Join(home, ".ssh", "id_fresh")) != 0o600 {
		t.Fatalf("generated private mode wrong")
	}
}

func TestKeyImportRegisterGlobalIdentity(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "k")
	genTestKey(t, priv, "", "me")

	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(sshDir, "config")
	if err := os.WriteFile(cfg, []byte("Host existing\n  HostName 1.2.3.4\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"key", "import", "--from", priv, "--name", "id_def", "--register", "--yes"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("import --register = %d; %s", code, stderr.String())
	}
	got, _ := os.ReadFile(cfg)
	wantLine := "IdentityFile " + filepath.Join(sshDir, "id_def")
	if !bytes.Contains(got, []byte(wantLine)) {
		t.Fatalf("config missing %q:\n%s", wantLine, got)
	}
	if !bytes.Contains(got, []byte("Host existing")) {
		t.Fatalf("registration clobbered the existing stanza:\n%s", got)
	}
	// Idempotent: re-register adds no second IdentityFile line.
	stderr.Reset()
	Run([]string{"key", "import", "--from", priv, "--name", "id_def", "--register", "--yes"}, &stdout, &stderr, BuildInfo{})
	after, _ := os.ReadFile(cfg)
	if n := bytes.Count(after, []byte("IdentityFile")); n != 1 {
		t.Fatalf("expected exactly one IdentityFile line, got %d:\n%s", n, after)
	}
}

func TestValidateKeyNameInput(t *testing.T) {
	ok := []string{"id_ed25519", "backup_key", "id-work"}
	for _, n := range ok {
		if err := validateKeyNameInput(n); err != nil {
			t.Errorf("validateKeyNameInput(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"", "   ", "a/b", "../escape", "dir\\name"}
	for _, n := range bad {
		if err := validateKeyNameInput(n); err == nil {
			t.Errorf("validateKeyNameInput(%q) = nil, want error", n)
		}
	}
}

func TestKeyReviewLines(t *testing.T) {
	info := sshkeys.KeyInfo{Type: "ssh-ed25519", Fingerprint: "SHA256:abc", Comment: "laptop"}
	lines := strings.Join(keyReviewLines("Import", info, "/home/u/.ssh/id_x", true, true), "\n")
	for _, want := range []string{"ssh-ed25519", "SHA256:abc", "laptop", "/home/u/.ssh/id_x", "(0600)", "(0644)", "default identity", "ssh-agent"} {
		if !strings.Contains(lines, want) {
			t.Errorf("review lines missing %q:\n%s", want, lines)
		}
	}

	// No fingerprint yet (generate preview) and no extras: those lines drop out.
	gen := strings.Join(keyReviewLines("Generate", sshkeys.KeyInfo{Type: "ed25519"}, "/p/id", false, false), "\n")
	if strings.Contains(gen, "fingerprint") {
		t.Errorf("generate preview should omit the fingerprint line:\n%s", gen)
	}
	if strings.Contains(gen, "default identity") || strings.Contains(gen, "ssh-agent") {
		t.Errorf("preview should omit register/agent lines when not chosen:\n%s", gen)
	}
}

func TestKeyMenuItems(t *testing.T) {
	want := map[string]bool{"import": false, "generate": false, "back": false}
	for _, it := range keyMenuItems() {
		if _, ok := want[it.Token]; ok {
			want[it.Token] = true
		}
	}
	for tok, seen := range want {
		if !seen {
			t.Errorf("key menu missing %q action", tok)
		}
	}
}

func TestParseAgentTTL(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		{"", 0, true},
		{"0", 0, true},
		{"8h", 8 * time.Hour, true},
		{"30m", 30 * time.Minute, true},
		{"90s", 90 * time.Second, true},
		{"-5m", 0, false},
		{"banana", 0, false},
	}
	for _, c := range cases {
		got, err := parseAgentTTL(c.in)
		if c.ok {
			if err != nil {
				t.Errorf("parseAgentTTL(%q) unexpected error %v", c.in, err)
				continue
			}
			if got != c.want {
				t.Errorf("parseAgentTTL(%q) = %v, want %v", c.in, got, c.want)
			}
		} else if err == nil {
			t.Errorf("parseAgentTTL(%q) expected an error", c.in)
		}
	}
}

func TestKeyImportAddToAgentNoAgentSoftSkips(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "k")
	genTestKey(t, priv, "", "x")

	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "") // no agent reachable
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "import", "--from", priv, "--name", "id_a", "--add-to-agent", "--yes", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("a missing agent must be a soft skip (exit 0), got %d; %s", code, stderr.String())
	}
	var out keyImportOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if !out.Imported {
		t.Fatalf("the import itself must still succeed: %#v", out)
	}
	if out.AddedToAgent || !out.AgentSkipped {
		t.Fatalf("expected agent skipped, got %#v", out)
	}
}

// startTestAgent boots a throwaway ssh-agent for the test and points
// SSH_AUTH_SOCK at it. It skips when no agent can be started.
func startTestAgent(t *testing.T) func() {
	t.Helper()
	if _, err := exec.LookPath("ssh-agent"); err != nil {
		t.Skip("ssh-agent not installed")
	}
	if _, err := exec.LookPath("ssh-add"); err != nil {
		t.Skip("ssh-add not installed")
	}
	out, err := exec.Command("ssh-agent", "-s").Output()
	if err != nil {
		t.Skipf("ssh-agent unavailable here: %v", err)
	}
	var sock, pid string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		for _, kv := range []struct {
			key string
			dst *string
		}{{"SSH_AUTH_SOCK=", &sock}, {"SSH_AGENT_PID=", &pid}} {
			if strings.HasPrefix(line, kv.key) {
				rest := strings.TrimPrefix(line, kv.key)
				if i := strings.IndexByte(rest, ';'); i >= 0 {
					rest = rest[:i]
				}
				*kv.dst = rest
			}
		}
	}
	if sock == "" {
		t.Skipf("could not parse SSH_AUTH_SOCK from: %s", out)
	}
	t.Setenv("SSH_AUTH_SOCK", sock)
	return func() {
		if pid != "" {
			_ = exec.Command("kill", pid).Run()
		}
	}
}

func TestKeyImportAddToAgentLoadsKey(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "agentkey")
	genTestKey(t, priv, "", "agent-test")

	stop := startTestAgent(t)
	defer stop()

	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "import", "--from", priv, "--name", "id_agent", "--add-to-agent", "--agent-ttl", "10m", "--yes", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("import --add-to-agent = %d; %s", code, stderr.String())
	}
	var out keyImportOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if !out.AddedToAgent || out.AgentSkipped {
		t.Fatalf("expected key added to agent, got %#v", out)
	}
	listed, err := exec.Command("ssh-add", "-l").CombinedOutput()
	if err != nil {
		t.Fatalf("ssh-add -l: %v: %s", err, listed)
	}
	if !bytes.Contains(listed, []byte(out.Fingerprint)) {
		t.Fatalf("agent does not list %s:\n%s", out.Fingerprint, listed)
	}
}

// TestImportSourceCandidates exercises the cleanup-eligibility logic without
// needing ssh-keygen: which source files would be removed by --delete-source.
func TestImportSourceCandidates(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "k")
	pub := priv + ".pub"
	if err := os.WriteFile(priv, []byte("PRIV"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pub, []byte("PUB"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "placed")

	// Distinct destination: both the private key and its .pub sibling qualify.
	got := importSourceCandidates(priv, sshkeys.PlaceResult{PrivatePath: dest, PublicPath: dest + ".pub"})
	if len(got) != 2 || got[0] != priv || got[1] != pub {
		t.Fatalf("want [%s %s], got %v", priv, pub, got)
	}

	// No .pub sibling: only the private key qualifies.
	solo := filepath.Join(t.TempDir(), "solo")
	if err := os.WriteFile(solo, []byte("PRIV"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = importSourceCandidates(solo, sshkeys.PlaceResult{PrivatePath: dest, PublicPath: dest + ".pub"})
	if len(got) != 1 || got[0] != solo {
		t.Fatalf("want [%s], got %v", solo, got)
	}

	// Source IS the destination (same file): nothing to delete.
	if got := importSourceCandidates(priv, sshkeys.PlaceResult{PrivatePath: priv, PublicPath: pub}); len(got) != 0 {
		t.Fatalf("in-place source must yield no candidates, got %v", got)
	}
}

// TestDeleteImportSourcesPartialFailure proves that a failed removal does not
// strand an already-deleted earlier file as "not deleted": deletion continues
// and the count reflects what was actually removed.
func TestDeleteImportSourcesPartialFailure(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permissions; cannot simulate a failed removal")
	}
	p1 := filepath.Join(t.TempDir(), "priv")
	if err := os.WriteFile(p1, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	roDir := t.TempDir()
	p2 := filepath.Join(roDir, "priv.pub")
	if err := os.WriteFile(p2, []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(roDir, 0o500); err != nil { // no write bit: removal denied
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })

	var stderr bytes.Buffer
	deleted, err := deleteImportSources([]string{p1, p2}, &stderr)
	if err == nil {
		t.Fatalf("expected an error for the unremovable file")
	}
	if deleted != 1 {
		t.Fatalf("expected 1 file removed (the deletable one), got %d", deleted)
	}
	if _, statErr := os.Stat(p1); !os.IsNotExist(statErr) {
		t.Fatalf("first file should be deleted, stat err=%v", statErr)
	}
	if _, statErr := os.Stat(p2); statErr != nil {
		t.Fatalf("second (unremovable) file should still exist: %v", statErr)
	}
}

func TestKeyImportDeleteSourceRemovesOriginals(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "backup_key")
	genTestKey(t, priv, "", "laptop")

	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "import", "--from", priv, "--delete-source", "--yes", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("key import = %d; stderr=%s", code, stderr.String())
	}
	var out keyImportOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if !out.Imported || !out.SourceDeleted {
		t.Fatalf("expected imported+source_deleted, got %#v", out)
	}
	// The copy in ~/.ssh must exist...
	if _, err := os.Stat(filepath.Join(home, ".ssh", "backup_key")); err != nil {
		t.Fatalf("placed key missing after delete-source: %v", err)
	}
	// ...and both original files must be gone.
	if _, err := os.Stat(priv); !os.IsNotExist(err) {
		t.Fatalf("source private key still present (err=%v)", err)
	}
	if _, err := os.Stat(priv + ".pub"); !os.IsNotExist(err) {
		t.Fatalf("source public key still present (err=%v)", err)
	}
}

func TestKeyImportDefaultKeepsSource(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "backup_key")
	genTestKey(t, priv, "", "laptop")

	t.Setenv("HOME", t.TempDir())
	var stdout, stderr bytes.Buffer
	code := Run([]string{"key", "import", "--from", priv, "--yes", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("key import = %d; stderr=%s", code, stderr.String())
	}
	var out keyImportOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if out.SourceDeleted {
		t.Fatalf("source must be kept without --delete-source: %#v", out)
	}
	if _, err := os.Stat(priv); err != nil {
		t.Fatalf("source must remain without --delete-source: %v", err)
	}
}

// TestKeyImportDeleteSourceKeepsInPlaceKey proves the SameFile guard: importing
// a key from its own location in ~/.ssh with --delete-source must not delete the
// key it just (no-op) "placed".
func TestKeyImportDeleteSourceKeepsInPlaceKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sshDir := filepath.Join(home, ".ssh")

	src := t.TempDir()
	priv := filepath.Join(src, "id_inplace")
	genTestKey(t, priv, "", "x")
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"key", "import", "--from", priv, "--name", "id_inplace", "--yes"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("seed import = %d; %s", code, stderr.String())
	}
	dest := filepath.Join(sshDir, "id_inplace")

	stdout.Reset()
	stderr.Reset()
	if code := Run([]string{"key", "import", "--from", dest, "--name", "id_inplace", "--delete-source", "--yes", "--json"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("in-place import = %d; %s", code, stderr.String())
	}
	var out keyImportOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json: %v\n%s", err, stdout.String())
	}
	if out.SourceDeleted {
		t.Fatalf("in-place import must not delete the placed key: %#v", out)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Fatalf("placed key was deleted by in-place --delete-source: %v", err)
	}
	if _, err := os.Stat(dest + ".pub"); err != nil {
		t.Fatalf("placed public key was deleted by in-place --delete-source: %v", err)
	}
}

func TestKeyImportNoClobber(t *testing.T) {
	src := t.TempDir()
	a := filepath.Join(src, "a")
	b := filepath.Join(src, "b")
	genTestKey(t, a, "", "a")
	genTestKey(t, b, "", "b")

	home := t.TempDir()
	t.Setenv("HOME", home)
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"key", "import", "--from", a, "--name", "id_test", "--yes"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("first import = %d; %s", code, stderr.String())
	}
	stderr.Reset()
	// A different key under the same name without --force must be refused.
	if code := Run([]string{"key", "import", "--from", b, "--name", "id_test", "--yes"}, &stdout, &stderr, BuildInfo{}); code == 0 {
		t.Fatalf("clobber without --force should fail; stderr=%s", stderr.String())
	}
	stderr.Reset()
	// With --force it succeeds.
	if code := Run([]string{"key", "import", "--from", b, "--name", "id_test", "--yes", "--force"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("forced overwrite = %d; %s", code, stderr.String())
	}
}
