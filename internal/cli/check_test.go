package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
)

func TestRunCheckAliasJSONNoICMP(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, logPath := writeFakeSSH(t, 0)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "prod", "--config", config, "--ssh-binary", fakeSSH, "--no-icmp", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	var out checkOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if !out.OK || len(out.Results) != 1 {
		t.Fatalf("output = %+v, want one ok result", out)
	}
	got := out.Results[0]
	if got.Kind != "alias" || got.Name != "prod" || got.Status != "ok" || got.ICMPStatus != "skipped" {
		t.Fatalf("result = %+v", got)
	}
	if !strings.Contains(readFile(t, logPath), "BatchMode=yes") {
		t.Fatalf("fake ssh log missing BatchMode probe: %q", readFile(t, logPath))
	}
}

func TestRunCheckAliasFailureReturnsTwo(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 255)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "prod", "--config", config, "--ssh-binary", fakeSSH, "--no-icmp"}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertContains(t, stdout.String(), "failed")
}

func TestRunCheckEmptySelectionIsNotHealthy(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "--filter", "nomatch", "--config", config}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertContains(t, stderr.String(), "no checks matched the given selector")

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"check", "--filter", "nomatch", "--json", "--config", config}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run --json returned %d, want 2; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), `"ok": false`)
	assertContains(t, stdout.String(), `"results": []`)
}

func TestRunCheckMissingSSHBinaryReturnsStructuredFailure(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	missing := filepath.Join(t.TempDir(), "missing-ssh")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "prod", "--config", config, "--ssh-binary", missing, "--no-icmp", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	var out checkOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if out.OK || len(out.Results) != 1 {
		t.Fatalf("output = %+v, want one failed result", out)
	}
	result := out.Results[0]
	if result.Status != "failed" || !strings.Contains(result.SSHError, "from --ssh-binary") || !strings.Contains(result.Message, "does not exist") {
		t.Fatalf("result = %+v, want missing SSH binary diagnostics", result)
	}
}

func TestRunCheckSavedForwardValidatesAndChecks(t *testing.T) {
	oldLocalBind := runLocalBindCheck
	runLocalBindCheck = func(string, int) string { return "ok" }
	t.Cleanup(func() { runLocalBindCheck = oldLocalBind })

	stateDir := t.TempDir()
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 0)
	if err := state.WriteForward(stateDir, state.StoredForward{
		Name:       "pg",
		SSHAlias:   "pgbox",
		LocalBind:  "127.0.0.1",
		LocalPort:  15432,
		RemoteHost: "127.0.0.1",
		RemotePort: 5432,
	}); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "--saved-forward", "pg", "--state-dir", stateDir, "--config", config, "--ssh-binary", fakeSSH, "--no-icmp", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var out checkOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if !out.OK || out.Results[0].Kind != "saved_forward" || out.Results[0].LocalBindStatus != "ok" {
		t.Fatalf("output = %+v", out)
	}
}

func TestRunCheckSavedForwardMissingAlias(t *testing.T) {
	stateDir := t.TempDir()
	config := writeConfig(t, `
Host other
  HostName other.example.com
`)
	if err := state.WriteForward(stateDir, state.StoredForward{
		Name:       "pg",
		SSHAlias:   "pgbox",
		LocalBind:  "127.0.0.1",
		LocalPort:  5432,
		RemoteHost: "127.0.0.1",
		RemotePort: 5432,
	}); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "--saved-forward", "pg", "--state-dir", stateDir, "--config", config, "--no-icmp", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertContains(t, stdout.String(), `"status": "invalid"`)
	assertContains(t, stdout.String(), `alias \"pgbox\" not found`)
}

// TestBuildCheckProbeGuardsDashAlias is the WP1 regression for the check
// probe: a dash-prefixed alias drawn from a hostile or shared ssh config must
// be placed after a positional "--" so OpenSSH never parses it as an option.
func TestBuildCheckProbeGuardsDashAlias(t *testing.T) {
	base := sshcmd.Command{Argv: []string{"ssh"}}
	cmd := buildCheckProbe(base, "-oProxyCommand=evil", nil, 5*time.Second)

	idxSep, idxAlias := -1, -1
	for i, arg := range cmd.Argv {
		switch {
		case arg == "--" && idxSep == -1:
			idxSep = i
		case arg == "-oProxyCommand=evil" && idxAlias == -1:
			idxAlias = i
		}
	}
	if idxSep == -1 || idxAlias != idxSep+1 {
		t.Fatalf("dash alias not guarded by --: %#v", cmd.Argv)
	}
}

// TestParseCheckFlagsAcceptsDashAliasAfterSeparator confirms that "check"
// handles a "--" end-of-flags marker like every other parser: a dash-prefixed
// alias after "--" is treated as a positional, not rejected as an unknown flag.
// Before this fix `ssherpa check -- -web` failed with "unknown check flag".
func TestParseCheckFlagsAcceptsDashAliasAfterSeparator(t *testing.T) {
	var stderr bytes.Buffer
	flags, ok := parseCheckFlags([]string{"--no-icmp", "--", "-web", "-oProxyCommand=evil"}, &stderr)
	if !ok {
		t.Fatalf("parseCheckFlags returned !ok; stderr=%q", stderr.String())
	}
	if !flags.NoICMP {
		t.Fatalf("flags.NoICMP = false, want true")
	}
	want := []string{"-web", "-oProxyCommand=evil"}
	if strings.Join(flags.Positional, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("Positional = %#v, want %#v", flags.Positional, want)
	}
}

// TestRunCheckAcceptsDashAliasAfterSeparator drives a dash alias end to end:
// `check -- -web` must be parsed as a positional (not rejected as an unknown
// flag), but because dash-prefixed aliases are filtered out of the inventory
// (OpenSSH would parse them as options) the outcome is the standard
// invalid/not-found result even when the config defines the Host block — and
// no ssh probe is ever spawned for it.
func TestRunCheckAcceptsDashAliasAfterSeparator(t *testing.T) {
	config := writeConfig(t, `
Host -web
  HostName attacker.example.com

Host prod
  HostName prod.example.com
`)
	fakeSSH, logPath := writeFakeSSH(t, 0)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "--config", config, "--ssh-binary", fakeSSH, "--no-icmp", "--json", "--", "-web"}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2 (alias not found); stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "unknown check flag") {
		t.Fatalf("dash alias rejected as flag: stderr=%q", stderr.String())
	}
	var out checkOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if len(out.Results) != 1 || out.Results[0].Name != "-web" || out.Results[0].Status != "invalid" {
		t.Fatalf("output = %+v, want one invalid result named -web", out)
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("ssh probe was spawned for a dash alias: %s", readFile(t, logPath))
	}
}

func TestDefaultRunICMPCheckProbeMissingPingIsUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got := defaultRunICMPCheckProbe("prod.example.com", time.Millisecond)

	if got.Status != "unavailable" {
		t.Fatalf("Status = %q, want unavailable", got.Status)
	}
}
