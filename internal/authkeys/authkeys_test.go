package authkeys

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	ed25519Key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIDb7Ccg8MuAtwJl6bsEjuCHWDtiRtivD3c1vzgbG7N1q alice@example"
	ecdsaKey   = "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBDxfAByeMchlvCAqslVGYuzLS4lr02wvFIn2rz4Jp40NrbYkbazkdAtflVPDCCewMSI2I0ujG0JJeZEjYarX8sI= ecdsa@example"
)

func TestParsePublicKeyLinePlain(t *testing.T) {
	key, err := ParsePublicKeyLine(ed25519Key)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine returned error: %v", err)
	}
	if key.Type != "ssh-ed25519" {
		t.Fatalf("Type = %q, want ssh-ed25519", key.Type)
	}
	if key.Comment != "alice@example" {
		t.Fatalf("Comment = %q, want alice@example", key.Comment)
	}
	if got := key.Render(); got != ed25519Key {
		t.Fatalf("Render = %q, want %q", got, ed25519Key)
	}
}

func TestParsePublicKeyLinePreservesOptions(t *testing.T) {
	line := `from="10.0.0.0/8",command="echo hello world",no-agent-forwarding ` + ed25519Key

	key, err := ParsePublicKeyLine(line)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine returned error: %v", err)
	}

	wantOptions := `from="10.0.0.0/8",command="echo hello world",no-agent-forwarding`
	if key.Options != wantOptions {
		t.Fatalf("Options = %q, want %q", key.Options, wantOptions)
	}
	if got := key.Render(); got != line {
		t.Fatalf("Render = %q, want %q", got, line)
	}
}

func TestParsePublicKeyLineSupportsCertTypes(t *testing.T) {
	line := strings.Replace(ed25519Key, "ssh-ed25519", "ssh-ed25519-cert-v01@openssh.com", 1)

	key, err := ParsePublicKeyLine(line)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine returned error: %v", err)
	}
	if key.Type != "ssh-ed25519-cert-v01@openssh.com" {
		t.Fatalf("Type = %q, want cert type", key.Type)
	}
}

func TestParsePublicKeyLineRejectsInvalidBlob(t *testing.T) {
	_, err := ParsePublicKeyLine("ssh-ed25519 not-base64 comment")
	if err == nil {
		t.Fatal("ParsePublicKeyLine returned nil error, want invalid base64")
	}
}

func TestParsePublicKeyLineRejectsControlCharsInComment(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"newline", ed25519Key + "\nssh-ed25519 AAAAinjected injected@example"},
		{"carriage return", ed25519Key + "\rinjected"},
		{"nul", ed25519Key + "\x00injected"},
		{"escape", ed25519Key + "\x1binjected"},
		{"delete", ed25519Key + "\x7finjected"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePublicKeyLine(tt.line)
			if err == nil {
				t.Fatal("ParsePublicKeyLine returned nil error, want rejected control character")
			}
			if !strings.Contains(err.Error(), "comment cannot contain control characters") {
				t.Fatalf("error = %v, want control-character diagnostic", err)
			}
		})
	}
}

func TestParsePublicKeyLineRejectsControlCharsInOptions(t *testing.T) {
	cases := []struct {
		name    string
		options string
	}{
		{"newline in quoted command", "command=\"echo a\nrm -rf /\""},
		{"newline between option fields", "command=\"echo a\"\nno-pty"},
		{"carriage return in quoted command", "command=\"echo a\rinjected\""},
		{"nul in quoted command", "command=\"echo a\x00b\""},
		{"escape in quoted from", "from=\"10.0.0.0/8\x1bfoo\""},
		{"delete in quoted environment", "environment=\"FOO=bar\x7f\""},
		{"c1 csi in quoted command", "command=\"echo a\u009bb\""},
		{"c1 nel in quoted from", "from=\"10.0.0.0/8\u0085foo\""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParsePublicKeyLine(tt.options + " " + ed25519Key)
			if err == nil {
				t.Fatal("ParsePublicKeyLine returned nil error, want rejected control character")
			}
			if !strings.Contains(err.Error(), "options cannot contain control characters") {
				t.Fatalf("error = %v, want options control-character diagnostic", err)
			}
		})
	}
}

