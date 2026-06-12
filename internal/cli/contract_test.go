package cli

// Contract-stabilization tests (audit WP10): per-command help topics,
// JSON schema_version envelopes, check exit-code unification and
// MESSAGE population, list empty-state hints, and theme-config
// forward compatibility surfacing.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
)

func TestRunHelpTopicPrintsPerCommandUsage(t *testing.T) {
	cases := []struct {
		topic   string
		want    []string
		exclude string
	}{
		{"add", []string{"ssherpa add --alias NAME --host HOST", "--dry-run"}, "Available Commands:"},
		{"edit", []string{"ssherpa edit set ALIAS", "delete-all"}, "Available Commands:"},
		{"jump", []string{"ssherpa jump --dest DEST --hop HOP"}, "Available Commands:"},
		{"proxy", []string{"ssherpa proxy saved rename OLD NEW"}, "Available Commands:"},
		{"forward", []string{"ssherpa forward saved save NAME --select ALIAS"}, "Available Commands:"},
		{"send", []string{"ssherpa send LOCAL_FILE --select ALIAS"}, "Available Commands:"},
		{"receive", []string{"ssherpa receive REMOTE_PATH --select ALIAS", "alias for"}, "Available Commands:"},
		{"recv", []string{"ssherpa receive REMOTE_PATH --select ALIAS"}, "Available Commands:"},
		{"check", []string{"ssherpa check --saved-forwards [--json]"}, "Available Commands:"},
		{"incoming", []string{"ssherpa incoming hook"}, "Available Commands:"},
		{"authkeys", []string{"--all-matching"}, "Available Commands:"},
		{"theme", []string{"ssherpa theme"}, "Available Commands:"},
		{"list", []string{"ssherpa list [--json]", "SSHERPA_IGNORE_USER_GIT"}, "Available Commands:"},
		{"show", []string{"ssherpa show ALIAS"}, "Available Commands:"},
		{"connect", []string{"--overlay-key", "--composer-key", "--latency-warn"}, "Available Commands:"},
		{"version", []string{"ssherpa version"}, "Available Commands:"},
		{"help", []string{"ssherpa help [COMMAND]"}, "Available Commands:"},
		// The session topic must list every dispatched subcommand —
		// the old global help listed 5 of 12.
		{"session", []string{
			"session list", "session map", "session show", "session log",
			"session replay", "session grep", "session export",
			"session bundle export", "session bundle import",
			"session identity", "session browse", "session stop-all",
			"session prune",
		}, "Available Commands:"},
	}
	for _, c := range cases {
		t.Run(c.topic, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run([]string{"help", c.topic}, &stdout, &stderr, BuildInfo{})
			if code != 0 {
				t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
			}
			for _, want := range c.want {
				assertContains(t, stdout.String(), want)
			}
			if c.exclude != "" && strings.Contains(stdout.String(), c.exclude) {
				t.Fatalf("help %s printed the global overview:\n%s", c.topic, stdout.String())
			}
		})
	}
}

func TestRunHelpUnknownTopicErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"help", "bogus"}, &stdout, &stderr, BuildInfo{})
	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), `unknown help topic "bogus"`)
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestRunHelpRejectsMultipleTopics(t *testing.T) {
	var stderr bytes.Buffer
	code := Run([]string{"help", "add", "edit"}, nil, &stderr, BuildInfo{})
	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "help accepts at most one command")
}

// TestRunSubcommandHelpFlagPrintsTopicUsage pins the second half of the
// help contract: `ssherpa COMMAND --help` prints the same per-command
// block as `ssherpa help COMMAND`, not the 124-line global dump.
func TestRunSubcommandHelpFlagPrintsTopicUsage(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"add", "--help"}, "ssherpa add --alias NAME --host HOST"},
		{[]string{"list", "-h"}, "ssherpa list [--json]"},
		{[]string{"check", "--help"}, "ssherpa check --saved-forwards [--json]"},
		{[]string{"forward", "saved", "--help"}, "ssherpa forward saved save NAME"},
		{[]string{"session", "replay", "--help"}, "ssherpa session replay SESSION_ID"},
		{[]string{"recv", "--help"}, "ssherpa receive REMOTE_PATH"},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.args, " "), func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(c.args, &stdout, &stderr, BuildInfo{})
			if code != 0 {
				t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
			}
			assertContains(t, stdout.String(), c.want)
			if strings.Contains(stdout.String(), "Available Commands:") {
				t.Fatalf("%v printed the global overview:\n%s", c.args, stdout.String())
			}
		})
	}
}

