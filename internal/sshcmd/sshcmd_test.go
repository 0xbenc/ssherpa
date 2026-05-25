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

func TestBuildJump(t *testing.T) {
	cmd := BuildJump(Command{Argv: []string{"ssh"}}, "prod", []string{"bastion", "edge"}, []string{"-A"})

	want := "ssh\x00-J\x00bastion,edge\x00prod\x00-A"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestBuildProxy(t *testing.T) {
	cmd := BuildProxy(Command{Argv: []string{"ssh"}}, "prod", "127.0.0.1", 1080, []string{"-v"})

	want := "ssh\x00-D\x00127.0.0.1:1080\x00-C\x00-N\x00-o\x00ExitOnForwardFailure=yes\x00prod\x00-v"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestValidateJumpRoute(t *testing.T) {
	tests := []struct {
		name        string
		destination string
		hops        []string
		wantErr     string
	}{
		{name: "valid", destination: "prod", hops: []string{"bastion", "edge"}},
		{name: "missing destination", hops: []string{"bastion"}, wantErr: "destination"},
		{name: "missing hops", destination: "prod", wantErr: "hop"},
		{name: "empty hop", destination: "prod", hops: []string{"bastion", ""}, wantErr: "empty"},
		{name: "destination hop", destination: "prod", hops: []string{"prod"}, wantErr: "destination"},
		{name: "duplicate hop", destination: "prod", hops: []string{"bastion", "bastion"}, wantErr: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJumpRoute(tt.destination, tt.hops)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateJumpRoute returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateJumpRoute error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateProxy(t *testing.T) {
	tests := []struct {
		name    string
		alias   string
		bind    string
		port    int
		wantErr string
	}{
		{name: "valid", alias: "prod", bind: "127.0.0.1", port: 1080},
		{name: "missing alias", bind: "127.0.0.1", port: 1080, wantErr: "alias"},
		{name: "missing bind", alias: "prod", port: 1080, wantErr: "bind"},
		{name: "low port", alias: "prod", bind: "127.0.0.1", port: 0, wantErr: "port"},
		{name: "high port", alias: "prod", bind: "127.0.0.1", port: 70000, wantErr: "port"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProxy(tt.alias, tt.bind, tt.port)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateProxy returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateProxy error = %v, want substring %q", err, tt.wantErr)
			}
		})
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
