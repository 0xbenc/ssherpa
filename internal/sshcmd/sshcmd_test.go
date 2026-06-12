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

// TestPassthroughBuildersOmitEndOfOptions pins the passthrough contract for
// the builders whose argv carries user SSH args after the destination
// (`ssherpa --select prod -- -L 8080:localhost:8080`): the argv must be
// [...options..., alias, sshArgs...] with NO "--" anywhere. OpenSSH stops
// option permutation at "--" (verified: `ssh -G -- prod -L 8080:...` reports
// zero localforward entries), so inserting one would silently turn
// passthrough flags into a remote command. Dash-prefixed aliases are instead
// rejected by ValidateDestination and filtered from the inventory.
func TestPassthroughBuildersOmitEndOfOptions(t *testing.T) {
	passthrough := []string{"-L", "8080:localhost:8080"}

	cases := []struct {
		name string
		argv []string
	}{
		{"direct", BuildDirect(Command{Argv: []string{"ssh"}}, "prod", passthrough).Argv},
		{"jump", BuildJump(Command{Argv: []string{"ssh"}}, "prod", []string{"bastion"}, passthrough).Argv},
		{"proxy", BuildProxy(Command{Argv: []string{"ssh"}}, "prod", "127.0.0.1", 1080, passthrough).Argv},
		{"forward", BuildForward(Command{Argv: []string{"ssh"}}, "prod", "127.0.0.1", 5432, "127.0.0.1", 5432, "", passthrough).Argv},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idxAlias := -1
			idxPassthrough := -1
			for i, arg := range tc.argv {
				switch {
				case arg == "--":
					t.Fatalf("argv contains \"--\", which breaks SSH passthrough args: %#v", tc.argv)
				case arg == "prod" && idxAlias == -1:
					idxAlias = i
				case arg == "8080:localhost:8080":
					idxPassthrough = i
				}
			}
			if idxAlias == -1 {
				t.Fatalf("argv does not contain the alias: %#v", tc.argv)
			}
			if idxPassthrough == -1 || tc.argv[idxPassthrough-1] != "-L" || idxPassthrough < idxAlias {
				t.Fatalf("passthrough -L spec not after the alias: idxAlias=%d idxPassthrough=%d argv=%#v", idxAlias, idxPassthrough, tc.argv)
			}
		})
	}
}

// TestGuardedBuildersPlaceEndOfOptionsBeforeAlias is the WP1 regression for
// the builders with no user passthrough after the destination: probe and sftp
// argv keep a positional "--" immediately before the alias so a dash-prefixed
// name from a hostile or shared ssh config can never be parsed as an option.
func TestGuardedBuildersPlaceEndOfOptionsBeforeAlias(t *testing.T) {
	const dash = "-oProxyCommand=touch /tmp/pwned"

	cases := []struct {
		name string
		argv []string
	}{
		{"probe", BuildProbe(Command{Argv: []string{"ssh"}}, dash, nil).Argv},
		{"sftp", BuildSFTP("sftp", SFTPTransfer{Alias: dash}).Argv},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idxSep := -1
			idxAlias := -1
			for i, arg := range tc.argv {
				switch {
				case arg == "--" && idxSep == -1:
					idxSep = i
				case arg == dash && idxAlias == -1:
					idxAlias = i
				}
			}
			if idxSep == -1 {
				t.Fatalf("argv has no -- guard: %#v", tc.argv)
			}
			if idxAlias == -1 {
				t.Fatalf("argv does not contain the dash alias: %#v", tc.argv)
			}
			if idxAlias != idxSep+1 {
				t.Fatalf("dash alias not immediately after --: idxSep=%d idxAlias=%d argv=%#v", idxSep, idxAlias, tc.argv)
			}
		})
	}
}

// TestValidateDestination covers the route-layer gate that replaces the "--"
// guard for the passthrough builders: dash-prefixed and control-character
// destinations are rejected before any argv is assembled.
func TestValidateDestination(t *testing.T) {
	valid := []string{"prod", "prod.example.com", "user@10.0.0.1", "a-b_c.d"}
	for _, name := range valid {
		if err := ValidateDestination(name); err != nil {
			t.Fatalf("ValidateDestination(%q) = %v, want nil", name, err)
		}
	}

	invalid := []struct {
		name string
		want string
	}{
		{"", "cannot be empty"},
		{"   ", "cannot be empty"},
		{"-", "would be parsed as an ssh option"},
		{"-oProxyCommand=touch /tmp/pwned", "would be parsed as an ssh option"},
		{"-L8080:localhost:8080", "would be parsed as an ssh option"},
		{"prod\x1b[2J", "control characters"},
		{"prod\nevil", "control characters"},
		{"prod\x7f", "control characters"},
	}
	for _, tc := range invalid {
		err := ValidateDestination(tc.name)
		if err == nil {
			t.Fatalf("ValidateDestination(%q) = nil, want error containing %q", tc.name, tc.want)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("ValidateDestination(%q) = %q, want substring %q", tc.name, err, tc.want)
		}
	}
}

