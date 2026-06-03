package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/authkeys"
	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/session"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

const (
	testEd25519Key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example"
	testECDSAKey   = "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBDxfAByeMchlvCAqslVGYuzLS4lr02wvFIn2rz4Jp40NrbYkbazkdAtflVPDCCewMSI2I0ujG0JJeZEjYarX8sI= ecdsa@example"
)

// TestMain keeps the CLI tests hermetic with respect to terminal detection.
// resolveSSHCommand reads the ambient environment, so a developer running the
// suite inside Kitty (KITTY_PID/KITTY_WINDOW_ID set) would otherwise resolve
// "kitten ssh" instead of plain "ssh" and break the [print] assertions. Kitty
// detection itself is covered hermetically in the sshcmd package; here we just
// pin the base ssh command. SSHERPA_NO_KITTY is honored by sshcmd.Resolve.
func TestMain(m *testing.M) {
	os.Setenv("SSHERPA_NO_KITTY", "1")
	os.Exit(m.Run())
}

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

func TestRunIncomingHook(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Run([]string{"incoming", "hook", "--shell", "zsh"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "ssherpa incoming mark --watch-parent")
	assertContains(t, stdout.String(), "$SSH_TTY")
}

func TestRunIncomingMarkWritesMarker(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USER", "ben")
	t.Setenv("SSH_TTY", "/dev/pts/9")
	t.Setenv("SSH_CLIENT", "192.168.1.50 51234 22")
	t.Setenv("SSHERPA_SESSION_ID", "session-1")
	t.Setenv("SSHERPA_ROUTE", "laptop,prod")

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"incoming", "mark", "--runtime-dir", dir}, &stdout, &stderr, BuildInfo{})
	if code != 0 {
		t.Fatalf("Run returned %d; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "incoming marker:")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir marker dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("marker entries = %d, want 1", len(entries))
	}
	data, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	assertContains(t, string(data), `"tty": "pts/9"`)
	assertContains(t, string(data), `"ssherpa_session_id": "session-1"`)
}

func TestRunHelpCommand(t *testing.T) {
	var stdout bytes.Buffer

	code := Run([]string{"help"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "Usage:")
	assertContains(t, stdout.String(), "Available Commands:")
	assertContains(t, stdout.String(), "theme      Build and save")
	assertContains(t, stdout.String(), "check      Test SSH aliases")
	assertContains(t, stdout.String(), "incoming   Inspect and mark incoming SSH sessions")
	assertContains(t, stdout.String(), "send       Send a local file")
	assertContains(t, stdout.String(), "Incoming Commands:")
	assertContains(t, stdout.String(), "forward saved list")
	assertContains(t, stdout.String(), "Theme Commands:")
	assertContains(t, stdout.String(), "Phase 10:")
}

func TestGeneratedShellAndManArtifactsExist(t *testing.T) {
	for _, path := range []string{
		"../../completions/ssherpa.bash",
		"../../completions/ssherpa.zsh",
		"../../completions/ssherpa.fish",
		"../../man/ssherpa.1",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated artifact %s: %v", path, err)
		}
		assertContains(t, string(data), "ssherpa")
	}
}

func TestPickerVersionLabel(t *testing.T) {
	cases := []struct {
		build BuildInfo
		want  string
	}{
		{BuildInfo{}, "dev"},
		{BuildInfo{Version: ""}, "dev"},
		{BuildInfo{Version: "dev"}, "dev"},
		{BuildInfo{Version: "1.1.0"}, "v1.1.0"},
		{BuildInfo{Version: "v1.1.0"}, "v1.1.0"},
		{BuildInfo{Version: "  v1.1.0  "}, "v1.1.0"},
		{BuildInfo{Version: "1.2.3-rc.1"}, "v1.2.3-rc.1"},
	}
	for _, c := range cases {
		t.Run(c.build.Version, func(t *testing.T) {
			if got := pickerVersionLabel(c.build); got != c.want {
				t.Fatalf("pickerVersionLabel(%q) = %q, want %q", c.build.Version, got, c.want)
			}
		})
	}
}

func TestRunConnectPrint(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod web
  HostName prod.example.com
  User alice
`)

	code := Run([]string{"--print", "--select", "prod", "--config", config, "--", "-L", "8080:localhost:8080"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 prod -L 8080:localhost:8080")
}

func TestRunConnectPrintJSON(t *testing.T) {
	var stdout bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	code := Run([]string{"--print", "--json", "--select=prod", "--config=" + config}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}

	var got struct {
		Argv  []string `json:"argv"`
		Alias string   `json:"alias"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v\n%s", err, stdout.String())
	}
	if strings.Join(got.Argv, "\x00") != "ssh\x00-o\x00SendEnv=SSHERPA_*\x00-o\x00ConnectTimeout=10\x00prod" || got.Alias != "prod" {
		t.Fatalf("print JSON = %#v", got)
	}
}

