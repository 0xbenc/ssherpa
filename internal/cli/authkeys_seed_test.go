package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/authkeys"
)

func TestParseAuthkeysSeedFlagsTargetsAndHops(t *testing.T) {
	var stderr bytes.Buffer

	flags, ok := parseAuthkeysSeedFlags([]string{
		"--key-file", "/tmp/id.pub",
		"--target", "prod",
		"--target=qa",
		"--hop", "qa=bastion,edge",
		"--dry-run",
	}, &stderr)

	if !ok {
		t.Fatalf("parseAuthkeysSeedFlags failed: %s", stderr.String())
	}
	if flags.KeyFile != "/tmp/id.pub" || !flags.DryRun {
		t.Fatalf("flags = %+v", flags)
	}
	if strings.Join(flags.Targets, ",") != "prod,qa" {
		t.Fatalf("targets = %#v", flags.Targets)
	}
	if got := strings.Join(flags.Hops["qa"], ","); got != "bastion,edge" {
		t.Fatalf("qa hops = %q", got)
	}
}

func TestAuthkeysSeedRemoteScriptAppendsMissingKeyOnly(t *testing.T) {
	dir := t.TempDir()
	sshDir := filepath.Join(dir, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}
	existing := strings.Replace(testEd25519Key, "alice@example", "old@example", 1)
	authPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(authPath, []byte(existing+"\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	key1 := mustParseAuthkeysSeedKey(t, testEd25519Key)
	key2 := mustParseAuthkeysSeedKey(t, testECDSAKey)

	cmd := exec.Command("sh")
	cmd.Env = append(os.Environ(), "HOME="+dir)
	cmd.Stdin = strings.NewReader(authkeysSeedRemoteScript([]authkeys.AuthorizedKey{key1, key2}, false))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		t.Fatalf("remote script failed: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
	}
	assertContains(t, stdout.String(), "SSHERPA_AUTHKEYS_SEED status=added added=1 already_present=1")
	got := readFile(t, authPath)
	if strings.Count(got, "ssh-ed25519") != 1 {
		t.Fatalf("duplicate ed25519 key added:\n%s", got)
	}
	assertContains(t, got, testECDSAKey)
}

func TestRunAuthkeysSeedExecutesFakeSSHWithJump(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName 10.0.0.20
Host bastion
  HostName 100.64.0.1
`)
	fakeSSH, argvLog, stdinLog := writeAuthkeysSeedFakeSSH(t, 0, "SSHERPA_AUTHKEYS_SEED status=added added=1 already_present=0\n", "")
	fakeKeygen, _ := writeFakeSSH(t, 0)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{
		"authkeys", "seed",
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
	assertContains(t, stdin, testEd25519Key)
	if strings.Contains(stdin, "sudo") {
		t.Fatalf("remote script unexpectedly contains sudo:\n%s", stdin)
	}
	assertContains(t, stdout.String(), "[added] prod added=1 already-present=0 route=bastion")
	assertContains(t, stdout.String(), "[summary] ok=1 changed=1 unchanged=0 failed=0")
}

func writeAuthkeysSeedFakeSSH(t *testing.T, exitCode int, stdoutText string, stderrText string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	stdinLog := filepath.Join(dir, "stdin.log")
	path := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(argvLog) + "\n" +
		"cat > " + shellQuote(stdinLog) + "\n" +
		"printf '%s' " + shellQuote(stdoutText) + "\n" +
		"printf '%s' " + shellQuote(stderrText) + " >&2\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return path, argvLog, stdinLog
}

func mustParseAuthkeysSeedKey(t *testing.T, line string) authkeys.AuthorizedKey {
	t.Helper()
	key, err := authkeys.ParsePublicKeyLine(line)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine: %v", err)
	}
	return key
}
