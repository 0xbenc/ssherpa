package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAuthkeysRevokeRemoteScriptRemovesMatchingIdentityOnly(t *testing.T) {
	dir := t.TempDir()
	sshDir := filepath.Join(dir, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	target := strings.Replace(testEd25519Key, "alice@example", "old@example", 1)
	authPath := filepath.Join(sshDir, "authorized_keys")
	contents := "# keep\n" + target + "\n" + testECDSAKey + "\n"
	if err := os.WriteFile(authPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	key := mustParseAuthkeysSeedKey(t, testEd25519Key)

	cmd := exec.Command("sh")
	cmd.Env = append(os.Environ(), "HOME="+dir)
	cmd.Stdin = strings.NewReader(authkeysRevokeRemoteScript(key, false))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("remote script failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	assertContains(t, stdout.String(), "SSHERPA_AUTHKEYS_REVOKE status=removed removed=1 not_found=0")
	got := readFile(t, authPath)
	if strings.Contains(got, "ssh-ed25519") {
		t.Fatalf("target identity was not removed:\n%s", got)
	}
	assertContains(t, got, "# keep")
	assertContains(t, got, testECDSAKey)
}

func TestAuthkeysRevokeRemoteScriptAlreadyAbsentIsUnchanged(t *testing.T) {
	dir := t.TempDir()
	key := mustParseAuthkeysSeedKey(t, testEd25519Key)

	cmd := exec.Command("sh")
	cmd.Env = append(os.Environ(), "HOME="+dir)
	cmd.Stdin = strings.NewReader(authkeysRevokeRemoteScript(key, false))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("remote script failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	assertContains(t, stdout.String(), "SSHERPA_AUTHKEYS_REVOKE status=unchanged removed=0 not_found=1")
}

func TestRunAuthkeysRevokeExecutesFakeSSHWithJump(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName 10.0.0.20
Host bastion
  HostName 100.64.0.1
`)
	fakeSSH, argvLog, stdinLog := writeAuthkeysRevokeFakeSSH(t, 0, "SSHERPA_AUTHKEYS_REVOKE status=removed removed=1 not_found=0\n", "SSHERPA_AUTHKEYS_REVOKE_VERIFY status=absent absent=1 present=0\n", "")
	fakeKeygen, _ := writeFakeSSH(t, 0)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{
		"authkeys", "revoke",
		"--key", testEd25519Key,
		"--target", "prod",
		"--hop", "prod=bastion",
		"--config", config,
		"--ssh-binary", fakeSSH,
		"--ssh-keygen", fakeKeygen,
		"--yes",
	}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d\nstdout=%s\nstderr=%s", code, stdout.String(), stderr.String())
	}
	assertContains(t, readFile(t, argvLog), "-J bastion prod sh -s")
	stdin := readFile(t, stdinLog)
	assertContains(t, stdin, "target_identity=")
	if strings.Contains(stdin, "sudo") {
		t.Fatalf("remote script unexpectedly contains sudo:\n%s", stdin)
	}
	assertContains(t, stdout.String(), "[removed] prod removed=1 not-found=0 route=bastion verify=absent")
	assertContains(t, stdout.String(), "[summary] ok=1 changed=1 unchanged=0 failed=0")
}

func writeAuthkeysRevokeFakeSSH(t *testing.T, exitCode int, revokeStdout string, verifyStdout string, stderrText string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	stdinLog := filepath.Join(dir, "stdin.log")
	path := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(argvLog) + "\n" +
		"input=$(cat)\n" +
		"printf '%s' \"$input\" > " + shellQuote(stdinLog) + "\n" +
		"case \"$input\" in\n" +
		"  *SSHERPA_AUTHKEYS_REVOKE_VERIFY*) printf '%s' " + shellQuote(verifyStdout) + " ;;\n" +
		"  *) printf '%s' " + shellQuote(revokeStdout) + " ;;\n" +
		"esac\n" +
		"printf '%s' " + shellQuote(stderrText) + " >&2\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return path, argvLog, stdinLog
}