func TestParsePublicKeyLineAllowsComplexQuotedOptions(t *testing.T) {
	cases := []struct {
		name    string
		options string
	}{
		{"quoted command", `command="echo hi"`},
		{"quoted from", `from="10.0.0.0/8"`},
		{"comma separated mixed quotes", `from="10.0.0.0/8",command="echo hello world",no-agent-forwarding`},
		{"escaped quote in command", `command="echo \"quoted\""`},
		{"environment and permitopen", `environment="FOO=bar",permitopen="192.0.2.1:80"`},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			line := tt.options + " " + ed25519Key
			key, err := ParsePublicKeyLine(line)
			if err != nil {
				t.Fatalf("ParsePublicKeyLine returned error: %v", err)
			}
			if key.Options != tt.options {
				t.Fatalf("Options = %q, want %q", key.Options, tt.options)
			}
			if got := key.Render(); got != line {
				t.Fatalf("Render = %q, want %q", got, line)
			}
		})
	}
}

func TestParsePublicKeyLineAllowsTabInComment(t *testing.T) {
	line := ed25519Key + "\twork laptop"

	key, err := ParsePublicKeyLine(line)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine returned error: %v", err)
	}
	if key.Comment != "alice@example\twork laptop" {
		t.Fatalf("Comment = %q, want tab preserved", key.Comment)
	}
	if got := key.Render(); got != line {
		t.Fatalf("Render = %q, want %q", got, line)
	}
}

func TestFingerprintMatchesOpenSSHFormat(t *testing.T) {
	key := mustParseKey(t, ed25519Key)

	fp, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("SHA256Fingerprint returned error: %v", err)
	}
	if fp != "SHA256:HIw5mTiqXNXNO2h1Vh9R81VrAaKPj4DqNvb3oWElxwk" {
		t.Fatalf("fingerprint = %q", fp)
	}
}

func TestValidatorUsesFakeSSHKeygen(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ssh-keygen")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ncat >/dev/null\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake ssh-keygen: %v", err)
	}

	result, err := Validator{SSHKeygenPath: fake}.Validate(mustParseKey(t, ed25519Key))
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if !result.UsedSSHKeygen || result.StructuralOnly {
		t.Fatalf("result = %#v, want fake ssh-keygen validation", result)
	}
}

func TestValidatorReportsMissingExplicitSSHKeygen(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-ssh-keygen")

	_, err := Validator{SSHKeygenPath: missing}.Validate(mustParseKey(t, ed25519Key))

	if err == nil {
		t.Fatal("Validate returned nil error, want missing ssh-keygen error")
	}
	if !strings.Contains(err.Error(), "from --ssh-keygen") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("error = %v, want explicit ssh-keygen diagnostic", err)
	}
}

func TestCollectFromDirPrefersAuthorizedKeysDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "outside.pub"), []byte(ed25519Key+"\n"), 0o644); err != nil {
		t.Fatalf("write outside pub: %v", err)
	}
	special := filepath.Join(dir, "authorized_keys")
	if err := os.Mkdir(special, 0o755); err != nil {
		t.Fatalf("mkdir authorized_keys: %v", err)
	}
	line := `from="192.0.2.0/24" ` + ecdsaKey
	if err := os.WriteFile(filepath.Join(special, "keys"), []byte("# comment\n"+line+"\n"), 0o644); err != nil {
		t.Fatalf("write authorized_keys file: %v", err)
	}

	result, err := CollectFromDir(dir, Validator{SkipSSHKeygen: true})
	if err != nil {
		t.Fatalf("CollectFromDir returned error: %v", err)
	}
	if len(result.Keys) != 1 {
		t.Fatalf("len(Keys) = %d, want 1", len(result.Keys))
	}
	if result.Keys[0].Render() != line {
		t.Fatalf("key = %q, want %q", result.Keys[0].Render(), line)
	}
	if result.Stats.Files != 1 || result.Stats.Valid != 1 || result.Stats.Ignored != 1 {
		t.Fatalf("Stats = %#v", result.Stats)
	}
}