func TestRunConnectAllowsHelpAsSSHPassthroughArg(t *testing.T) {
	var stdout bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	code := Run([]string{"--print", "--select", "prod", "--config", config, "--", "--help"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 prod --help")
}

func TestRunSendPrintsSFTPCommandAndBatch(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	local := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local payload: %v", err)
	}

	code := Run([]string{"send", local, "--select", "prod", "--remote", "/tmp/payload.txt", "--config", config, "--print"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertContains(t, stdout.String(), "[print] sftp -b - -F "+config+" prod")
	assertContains(t, stdout.String(), "put "+local+" /tmp/payload.txt")
}

func TestRunSendExecutesFakeSFTP(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	local := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local payload: %v", err)
	}
	fakeSFTP, argvLog, batchLog := writeFakeSFTP(t, 0)

	code := Run([]string{"send", local, "--select", "prod", "--remote", "/tmp/payload.txt", "--config", config, "--sftp-binary", fakeSFTP}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "fake sftp stdout")
	if got := strings.TrimSpace(readFile(t, argvLog)); got != "-b - -F "+config+" prod" {
		t.Fatalf("fake sftp argv = %q", got)
	}
	if got := readFile(t, batchLog); got != "put "+local+" /tmp/payload.txt\n" {
		t.Fatalf("fake sftp batch = %q", got)
	}
}

func TestRunSendRefusesExistingRemoteDestinationWithoutForce(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	local := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local payload: %v", err)
	}
	fakeSFTP, _, _ := writeFakeSFTP(t, 0)

	code := Run([]string{"send", local, "--select", "prod", "--remote", "/var/log/app.log", "--config", config, "--sftp-binary", fakeSFTP}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "already exists")
	assertContains(t, stderr.String(), "Use --force to overwrite")
}

func TestRunSendForceAllowsExistingRemoteDestination(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	local := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local payload: %v", err)
	}
	fakeSFTP, _, batchLog := writeFakeSFTP(t, 0)

	code := Run([]string{"send", local, "--select", "prod", "--remote", "/var/log/app.log", "--config", config, "--sftp-binary", fakeSFTP, "--force"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := readFile(t, batchLog); got != "put "+local+" /var/log/app.log\n" {
		t.Fatalf("fake sftp batch = %q", got)
	}
}

func TestRunReceiveExecutesFakeSFTP(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	localDir := t.TempDir()
	fakeSFTP, _, batchLog := writeFakeSFTP(t, 0)

	code := Run([]string{"receive", "/var/log/app.log", "--select", "prod", "--local", localDir, "--config", config, "--sftp-binary", fakeSFTP}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	wantLocal := filepath.Join(localDir, "app.log")
	if got := readFile(t, batchLog); got != "get /var/log/app.log "+wantLocal+"\n" {
		t.Fatalf("fake sftp batch = %q, want local dir expanded", got)
	}
}

func TestRunReceiveRefusesExistingLocalDestinationWithoutForce(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "app.log"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write existing local file: %v", err)
	}
	fakeSFTP, _, _ := writeFakeSFTP(t, 0)

	code := Run([]string{"receive", "/var/log/app.log", "--select", "prod", "--local", localDir, "--config", config, "--sftp-binary", fakeSFTP}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "already exists")
	assertContains(t, stderr.String(), "Use --force to overwrite")
}

func TestRunReceiveForceAllowsExistingLocalDestination(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	localDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(localDir, "app.log"), []byte("old"), 0o600); err != nil {
		t.Fatalf("write existing local file: %v", err)
	}
	fakeSFTP, _, batchLog := writeFakeSFTP(t, 0)

	code := Run([]string{"receive", "/var/log/app.log", "--select", "prod", "--local", localDir, "--config", config, "--sftp-binary", fakeSFTP, "--force"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	wantLocal := filepath.Join(localDir, "app.log")
	if got := readFile(t, batchLog); got != "get /var/log/app.log "+wantLocal+"\n" {
		t.Fatalf("fake sftp batch = %q, want forced receive batch", got)
	}
}

func TestRunReceiveRejectsRemoteDirectory(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	localDir := t.TempDir()
	fakeSFTP, _, _ := writeFakeSFTP(t, 0)

	code := Run([]string{"receive", "/var/log/logs", "--select", "prod", "--local", localDir, "--config", config, "--sftp-binary", fakeSFTP}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "is a directory")
	assertContains(t, stderr.String(), "Choose a file or archive the directory first")
}

func TestRunReceiveDefaultsLocalPathToRemoteBasename(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	localDir := t.TempDir()
	t.Chdir(localDir)
	fakeSFTP, _, batchLog := writeFakeSFTP(t, 0)

	code := Run([]string{"receive", "/var/log/app.log", "--select", "prod", "--config", config, "--sftp-binary", fakeSFTP}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	wantLocal := filepath.Join(localDir, "app.log")
	if got := readFile(t, batchLog); got != "get /var/log/app.log "+wantLocal+"\n" {
		t.Fatalf("fake sftp batch = %q, want local basename expanded", got)
	}
}

func TestRecordTransferEventWritesAuditDetails(t *testing.T) {
	stateDir := t.TempDir()
	local := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o600); err != nil {
		t.Fatalf("write local payload: %v", err)
	}
	record := state.SessionRecord{ID: "session-1", TargetAlias: "prod"}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord returned error: %v", err)
	}

	recordTransferEvent(stateDir, record.ID, sshcmd.SFTPTransfer{
		Direction:  sshcmd.SFTPTransferSend,
		Alias:      "prod",
		LocalPath:  local,
		RemotePath: "/tmp/payload.txt",
	})

	got, err := state.ReadRecord(stateDir, record.ID)
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("events = %#v, want one transfer event", got.Events)
	}
	event := got.Events[0]
	if event.Type != "transfer_send" {
		t.Fatalf("event type = %q, want transfer_send", event.Type)
	}
	assertContains(t, event.Message, `transport=sftp`)
	assertContains(t, event.Message, `local="`+local+`"`)
	assertContains(t, event.Message, `remote="prod:/tmp/payload.txt"`)
	assertContains(t, event.Message, `bytes=5`)
	assertContains(t, event.Message, `sha256=2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824`)
}

func TestRecordInbandTransferEventWritesAuditDetails(t *testing.T) {
	stateDir := t.TempDir()
	record := state.SessionRecord{ID: "session-1", TargetAlias: "prod"}
	if err := state.WriteRecord(stateDir, record); err != nil {
		t.Fatalf("WriteRecord returned error: %v", err)
	}

	recordInbandTransferEvent(stateDir, record.ID, session.InbandSendResult{
		LocalPath:  "/tmp/local.txt",
		RemotePath: "/srv/remote.txt",
		Size:       42,
		SHA256:     "abc123",
	})

	got, err := state.ReadRecord(stateDir, record.ID)
	if err != nil {
		t.Fatalf("ReadRecord returned error: %v", err)
	}
	if len(got.Events) != 1 {
		t.Fatalf("events = %#v, want one transfer event", got.Events)
	}
	event := got.Events[0]
	if event.Type != "transfer_send" {
		t.Fatalf("event type = %q, want transfer_send", event.Type)
	}
	assertContains(t, event.Message, `transport=inband`)
	assertContains(t, event.Message, `local="/tmp/local.txt"`)
	assertContains(t, event.Message, `remote="/srv/remote.txt"`)
	assertContains(t, event.Message, `bytes=42`)
	assertContains(t, event.Message, `sha256=abc123`)
}

func TestValidateOverlayInbandSendRequiresIdlePromptAndCWD(t *testing.T) {
	send := func(session.InbandSendRequest) (session.InbandSendResult, error) {
		return session.InbandSendResult{}, nil
	}
	tests := []struct {
		name       string
		req        session.OverlayTransferRequest
		forcedMode bool
		want       string
	}{
		{
			name: "missing sender",
			req: session.OverlayTransferRequest{
				RemotePrompt: state.RemotePromptPrompt,
				RemoteCWD:    "/srv",
			},
			want: "sender is not available",
		},
		{
			name: "unknown prompt",
			req: session.OverlayTransferRequest{
				InbandSend: send,
				RemoteCWD:  "/srv",
			},
			want: "remote prompt state is unknown",
		},
		{
			name: "prompt start normally blocked",
			req: session.OverlayTransferRequest{
				InbandSend:   send,
				RemotePrompt: state.RemotePromptPromptStart,
				RemoteCWD:    "/srv",
			},
			want: "not idle",
		},
		{
			name: "prompt start allowed for forced transport tests",
			req: session.OverlayTransferRequest{
				InbandSend:   send,
				RemotePrompt: state.RemotePromptPromptStart,
				RemoteCWD:    "/srv",
			},
			forcedMode: true,
		},
		{
			name: "running prompt blocked even when prompt start allowed",
			req: session.OverlayTransferRequest{
				InbandSend:   send,
				RemotePrompt: state.RemotePromptRunning,
				RemoteCWD:    "/srv",
			},
			forcedMode: true,
			want:       "not idle",
		},
		{
			name: "unknown cwd",
			req: session.OverlayTransferRequest{
				InbandSend:   send,
				RemotePrompt: state.RemotePromptPrompt,
			},
			want: "remote cwd is unknown",
		},
		{
			name: "unknown cwd allowed for forced transport tests",
			req: session.OverlayTransferRequest{
				InbandSend:   send,
				RemotePrompt: state.RemotePromptPrompt,
			},
			forcedMode: true,
		},
		{
			name: "ready",
			req: session.OverlayTransferRequest{
				InbandSend:   send,
				RemotePrompt: state.RemotePromptPrompt,
				RemoteCWD:    "/srv",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOverlayInbandSend(tt.req, tt.forcedMode)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("validateOverlayInbandSend returned error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestForceOverlayInbandSendReadsTransportEnv(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{"", false},
		{"auto", false},
		{"sftp", false},
		{"inband", true},
		{"in-band", true},
		{"transport-c", true},
		{"c", true},
		{" PTY ", true},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv(transferTransportEnv, tt.value)
			if got := forceOverlayInbandSend(); got != tt.want {
				t.Fatalf("forceOverlayInbandSend() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRunConnectDirectExecutesFakeSSH(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, logPath := writeFakeSSH(t, 0)

	code := Run([]string{"--direct", "--select", "prod", "--config", config, "--ssh-binary", fakeSSH, "--", "-v"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "[exec]")
	assertContains(t, stdout.String(), "fake ssh stdout")

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("os.ReadFile returned error: %v", err)
	}
	if strings.TrimSpace(string(logBytes)) != "prod -v" {
		t.Fatalf("fake ssh argv log = %q, want %q", string(logBytes), "prod -v")
	}
}

func TestRunConnectPropagatesFakeSSHExitCode(t *testing.T) {
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 7)

	code := Run([]string{"--direct", "--select", "prod", "--config", config, "--ssh-binary", fakeSSH}, nil, &stderr, BuildInfo{})

	if code != 7 {
		t.Fatalf("Run returned %d, want 7; stderr = %q", code, stderr.String())
	}
}

func TestRunConnectDefaultSuperviseRecordsSession(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 0)
	stateDir := t.TempDir()

	code := Run([]string{"--state-dir", stateDir, "--select", "prod", "--config", config, "--ssh-binary", fakeSSH, "--", "-v"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "[supervise]")
	assertContains(t, stdout.String(), "fake ssh stdout")

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.TargetAlias != "prod" || strings.Join(record.Route, " -> ") != "prod" {
		t.Fatalf("record route = %#v", record)
	}
	if record.Status() != "exited" || record.ExitCode == nil || *record.ExitCode != 0 {
		t.Fatalf("record lifecycle = %#v", record)
	}
	if got := strings.Join(record.SSHArgv, "\x00"); !strings.Contains(got, "prod\x00-v") {
		t.Fatalf("record argv = %#v", record.SSHArgv)
	}
	if got := strings.Join(record.SSHArgv, "\x00"); !strings.Contains(got, "SendEnv=SSHERPA_*") {
		t.Fatalf("record argv = %#v, want session env forwarding", record.SSHArgv)
	}
}

func TestRunConnectRejectsUnknownFlag(t *testing.T) {
	var stderr bytes.Buffer

	code := Run([]string{"--bogus"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), `unknown flag "--bogus"`)
}

func TestRunConnectRejectsUnsafeLatencyFlagCombinations(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "disconnect requires warning threshold",
			args: []string{"--select", "prod", "--config", config, "--latency-disconnect", "30s"},
			want: "--latency-disconnect requires --latency-warn",
		},
		{
			name: "direct mode cannot watchdog",
			args: []string{"--direct", "--select", "prod", "--config", config, "--latency-warn", "2s"},
			want: "latency watchdog requires supervised mode",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(tt.args, nil, &stderr, BuildInfo{})
			if code != 1 {
				t.Fatalf("Run returned %d, want 1; stderr = %q", code, stderr.String())
			}
			assertContains(t, stderr.String(), tt.want)
		})
	}
}

func TestRunConnectRejectsUnsafeComposerFlagCombinations(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no composer conflicts with key",
			args: []string{"--select", "prod", "--config", config, "--no-composer", "--composer-key", "ctrl-r"},
			want: "--composer-key cannot be used with --no-composer",
		},
		{
			name: "direct mode cannot configure composer",
			args: []string{"--direct", "--select", "prod", "--config", config, "--no-composer"},
			want: "composer flags require supervised mode",
		},
		{
			name: "reserved overlay key",
			args: []string{"--select", "prod", "--config", config, "--composer-key", "ctrl-]"},
			want: "reserved key Ctrl-]",
		},
		{
			name: "invalid key",
			args: []string{"--select", "prod", "--config", config, "--composer-key", "enter"},
			want: "must be a control key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(tt.args, nil, &stderr, BuildInfo{})
			if code != 1 {
				t.Fatalf("Run returned %d, want 1; stderr = %q", code, stderr.String())
			}
			assertContains(t, stderr.String(), tt.want)
		})
	}
}

