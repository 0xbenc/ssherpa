package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/0xbenc/ssherpa/internal/portable"
	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
)

func seedExportConfig(t *testing.T) string {
	t.Helper()
	return writeConfig(t, `Host prod
  HostName prod.example.com
  User alice
  Port 2222
  IdentityFile ~/.ssh/prod_key
  IdentitiesOnly yes
  ProxyJump bastion

Host bastion
  HostName bastion.example.com
`)
}

func seedExportState(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := state.WriteForward(dir, state.StoredForward{Name: "db", SSHAlias: "prod", LocalPort: 5432, RemoteHost: "db.internal", RemotePort: 5432}); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}
	if err := state.WriteProxy(dir, state.StoredProxy{Name: "socks", SSHAlias: "prod", Port: 1080}); err != nil {
		t.Fatalf("WriteProxy: %v", err)
	}
	return dir
}

func TestRunExportWritesBundle(t *testing.T) {
	var stdout, stderr bytes.Buffer
	config := seedExportConfig(t)
	stateDir := seedExportState(t)
	out := filepath.Join(t.TempDir(), "bundle.json")

	code := runExport([]string{"--output", out, "--config", config, "--state-dir", stateDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runExport = %d, want 0; stderr=%q", code, stderr.String())
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}
	bundle, err := portable.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(bundle.Aliases) != 2 || len(bundle.Forwards) != 1 || len(bundle.Proxies) != 1 {
		t.Fatalf("counts = %d/%d/%d, want 2/1/1", len(bundle.Aliases), len(bundle.Forwards), len(bundle.Proxies))
	}
	var prod portable.AliasEntry
	for _, a := range bundle.Aliases {
		if a.Alias == "prod" {
			prod = a
		}
	}
	if !prod.IdentitiesOnly || prod.ProxyJump != "bastion" {
		t.Fatalf("prod alias did not round-trip managed fields: %#v", prod)
	}
}

func TestRunExportCherryPickAlias(t *testing.T) {
	var stdout, stderr bytes.Buffer
	config := seedExportConfig(t)
	stateDir := seedExportState(t)
	out := filepath.Join(t.TempDir(), "bundle.json")

	code := runExport([]string{"--output", out, "--alias", "prod", "--config", config, "--state-dir", stateDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runExport = %d, want 0; stderr=%q", code, stderr.String())
	}
	data, _ := os.ReadFile(out)
	bundle, _ := portable.Unmarshal(data)
	if len(bundle.Aliases) != 1 || bundle.Aliases[0].Alias != "prod" {
		t.Fatalf("cherry-pick exported %#v, want only prod", bundle.Aliases)
	}
	if len(bundle.Forwards) != 0 || len(bundle.Proxies) != 0 {
		t.Fatalf("cherry-pick should not include presets: %#v %#v", bundle.Forwards, bundle.Proxies)
	}
}

func TestRunExportUnknownSelectorExits2(t *testing.T) {
	var stdout, stderr bytes.Buffer
	config := seedExportConfig(t)
	stateDir := seedExportState(t)
	out := filepath.Join(t.TempDir(), "bundle.json")

	code := runExport([]string{"--output", out, "--alias", "nope", "--config", config, "--state-dir", stateDir}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("runExport unknown selector = %d, want 2; stderr=%q", code, stderr.String())
	}
}

func TestRunImportRoundTrip(t *testing.T) {
	var stdout, stderr bytes.Buffer
	srcConfig := seedExportConfig(t)
	srcState := seedExportState(t)
	out := filepath.Join(t.TempDir(), "bundle.json")
	if code := runExport([]string{"--output", out, "--config", srcConfig, "--state-dir", srcState}, &stdout, &stderr); code != 0 {
		t.Fatalf("export setup failed: %d %q", code, stderr.String())
	}

	// Fresh empty target.
	dstConfig := writeConfig(t, "")
	dstState := t.TempDir()
	stdout.Reset()
	stderr.Reset()

	code := runImport([]string{out, "--config", dstConfig, "--state-dir", dstState}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runImport = %d, want 0; stderr=%q", code, stderr.String())
	}

	if _, found, _ := sshconfig.ExistingAliasSpec(dstConfig, "prod"); !found {
		t.Fatalf("imported alias prod not found in %s", dstConfig)
	}
	if _, err := state.ReadForward(dstState, "db"); err != nil {
		t.Fatalf("imported forward db missing: %v", err)
	}
	if _, err := state.ReadProxy(dstState, "socks"); err != nil {
		t.Fatalf("imported proxy socks missing: %v", err)
	}
}

func TestRunImportSkipsExistingWithoutForce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	srcConfig := seedExportConfig(t)
	srcState := seedExportState(t)
	out := filepath.Join(t.TempDir(), "bundle.json")
	runExport([]string{"--output", out, "--config", srcConfig, "--state-dir", srcState}, &stdout, &stderr)

	// Target already has prod with a different hostname.
	dstConfig := writeConfig(t, "Host prod\n  HostName old.example.com\n")
	dstState := t.TempDir()

	stdout.Reset()
	stderr.Reset()
	code := runImport([]string{out, "--config", dstConfig, "--state-dir", dstState, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runImport = %d, want 0; stderr=%q", code, stderr.String())
	}
	// prod should be skipped (still old hostname), bastion imported.
	if got := readFile(t, dstConfig); !bytes.Contains([]byte(got), []byte("old.example.com")) {
		t.Fatalf("prod was overwritten without --force:\n%s", got)
	}

	// Now with --force it should overwrite prod.
	stdout.Reset()
	stderr.Reset()
	if code := runImport([]string{out, "--config", dstConfig, "--state-dir", dstState, "--force"}, &stdout, &stderr); code != 0 {
		t.Fatalf("runImport --force = %d, want 0; stderr=%q", code, stderr.String())
	}
	if got := readFile(t, dstConfig); !bytes.Contains([]byte(got), []byte("prod.example.com")) {
		t.Fatalf("--force did not overwrite prod:\n%s", got)
	}
}

func TestRunImportRejectsFutureSchema(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := filepath.Join(t.TempDir(), "future.json")
	if err := os.WriteFile(path, []byte(`{"schema_version": 999}`), 0o600); err != nil {
		t.Fatalf("write future bundle: %v", err)
	}
	dstConfig := writeConfig(t, "")
	code := runImport([]string{path, "--config", dstConfig, "--state-dir", t.TempDir()}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("runImport future-schema = 0, want non-zero")
	}
}

func TestRunImportSkipsInvalidEntryButImportsRest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// Bundle with one invalid alias (wildcard) and one valid alias.
	bundle := portable.Bundle{
		SchemaVersion: portable.BundleSchemaVersion,
		Aliases: []portable.AliasEntry{
			{Alias: "*.bad", HostName: "h"},
			{Alias: "good", HostName: "good.example.com"},
		},
	}
	data, _ := portable.Marshal(bundle, "")
	path := filepath.Join(t.TempDir(), "mixed.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	dstConfig := writeConfig(t, "")
	code := runImport([]string{path, "--config", dstConfig, "--state-dir", t.TempDir()}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runImport = %d, want 0 (partial success); stderr=%q", code, stderr.String())
	}
	if _, found, _ := sshconfig.ExistingAliasSpec(dstConfig, "good"); !found {
		t.Fatalf("valid alias not imported alongside invalid one")
	}
}

func TestParseIncludeCategories(t *testing.T) {
	all, err := parseIncludeCategories("")
	if err != nil || !all[portingKindAlias] || !all[portingKindForward] || !all[portingKindProxy] {
		t.Fatalf("empty include = %#v err=%v, want all three", all, err)
	}
	sub, err := parseIncludeCategories("aliases,proxies")
	if err != nil || !sub[portingKindAlias] || sub[portingKindForward] || !sub[portingKindProxy] {
		t.Fatalf("subset include = %#v err=%v", sub, err)
	}
	if _, err := parseIncludeCategories("bogus"); err == nil {
		t.Fatalf("bogus category should error")
	}
}
