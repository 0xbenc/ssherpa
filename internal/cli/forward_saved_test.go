package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/state"
)

func TestRunForwardSavedSaveShowList(t *testing.T) {
	stateDir := t.TempDir()
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
Host bastion
  HostName bastion.example.com
`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"forward", "saved", "save", "pg",
		"--state-dir", stateDir,
		"--config", config,
		"--select", "pgbox",
		"--local", "15432",
		"--remote", "127.0.0.1:5432",
		"--through", "bastion",
		"--description", "postgres",
		"--yes",
	}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run save returned %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	assertContains(t, stdout.String(), `forward saved as "pg"`)

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"forward", "saved", "show", "pg", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run show returned %d; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "ssh-alias   pgbox")
	assertContains(t, stdout.String(), "through     bastion")
	assertContains(t, stdout.String(), "description postgres")

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"forward", "saved", "list", "--state-dir", stateDir, "--json"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run list returned %d; stderr=%q", code, stderr.String())
	}
	var specs []state.StoredForward
	if err := json.Unmarshal(stdout.Bytes(), &specs); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, stdout.String())
	}
	if len(specs) != 1 || specs[0].Name != "pg" || specs[0].Description != "postgres" {
		t.Fatalf("specs = %+v", specs)
	}
}

func TestRunForwardSavedEditDeleteRename(t *testing.T) {
	stateDir := t.TempDir()
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
Host redisbox
  HostName redisbox.example.com
`)
	if err := state.WriteForward(stateDir, state.StoredForward{
		Name:        "pg",
		SSHAlias:    "pgbox",
		LocalBind:   "127.0.0.1",
		LocalPort:   15432,
		RemoteHost:  "127.0.0.1",
		RemotePort:  5432,
		Description: "old",
	}); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}
	created := mustReadForward(t, stateDir, "pg").CreatedAt

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"forward", "saved", "edit", "pg",
		"--state-dir", stateDir,
		"--config", config,
		"--select", "redisbox",
		"--local", "16379",
		"--remote", "127.0.0.1:6379",
		"--clear-description",
	}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run edit returned %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	edited := mustReadForward(t, stateDir, "pg")
	if edited.SSHAlias != "redisbox" || edited.LocalPort != 16379 || edited.Description != "" {
		t.Fatalf("edited = %+v", edited)
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"forward", "saved", "rename", "pg", "redis", "--state-dir", stateDir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run rename returned %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	renamed := mustReadForward(t, stateDir, "redis")
	if !renamed.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt = %s, want %s", renamed.CreatedAt, created)
	}
	if _, err := state.ReadForward(stateDir, "pg"); err == nil {
		t.Fatalf("old forward still exists after rename")
	}

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"forward", "saved", "delete", "redis", "--state-dir", stateDir, "--yes"}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run delete returned %d; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := state.ReadForward(stateDir, "redis"); err == nil {
		t.Fatalf("forward still exists after delete")
	}
}

func TestRunForwardSavedSaveValidatesAlias(t *testing.T) {
	stateDir := t.TempDir()
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"forward", "saved", "save", "bad",
		"--state-dir", stateDir,
		"--config", config,
		"--select", "missing",
		"--local", "15432",
		"--remote", "127.0.0.1:5432",
		"--yes",
	}, &stdout, &stderr, BuildInfo{})
	if code != 2 {
		t.Fatalf("Run returned %d, want 2; stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), `alias "missing" not found`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func mustReadForward(t *testing.T, stateDir string, name string) state.StoredForward {
	t.Helper()
	spec, err := state.ReadForward(stateDir, name)
	if err != nil {
		t.Fatalf("ReadForward(%s): %v", name, err)
	}
	return spec
}