func TestRunConnectPickerAddCarriesConfigFlag(t *testing.T) {
	args := connectFlagsAsAddArgs(connectFlags{inventoryFlags: inventoryFlags{Config: "/tmp/config"}})

	if strings.Join(args, "\x00") != "--config\x00/tmp/config" {
		t.Fatalf("args = %#v, want config passthrough", args)
	}
}

func TestPickerSummaryUsesPluralizedCompactCounts(t *testing.T) {
	inventory := hostlist.Inventory{
		Aliases: []hostlist.Alias{{Name: "prod", Warnings: []string{"warning"}}},
	}
	summary := pickerSummary(connectFlags{}, nil, inventory, 0, 1, 2, 3)

	if len(summary) == 0 || summary[0] != "1 host  1 warning  1 session  2 tunnels  3 incoming" {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestPickerSessionCountsIgnoreRemoteMirrors(t *testing.T) {
	stateDir := t.TempDir()
	if err := state.WriteRecord(stateDir, state.SessionRecord{
		ID:          "local-active",
		Kind:        state.KindInteractive,
		TargetAlias: "prod",
		LocalPID:    os.Getpid(),
		RunnerMode:  "supervised",
	}); err != nil {
		t.Fatalf("WriteRecord local-active: %v", err)
	}
	if err := state.WriteRecord(stateDir, state.SessionRecord{
		ID:           "mirrored-active",
		ParentID:     "local-active",
		TargetAlias:  "nested",
		RemoteMirror: true,
		RunnerMode:   "supervised",
	}); err != nil {
		t.Fatalf("WriteRecord mirrored-active: %v", err)
	}

	total, active := pickerSessionCounts(stateDir)
	if total != 1 || active != 1 {
		t.Fatalf("pickerSessionCounts = total %d active %d, want only local active counted", total, active)
	}
	if got := pickerStoppableSessionCount(stateDir); got != 1 {
		t.Fatalf("pickerStoppableSessionCount = %d, want local active only", got)
	}
}

func TestRunConnectPickerRouteRowsCarryConnectFlags(t *testing.T) {
	flags := connectFlags{
		inventoryFlags: inventoryFlags{
			All:    true,
			Filter: "prod",
			User:   "alice",
			Config: "/tmp/config",
		},
		Print:             true,
		SSHBinary:         "/tmp/fake-ssh",
		Direct:            true,
		StateDir:          "/tmp/state",
		LatencyWarn:       2 * time.Second,
		LatencyDisconnect: 30 * time.Second,
		ComposerKey:       0x12,
		ComposerKeyName:   "Ctrl-R",
		NoKitty:           true,
		NoColor:           true,
		ThemeFile:         "/tmp/theme.conf",
		SSHArgs:           []string{"-v"},
	}

	args := connectFlagsAsJumpArgs(flags)
	want := "--all\x00--print\x00--filter\x00prod\x00--user\x00alice\x00--config\x00/tmp/config\x00--ssh-binary\x00/tmp/fake-ssh\x00--direct\x00--state-dir\x00/tmp/state\x00--latency-warn\x002s\x00--latency-disconnect\x0030s\x00--composer-key\x00Ctrl-R\x00--no-kitty\x00--no-color\x00--theme-file\x00/tmp/theme.conf\x00--\x00-v"
	if got := strings.Join(args, "\x00"); got != want {
		t.Fatalf("jump args = %#v, want %q", args, want)
	}

	proxyArgs := connectFlagsAsProxyArgs(flags)
	if got := strings.Join(proxyArgs, "\x00"); got != want {
		t.Fatalf("proxy args = %#v, want %q", proxyArgs, want)
	}
}

func TestParseThemeFlags(t *testing.T) {
	tests := []struct {
		name string
		run  func([]string, io.Writer) (string, string, bool)
	}{
		{
			name: "connect",
			run: func(args []string, stderr io.Writer) (string, string, bool) {
				flags, ok := parseConnectFlags(args, stderr)
				return flags.ThemeName, flags.ThemeFile, ok
			},
		},
		{
			name: "jump",
			run: func(args []string, stderr io.Writer) (string, string, bool) {
				flags, ok := parseJumpFlags(args, stderr)
				return flags.ThemeName, flags.ThemeFile, ok
			},
		},
		{
			name: "proxy",
			run: func(args []string, stderr io.Writer) (string, string, bool) {
				flags, ok := parseProxyFlags(args, stderr)
				return flags.ThemeName, flags.ThemeFile, ok
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			theme, file, ok := tt.run([]string{"--theme=vivid", "--theme-file", "/tmp/theme.conf"}, &stderr)
			if !ok {
				t.Fatalf("parse failed: %s", stderr.String())
			}
			if theme != "vivid" || file != "/tmp/theme.conf" {
				t.Fatalf("theme = %q, file = %q; want vivid and /tmp/theme.conf", theme, file)
			}
		})
	}
}

func TestParseThemeCommandFlags(t *testing.T) {
	var stderr bytes.Buffer
	flags, ok := parseThemeFlags([]string{"--theme", "vivid", "--theme-file=/tmp/theme.conf", "--no-color"}, &stderr)
	if !ok {
		t.Fatalf("parseThemeFlags failed: %s", stderr.String())
	}
	if flags.ThemeName != "" || flags.ThemeFile != "/tmp/theme.conf" || !flags.NoColor {
		t.Fatalf("flags = %#v, want theme file and no color", flags)
	}
}

func TestFormatThemeConfig(t *testing.T) {
	got := string(formatThemeConfig(termstyle.ThemeConfig{
		Specs: map[termstyle.Role]string{
			termstyle.RolePrimary: "cyan",
			termstyle.RolePill:    "bold reverse",
		},
	}))

	assertNotContains(t, got, "theme =")
	assertContains(t, got, "primary = cyan")
	assertContains(t, got, "pill = bold reverse")
}

func TestLoadThemeConfigInvalidCanBeReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "theme.conf")
	if err := os.WriteFile(path, []byte("primary = imaginary\n"), 0o600); err != nil {
		t.Fatalf("write theme: %v", err)
	}

	cfg, warning, err := loadThemeConfig(path)
	if err != nil {
		t.Fatalf("loadThemeConfig returned error: %v", err)
	}
	if cfg.BaseName != "" || warning == "" {
		t.Fatalf("cfg = %#v, warning = %q; want empty cfg with warning", cfg, warning)
	}
}

