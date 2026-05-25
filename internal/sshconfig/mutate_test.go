package sshconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanAddOrUpdateAddsToMissingConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ssh", "config")

	plan, err := PlanAddOrUpdate(path, AliasSpec{
		Alias:          "prod",
		HostName:       "prod.example.com",
		User:           "alice",
		Port:           "2222",
		IdentityFile:   "~/.ssh/prod key",
		IdentitiesOnly: true,
	})
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}
	if plan.Action != "added" || !plan.Changed {
		t.Fatalf("plan = %#v, want added changed", plan)
	}

	want := "# Created by ssherpa\n\n" +
		"Host prod\n" +
		"  HostName prod.example.com\n" +
		"  User alice\n" +
		"  Port 2222\n" +
		"  IdentityFile \"~/.ssh/prod key\"\n" +
		"  IdentitiesOnly yes\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("PlanAddOrUpdate wrote file; stat err = %v", err)
	}
}

func TestPlanAddOrUpdateAppendsToExistingConfig(t *testing.T) {
	path := writeTempConfig(t, "# mine\nHost old\n  HostName old.example.com\n")

	plan, err := PlanAddOrUpdate(path, AliasSpec{Alias: "prod", HostName: "prod.example.com"})
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}

	want := "# mine\n" +
		"Host old\n" +
		"  HostName old.example.com\n" +
		"\n" +
		"Host prod\n" +
		"  HostName prod.example.com\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
}

func TestPlanAddOrUpdateUpdatesSingleAliasAndPreservesUnrelatedLines(t *testing.T) {
	path := writeTempConfig(t, `# before
Host prod
  # local note
  HostName old.example.com
  ForwardAgent yes
  User old
  IdentityFile ~/.ssh/old

Host other
  HostName other.example.com
`)

	plan, err := PlanAddOrUpdate(path, AliasSpec{
		Alias:          "prod",
		HostName:       "new.example.com",
		User:           "alice",
		Port:           "2200",
		IdentityFile:   "~/.ssh/new",
		IdentitiesOnly: true,
	})
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}

	got := string(plan.NewData)
	for _, want := range []string{
		"# before",
		"  # local note",
		"  ForwardAgent yes",
		"Host other",
		"  HostName new.example.com",
		"  User alice",
		"  Port 2200",
		"  IdentityFile ~/.ssh/new",
		"  IdentitiesOnly yes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("NewData missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "old.example.com") || strings.Contains(got, "User old") {
		t.Fatalf("NewData kept old managed fields:\n%s", got)
	}
}

func TestPlanAddOrUpdateSplitsMultiAliasStanza(t *testing.T) {
	path := writeTempConfig(t, `Host prod web # team hosts
  HostName shared.example.com
  User old

Host later
  HostName later.example.com
`)

	plan, err := PlanAddOrUpdate(path, AliasSpec{Alias: "prod", HostName: "prod.example.com", User: "alice"})
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}

	want := "Host web # team hosts\n" +
		"  HostName shared.example.com\n" +
		"  User old\n" +
		"\n" +
		"Host prod\n" +
		"  HostName prod.example.com\n" +
		"  User alice\n" +
		"\n" +
		"Host later\n" +
		"  HostName later.example.com\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
}

func TestPlanAddOrUpdateRejectsMultiAliasWildcardSplit(t *testing.T) {
	path := writeTempConfig(t, "Host prod *.example.com\n  HostName shared.example.com\n")

	_, err := PlanAddOrUpdate(path, AliasSpec{Alias: "prod", HostName: "prod.example.com"})
	if err == nil {
		t.Fatalf("PlanAddOrUpdate returned nil error, want wildcard split error")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("error = %v, want wildcard message", err)
	}
}

