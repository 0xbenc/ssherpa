package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestDefaultRunICMPCheckProbeMissingPingIsUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got := defaultRunICMPCheckProbe("prod.example.com", time.Millisecond)

	if got.Status != "unavailable" {
		t.Fatalf("Status = %q, want unavailable", got.Status)
	}
}