func TestCollectFromDirReportsInvalidAndDuplicate(t *testing.T) {
	dir := t.TempDir()
	contents := strings.Join([]string{
		ed25519Key,
		`from="10.0.0.0/8" ` + ed25519Key,
		"ssh-ed25519 not@@base64 comment",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "a.pub"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	result, err := CollectFromDir(dir, Validator{SkipSSHKeygen: true})
	if err != nil {
		t.Fatalf("CollectFromDir returned error: %v", err)
	}
	if result.Stats.Valid != 1 || result.Stats.Duplicate != 1 || result.Stats.Invalid != 1 {
		t.Fatalf("Stats = %#v", result.Stats)
	}
	if len(result.Diagnostics) != 2 {
		t.Fatalf("len(Diagnostics) = %d, want invalid and duplicate-options warning", len(result.Diagnostics))
	}
}

func TestCollectFromDirReportsControlCharCommentInvalid(t *testing.T) {
	dir := t.TempDir()
	contents := strings.Join([]string{
		ed25519Key + "\rinjected",
		ecdsaKey,
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, "a.pub"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	result, err := CollectFromDir(dir, Validator{SkipSSHKeygen: true})
	if err != nil {
		t.Fatalf("CollectFromDir returned error: %v", err)
	}
	if result.Stats.Valid != 1 || result.Stats.Invalid != 1 || result.Stats.Added != 0 {
		t.Fatalf("Stats = %#v", result.Stats)
	}
	if len(result.Keys) != 1 || result.Keys[0].Render() != ecdsaKey {
		t.Fatalf("Keys = %#v, want only valid ecdsa key", result.Keys)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "comment cannot contain control characters") {
		t.Fatalf("Diagnostics = %#v, want control-character warning", result.Diagnostics)
	}
}

func TestPlanAddRejectsProgrammaticControlCharComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	key := mustParseKey(t, ed25519Key)
	key.Comment = "alice@example\nssh-ed25519 AAAAinjected injected@example"

	_, err := PlanAdd(path, key, PlanOptions{Validator: Validator{SkipSSHKeygen: true}})
	if err == nil {
		t.Fatal("PlanAdd returned nil error, want rejected control character")
	}
	if !strings.Contains(err.Error(), "comment cannot contain control characters") {
		t.Fatalf("error = %v, want control-character diagnostic", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("authorized_keys exists after rejected plan, err=%v", err)
	}
}

func TestPlanAddRejectsProgrammaticControlCharOptions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	key := mustParseKey(t, ed25519Key)
	key.Options = "command=\"echo a\nrm -rf /\""

	_, err := PlanAdd(path, key, PlanOptions{Validator: Validator{SkipSSHKeygen: true}})
	if err == nil {
		t.Fatal("PlanAdd returned nil error, want rejected control character")
	}
	if !strings.Contains(err.Error(), "options cannot contain control characters") {
		t.Fatalf("error = %v, want options control-character diagnostic", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("authorized_keys exists after rejected plan, err=%v", err)
	}
}

func TestPlanAddDuplicateWithDifferentOptionsWarnsWithoutChanging(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	existing := `from="10.0.0.0/8" ` + ed25519Key + "\n"
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	plan, err := PlanAdd(path, mustParseKey(t, ed25519Key), PlanOptions{Validator: Validator{SkipSSHKeygen: true}})
	if err != nil {
		t.Fatalf("PlanAdd returned error: %v", err)
	}
	if plan.Changed {
		t.Fatalf("Changed = true, want false")
	}
	if plan.Stats.AlreadyPresent != 1 || len(plan.Diagnostics) != 1 {
		t.Fatalf("Stats = %#v Diagnostics = %#v", plan.Stats, plan.Diagnostics)
	}
}

func TestPlanMergeAppendsOnlyNewKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte(ed25519Key+"\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	keysDir := filepath.Join(dir, "keys")
	if err := os.Mkdir(keysDir, 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, "a.pub"), []byte(ed25519Key+"\n"+ecdsaKey+"\n"), 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	plan, err := PlanMerge(path, keysDir, PlanOptions{Validator: Validator{SkipSSHKeygen: true}})
	if err != nil {
		t.Fatalf("PlanMerge returned error: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if plan.Stats.Added != 1 || plan.Stats.AlreadyPresent != 1 {
		t.Fatalf("Stats = %#v", plan.Stats)
	}
	if got := string(plan.NewData); !strings.Contains(got, ecdsaKey) || strings.Count(got, "ssh-ed25519") != 1 {
		t.Fatalf("NewData = %q", got)
	}
}

func TestPlanReplaceRendersOnlyImportedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(path, []byte("# keep me out\n"+ed25519Key+"\n"), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	keysDir := filepath.Join(dir, "keys")
	if err := os.Mkdir(keysDir, 0o755); err != nil {
		t.Fatalf("mkdir keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, "z.pub"), []byte(ecdsaKey+"\n"), 0o644); err != nil {
		t.Fatalf("write pub: %v", err)
	}

	plan, err := PlanReplace(path, keysDir, PlanOptions{Validator: Validator{SkipSSHKeygen: true}})
	if err != nil {
		t.Fatalf("PlanReplace returned error: %v", err)
	}
	got := string(plan.NewData)
	if strings.Contains(got, "keep me out") || strings.Contains(got, "ssh-ed25519") {
		t.Fatalf("NewData kept old content: %q", got)
	}
	if !strings.Contains(got, ecdsaKey) {
		t.Fatalf("NewData missing imported key: %q", got)
	}
}

func TestPlanDeletePreservesCommentsAndUnrelatedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	key := mustParseKey(t, ed25519Key)
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	contents := "# first\n" + ed25519Key + "\n" + ecdsaKey + "\n# last\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	plan, err := PlanDelete(path, []string{fp})
	if err != nil {
		t.Fatalf("PlanDelete returned error: %v", err)
	}
	got := string(plan.NewData)
	if strings.Contains(got, "ssh-ed25519") {
		t.Fatalf("NewData still contains deleted key: %q", got)
	}
	if !strings.Contains(got, "# first\n") || !strings.Contains(got, ecdsaKey) || !strings.Contains(got, "# last\n") {
		t.Fatalf("NewData did not preserve unrelated lines: %q", got)
	}
	if plan.Stats.Deleted != 1 {
		t.Fatalf("Deleted = %d, want 1", plan.Stats.Deleted)
	}
}

func TestPlanDeleteCarriesEveryEntrySharingFingerprint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")
	key := mustParseKey(t, ed25519Key)
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		t.Fatalf("fingerprint: %v", err)
	}
	contents := `from="10.0.0.0/8" ` + ed25519Key + "\n" +
		`command="uptime",no-pty ` + ed25519Key + "\n" +
		ecdsaKey + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	plan, err := PlanDelete(path, []string{fp})
	if err != nil {
		t.Fatalf("PlanDelete returned error: %v", err)
	}
	if plan.Stats.Deleted != 2 || len(plan.Keys) != 2 {
		t.Fatalf("Deleted = %d Keys = %#v, want both matching entries", plan.Stats.Deleted, plan.Keys)
	}
	if plan.Keys[0].Options != `from="10.0.0.0/8"` || plan.Keys[0].Line != 1 {
		t.Fatalf("Keys[0] = %#v, want first entry with options", plan.Keys[0])
	}
	if plan.Keys[1].Options != `command="uptime",no-pty` || plan.Keys[1].Line != 2 {
		t.Fatalf("Keys[1] = %#v, want second entry with options", plan.Keys[1])
	}
	if got := string(plan.NewData); strings.Contains(got, "ssh-ed25519") || !strings.Contains(got, ecdsaKey) {
		t.Fatalf("NewData = %q, want only unrelated key preserved", got)
	}
}

func mustParseKey(t *testing.T, line string) AuthorizedKey {
	t.Helper()
	key, err := ParsePublicKeyLine(line)
	if err != nil {
		t.Fatalf("ParsePublicKeyLine(%q): %v", line, err)
	}
	return key
}
