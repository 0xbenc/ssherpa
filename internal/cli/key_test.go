package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