func TestRunJumpPrint(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com

Host bastion
  HostName bastion.example.com

Host edge
  HostName edge.example.com
`)

	code := Run([]string{"jump", "--dest", "prod", "--hop", "bastion", "--hop", "edge", "--print", "--config", config, "--", "-A"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 -J bastion,edge prod -A")
}

func TestRunJumpExecutesFakeSSH(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com

Host bastion
  HostName bastion.example.com
`)
	fakeSSH, logPath := writeFakeSSH(t, 0)

	code := Run([]string{"jump", "--direct", "--dest", "prod", "--hop", "bastion", "--config", config, "--ssh-binary", fakeSSH, "--", "-v"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "[exec]")
	assertContains(t, stdout.String(), "fake ssh stdout")
	if got := strings.TrimSpace(readFile(t, logPath)); got != "-J bastion prod -v" {
		t.Fatalf("fake ssh argv log = %q, want %q", got, "-J bastion prod -v")
	}
}

func TestRunJumpRejectsInvalidRoutes(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com

Host bastion
  HostName bastion.example.com
`)

	tests := []struct {
		name string
		args []string
		want string
		code int
	}{
		{name: "duplicate hop", args: []string{"jump", "--dest", "prod", "--hop", "bastion", "--hop", "bastion", "--config", config}, want: "duplicate", code: 1},
		{name: "destination as hop", args: []string{"jump", "--dest", "prod", "--hop", "prod", "--config", config}, want: "destination", code: 1},
		{name: "missing hop", args: []string{"jump", "--dest", "prod", "--config", config}, want: "requires --dest", code: 1},
		{name: "unknown alias", args: []string{"jump", "--dest", "prod", "--hop", "missing", "--config", config}, want: "alias not found", code: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(tt.args, nil, &stderr, BuildInfo{})
			if code != tt.code {
				t.Fatalf("Run returned %d, want %d; stderr = %q", code, tt.code, stderr.String())
			}
			assertContains(t, stderr.String(), tt.want)
		})
	}
}

func TestRunProxyPrintDefaultBindPort(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	code := Run([]string{"proxy", "--select", "prod", "--print", "--config", config}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 -D 127.0.0.1:1080 -C -N -o ExitOnForwardFailure=yes prod")
}

func TestRunProxyPrintCustomBindPortAndPassthrough(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	code := Run([]string{"proxy", "--select", "prod", "--bind", "0.0.0.0", "--port", "1081", "--print", "--config", config, "--", "-v"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 -D 0.0.0.0:1081 -C -N -o ExitOnForwardFailure=yes prod -v")
}

func TestRunProxyExecutesFakeSSH(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)
	fakeSSH, logPath := writeFakeSSH(t, 0)

	code := Run([]string{"proxy", "--direct", "prod", "--port", "1081", "--config", config, "--ssh-binary", fakeSSH, "--", "-v"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "[exec]")
	assertContains(t, stdout.String(), "fake ssh stdout")
	want := "-D 127.0.0.1:1081 -C -N -o ExitOnForwardFailure=yes prod -v"
	if got := strings.TrimSpace(readFile(t, logPath)); got != want {
		t.Fatalf("fake ssh argv log = %q, want %q", got, want)
	}
}

func TestRunProxyRejectsInvalidInputs(t *testing.T) {
	config := writeConfig(t, `
Host prod
  HostName prod.example.com
`)

	tests := []struct {
		name string
		args []string
		want string
		code int
	}{
		{name: "bad port", args: []string{"proxy", "--select", "prod", "--port", "70000", "--config", config}, want: "proxy port", code: 1},
		{name: "unknown alias", args: []string{"proxy", "--select", "missing", "--config", config}, want: "alias", code: 2},
		{name: "empty bind", args: []string{"proxy", "--select", "prod", "--bind", "", "--config", config}, want: "bind", code: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(tt.args, nil, &stderr, BuildInfo{})
			if code != tt.code {
				t.Fatalf("Run returned %d, want %d; stderr = %q", code, tt.code, stderr.String())
			}
			assertContains(t, stderr.String(), tt.want)
		})
	}
}

func TestRunForwardPrintLoopback(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)

	code := Run([]string{"forward", "--select", "pgbox", "--local", "5432", "--remote", "127.0.0.1:5432", "--print", "--config", config}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 -L 127.0.0.1:5432:127.0.0.1:5432 -N -o ExitOnForwardFailure=yes pgbox")
}

func TestRunForwardPrintCustomBindThroughAndPassthrough(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com

Host bastion
  HostName bastion.example.com
`)

	code := Run([]string{"forward", "--select", "pgbox", "--local", "0.0.0.0:5433", "--remote", "db.internal:5432", "--through", "bastion", "--print", "--config", config, "--", "-v"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 -J bastion -L 0.0.0.0:5433:db.internal:5432 -N -o ExitOnForwardFailure=yes pgbox -v")
}

func TestRunForwardAcceptsPositionalAlias(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)

	code := Run([]string{"forward", "pgbox", "--local", "5432", "--remote", "127.0.0.1:5432", "--print", "--config", config}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[print] ssh -o 'SendEnv=SSHERPA_*' -o ConnectTimeout=10 -L 127.0.0.1:5432:127.0.0.1:5432 -N -o ExitOnForwardFailure=yes pgbox")
}

func TestRunForwardExecutesFakeSSH(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	fakeSSH, logPath := writeFakeSSH(t, 0)

	code := Run([]string{"forward", "--direct", "pgbox", "--local", "5433", "--remote", "127.0.0.1:5432", "--config", config, "--ssh-binary", fakeSSH, "--", "-v"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "[exec]")
	assertContains(t, stdout.String(), "fake ssh stdout")
	want := "-L 127.0.0.1:5433:127.0.0.1:5432 -N -o ExitOnForwardFailure=yes pgbox -v"
	if got := strings.TrimSpace(readFile(t, logPath)); got != want {
		t.Fatalf("fake ssh argv log = %q, want %q", got, want)
	}
}

func TestRunForwardSupervisedRecordsTunnelKind(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 0)
	stateDir := t.TempDir()

	code := Run([]string{"forward", "--state-dir", stateDir, "--select", "pgbox", "--local", "5432", "--remote", "127.0.0.1:5432", "--config", config, "--ssh-binary", fakeSSH}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "[supervise]")

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.Kind != state.KindTunnel {
		t.Fatalf("record.Kind = %q, want %q", record.Kind, state.KindTunnel)
	}
	if record.TargetAlias != "pgbox" || strings.Join(record.Route, " -> ") != "pgbox" {
		t.Fatalf("record route = %#v", record)
	}
	if got := strings.Join(record.SSHArgv, " "); !strings.Contains(got, "-L 127.0.0.1:5432:127.0.0.1:5432 -N") {
		t.Fatalf("record argv missing forward spec: %#v", record.SSHArgv)
	}
}

func TestRunProxySupervisedRecordsProxyKind(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host bastion
  HostName bastion.example.com
`)
	fakeSSH, _ := writeFakeSSH(t, 0)
	stateDir := t.TempDir()

	code := Run([]string{"proxy", "--state-dir", stateDir, "--select", "bastion", "--port", "1080", "--config", config, "--ssh-binary", fakeSSH}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %#v, want one", records)
	}
	record := records[0]
	if record.Kind != state.KindProxy || record.Proxy == nil {
		t.Fatalf("record proxy metadata = %#v", record)
	}
	if record.Proxy.Port != 1080 || record.Proxy.Bind != defaultProxyBind {
		t.Fatalf("record.Proxy = %#v", record.Proxy)
	}
	if got := strings.Join(record.SSHArgv, " "); !strings.Contains(got, "-D 127.0.0.1:1080 -C -N") {
		t.Fatalf("record argv missing proxy spec: %#v", record.SSHArgv)
	}
}

func TestRunForwardRejectsInvalidInputs(t *testing.T) {
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com

Host bastion
  HostName bastion.example.com
`)

	tests := []struct {
		name string
		args []string
		want string
		code int
	}{
		{name: "missing local", args: []string{"forward", "--select", "pgbox", "--remote", "127.0.0.1:5432", "--config", config}, want: "--local", code: 1},
		{name: "missing remote", args: []string{"forward", "--select", "pgbox", "--local", "5432", "--config", config}, want: "--remote", code: 1},
		{name: "bad local port bare", args: []string{"forward", "--select", "pgbox", "--local", "70000", "--remote", "127.0.0.1:5432", "--config", config}, want: "--local port", code: 1},
		{name: "bad local bind:port", args: []string{"forward", "--select", "pgbox", "--local", "0.0.0.0:abc", "--remote", "127.0.0.1:5432", "--config", config}, want: "--local port", code: 1},
		{name: "malformed remote", args: []string{"forward", "--select", "pgbox", "--local", "5432", "--remote", "no-port", "--config", config}, want: "--remote", code: 1},
		{name: "bad remote port", args: []string{"forward", "--select", "pgbox", "--local", "5432", "--remote", "host:abc", "--config", config}, want: "--remote port", code: 1},
		{name: "remote missing host", args: []string{"forward", "--select", "pgbox", "--local", "5432", "--remote", ":5432", "--config", config}, want: "missing host", code: 1},
		{name: "unknown alias", args: []string{"forward", "--select", "missing", "--local", "5432", "--remote", "127.0.0.1:5432", "--config", config}, want: "alias", code: 2},
		{name: "unknown through", args: []string{"forward", "--select", "pgbox", "--local", "5432", "--remote", "127.0.0.1:5432", "--through", "nope", "--config", config}, want: "alias", code: 2},
		{name: "through equals destination", args: []string{"forward", "--select", "pgbox", "--local", "5432", "--remote", "127.0.0.1:5432", "--through", "pgbox", "--config", config}, want: "destination", code: 1},
		{name: "two positional aliases", args: []string{"forward", "pgbox", "bastion", "--local", "5432", "--remote", "127.0.0.1:5432", "--config", config}, want: "only one alias", code: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(tt.args, nil, &stderr, BuildInfo{})
			if code != tt.code {
				t.Fatalf("Run returned %d, want %d; stderr = %q", code, tt.code, stderr.String())
			}
			assertContains(t, stderr.String(), tt.want)
		})
	}
}

