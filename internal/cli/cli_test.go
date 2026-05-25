package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{"version"}, &stdout, &stderr, BuildInfo{
		Version: "1.2.3",
		Commit:  "abc123",
		Date:    "2026-05-24T23:59:00Z",
	})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	want := "ssherpa 1.2.3\ncommit: abc123\nbuilt: 2026-05-24T23:59:00Z\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunVersionDefaults(t *testing.T) {
	var stdout bytes.Buffer

	code := Run([]string{"--version"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}

	want := "ssherpa dev\ncommit: none\nbuilt: unknown\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestRunHelpForNoArgs(t *testing.T) {
	var stdout bytes.Buffer

	code := Run(nil, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "Usage:")
	assertContains(t, stdout.String(), "Phase 1:")
}

func TestRunHelpCommand(t *testing.T) {
	var stdout bytes.Buffer

	code := Run([]string{"help"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "Available Commands:")
}

func TestRunListJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
  User alice

Host gitbox
  HostName git.example.com
  User git

Host *.example.com
  User wildcard
`)

	code := Run([]string{"list", "--json", "--config", config}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}

	var got struct {
		Aliases []struct {
			Name     string `json:"name"`
			HostName string `json:"hostname"`
			User     string `json:"user"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v\n%s", err, stdout.String())
	}

	if len(got.Aliases) != 1 {
		t.Fatalf("aliases = %#v, want only non-pattern non-git alias", got.Aliases)
	}
	if got.Aliases[0].Name != "prod" || got.Aliases[0].HostName != "prod.example.com" || got.Aliases[0].User != "alice" {
		t.Fatalf("alias = %#v, want prod target", got.Aliases[0])
	}
}

func TestRunListAllCanIncludeGitAndPatterns(t *testing.T) {
	t.Setenv("SSHERPA_IGNORE_USER_GIT", "0")

	var stdout bytes.Buffer
	config := writeConfig(t, `
Host gitbox
  HostName git.example.com
  User git

Host *.example.com
  User wildcard
`)

	code := Run([]string{"list", "--json", "--all", "--config=" + config}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}

	var got struct {
		Aliases []struct {
			Name string `json:"name"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v\n%s", err, stdout.String())
	}

	names := make([]string, 0, len(got.Aliases))
	for _, alias := range got.Aliases {
		names = append(names, alias.Name)
	}
	if strings.Join(names, ",") != "gitbox,*.example.com" {
		t.Fatalf("names = %#v, want gitbox and pattern", names)
	}
}

func TestRunShowJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
  User alice
`)

	code := Run([]string{"show", "prod", "--json", "--config", config}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}

	var got struct {
		Alias *struct {
			Name     string `json:"name"`
			HostName string `json:"hostname"`
		} `json:"alias"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v\n%s", err, stdout.String())
	}
	if got.Alias == nil || got.Alias.Name != "prod" || got.Alias.HostName != "prod.example.com" {
		t.Fatalf("alias = %#v, want prod", got.Alias)
	}
}

func TestRunShowJSONCanShowGitUserAlias(t *testing.T) {
	var stdout bytes.Buffer
	config := writeConfig(t, `
Host gitbox
  HostName git.example.com
  User git
`)

	code := Run([]string{"show", "gitbox", "--json", "--config", config}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}

	var got struct {
		Alias *struct {
			Name string `json:"name"`
			User string `json:"user"`
		} `json:"alias"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v\n%s", err, stdout.String())
	}
	if got.Alias == nil || got.Alias.Name != "gitbox" || got.Alias.User != "git" {
		t.Fatalf("alias = %#v, want gitbox", got.Alias)
	}
}

func TestRunShowMissingJSONReturnsTwo(t *testing.T) {
	var stdout bytes.Buffer
	config := writeConfig(t, `Host prod
  HostName prod.example.com
`)

	code := Run([]string{"show", "missing", "--json", "--config", config}, &stdout, nil, BuildInfo{})

	if code != 2 {
		t.Fatalf("Run returned %d, want 2", code)
	}
	assertContains(t, stdout.String(), `"alias": null`)
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{"bogus"}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	assertContains(t, stderr.String(), `unknown command or flag "bogus"`)
	assertContains(t, stderr.String(), "Usage:")
}

func TestRunRejectsExtraVersionArgs(t *testing.T) {
	var stderr bytes.Buffer

	code := Run([]string{"version", "extra"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "version does not accept arguments: extra")
}

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(strings.TrimLeft(contents, "\n")), 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	return path
}

func assertContains(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want substring %q", got, want)
	}
}