func TestPlanDeleteAliasRemovesSingleAliasStanza(t *testing.T) {
	path := writeTempConfig(t, `Host old
  HostName old.example.com

Host prod
  HostName prod.example.com
  User alice

Host keep
  HostName keep.example.com
`)

	plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err != nil {
		t.Fatalf("PlanDeleteAlias returned error: %v", err)
	}

	got := string(plan.NewData)
	if strings.Contains(got, "Host prod") || strings.Contains(got, "prod.example.com") {
		t.Fatalf("NewData still contains prod stanza:\n%s", got)
	}
	if !strings.Contains(got, "Host old") || !strings.Contains(got, "Host keep") {
		t.Fatalf("NewData removed unrelated stanzas:\n%s", got)
	}
}

func TestPlanDeleteAliasRemovesOnlyTokenFromMultiAliasStanza(t *testing.T) {
	path := writeTempConfig(t, `Host prod web admin # shared
  HostName shared.example.com
  User alice
`)

	plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err != nil {
		t.Fatalf("PlanDeleteAlias returned error: %v", err)
	}

	want := "Host web admin # shared\n" +
		"  HostName shared.example.com\n" +
		"  User alice\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
}

func TestPlanDeleteAliasRejectsWildcardStanzaWithoutExplicitPermission(t *testing.T) {
	path := writeTempConfig(t, "Host prod *.example.com\n  User alice\n")

	_, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err == nil {
		t.Fatalf("PlanDeleteAlias returned nil error, want wildcard protection")
	}

	plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{AllowPatterns: true})
	if err != nil {
		t.Fatalf("PlanDeleteAlias with AllowPatterns returned error: %v", err)
	}
	if !strings.Contains(string(plan.NewData), "Host *.example.com") {
		t.Fatalf("NewData = %q, want remaining wildcard", string(plan.NewData))
	}
}

func TestPlanDeleteAliasesRemovesDuplicatesInOneFile(t *testing.T) {
	path := writeTempConfig(t, `Host prod
  HostName one.example.com

Host prod
  HostName two.example.com

Host keep
  HostName keep.example.com
`)

	plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err != nil {
		t.Fatalf("PlanDeleteAlias returned error: %v", err)
	}
	if strings.Contains(string(plan.NewData), "Host prod") {
		t.Fatalf("NewData still contains duplicate prod:\n%s", string(plan.NewData))
	}
	if !strings.Contains(string(plan.NewData), "Host keep") {
		t.Fatalf("NewData removed keep:\n%s", string(plan.NewData))
	}
}

func TestExistingAliasSpecReadsManagedFields(t *testing.T) {
	path := writeTempConfig(t, `Host prod web
  HostName prod.example.com
  User alice
  Port 2222
  IdentityFile "~/.ssh/prod key"
  IdentitiesOnly yes
`)

	spec, ok, err := ExistingAliasSpec(path, "prod")
	if err != nil {
		t.Fatalf("ExistingAliasSpec returned error: %v", err)
	}
	if !ok {
		t.Fatalf("ExistingAliasSpec ok = false, want true")
	}
	want := AliasSpec{
		Alias:          "prod",
		HostName:       "prod.example.com",
		User:           "alice",
		Port:           "2222",
		IdentityFile:   "~/.ssh/prod key",
		IdentitiesOnly: true,
	}
	if spec != want {
		t.Fatalf("spec = %#v, want %#v", spec, want)
	}
}

func TestValidateAliasSpec(t *testing.T) {
	tests := []struct {
		name string
		spec AliasSpec
	}{
		{name: "empty alias", spec: AliasSpec{HostName: "host"}},
		{name: "space alias", spec: AliasSpec{Alias: "bad alias", HostName: "host"}},
		{name: "pattern alias", spec: AliasSpec{Alias: "*.example.com", HostName: "host"}},
		{name: "missing host", spec: AliasSpec{Alias: "prod"}},
		{name: "bad port", spec: AliasSpec{Alias: "prod", HostName: "host", Port: "70000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAliasSpec(tt.spec, false); err == nil {
				t.Fatalf("ValidateAliasSpec returned nil error")
			}
		})
	}
}

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(strings.TrimLeft(contents, "\n")), 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	return path
}