func TestRunConnectHelpFlagPrintsConnectUsage(t *testing.T) {
	var stdout bytes.Buffer
	code := Run([]string{"--print", "--help"}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "--overlay-key")
	assertContains(t, stdout.String(), "--select ALIAS")
	if strings.Contains(stdout.String(), "Available Commands:") {
		t.Fatalf("connect --help printed the global overview:\n%s", stdout.String())
	}
}

func TestRunListJSONIncludesSchemaVersion(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	var stdout bytes.Buffer
	code := Run([]string{"list", "--json", "--config", config}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	var got struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
	}
}

func TestRunShowJSONIncludesSchemaVersion(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	var stdout bytes.Buffer
	code := Run([]string{"show", "prod", "--json", "--config", config}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	var got struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
	}
}

func TestRunCheckJSONIncludesSchemaVersion(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 0)
	var stdout bytes.Buffer
	code := Run([]string{"check", "prod", "--config", config, "--ssh-binary", fakeSSH, "--no-icmp", "--json"}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	var got struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if got.SchemaVersion != 1 {
		t.Fatalf("schema_version = %d, want 1", got.SchemaVersion)
	}
}

// TestRunListEmptyInventoryPrintsHint pins the list empty-state
// contract: zero hosts (missing config, unreadable config, or simply
// no Host stanzas) prints a one-line actionable hint on stderr while
// the exit code stays 0 for scripts.
func TestRunListEmptyInventoryPrintsHint(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-config")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"list", "--config", missing}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0 (exit code unchanged)", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	assertContains(t, stderr.String(), "no hosts found in "+missing)
	assertContains(t, stderr.String(), `run "ssherpa add"`)
}

func TestRunListEmptyFilterPrintsFilterHint(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	var stdout, stderr bytes.Buffer
	code := Run([]string{"list", "--config", config, "--filter", "nomatch"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stderr.String(), "no hosts matched the current filter")
}

func TestRunListJSONEmptyInventoryStaysSilent(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-config")
	var stdout, stderr bytes.Buffer
	code := Run([]string{"list", "--json", "--config", missing}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for --json consumers", stderr.String())
	}
	assertContains(t, stdout.String(), `"aliases": []`)
}

// TestRunCheckInventoryLoadFailureExitsTwo pins the exit-code
// unification: check's inventory-load failure exits 2 like list/show
// and every other inventory consumer, not the old check-only 3.
func TestRunCheckInventoryLoadFailureExitsTwo(t *testing.T) {
	t.Setenv("HOME", "")

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "prod", "--no-icmp"}, &stdout, &stderr, BuildInfo{})

	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "home directory")
}

// TestRunCheckSavedForwardsListFailureExitsOne pins the second half of
// removing exit 3: state-layer failures exit 1, matching forward list.
func TestRunCheckSavedForwardsListFailureExitsOne(t *testing.T) {
	stateDir := t.TempDir()
	// `forwards` as a regular file makes the catalog listing fail with
	// ENOTDIR — a real read failure rather than the tolerated ENOENT.
	if err := os.WriteFile(filepath.Join(stateDir, "forwards"), []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 0)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "--saved-forwards", "--state-dir", stateDir, "--config", config, "--ssh-binary", fakeSSH, "--no-icmp"}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "list saved forwards")
}

// TestRunCheckFailureCapturesSSHStderrTail drives a failing probe end
// to end with a fake ssh that prints a resolution error on stderr.
// Both ssh_error and message must carry the stderr tail — before this
// fix, ssh stderr was discarded and MESSAGE rendered empty.
func TestRunCheckFailureCapturesSSHStderrTail(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	dir := t.TempDir()
	fakeSSH := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\n" +
		"echo 'debug noise' >&2\n" +
		"echo 'ssh: Could not resolve hostname prod.example.com: nodename nor servname provided' >&2\n" +
		"exit 255\n"
	if err := os.WriteFile(fakeSSH, []byte(script), 0o700); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{"check", "prod", "--config", config, "--ssh-binary", fakeSSH, "--no-icmp", "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stderr=%q", code, stderr.String())
	}
	var out checkOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if len(out.Results) != 1 {
		t.Fatalf("results = %+v, want one", out.Results)
	}
	result := out.Results[0]
	if !strings.Contains(result.SSHError, "Could not resolve hostname") {
		t.Fatalf("ssh_error = %q, want ssh stderr tail", result.SSHError)
	}
	if strings.Contains(result.SSHError, "debug noise") {
		t.Fatalf("ssh_error = %q, want only the last stderr line", result.SSHError)
	}
	if result.Message != result.SSHError {
		t.Fatalf("message = %q, want backfilled from ssh_error %q", result.Message, result.SSHError)
	}

	// The human table renders the same message in the MESSAGE column.
	stdout.Reset()
	code = Run([]string{"check", "prod", "--config", config, "--ssh-binary", fakeSSH, "--no-icmp"}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2", code)
	}
	assertContains(t, stdout.String(), "Could not resolve hostname")
}

