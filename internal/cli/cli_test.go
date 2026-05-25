package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/authkeys"
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

func TestRunHelpCommand(t *testing.T) {
	var stdout bytes.Buffer

	code := Run([]string{"help"}, &stdout, nil, BuildInfo{})

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	assertContains(t, stdout.String(), "Usage:")
	assertContains(t, stdout.String(), "Available Commands:")
	assertContains(t, stdout.String(), "theme      Build and save")
	assertContains(t, stdout.String(), "Theme Commands:")
	assertContains(t, stdout.String(), "Phase 10:")
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
	assertContains(t, stdout.String(), "[print] ssh prod -L 8080:localhost:8080")
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
	if strings.Join(got.Argv, "\x00") != "ssh\x00prod" || got.Alias != "prod" {
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
	assertContains(t, stdout.String(), "[print] ssh prod --help")
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

func TestRunConnectRejectsInvalidThemeFlag(t *testing.T) {
	var stderr bytes.Buffer
	code := Run([]string{"--print", "--theme", "imaginary"}, nil, &stderr, BuildInfo{})

	if code != 1 {
		t.Fatalf("Run returned %d, want 1; stderr = %q", code, stderr.String())
	}
	assertContains(t, stderr.String(), "unknown theme")
}

func TestRunConnectPickerAddCarriesConfigFlag(t *testing.T) {
	args := connectFlagsAsAddArgs(connectFlags{inventoryFlags: inventoryFlags{Config: "/tmp/config"}})

	if strings.Join(args, "\x00") != "--config\x00/tmp/config" {
		t.Fatalf("args = %#v, want config passthrough", args)
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
		ThemeName:         "vivid",
		ThemeFile:         "/tmp/theme.conf",
		SSHArgs:           []string{"-v"},
	}

	args := connectFlagsAsJumpArgs(flags)
	want := "--all\x00--print\x00--filter\x00prod\x00--user\x00alice\x00--config\x00/tmp/config\x00--ssh-binary\x00/tmp/fake-ssh\x00--direct\x00--state-dir\x00/tmp/state\x00--latency-warn\x002s\x00--latency-disconnect\x0030s\x00--composer-key\x00Ctrl-R\x00--no-kitty\x00--no-color\x00--theme\x00vivid\x00--theme-file\x00/tmp/theme.conf\x00--\x00-v"
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
	if flags.ThemeName != "vivid" || flags.ThemeFile != "/tmp/theme.conf" || !flags.NoColor {
		t.Fatalf("flags = %#v, want vivid theme file and no color", flags)
	}
}

func TestFormatThemeConfig(t *testing.T) {
	got := string(formatThemeConfig(termstyle.ThemeConfig{
		BaseName: "terminal",
		Specs: map[termstyle.Role]string{
			termstyle.RolePrimary: "cyan",
			termstyle.RolePill:    "bold reverse",
		},
	}))

	assertContains(t, got, "theme = terminal")
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
	assertContains(t, stdout.String(), "[print] ssh -J bastion,edge prod -A")
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
	assertContains(t, stdout.String(), "[print] ssh -D 127.0.0.1:1080 -C -N -o ExitOnForwardFailure=yes prod")
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
	assertContains(t, stdout.String(), "[print] ssh -D 0.0.0.0:1081 -C -N -o ExitOnForwardFailure=yes prod -v")
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
	assertContains(t, showStdout.String(), "route:\tbastion -> prod")
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
	assertContains(t, stdout.String(), "+- prod [active]")
	assertContains(t, stdout.String(), "route: bastion -> prod")
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
