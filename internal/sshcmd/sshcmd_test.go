package sshcmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveUsesExplicitSSHBinary(t *testing.T) {
	cmd := Resolve(ResolveOptions{SSHBinary: "/tmp/fake-ssh"})

	if got := strings.Join(cmd.Argv, "\x00"); got != "/tmp/fake-ssh" {
		t.Fatalf("Argv = %#v", cmd.Argv)
	}
}

func TestResolveUsesEnvSSHBinary(t *testing.T) {
	cmd := Resolve(ResolveOptions{Env: []string{"SSHERPA_SSH_BINARY=/tmp/env-ssh"}})

	if got := strings.Join(cmd.Argv, "\x00"); got != "/tmp/env-ssh" {
		t.Fatalf("Argv = %#v", cmd.Argv)
	}
}

func TestResolvePrefersKittenInsideKitty(t *testing.T) {
	cmd := Resolve(ResolveOptions{
		Env: []string{"TERM=xterm-kitty"},
		LookPath: func(name string) (string, error) {
			if name == "kitten" {
				return "/usr/bin/kitten", nil
			}
			return "", os.ErrNotExist
		},
	})

	if got := strings.Join(cmd.Argv, "\x00"); got != "kitten\x00ssh" {
		t.Fatalf("Argv = %#v", cmd.Argv)
	}
}

func TestResolveCanDisableKitty(t *testing.T) {
	cmd := Resolve(ResolveOptions{
		NoKitty: true,
		Env:     []string{"TERM=xterm-kitty"},
		LookPath: func(name string) (string, error) {
			return "/usr/bin/" + name, nil
		},
	})

	if got := strings.Join(cmd.Argv, "\x00"); got != "ssh" {
		t.Fatalf("Argv = %#v", cmd.Argv)
	}
}

func TestBuildDirect(t *testing.T) {
	cmd := BuildDirect(Command{Argv: []string{"ssh"}}, "prod", []string{"-L", "8080:localhost:8080"})

	want := "ssh\x00prod\x00-L\x008080:localhost:8080"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestQuoteArgv(t *testing.T) {
	got := QuoteArgv([]string{"ssh", "prod web", "quote'key"})
	want := "ssh 'prod web' 'quote'\\''key'"

	if got != want {
		t.Fatalf("QuoteArgv = %q, want %q", got, want)
	}
}

func TestRunDirectPropagatesExitCode(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-ssh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 7\n"), 0o700); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}

	var stderr bytes.Buffer
	code := RunDirect(Command{Argv: []string{script}}, nil, nil, &stderr)

	if code != 7 {
		t.Fatalf("RunDirect returned %d, want 7; stderr = %q", code, stderr.String())
	}
}