func TestStderrTail(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"\n\n", ""},
		{"one line\n", "one line"},
		{"first\nlast\n", "last"},
		{"first\nlast\n\n  \n", "last"},
		{"no newline", "no newline"},
	}
	for _, c := range cases {
		if got := stderrTail(c.in); got != c.want {
			t.Fatalf("stderrTail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFillCheckMessage(t *testing.T) {
	cases := []struct {
		name string
		in   checkResult
		want string
	}{
		{"keeps existing message", checkResult{Status: "invalid", Message: "alias not found", SSHError: "x"}, "alias not found"},
		{"ok stays empty", checkResult{Status: "ok"}, ""},
		{"failed uses ssh error", checkResult{Status: "failed", SSHError: "ssh exited 255"}, "ssh exited 255"},
		{"failed bind in use", checkResult{Status: "failed", LocalBindStatus: "in_use"}, "local bind in use"},
		{"failed icmp", checkResult{Status: "failed", ICMPStatus: "failed"}, "icmp probe failed"},
		{"failed with nothing known", checkResult{Status: "failed"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := fillCheckMessage(c.in).Message; got != c.want {
				t.Fatalf("Message = %q, want %q", got, c.want)
			}
		})
	}
}

// TestCheckProbeTimeoutMessage pins the timeout path: a probe killed by
// the deadline still produces a non-empty MESSAGE.
func TestCheckProbeTimeoutMessage(t *testing.T) {
	oldProbe := runSSHCheckProbe
	runSSHCheckProbe = func(cmd sshcmd.Command, timeout time.Duration) sshProbeResult {
		return sshProbeResult{Duration: timeout, ExitCode: 124, Err: context.DeadlineExceeded}
	}
	t.Cleanup(func() { runSSHCheckProbe = oldProbe })

	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 0)
	var stdout bytes.Buffer
	code := Run([]string{"check", "prod", "--config", config, "--ssh-binary", fakeSSH, "--no-icmp", "--json"}, &stdout, nil, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2", code)
	}
	var out checkOutput
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if out.Results[0].Message == "" {
		t.Fatalf("message = empty, want timeout description; result=%+v", out.Results[0])
	}
}

// TestReportThemeWarningsSurfacesUnknownKeys pins the stderr-on-load
// half of the theme forward-compat contract.
func TestReportThemeWarningsSurfacesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.conf")
	if err := os.WriteFile(path, []byte("primary = magenta\nhyperlink = blue\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	var stderr bytes.Buffer
	reportThemeWarnings(path, &stderr)

	assertContains(t, stderr.String(), path)
	assertContains(t, stderr.String(), `unknown theme role "hyperlink"`)

	// Clean configs and missing files stay silent.
	stderr.Reset()
	reportThemeWarnings(filepath.Join(dir, "missing.conf"), &stderr)
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty for missing file", stderr.String())
	}
}

// TestRunConnectPrintReportsThemeWarnings drives the same surfacing
// through a real headless invocation.
func TestRunConnectPrintReportsThemeWarnings(t *testing.T) {
	dir := t.TempDir()
	themeFile := filepath.Join(dir, "theme.conf")
	if err := os.WriteFile(themeFile, []byte("primary = magenta\nfrom-the-future = blue\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{"--print", "--select", "prod", "--config", config, "--theme-file", themeFile}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), `unknown theme role "from_the_future"`)
	assertContains(t, stdout.String(), "[print]")
}

// TestLoadThemeConfigSurfacesUnknownKeyWarnings covers the theme-editor
// path: unknown keys load with a visible warning instead of nuking the
// whole config.
func TestLoadThemeConfigSurfacesUnknownKeyWarnings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "theme.conf")
	if err := os.WriteFile(path, []byte("primary = magenta\nhyperlink = blue\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}

	cfg, warning, err := loadThemeConfig(path)
	if err != nil {
		t.Fatalf("loadThemeConfig returned error: %v", err)
	}
	if !strings.Contains(warning, `unknown theme role "hyperlink"`) {
		t.Fatalf("warning = %q, want unknown-key notice", warning)
	}
	if cfg.Specs == nil || cfg.Specs["primary"] != "magenta" {
		t.Fatalf("cfg = %+v, want primary spec preserved", cfg)
	}
}
