package inband

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBase64Length(t *testing.T) {
	tests := []struct {
		size int64
		want int64
	}{
		{0, 0},
		{1, 4},
		{2, 4},
		{3, 4},
		{4, 8},
		{1024, 1368},
	}
	for _, tt := range tests {
		if got := Base64Length(tt.size); got != tt.want {
			t.Fatalf("Base64Length(%d) = %d, want %d", tt.size, got, tt.want)
		}
	}
}

func TestNewSendPlanBuildsHardenedCommands(t *testing.T) {
	hash := strings.Repeat("a", 64)
	plan, err := NewSendPlan(SendOptions{
		Destination: "/srv/app/it's here.bin",
		Size:        5,
		SHA256:      hash,
		Nonce:       "test nonce!",
	})
	if err != nil {
		t.Fatalf("NewSendPlan returned error: %v", err)
	}
	if plan.Base64Length != 8 {
		t.Fatalf("Base64Length = %d, want 8", plan.Base64Length)
	}
	if plan.TempPath != "/srv/app/it's here.bin.ssherpa.testnonce.tmp" {
		t.Fatalf("TempPath = %q", plan.TempPath)
	}
	for _, want := range []string{"stty -echo -ixon -icanon", "'SSHERPA_''C_READY", "head -c 8", "base64 -d", "base64 -D", ShellQuote(plan.TempPath), ShellQuote(plan.TempPath + ".b64")} {
		if !strings.Contains(plan.ReceiverCommand, want) {
			t.Fatalf("receiver command = %q, want substring %q", plan.ReceiverCommand, want)
		}
	}
	for _, want := range []string{"sha256sum", "shasum -a 256", "openssl dgst -sha256", "exists 73", "mv", "'SSHERPA_''C_DONE", ShellQuote("/srv/app/it's here.bin")} {
		if !strings.Contains(plan.CommitCommand, want) {
			t.Fatalf("commit command = %q, want substring %q", plan.CommitCommand, want)
		}
	}
	if !strings.Contains(plan.ProbeCommand, "command -v base64") || !strings.Contains(plan.ProbeCommand, "'SSHERPA_''C_PROBE") {
		t.Fatalf("probe command = %q, want base64 probe and split sentinel", plan.ProbeCommand)
	}
}

// TestPlanCommandsOmitLiteralSentinels is the HIGH-26 regression: the
// session driver matches literal sentinels in PTY output, and the remote
// tty echoes every typed command, so no command text may ever contain a
// sentinel verbatim.
func TestPlanCommandsOmitLiteralSentinels(t *testing.T) {
	plan, err := NewSendPlan(SendOptions{
		Destination: "/srv/app/out.bin",
		Size:        5,
		SHA256:      strings.Repeat("a", 64),
		Nonce:       "n1",
	})
	if err != nil {
		t.Fatalf("NewSendPlan returned error: %v", err)
	}
	commands := map[string]string{
		"probe":    plan.ProbeCommand,
		"receiver": plan.ReceiverCommand,
		"commit":   plan.CommitCommand,
	}
	sentinels := []string{ProbePrefix, ReadyPrefix, DonePrefix, FailPrefix}
	for name, command := range commands {
		for _, sentinel := range sentinels {
			if strings.Contains(command, sentinel) {
				t.Errorf("%s command contains literal sentinel %q; its PTY echo would satisfy the driver's output matcher: %q", name, sentinel, command)
			}
		}
	}
}

func TestNewSendPlanRejectsOversizePayload(t *testing.T) {
	_, err := NewSendPlan(SendOptions{
		Destination: "/tmp/out",
		Size:        11,
		MaxBytes:    10,
		SHA256:      strings.Repeat("a", 64),
	})
	if err == nil || !strings.Contains(err.Error(), "max in-band size") {
		t.Fatalf("error = %v, want max-size error", err)
	}
}

// TestNewSendPlanRejectsSentinelBearingPaths pins that no plan may
// embed the sentinel stem in a path: the receiver and commit commands
// quote paths verbatim, so /tmp/SSHERPA_C_READY.bin would put a literal
// sentinel back into the typed command and its PTY echo would satisfy
// the driver's matcher — the exact hole the quote-split closed.
func TestNewSendPlanRejectsSentinelBearingPaths(t *testing.T) {
	hash := strings.Repeat("a", 64)
	tests := []struct {
		name string
		opts SendOptions
	}{
		{"destination", SendOptions{Destination: "/tmp/SSHERPA_C_READY.bin", Size: 5, SHA256: hash}},
		{"temp path", SendOptions{Destination: "/tmp/out.bin", TempPath: "/tmp/SSHERPA_C_DONE.tmp", Size: 5, SHA256: hash}},
		{"nonce-derived temp path", SendOptions{Destination: "/tmp/out.bin", Nonce: "SSHERPA_C_PROBE", Size: 5, SHA256: hash}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewSendPlan(tt.opts); err == nil || !strings.Contains(err.Error(), "sentinel") {
				t.Fatalf("NewSendPlan error = %v, want sentinel rejection", err)
			}
		})
	}

	if _, _, err := NewSendPlanFromReader("/tmp/SSHERPA_C_READY.bin", "n1", strings.NewReader("hello"), 0); err == nil || !strings.Contains(err.Error(), "sentinel") {
		t.Fatalf("NewSendPlanFromReader error = %v, want sentinel rejection", err)
	}
}

