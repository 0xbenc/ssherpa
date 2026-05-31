package inband

import (
	"crypto/sha256"
	"encoding/hex"
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
	for _, want := range []string{"stty -echo -ixon -icanon", ReadyPrefix, "head -c 8", "base64 -d", "base64 -D", ShellQuote(plan.TempPath), ShellQuote(plan.TempPath + ".b64")} {
		if !strings.Contains(plan.ReceiverCommand, want) {
			t.Fatalf("receiver command = %q, want substring %q", plan.ReceiverCommand, want)
		}
	}
	for _, want := range []string{"sha256sum", "shasum -a 256", "openssl dgst -sha256", "rc=73", "mv", DonePrefix, ShellQuote("/srv/app/it's here.bin")} {
		if !strings.Contains(plan.CommitCommand, want) {
			t.Fatalf("commit command = %q, want substring %q", plan.CommitCommand, want)
		}
	}
	if !strings.Contains(plan.ProbeCommand, "command -v base64") || !strings.Contains(plan.ProbeCommand, ProbePrefix) {
		t.Fatalf("probe command = %q, want base64 probe and sentinel", plan.ProbeCommand)
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
