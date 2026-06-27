package sshkeys

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// genKey creates a keypair with ssh-keygen, skipping the test when generation
// is unavailable (e.g. macOS CI where a long $TMPDIR breaks the agent socket).
func genKey(t *testing.T, path, passphrase, comment string) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", path, "-N", passphrase, "-C", comment).CombinedOutput()
	if err != nil {
		t.Skipf("ssh-keygen generation unavailable here: %v: %s", err, out)
	}
}

func plainWrite(path string, data []byte, mode os.FileMode, _ bool) (string, error) {
	return "", os.WriteFile(path, data, mode)
}

func TestDeriveUnencrypted(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id")
	genKey(t, priv, "", "plain-key")

	info, err := Keygen{}.Derive(context.Background(), priv, "")
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if info.Type != "ssh-ed25519" {
		t.Fatalf("type = %q, want ssh-ed25519", info.Type)
	}
	if info.Comment != "plain-key" {
		t.Fatalf("comment = %q, want plain-key", info.Comment)
	}
	if !strings.HasPrefix(info.Fingerprint, "SHA256:") {
		t.Fatalf("fingerprint = %q", info.Fingerprint)
	}
	// Cross-check the fingerprint against ssh-keygen -lf on the .pub.
	out, err := exec.Command("ssh-keygen", "-lf", priv+".pub").Output()
	if err != nil {
		t.Fatalf("ssh-keygen -lf: %v", err)
	}
	if !strings.Contains(string(out), info.Fingerprint) {
		t.Fatalf("fingerprint %q not in ssh-keygen -lf output:\n%s", info.Fingerprint, out)
	}
}

func TestDeriveEncryptedNeedsPassphrase(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id")
	genKey(t, priv, "hunter2", "enc-key")

	// Empty passphrase on an encrypted key -> ErrEncrypted (a prompt cue).
	if _, err := (Keygen{}).Derive(context.Background(), priv, ""); err != ErrEncrypted {
		t.Fatalf("empty passphrase err = %v, want ErrEncrypted", err)
	}
	// Wrong passphrase -> a non-sentinel error.
	if _, err := (Keygen{}).Derive(context.Background(), priv, "wrong"); err == nil || err == ErrEncrypted {
		t.Fatalf("wrong passphrase err = %v, want a plain error", err)
	}
	// Right passphrase (via the env-fed askpass helper) -> success.
	info, err := Keygen{}.Derive(context.Background(), priv, "hunter2")
	if err != nil {
		t.Fatalf("right passphrase: %v", err)
	}
	if info.Type != "ssh-ed25519" || !strings.HasPrefix(info.Fingerprint, "SHA256:") {
		t.Fatalf("info = %#v", info)
	}
}

func TestDeriveRejectsNonKey(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk")
	if err := os.WriteFile(junk, []byte("not a key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Keygen{}).Derive(context.Background(), junk, ""); err == nil || err == ErrEncrypted {
		t.Fatalf("non-key err = %v, want a plain error", err)
	}
}

func TestDeriveBytesLeavesSourceUntouched(t *testing.T) {
	dir := t.TempDir()
	priv := filepath.Join(dir, "id")
	genKey(t, priv, "", "bytes-key")
	data, err := os.ReadFile(priv)
	if err != nil {
		t.Fatal(err)
	}
	// Loosen the source perms; DeriveBytes must not need to touch the source.
	if err := os.Chmod(priv, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := Keygen{}.DeriveBytes(context.Background(), data, "")
	if err != nil {
		t.Fatalf("DeriveBytes: %v", err)
	}
	if info.Type != "ssh-ed25519" {
		t.Fatalf("type = %q", info.Type)
	}
	if st, _ := os.Stat(priv); st.Mode().Perm() != 0o644 {
		t.Fatalf("source perms changed to %o; DeriveBytes must not touch the source", st.Mode().Perm())
	}
}

func TestPlaceWritesCorrectPermsAndNoClobber(t *testing.T) {
	src := t.TempDir()
	priv := filepath.Join(src, "id")
	genKey(t, priv, "", "place-key")
	privBytes, _ := os.ReadFile(priv)
	info, err := Keygen{}.DeriveBytes(context.Background(), privBytes, "")
	if err != nil {
		t.Fatalf("DeriveBytes: %v", err)
	}

	ssh := filepath.Join(t.TempDir(), ".ssh")
	res, err := Place(ssh, "id_imported", privBytes, info.PublicLine, false, plainWrite)
	if err != nil {
		t.Fatalf("Place: %v", err)
	}
	assertMode(t, ssh, 0o700)
	assertMode(t, res.PrivatePath, 0o600)
	assertMode(t, res.PublicPath, 0o644)
	if res.Skipped {
		t.Fatal("first Place should not be skipped")
	}

	// Same bytes again -> no-op skip.
	res2, err := Place(ssh, "id_imported", privBytes, info.PublicLine, false, plainWrite)
	if err != nil || !res2.Skipped {
		t.Fatalf("identical Place: skipped=%v err=%v, want skip", res2.Skipped, err)
	}

	// Different bytes, same name, no force -> refuse.
	other := t.TempDir()
	genKey(t, filepath.Join(other, "id"), "", "other")
	otherBytes, _ := os.ReadFile(filepath.Join(other, "id"))
	if _, err := Place(ssh, "id_imported", otherBytes, info.PublicLine, false, plainWrite); err == nil {
		t.Fatal("Place over a different key without force should refuse")
	}
}

func TestPlaceRejectsBadName(t *testing.T) {
	ssh := filepath.Join(t.TempDir(), ".ssh")
	for _, name := range []string{"", "a/b", "../escape"} {
		if _, err := Place(ssh, name, []byte("x"), "ssh-ed25519 AAAA c", false, plainWrite); err == nil {
			t.Fatalf("name %q should be rejected", name)
		}
	}
}

func requireKeygen(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	probe := filepath.Join(t.TempDir(), "probe")
	if out, err := exec.Command("ssh-keygen", "-t", "ed25519", "-f", probe, "-N", "").CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen generation unavailable here: %v: %s", err, out)
	}
}

func TestGenerate(t *testing.T) {
	requireKeygen(t)
	ssh := filepath.Join(t.TempDir(), ".ssh")
	info, res, err := Keygen{}.Generate(context.Background(), ssh, "id_gen", GenerateOptions{Comment: "fresh"}, false)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if info.Type != "ssh-ed25519" || !strings.HasPrefix(info.Fingerprint, "SHA256:") {
		t.Fatalf("info = %#v", info)
	}
	assertMode(t, ssh, 0o700)
	assertMode(t, res.PrivatePath, 0o600)
	assertMode(t, res.PublicPath, 0o644)

	// No-clobber without force.
	if _, _, err := (Keygen{}).Generate(context.Background(), ssh, "id_gen", GenerateOptions{}, false); err == nil {
		t.Fatal("regenerate without force should refuse")
	}
	// Force overwrites and yields a different key.
	info2, _, err := Keygen{}.Generate(context.Background(), ssh, "id_gen", GenerateOptions{}, true)
	if err != nil {
		t.Fatalf("forced regenerate: %v", err)
	}
	if info2.Fingerprint == info.Fingerprint {
		t.Fatal("forced regenerate produced the same key")
	}
}

func TestGenerateRejectsBadType(t *testing.T) {
	ssh := filepath.Join(t.TempDir(), ".ssh")
	if _, _, err := (Keygen{}).Generate(context.Background(), ssh, "id", GenerateOptions{Type: "dsa-bogus"}, false); err == nil {
		t.Fatal("unsupported type should be rejected")
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if st.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, st.Mode().Perm(), want)
	}
}