func TestRunForwardReconnectsOnTransientExit(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	// Two attempts exit 255 (transient network error), third succeeds.
	fakeSSH, logPath := writeFakeSSHFlaky(t, []int{255, 255, 0})
	stateDir := t.TempDir()

	code := Run([]string{
		"forward", "--state-dir", stateDir,
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--reconnect-backoff", "1ms", "--reconnect-max-backoff", "1ms",
		"--config", config, "--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := countLines(readFile(t, logPath)); got != 3 {
		t.Fatalf("fake-ssh invoked %d times, want 3 (two transient retries + one success); log=%q", got, readFile(t, logPath))
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1; want the same SessionRecord.ID across retries", len(records))
	}
	rec := records[0]
	if rec.Forward == nil {
		t.Fatalf("record.Forward is nil")
	}
	if rec.Forward.RetryCount < 2 {
		t.Fatalf("record.Forward.RetryCount = %d, want >= 2", rec.Forward.RetryCount)
	}
	var schedCount, attemptCount int
	for _, ev := range rec.Events {
		switch ev.Type {
		case "reconnect_scheduled":
			schedCount++
		case "reconnect_gave_up":
			t.Fatalf("got reconnect_gave_up event despite eventual success: %+v", ev)
		}
		_ = attemptCount
	}
	if schedCount != 2 {
		t.Fatalf("reconnect_scheduled events = %d, want 2; events=%+v", schedCount, rec.Events)
	}
}

func TestRunForwardGivesUpOnBindFailure(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	// exit 1 mimics ExitOnForwardFailure (local port in use).
	fakeSSH, logPath := writeFakeSSHFlaky(t, []int{1})
	stateDir := t.TempDir()

	code := Run([]string{
		"forward", "--state-dir", stateDir,
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--reconnect-backoff", "1ms",
		"--config", config, "--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1 (give-up on bind failure); stderr=%q", code, stderr.String())
	}
	if got := countLines(readFile(t, logPath)); got != 1 {
		t.Fatalf("fake-ssh invoked %d times, want 1 (no retry on bind failure)", got)
	}
}

func TestRunForwardRespectsNoReconnect(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	// exit 255 would normally retry, but --no-reconnect overrides.
	fakeSSH, logPath := writeFakeSSHFlaky(t, []int{255})
	stateDir := t.TempDir()

	code := Run([]string{
		"forward", "--state-dir", stateDir,
		"--no-reconnect",
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--config", config, "--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})

	if code != 255 {
		t.Fatalf("Run returned %d, want 255; stderr=%q", code, stderr.String())
	}
	if got := countLines(readFile(t, logPath)); got != 1 {
		t.Fatalf("fake-ssh invoked %d times, want 1 (--no-reconnect)", got)
	}
}

func TestRunForwardRespectsReconnectMaxCap(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	// Every attempt fails with 255 (transient); cap at 2 attempts.
	fakeSSH, logPath := writeFakeSSHFlaky(t, []int{255})
	stateDir := t.TempDir()

	code := Run([]string{
		"forward", "--state-dir", stateDir,
		"--reconnect-max", "2",
		"--reconnect-backoff", "1ms", "--reconnect-max-backoff", "1ms",
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--config", config, "--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})

	if code != 255 {
		t.Fatalf("Run returned %d, want 255; stderr=%q", code, stderr.String())
	}
	if got := countLines(readFile(t, logPath)); got != 2 {
		t.Fatalf("fake-ssh invoked %d times, want exactly 2 (capped); log=%q", got, readFile(t, logPath))
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	var gaveUp bool
	for _, ev := range records[0].Events {
		if ev.Type == "reconnect_gave_up" {
			gaveUp = true
			break
		}
	}
	if !gaveUp {
		t.Fatalf("expected reconnect_gave_up event after hitting cap; events=%+v", records[0].Events)
	}
}

func TestRunForwardBackgroundSpawnsChildWithSupervisorArgs(t *testing.T) {
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com

Host bastion
  HostName bastion.example.com
`)
	stateDir := t.TempDir()

	// Capture the would-be spawn instead of actually forking. The
	// daemonStartProcess seam is package-private so tests can swap it
	// without exposing the internal protocol publicly.
	var capturedName string
	var capturedArgs []string
	var capturedAttr *os.ProcAttr
	defer func(orig func(string, []string, *os.ProcAttr) (int, error)) {
		daemonStartProcess = orig
	}(daemonStartProcess)
	defer func(orig time.Duration) {
		daemonRecordReadyTimeout = orig
	}(daemonRecordReadyTimeout)
	daemonRecordReadyTimeout = 0
	daemonStartProcess = func(name string, argv []string, attr *os.ProcAttr) (int, error) {
		capturedName = name
		capturedArgs = append([]string(nil), argv...)
		capturedAttr = attr
		return 31337, nil
	}

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"forward", "--background",
		"--state-dir", stateDir,
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--through", "bastion",
		"--config", config,
	}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if capturedName == "" {
		t.Fatalf("daemonStartProcess not called")
	}
	if capturedAttr == nil {
		t.Fatalf("daemonStartProcess ProcAttr is nil")
	}
	if capturedAttr.Sys == nil {
		t.Fatalf("ProcAttr.Sys is nil; expected SysProcAttr.Setsid=true")
	}
	if !capturedAttr.Sys.Setsid {
		t.Fatalf("ProcAttr.Sys.Setsid = false, want true")
	}

	// Validate the child argv: hidden supervisor flags first, then
	// 'forward' subcommand, then the original forward args minus
	// --background.
	if len(capturedArgs) < 2 || capturedArgs[1] != "--__supervisor" {
		t.Fatalf("child argv[1] = %q, want %q; full=%v", capturedArgs[1], "--__supervisor", capturedArgs)
	}
	if !containsAdjacent(capturedArgs, "--__detached-id") {
		t.Fatalf("child argv missing --__detached-id pair; full=%v", capturedArgs)
	}
	if !containsAdjacent(capturedArgs, "--__detached-state-dir") {
		t.Fatalf("child argv missing --__detached-state-dir pair; full=%v", capturedArgs)
	}
	if !contains(capturedArgs, "forward") {
		t.Fatalf("child argv missing 'forward' subcommand; full=%v", capturedArgs)
	}
	for _, arg := range capturedArgs {
		if arg == "--background" {
			t.Fatalf("--background leaked into child argv; full=%v", capturedArgs)
		}
	}

	// Parent stdout should include the detach summary lines.
	assertContains(t, stdout.String(), "ssherpa: forward detached")
	assertContains(t, stdout.String(), "daemon pid: 31337")
	assertContains(t, stdout.String(), "session id:")
	assertContains(t, stdout.String(), "log file:")
}

func TestWaitForDetachedRecordWaitsUntilRecordExists(t *testing.T) {
	stateDir := t.TempDir()
	sessionID := "detached-ready"
	go func() {
		time.Sleep(50 * time.Millisecond)
		_ = state.WriteRecord(stateDir, state.SessionRecord{
			ID:           sessionID,
			StartedAt:    time.Now(),
			RunnerMode:   "supervised",
			StateVersion: state.StateVersion,
		})
	}()

	if !waitForDetachedRecord(stateDir, sessionID, time.Second) {
		t.Fatalf("waitForDetachedRecord returned false")
	}
}

func TestRunForwardBackgroundRejectsPrint(t *testing.T) {
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	var stderr bytes.Buffer
	code := Run([]string{
		"forward", "--background", "--print",
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--config", config,
	}, nil, &stderr, BuildInfo{})
	if code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "mutually exclusive")
}

func TestRunForwardBackgroundRejectsDirect(t *testing.T) {
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	var stderr bytes.Buffer
	code := Run([]string{
		"forward", "--background", "--direct",
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--config", config,
	}, nil, &stderr, BuildInfo{})
	if code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr=%q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "supervised mode")
}

func TestRunSupervisorChildRoutesToDetachedForward(t *testing.T) {
	config := writeConfig(t, `
Host pgbox
  HostName pgbox.example.com
`)
	fakeSSH, logPath := writeFakeSSHFlaky(t, []int{0})
	stateDir := t.TempDir()
	const recordID = "child-routes-test-id"

	var stdout, stderr bytes.Buffer
	code := Run([]string{
		"--__supervisor",
		"--__detached-id", recordID,
		"--__detached-state-dir", stateDir,
		"forward",
		"--select", "pgbox",
		"--local", "5432", "--remote", "127.0.0.1:5432",
		"--config", config, "--ssh-binary", fakeSSH,
	}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := countLines(readFile(t, logPath)); got != 1 {
		t.Fatalf("fake-ssh invoked %d times, want 1", got)
	}

	records, err := state.ListRecords(stateDir)
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1", len(records))
	}
	rec := records[0]
	if rec.ID != recordID {
		t.Fatalf("record.ID = %q, want pre-assigned %q", rec.ID, recordID)
	}
	if rec.Forward == nil || !rec.Forward.Detached {
		t.Fatalf("record.Forward.Detached not set: %+v", rec.Forward)
	}
}

// contains reports whether s appears in slice.
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

// containsAdjacent reports whether name appears in slice followed by
// at least one more element — i.e. name is used as a "flag VALUE"
// pair somewhere in the argv. Doesn't validate the value.
func containsAdjacent(slice []string, name string) bool {
	for i, item := range slice {
		if item == name && i+1 < len(slice) {
			return true
		}
	}
	return false
}

func TestRunSessionListShowAndPrune(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Now().UTC()
	oldEnd := now.Add(-8 * 24 * time.Hour)
	recentEnd := now.Add(-time.Hour)
	exitCode := 0

	oldRecord := state.SessionRecord{
		ID:               "old",
		TargetAlias:      "prod",
		Route:            []string{"bastion", "prod"},
		Hops:             []string{"bastion"},
		StartedAt:        oldEnd.Add(-time.Hour),
		EndedAt:          &oldEnd,
		ExitCode:         &exitCode,
		LocalPID:         100,
		SSHPID:           101,
		RunnerMode:       "supervised",
		DisconnectReason: "latency unhealthy for 30s",
		Events: []state.SessionEvent{{
			Time:            oldEnd.Add(-30 * time.Second),
			Type:            "latency_disconnect",
			Message:         "latency unhealthy for 30s",
			LatencyMillis:   5000,
			ThresholdMillis: 2000,
		}},
	}
	recentRecord := state.SessionRecord{
		ID:          "recent",
		TargetAlias: "db",
		Route:       []string{"db"},
		StartedAt:   recentEnd.Add(-time.Hour),
		EndedAt:     &recentEnd,
		ExitCode:    &exitCode,
		LocalPID:    200,
		SSHPID:      201,
		RunnerMode:  "supervised",
	}
	if err := state.WriteRecord(stateDir, oldRecord); err != nil {
		t.Fatalf("WriteRecord old returned error: %v", err)
	}
	if err := state.WriteRecord(stateDir, recentRecord); err != nil {
		t.Fatalf("WriteRecord recent returned error: %v", err)
	}

	var listStdout bytes.Buffer
	code := Run([]string{"session", "list", "--json", "--state-dir", stateDir}, &listStdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session list returned %d, want 0", code)
	}
	var listed []state.SessionRecord
	if err := json.Unmarshal(listStdout.Bytes(), &listed); err != nil {
		t.Fatalf("json.Unmarshal list returned error: %v\n%s", err, listStdout.String())
	}
	if len(listed) != 2 || listed[0].ID != "recent" || listed[1].ID != "old" {
		t.Fatalf("listed = %#v, want newest-first records", listed)
	}

	var showStdout bytes.Buffer
	code = Run([]string{"session", "show", "old", "--state-dir", stateDir}, &showStdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session show returned %d, want 0", code)
	}
	assertContains(t, showStdout.String(), "route:\there -> bastion -> prod")
	assertContains(t, showStdout.String(), "exit_code:\t0")
	assertContains(t, showStdout.String(), "disconnect_reason:\tlatency unhealthy")
	assertContains(t, showStdout.String(), "events:")
	assertContains(t, showStdout.String(), "latency_disconnect")

	var pruneStdout bytes.Buffer
	code = Run([]string{"session", "prune", "--older-than", "168h", "--dry-run", "--state-dir", stateDir}, &pruneStdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session prune dry-run returned %d, want 0", code)
	}
	assertContains(t, pruneStdout.String(), "would remove 1 session record")
	if _, err := os.Stat(state.RecordPath(stateDir, "old")); err != nil {
		t.Fatalf("old record removed during dry-run: %v", err)
	}

	code = Run([]string{"session", "prune", "--older-than", "168h", "--state-dir", stateDir}, nil, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session prune apply returned %d, want 0", code)
	}
	if _, err := os.Stat(state.RecordPath(stateDir, "old")); !os.IsNotExist(err) {
		t.Fatalf("old record still exists, err=%v", err)
	}
}

func TestRunSessionShowMissing(t *testing.T) {
	var stderr bytes.Buffer

	code := Run([]string{"session", "show", "missing", "--state-dir", t.TempDir()}, nil, &stderr, BuildInfo{})

	if code != 2 {
		t.Fatalf("session show returned %d, want 2", code)
	}
	assertContains(t, stderr.String(), "show session")
}

func TestRunSessionMapBuildsLineage(t *testing.T) {
	stateDir := t.TempDir()
	now := time.Now().UTC()
	exitCode := 0
	root := state.SessionRecord{
		ID:          "root",
		TargetAlias: "bastion",
		Route:       []string{"bastion"},
		StartedAt:   now.Add(-2 * time.Minute),
		EndedAt:     ptrTime(now.Add(-time.Minute)),
		ExitCode:    &exitCode,
		RunnerMode:  "supervised",
	}
	child := state.SessionRecord{
		ID:          "child",
		ParentID:    "root",
		Depth:       1,
		TargetAlias: "prod",
		Route:       []string{"bastion", "prod"},
		StartedAt:   now.Add(-time.Minute),
		RunnerMode:  "supervised",
	}
	if err := state.WriteRecord(stateDir, root); err != nil {
		t.Fatalf("WriteRecord root returned error: %v", err)
	}
	if err := state.WriteRecord(stateDir, child); err != nil {
		t.Fatalf("WriteRecord child returned error: %v", err)
	}

	var stdout bytes.Buffer
	code := Run([]string{"session", "map", "--state-dir", stateDir}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session map returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "Session route map")
	assertContains(t, stdout.String(), "+- prod [jump] [active]")
	assertContains(t, stdout.String(), "path: here -> bastion -> prod")
	assertNotContains(t, stdout.String(), "bastion [exit 0]")

	stdout.Reset()
	code = Run([]string{"session", "map", "--json", "--state-dir", stateDir}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session map --json returned %d, want 0", code)
	}
	var got struct {
		Scope    string `json:"scope"`
		Total    int    `json:"total"`
		Active   int    `json:"active"`
		Recorded int    `json:"recorded"`
		Roots    []struct {
			Record   state.SessionRecord `json:"record"`
			Children []struct {
				Record state.SessionRecord `json:"record"`
			} `json:"children"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal map returned error: %v\n%s", err, stdout.String())
	}
	if got.Scope != "active" || got.Total != 1 || got.Active != 1 || got.Recorded != 2 || len(got.Roots) != 1 || got.Roots[0].Record.ID != "child" || len(got.Roots[0].Children) != 0 {
		t.Fatalf("active map json = %#v", got)
	}

	stdout.Reset()
	code = Run([]string{"session", "map", "--all", "--json", "--state-dir", stateDir}, &stdout, nil, BuildInfo{})
	if code != 0 {
		t.Fatalf("session map --all --json returned %d, want 0", code)
	}
	got = struct {
		Scope    string `json:"scope"`
		Total    int    `json:"total"`
		Active   int    `json:"active"`
		Recorded int    `json:"recorded"`
		Roots    []struct {
			Record   state.SessionRecord `json:"record"`
			Children []struct {
				Record state.SessionRecord `json:"record"`
			} `json:"children"`
		} `json:"roots"`
	}{}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal map --all returned error: %v\n%s", err, stdout.String())
	}
	if got.Scope != "all" || got.Total != 2 || got.Recorded != 2 || len(got.Roots) != 1 || got.Roots[0].Record.ID != "root" || len(got.Roots[0].Children) != 1 || got.Roots[0].Children[0].Record.ID != "child" {
		t.Fatalf("map json = %#v", got)
	}
}