func TestParseCompletion(t *testing.T) {
	hash := strings.Repeat("b", 64)
	ok, err := ParseCompletion("noise\nSSHERPA_C_DONE 0 "+hash+"\n", hash)
	if err != nil || !ok {
		t.Fatalf("ParseCompletion returned ok=%v err=%v, want success", ok, err)
	}
	if ok, err := ParseCompletion("SSHERPA_C_DONE 0 "+strings.Repeat("c", 64)+"\n", hash); ok || err == nil || !strings.Contains(err.Error(), "did not match") {
		t.Fatalf("mismatch returned ok=%v err=%v, want mismatch error", ok, err)
	}
	if ok, err := ParseCompletion("SSHERPA_C_DONE 1 "+hash+"\n", hash); ok || err == nil || !strings.Contains(err.Error(), "status 1") {
		t.Fatalf("failure returned ok=%v err=%v, want status error", ok, err)
	}
	if ok, err := ParseCompletion("SSHERPA_C_DONE 73 "+hash+"\n", hash); ok || err == nil || !strings.Contains(err.Error(), "status 73") {
		t.Fatalf("overwrite refusal returned ok=%v err=%v, want status 73 error", ok, err)
	}
}

func TestParseCompletionFailureSentinels(t *testing.T) {
	hash := strings.Repeat("b", 64)
	tests := []struct {
		name    string
		output  string
		wantErr string
	}{
		{"head", FailPrefix + " head 141\n", "head exit 141"},
		{"base64", FailPrefix + " base64 1\n", "base64 decode failed (exit 1)"},
		{"hash", FailPrefix + " hash " + strings.Repeat("c", 64) + "\n", "did not match local sha256 " + hash},
		{"hash none", FailPrefix + " hash none\n", "remote sha256 none"},
		{"exists", FailPrefix + " exists 73\n", "refusing to overwrite"},
		{"mv", FailPrefix + " mv 1\n", "rename into destination failed (exit 1)"},
		{"unknown reason", FailPrefix + " gremlins 9\n", "gremlins"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, err := ParseCompletion("noise\n"+tt.output, hash)
			if ok {
				t.Fatalf("ParseCompletion returned ok for failure output %q", tt.output)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
			if errors.Is(err, ErrNoCompletion) {
				t.Fatalf("error %v would not fail fast in the session driver", err)
			}
		})
	}
}

// TestParseCompletionIgnoresPartialAndEchoedLines pins the two ways a
// sentinel may appear in the buffer without being a real result: the PTY
// echo of the typed command (quote-split, so the literal never matches)
// and a final line still being streamed (no newline yet, so it must not
// be parsed as a mismatch).
func TestParseCompletionIgnoresPartialAndEchoedLines(t *testing.T) {
	hash := strings.Repeat("b", 64)
	plan, err := NewSendPlan(SendOptions{Destination: "/srv/app/out.bin", Size: 5, SHA256: hash, Nonce: "echo"})
	if err != nil {
		t.Fatalf("NewSendPlan returned error: %v", err)
	}

	echoed := plan.ProbeCommand + "\r\n" + plan.ReceiverCommand + "\r\n"
	if ok, err := ParseCompletion(echoed, hash); ok || !errors.Is(err, ErrNoCompletion) {
		t.Fatalf("echoed commands returned ok=%v err=%v, want ErrNoCompletion", ok, err)
	}

	partials := []string{
		DonePrefix + " 0 " + hash[:10],
		FailPrefix + " head",
	}
	for _, partial := range partials {
		if ok, err := ParseCompletion("noise\n"+partial, hash); ok || !errors.Is(err, ErrNoCompletion) {
			t.Fatalf("partial line %q returned ok=%v err=%v, want ErrNoCompletion", partial, ok, err)
		}
	}
}