func TestBuildJump(t *testing.T) {
	cmd := BuildJump(Command{Argv: []string{"ssh"}}, "prod", []string{"bastion", "edge"}, []string{"-A"})

	want := "ssh\x00-J\x00bastion,edge\x00prod\x00-A"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithSessionEnvForwarding(t *testing.T) {
	cmd := WithSessionEnvForwarding(Command{Argv: []string{"ssh", "-J", "bastion", "prod"}})

	want := "ssh\x00-o\x00SendEnv=SSHERPA_*\x00-J\x00bastion\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithSessionEnvForwardingKeepsKittyPrefix(t *testing.T) {
	cmd := WithSessionEnvForwarding(Command{Argv: []string{"kitten", "ssh", "prod"}})

	want := "kitten\x00ssh\x00-o\x00SendEnv=SSHERPA_*\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithSessionEnvForwardingIsIdempotent(t *testing.T) {
	cmd := WithSessionEnvForwarding(Command{Argv: []string{"ssh", "-o", "SendEnv=SSHERPA_*", "prod"}})

	want := "ssh\x00-o\x00SendEnv=SSHERPA_*\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithControlMaster(t *testing.T) {
	cmd := WithControlMaster(Command{Argv: []string{"ssh", "-J", "bastion", "prod"}}, "/tmp/ssherpa.sock")

	want := "ssh\x00-o\x00ControlMaster=auto\x00-o\x00ControlPath=/tmp/ssherpa.sock\x00-o\x00ControlPersist=10m\x00-J\x00bastion\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithControlMasterKeepsKittyPrefix(t *testing.T) {
	cmd := WithControlMaster(Command{Argv: []string{"kitten", "ssh", "prod"}}, "/tmp/ssherpa.sock")

	want := "kitten\x00ssh\x00-o\x00ControlMaster=auto\x00-o\x00ControlPath=/tmp/ssherpa.sock\x00-o\x00ControlPersist=10m\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithControlMasterSkipsExistingControlPathAndNonSSHCommands(t *testing.T) {
	withExisting := WithControlMaster(Command{Argv: []string{"ssh", "-o", "ControlPath=/tmp/old.sock", "prod"}}, "/tmp/new.sock")
	if got, want := strings.Join(withExisting.Argv, "\x00"), "ssh\x00-o\x00ControlPath=/tmp/old.sock\x00prod"; got != want {
		t.Fatalf("existing Argv = %#v, want %q", withExisting.Argv, want)
	}

	nonSSH := WithControlMaster(Command{Argv: []string{"sh", "-c", "true"}}, "/tmp/new.sock")
	if got, want := strings.Join(nonSSH.Argv, "\x00"), "sh\x00-c\x00true"; got != want {
		t.Fatalf("non-ssh Argv = %#v, want %q", nonSSH.Argv, want)
	}
}

func TestWithConnectTimeout(t *testing.T) {
	cmd := WithConnectTimeout(Command{Argv: []string{"ssh", "prod"}}, 10)

	want := "ssh\x00-o\x00ConnectTimeout=10\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithConnectTimeoutKeepsKittyPrefix(t *testing.T) {
	cmd := WithConnectTimeout(Command{Argv: []string{"kitten", "ssh", "prod"}}, 10)

	want := "kitten\x00ssh\x00-o\x00ConnectTimeout=10\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestWithConnectTimeoutSkipsExistingTimeoutAndNonSSHCommands(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string
	}{
		{
			name: "separate -o",
			argv: []string{"ssh", "-o", "ConnectTimeout=3", "prod"},
			want: "ssh\x00-o\x00ConnectTimeout=3\x00prod",
		},
		{
			name: "compact -o",
			argv: []string{"ssh", "-oConnectTimeout=4", "prod"},
			want: "ssh\x00-oConnectTimeout=4\x00prod",
		},
		{
			name: "non ssh",
			argv: []string{"sh", "-c", "true"},
			want: "sh\x00-c\x00true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := WithConnectTimeout(Command{Argv: tt.argv}, 10)
			if got := strings.Join(cmd.Argv, "\x00"); got != tt.want {
				t.Fatalf("Argv = %#v, want %q", cmd.Argv, tt.want)
			}
		})
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

	want := "ssh\x00-o\x00BatchMode=yes\x00-o\x00ConnectTimeout=5\x00-J\x00bastion,edge\x00--\x00prod\x00true"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestBuildSFTP(t *testing.T) {
	cmd := BuildSFTP("sftp", SFTPTransfer{Alias: "prod", Config: "/tmp/ssh_config"})

	want := "sftp\x00-b\x00-\x00-F\x00/tmp/ssh_config\x00--\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestBuildSFTPWithJumpHops(t *testing.T) {
	cmd := BuildSFTP("sftp", SFTPTransfer{Alias: "prod", Config: "/tmp/ssh_config", Hops: []string{"bastion", "edge"}})

	want := "sftp\x00-b\x00-\x00-F\x00/tmp/ssh_config\x00-J\x00bastion,edge\x00--\x00prod"
	if got := strings.Join(cmd.Argv, "\x00"); got != want {
		t.Fatalf("Argv = %#v, want %q", cmd.Argv, want)
	}
}

func TestBuildSFTPWithControlPath(t *testing.T) {
	cmd := BuildSFTP("sftp", SFTPTransfer{Alias: "prod", Config: "/tmp/ssh_config", ControlPath: "/tmp/ssherpa.sock"})

	want := "sftp\x00-b\x00-\x00-o\x00ControlMaster=auto\x00-o\x00ControlPath=/tmp/ssherpa.sock\x00-F\x00/tmp/ssh_config\x00--\x00prod"
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

func TestValidateCommandBinaryUsesLookPath(t *testing.T) {
	err := ValidateCommandBinary(Command{Argv: []string{"ssh"}}, BinaryRequirement{
		Name: "ssh",
		Role: "SSH client",
		LookPath: func(name string) (string, error) {
			if name != "ssh" {
				t.Fatalf("LookPath called with %q, want ssh", name)
			}
			return "/usr/bin/ssh", nil
		},
	})
	if err != nil {
		t.Fatalf("ValidateCommandBinary returned error: %v", err)
	}
}

func TestValidateCommandBinaryReportsMissingPathLookup(t *testing.T) {
	err := ValidateCommandBinary(Command{Argv: []string{"ssh"}}, BinaryRequirement{
		Name: "ssh",
		Role: "SSH client",
		Hint: OpenSSHClientInstallHint,
		LookPath: func(string) (string, error) {
			return "", os.ErrNotExist
		},
	})
	if err == nil {
		t.Fatal("ValidateCommandBinary returned nil, want missing binary error")
	}
	assertErrorContains(t, err, "SSH client")
	assertErrorContains(t, err, "not found in PATH")
	assertErrorContains(t, err, "OpenSSH client")
}

func TestValidateCommandBinaryReportsSourceForMissingFlagPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-ssh")

	err := ValidateCommandBinary(Command{Argv: []string{missing}}, BinaryRequirement{
		Name: "ssh",
		Role: "SSH client",
		Flag: "--ssh-binary",
		Hint: OpenSSHClientInstallHint,
	})
	if err == nil {
		t.Fatal("ValidateCommandBinary returned nil, want missing binary error")
	}
	assertErrorContains(t, err, "from --ssh-binary")
	assertErrorContains(t, err, "does not exist")
	assertErrorContains(t, err, missing)
}

func TestValidateCommandBinaryReportsSourceForMissingEnvBinary(t *testing.T) {
	err := ValidateCommandBinary(Command{Argv: []string{"missing-env-ssh"}}, BinaryRequirement{
		Name:   "ssh",
		Role:   "SSH client",
		EnvVar: "SSHERPA_SSH_BINARY",
		Hint:   OpenSSHClientInstallHint,
		LookPath: func(string) (string, error) {
			return "", os.ErrNotExist
		},
	})
	if err == nil {
		t.Fatal("ValidateCommandBinary returned nil, want missing binary error")
	}
	assertErrorContains(t, err, "from SSHERPA_SSH_BINARY")
	assertErrorContains(t, err, "not found in PATH")
}

func TestValidateCommandBinaryRejectsDirectoryAndNonExecutablePath(t *testing.T) {
	dir := t.TempDir()
	if err := ValidateCommandBinary(Command{Argv: []string{dir}}, BinaryRequirement{Name: "ssh", Role: "SSH client"}); err == nil || !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("directory error = %v, want directory message", err)
	}

	file := filepath.Join(dir, "not-executable")
	if err := os.WriteFile(file, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := ValidateCommandBinary(Command{Argv: []string{file}}, BinaryRequirement{Name: "ssh", Role: "SSH client"}); err == nil || !strings.Contains(err.Error(), "not executable") {
		t.Fatalf("non-executable error = %v, want executable message", err)
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

func assertErrorContains(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want substring %q", err, want)
	}
}
