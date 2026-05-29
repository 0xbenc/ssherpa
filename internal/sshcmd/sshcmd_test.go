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

func TestBuildProbe(t *testing.T) {
	cmd := BuildProbe(Command{Argv: []string{"ssh"}}, "prod", []string{"bastion", "edge"})

	want := "ssh\x00-o\x00BatchMode=yes\x00-o\x00ConnectTimeout=5\x00-J\x00bastion,edge\x00prod\x00true"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestBuildSFTP(t *testing.T) {
	cmd := BuildSFTP("sftp", SFTPTransfer{Alias: "prod", Config: "/tmp/ssh_config"})

	want := "sftp\x00-b\x00-\x00-F\x00/tmp/ssh_config\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestBuildSFTPWithJumpHops(t *testing.T) {
	cmd := BuildSFTP("sftp", SFTPTransfer{Alias: "prod", Config: "/tmp/ssh_config", Hops: []string{"bastion", "edge"}})

	want := "sftp\x00-b\x00-\x00-F\x00/tmp/ssh_config\x00-J\x00bastion,edge\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestBuildSFTPBatch(t *testing.T) {
	send := SFTPTransfer{
		Direction:  SFTPTransferSend,
		LocalPath:  "/tmp/local file.txt",
		RemotePath: "/srv/app/local file.txt",
	}
	if got, want := BuildSFTPBatch(send), "put \"/tmp/local file.txt\" \"/srv/app/local file.txt\"\n"; got != want {
		t.Fatalf("send batch = %q, want %q", got, want)
	}

	receive := SFTPTransfer{
		Direction:  SFTPTransferReceive,
		LocalPath:  "/tmp/out.txt",
		RemotePath: "/srv/app/out.txt",
	}
	if got, want := BuildSFTPBatch(receive), "get /srv/app/out.txt /tmp/out.txt\n"; got != want {
		t.Fatalf("receive batch = %q, want %q", got, want)
	}
}

func TestValidateSFTPTransfer(t *testing.T) {
	valid := SFTPTransfer{Direction: SFTPTransferSend, Alias: "prod", LocalPath: "/tmp/file", RemotePath: "file"}
	if err := ValidateSFTPTransfer(valid); err != nil {
		t.Fatalf("ValidateSFTPTransfer returned error: %v", err)
	}
	invalid := valid
	invalid.RemotePath = ""
	if err := ValidateSFTPTransfer(invalid); err == nil || !strings.Contains(err.Error(), "remote path") {
		t.Fatalf("ValidateSFTPTransfer error = %v, want remote path", err)
	}
}

func TestBuildForward(t *testing.T) {
	tests := []struct {
		name       string
		alias      string
		localBind  string
		localPort  int
		remoteHost string
		remotePort int
		through    string
		extraArgs  []string
		want       string
	}{
		{
			name:       "loopback default",
			alias:      "pgbox",
			localBind:  "127.0.0.1",
			localPort:  5432,
			remoteHost: "127.0.0.1",
			remotePort: 5432,
			want:       "ssh\x00-L\x00127.0.0.1:5432:127.0.0.1:5432\x00-N\x00-o\x00ExitOnForwardFailure=yes\x00pgbox",
		},
		{
			name:       "with through hop and passthrough",
			alias:      "pgbox",
			localBind:  "0.0.0.0",
			localPort:  5433,
			remoteHost: "db.internal",
			remotePort: 5432,
			through:    "bastion",
			extraArgs:  []string{"-v"},
			want:       "ssh\x00-J\x00bastion\x00-L\x000.0.0.0:5433:db.internal:5432\x00-N\x00-o\x00ExitOnForwardFailure=yes\x00pgbox\x00-v",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := BuildForward(Command{Argv: []string{"ssh"}}, tt.alias, tt.localBind, tt.localPort, tt.remoteHost, tt.remotePort, tt.through, tt.extraArgs)
			if got := strings.Join(cmd.Argv, "\x00"); got != tt.want {
				t.Fatalf("Argv = %#v, want %q", cmd.Argv, tt.want)
			}
		})
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

func TestValidateForward(t *testing.T) {
	tests := []struct {
		name       string
		alias      string
		localBind  string
		localPort  int
		remoteHost string
		remotePort int
		through    string
		wantErr    string
	}{
		{name: "valid", alias: "prod", localBind: "127.0.0.1", localPort: 5432, remoteHost: "127.0.0.1", remotePort: 5432},
		{name: "valid with through", alias: "prod", localBind: "127.0.0.1", localPort: 5432, remoteHost: "db", remotePort: 5432, through: "bastion"},
		{name: "missing alias", localBind: "127.0.0.1", localPort: 5432, remoteHost: "127.0.0.1", remotePort: 5432, wantErr: "alias"},
		{name: "missing local bind", alias: "prod", localPort: 5432, remoteHost: "127.0.0.1", remotePort: 5432, wantErr: "bind"},
		{name: "low local port", alias: "prod", localBind: "127.0.0.1", localPort: 0, remoteHost: "127.0.0.1", remotePort: 5432, wantErr: "local port"},
		{name: "high local port", alias: "prod", localBind: "127.0.0.1", localPort: 70000, remoteHost: "127.0.0.1", remotePort: 5432, wantErr: "local port"},
		{name: "missing remote host", alias: "prod", localBind: "127.0.0.1", localPort: 5432, remotePort: 5432, wantErr: "remote host"},
		{name: "low remote port", alias: "prod", localBind: "127.0.0.1", localPort: 5432, remoteHost: "127.0.0.1", remotePort: 0, wantErr: "remote port"},
		{name: "high remote port", alias: "prod", localBind: "127.0.0.1", localPort: 5432, remoteHost: "127.0.0.1", remotePort: 70000, wantErr: "remote port"},
		{name: "through equals alias", alias: "prod", localBind: "127.0.0.1", localPort: 5432, remoteHost: "127.0.0.1", remotePort: 5432, through: "prod", wantErr: "destination"},
		{name: "through has whitespace", alias: "prod", localBind: "127.0.0.1", localPort: 5432, remoteHost: "127.0.0.1", remotePort: 5432, through: " bastion ", wantErr: "whitespace"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateForward(tt.alias, tt.localBind, tt.localPort, tt.remoteHost, tt.remotePort, tt.through)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateForward returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ValidateForward error = %v, want substring %q", err, tt.wantErr)
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