func TestNewSendPlanFromReaderHashesPayload(t *testing.T) {
	payload := "hello"
	sum := sha256.Sum256([]byte(payload))
	plan, data, err := NewSendPlanFromReader("/tmp/out", "abc", strings.NewReader(payload), 0)
	if err != nil {
		t.Fatalf("NewSendPlanFromReader returned error: %v", err)
	}
	if string(data) != payload {
		t.Fatalf("data = %q, want %q", data, payload)
	}
	if plan.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("SHA256 = %q, want payload digest", plan.SHA256)
	}
}

func TestShellQuote(t *testing.T) {
	if got, want := ShellQuote("it's here"), "'it'\\''s here'"; got != want {
		t.Fatalf("ShellQuote returned %q, want %q", got, want)
	}
}

// runShell executes a plan one-liner under a real POSIX shell, the same
// way the remote side would, and returns its combined output.
func runShell(t *testing.T, command string, stdin []byte) string {
	t.Helper()
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	cmd := exec.Command(shell, "-c", command)
	cmd.Stdin = bytes.NewReader(stdin)
	out, _ := cmd.CombinedOutput()
	return string(out)
}

func TestProbeCommandEmitsLiteralSentinelInOutput(t *testing.T) {
	out := runShell(t, ProbeCommand(), nil)
	if !strings.Contains(out, ProbePrefix+" ok") && !strings.Contains(out, ProbePrefix+" fail") {
		t.Fatalf("probe output = %q, want literal %q ok/fail sentinel", out, ProbePrefix)
	}
}

func TestReceiverCommandRoundTripUnderShell(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	payload := []byte("hello in-band")
	sum := sha256.Sum256(payload)
	plan, err := NewSendPlan(SendOptions{
		Destination: dest,
		Size:        int64(len(payload)),
		SHA256:      hex.EncodeToString(sum[:]),
		Nonce:       "roundtrip",
	})
	if err != nil {
		t.Fatalf("NewSendPlan returned error: %v", err)
	}

	out := runShell(t, plan.ReceiverCommand, []byte(base64.StdEncoding.EncodeToString(payload)))

	if !strings.Contains(out, ReadyPrefix) {
		t.Fatalf("receiver output = %q, want literal %q", out, ReadyPrefix)
	}
	ok, err := ParseCompletion(out, plan.SHA256)
	if err != nil || !ok {
		t.Fatalf("ParseCompletion(ok=%v, err=%v) for output %q", ok, err, out)
	}
	data, err := os.ReadFile(dest)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("destination = %q err=%v, want payload", data, err)
	}
	assertRemoved(t, plan.TempPath)
	assertRemoved(t, plan.TempPath+".b64")
}

func TestReceiverCommandFailurePathsCleanUpAndReportReasons(t *testing.T) {
	payload := []byte("hello in-band")
	sum := sha256.Sum256(payload)
	goodHash := hex.EncodeToString(sum[:])
	encoded := base64.StdEncoding.EncodeToString(payload)

	tests := []struct {
		name     string
		planHash string
		stdin    string
		prepare  func(t *testing.T, dest string)
		wantErr  string
	}{
		{
			name:     "decode failure",
			planHash: goodHash,
			stdin:    strings.Repeat("!", len(encoded)),
			wantErr:  "base64 decode failed",
		},
		{
			name:     "hash mismatch",
			planHash: strings.Repeat("0", 64),
			stdin:    encoded,
			wantErr:  "did not match local sha256",
		},
		{
			name:     "destination exists",
			planHash: goodHash,
			stdin:    encoded,
			prepare: func(t *testing.T, dest string) {
				if err := os.WriteFile(dest, []byte("original"), 0o600); err != nil {
					t.Fatalf("write destination: %v", err)
				}
			},
			wantErr: "refusing to overwrite",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "out.bin")
			if tt.prepare != nil {
				tt.prepare(t, dest)
			}
			plan, err := NewSendPlan(SendOptions{
				Destination: dest,
				Size:        int64(len(payload)),
				SHA256:      tt.planHash,
				Nonce:       "failure",
			})
			if err != nil {
				t.Fatalf("NewSendPlan returned error: %v", err)
			}

			out := runShell(t, plan.ReceiverCommand, []byte(tt.stdin))

			ok, err := ParseCompletion(out, plan.SHA256)
			if ok || err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ParseCompletion(ok=%v, err=%v) for output %q, want error %q", ok, err, out, tt.wantErr)
			}
			assertRemoved(t, plan.TempPath)
			assertRemoved(t, plan.TempPath+".b64")
			if tt.prepare != nil {
				data, err := os.ReadFile(dest)
				if err != nil || string(data) != "original" {
					t.Fatalf("destination = %q err=%v, want original preserved", data, err)
				}
			}
		})
	}
}

func assertRemoved(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("remote temp artifact %s still present (err=%v)", path, err)
	}
}