func TestRunAddDryRunDoesNotWrite(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, "Host old\n  HostName old.example.com\n")

	code := Run([]string{"add", "--alias", "prod", "--host", "prod.example.com", "--config", config, "--dry-run"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[would-added] prod")
	assertContains(t, stdout.String(), "+Host prod")
	assertNotContains(t, readFile(t, config), "prod.example.com")
}

func TestRunAddWritesConfigAndCreatesBackup(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, "Host old\n  HostName old.example.com\n")

	code := Run([]string{"add", "--alias", "prod", "--host", "prod.example.com", "--user", "alice", "--config", config, "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[added] prod")
	assertContains(t, stdout.String(), "[backup]")
	got := readFile(t, config)
	assertContains(t, got, "Host prod")
	assertContains(t, got, "  HostName prod.example.com")
	assertContains(t, got, "  User alice")

	backups := globBackups(t, config)
	if len(backups) != 1 {
		t.Fatalf("backups = %#v, want one backup", backups)
	}
	assertContains(t, readFile(t, backups[0]), "Host old")
}

func TestRunAddUpdatesIncludedSourceByDefault(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	include := filepath.Join(dir, "config.d", "hosts.conf")
	if err := os.MkdirAll(filepath.Dir(include), 0o700); err != nil {
		t.Fatalf("os.MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(root, []byte("Include config.d/*.conf\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile root returned error: %v", err)
	}
	if err := os.WriteFile(include, []byte("Host prod\n  HostName old.example.com\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile include returned error: %v", err)
	}

	code := Run([]string{"add", "--alias", "prod", "--host", "new.example.com", "--yes", "--config", root}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, readFile(t, root), "Include")
	assertContains(t, readFile(t, root), "Host prod")
	assertNotContains(t, readFile(t, include), "new.example.com")
}

func TestRunAddWithoutExplicitConfigUpdatesIncludedSource(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".ssh", "config")
	include := filepath.Join(home, ".ssh", "config.d", "hosts.conf")
	if err := os.MkdirAll(filepath.Dir(include), 0o700); err != nil {
		t.Fatalf("os.MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(root, []byte("Include config.d/*.conf\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile root returned error: %v", err)
	}
	if err := os.WriteFile(include, []byte("Host prod\n  HostName old.example.com\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile include returned error: %v", err)
	}

	code := Run([]string{"add", "--alias", "prod", "--host", "new.example.com", "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertNotContains(t, readFile(t, root), "Host prod")
	assertContains(t, readFile(t, include), "new.example.com")
}

func TestRunEditSetUpdatesAlias(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `Host prod
  HostName old.example.com
  User old
  ForwardAgent yes
`)

	code := Run([]string{"edit", "set", "prod", "--host", "new.example.com", "--user", "alice", "--clear-port", "--config", config, "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	got := readFile(t, config)
	assertContains(t, got, "HostName new.example.com")
	assertContains(t, got, "User alice")
	assertContains(t, got, "ForwardAgent yes")
	assertNotContains(t, got, "old.example.com")
}

func TestRunEditDeleteRemovesOnlySelectedTokenFromMultiAlias(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `Host prod web
  HostName shared.example.com
`)

	code := Run([]string{"edit", "delete", "prod", "--config", config, "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	got := readFile(t, config)
	assertContains(t, got, "Host web")
	assertNotContains(t, got, "Host prod web")
}

func TestRunEditDeleteRequiresAllSourcesForDuplicateFiles(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".ssh", "config")
	first := filepath.Join(home, ".ssh", "config.d", "a.conf")
	second := filepath.Join(home, ".ssh", "config.d", "b.conf")
	if err := os.MkdirAll(filepath.Dir(first), 0o700); err != nil {
		t.Fatalf("os.MkdirAll returned error: %v", err)
	}
	if err := os.WriteFile(root, []byte("Include config.d/*.conf\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile root returned error: %v", err)
	}
	if err := os.WriteFile(first, []byte("Host prod\n  HostName one.example.com\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile first returned error: %v", err)
	}
	if err := os.WriteFile(second, []byte("Host prod\n  HostName two.example.com\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile second returned error: %v", err)
	}

	code := Run([]string{"edit", "delete", "prod", "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "--all-sources")

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"edit", "delete", "prod", "--all-sources", "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertNotContains(t, readFile(t, first), "Host prod")
	assertNotContains(t, readFile(t, second), "Host prod")
}

func TestRunEditDeleteProtectsWildcardStanzas(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `Host prod *.example.com
  User alice
`)

	code := Run([]string{"edit", "delete", "prod", "--config", config, "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "wildcard")

	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"edit", "delete", "prod", "--config", config, "--delete-patterns", "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, readFile(t, config), "Host *.example.com")
	assertNotContains(t, readFile(t, config), "Host prod *.example.com")
}

func TestRunEditDeleteAllRequiresExactConfirmation(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `Host prod
  HostName prod.example.com

Host web
  HostName web.example.com
`)

	code := Run([]string{"edit", "delete-all", "--config", config, "--confirm", "delete 1 aliases"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[skipped] confirmation did not match")
	assertContains(t, readFile(t, config), "Host prod")
	assertContains(t, readFile(t, config), "Host web")
}

func TestRunEditDeleteAllDryRunShowsDiff(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	config := writeConfig(t, `Host prod
  HostName prod.example.com

Host web
  HostName web.example.com
`)

	code := Run([]string{"edit", "delete-all", "--config", config, "--dry-run"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[would-removed] 2 aliases")
	assertContains(t, stdout.String(), "-Host prod")
	assertContains(t, readFile(t, config), "Host prod")
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

func TestRunAuthkeysUnknownSubcommand(t *testing.T) {
	var stderr bytes.Buffer

	code := Run([]string{"authkeys", "bogus"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), `unknown authkeys command "bogus"`)
}

func TestRunAuthkeysListJSONMissingFile(t *testing.T) {
	var stdout bytes.Buffer
	path := filepath.Join(t.TempDir(), ".ssh", "authorized_keys")

	code := Run([]string{"authkeys", "list", "--json", "--path", path}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}

	var got struct {
		Path string `json:"path"`
		Keys []any  `json:"keys"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v\n%s", err, stdout.String())
	}
	if got.Path != path || len(got.Keys) != 0 {
		t.Fatalf("output = %#v, want path and no keys", got)
	}
}

func TestRunAuthkeysListUsesEnvironmentPath(t *testing.T) {
	var stdout bytes.Buffer
	path := filepath.Join(t.TempDir(), "authorized_keys")
	t.Setenv("SSHERPA_AUTHORIZED_KEYS_PATH", path)

	code := Run([]string{"authkeys", "list", "--json"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), path)
}

func TestRunAuthkeysAddCreatesAuthorizedKeysWithMode600(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, ".ssh", "authorized_keys")
	fake := writeFakeSSHKeygen(t, dir, 0)

	code := Run([]string{"authkeys", "add", "--key", testEd25519Key, "--path", path, "--yes", "--ssh-keygen", fake}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[added]")
	assertContains(t, stdout.String(), "valid=1 added=1")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	assertContains(t, string(data), "# Created by ssherpa authkeys")
	assertContains(t, string(data), testEd25519Key)
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat authorized_keys: %v", err)
	}
	if got := stat.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
}

func TestRunAuthkeysAddKeyFile(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	keyFile := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(keyFile, []byte("# comment\n"+testEd25519Key+"\n"), 0o644); err != nil {
		t.Fatalf("write key file: %v", err)
	}
	fake := writeFakeSSHKeygen(t, dir, 0)

	code := Run([]string{"authkeys", "add", "--key-file", keyFile, "--path", path, "--yes", "--ssh-keygen", fake}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[added]")
	assertContains(t, readFile(t, path), testEd25519Key)
}

func TestRunAuthkeysMergeDryRunPreservesOptions(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	keysDir := filepath.Join(dir, "keys")
	if err := os.Mkdir(keysDir, 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	line := `from="10.0.0.0/8",command="echo hello world" ` + testEd25519Key
	if err := os.WriteFile(filepath.Join(keysDir, "alice.pub"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}
	fake := writeFakeSSHKeygen(t, dir, 0)

	code := Run([]string{"authkeys", "merge", "--from-dir", keysDir, "--path", path, "--dry-run", "--ssh-keygen", fake}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[would-merged]")
	assertContains(t, stdout.String(), line)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("authorized_keys exists after dry-run, err=%v", err)
	}
}

func TestRunAuthkeysReplaceCreatesBackup(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte(testEd25519Key+"\n"), 0o644); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	keysDir := filepath.Join(dir, "keys")
	if err := os.Mkdir(keysDir, 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, "ecdsa.pub"), []byte(testECDSAKey+"\n"), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}
	fake := writeFakeSSHKeygen(t, dir, 0)

	code := Run([]string{"authkeys", "replace", "--from-dir", keysDir, "--path", path, "--yes", "--ssh-keygen", fake}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[replaced]")
	assertContains(t, stdout.String(), "[backup]")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	if strings.Contains(string(data), "ssh-ed25519") || !strings.Contains(string(data), testECDSAKey) {
		t.Fatalf("authorized_keys = %q", string(data))
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat authorized_keys: %v", err)
	}
	if got := stat.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	backup := firstBackupPath(t, dir, "authorized_keys.ssherpa-backup.")
	backupData, err := os.ReadFile(backup)
	if err != nil {
		t.Fatalf("read backup: %v", err)
	}
	assertContains(t, string(backupData), testEd25519Key)
}

func TestRunAuthkeysReplaceRejectsDirectoryWithNoValidKeys(t *testing.T) {
	var stderr bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	keysDir := filepath.Join(dir, "keys")
	if err := os.Mkdir(keysDir, 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, "bad.pub"), []byte("ssh-ed25519 not@@base64 bad\n"), 0o644); err != nil {
		t.Fatalf("write key: %v", err)
	}

	code := Run([]string{"authkeys", "replace", "--from-dir", keysDir, "--path", path, "--yes"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "not valid base64")
	assertContains(t, stderr.String(), "no valid SSH public keys")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("authorized_keys exists after rejected replace, err=%v", err)
	}
}

func TestRunAuthkeysDeleteByFingerprintPreservesComments(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	contents := "# first\n" + testEd25519Key + "\n" + testECDSAKey + "\n# last\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	key, err := authkeys.ParsePublicKeyLine(testEd25519Key)
	if err != nil {
		t.Fatalf("parse key: %v", err)
	}
	fingerprint, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}

	code := Run([]string{"authkeys", "delete", "--fingerprint", fingerprint, "--path", path, "--yes"}, &stdout, &stderr, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0; stderr = %q", code, stderr.String())
	}
	assertContains(t, stdout.String(), "[removed]")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read authorized_keys: %v", err)
	}
	got := string(data)
	if strings.Contains(got, "ssh-ed25519") {
		t.Fatalf("authorized_keys still has deleted key: %q", got)
	}
	assertContains(t, got, "# first\n")
	assertContains(t, got, testECDSAKey)
	assertContains(t, got, "# last\n")
	_ = firstBackupPath(t, dir, "authorized_keys.ssherpa-backup.")
}

func TestRunAuthkeysAddRejectsInvalidKey(t *testing.T) {
	var stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "authorized_keys")

	code := Run([]string{"authkeys", "add", "--key", "ssh-ed25519 not@@base64 bad", "--path", path, "--yes"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1", code)
	}
	assertContains(t, stderr.String(), "not valid base64")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("authorized_keys exists after rejected add, err=%v", err)
	}
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

func writeFakeSSH(t *testing.T, exitCode int) (string, string) {
	t.Helper()

	dir := t.TempDir()
	logPath := filepath.Join(dir, "argv.log")
	path := filepath.Join(dir, "fake-ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(logPath) + "\n" +
		"printf '%s\\n' 'fake ssh stdout'\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	return path, logPath
}

func writeFakeSFTP(t *testing.T, exitCode int) (string, string, string) {
	t.Helper()

	dir := t.TempDir()
	argvLog := filepath.Join(dir, "argv.log")
	batchLog := filepath.Join(dir, "batch.log")
	path := filepath.Join(dir, "fake-sftp")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" > " + shellQuote(argvLog) + "\n" +
		"batch=$(cat)\n" +
		"printf '%s\\n' \"$batch\" > " + shellQuote(batchLog) + "\n" +
		"case \"$batch\" in\n" +
		"  *'cd /var/log'*'ls -la'*) printf '%s\\n' 'Remote working directory: /var/log' 'drwxr-xr-x    2 root root     4096 May 29 10:10 logs' '-rw-r--r--    1 root root      120 May 29 10:11 app.log' ;;\n" +
		"  *'ls -la'*) printf '%s\\n' 'Remote working directory: /tmp' '-rw-r--r--    1 root root      120 May 29 10:11 other.log' ;;\n" +
		"  *) printf '%s\\n' 'fake sftp stdout' ;;\n" +
		"esac\n" +
		"exit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	return path, argvLog, batchLog
}

// writeFakeSSHFlaky drops a shell script whose Nth invocation exits
// with exitCodes[N-1]. Once the slice is exhausted, every subsequent
// invocation uses the final element. It also appends each invocation's
// argv (space-separated) to argv.log so reconnect tests can count
// attempts. Useful for reconnect scenarios where the first few attempts
// should fail (e.g. 255, 255) and a later one should succeed (0).
func writeFakeSSHFlaky(t *testing.T, exitCodes []int) (string, string) {
	t.Helper()
	if len(exitCodes) == 0 {
		t.Fatalf("writeFakeSSHFlaky needs at least one exit code")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "argv.log")
	countPath := filepath.Join(dir, "invocation-count")
	path := filepath.Join(dir, "fake-ssh")

	var caseBranches strings.Builder
	for i, code := range exitCodes {
		fmt.Fprintf(&caseBranches, "  %d) exit %d ;;\n", i+1, code)
	}
	// Final fallback: anything past the explicit cases uses the last
	// exit code so a flaky-then-stable script reads naturally.
	fmt.Fprintf(&caseBranches, "  *) exit %d ;;\n", exitCodes[len(exitCodes)-1])

	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> " + shellQuote(logPath) + "\n" +
		"N=$(cat " + shellQuote(countPath) + " 2>/dev/null || echo 0)\n" +
		"N=$((N + 1))\n" +
		"printf '%s' \"$N\" > " + shellQuote(countPath) + "\n" +
		"printf '%s\\n' 'fake ssh stdout'\n" +
		"case \"$N\" in\n" +
		caseBranches.String() +
		"esac\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	return path, logPath
}

// countLines returns the number of newline-terminated lines in s.
// Used by reconnect tests to count attempt invocations recorded in
// the fake-ssh argv log.
func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n")
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) returned error: %v", path, err)
	}
	return string(data)
}

func ptrTime(value time.Time) *time.Time {
	return &value
}

func globBackups(t *testing.T, path string) []string {
	t.Helper()

	matches, err := filepath.Glob(path + ".ssherpa-backup.*")
	if err != nil {
		t.Fatalf("filepath.Glob returned error: %v", err)
	}
	return matches
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func assertContains(t *testing.T, got string, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("got %q, want substring %q", got, want)
	}
}

func writeFakeSSHKeygen(t *testing.T, dir string, exitCode int) string {
	t.Helper()
	path := filepath.Join(dir, "ssh-keygen")
	script := "#!/bin/sh\ncat >/dev/null\nexit " + strconv.Itoa(exitCode) + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh-keygen: %v", err)
	}
	return path
}

func firstBackupPath(t *testing.T, dir string, prefix string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			return filepath.Join(dir, entry.Name())
		}
	}
	t.Fatalf("no backup with prefix %q in %s", prefix, dir)
	return ""
}

func assertNotContains(t *testing.T, got string, unwanted string) {
	t.Helper()
	if strings.Contains(got, unwanted) {
		t.Fatalf("got %q, unwanted substring %q", got, unwanted)
	}
}
