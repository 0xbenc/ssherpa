package hostlist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/sshconfig"
)

func TestBuildDefaultInventory(t *testing.T) {
	graph := loadMatrix(t)

	inventory := Build(graph, Options{IgnoreGitUser: true})

	names := aliasNames(inventory.Aliases)
	want := []string{"quoted", "prod", "conditional"}
	if !sameStrings(names, want) {
		t.Fatalf("aliases = %#v, want %#v", names, want)
	}

	prod := findAlias(t, inventory.Aliases, "prod")
	if prod.HostName != "prod.example.com" || prod.User != "alice" || prod.Port != "2222" {
		t.Fatalf("prod = %#v, want parsed target", prod)
	}
	if got := prod.IdentityFiles; !sameStrings(got, []string{"~/.ssh/prod key", "~/.ssh/prod_ed25519"}) {
		t.Fatalf("prod.IdentityFiles = %#v", got)
	}
	if len(prod.Warnings) != 1 {
		t.Fatalf("prod warnings = %#v, want duplicate warning", prod.Warnings)
	}

	quoted := findAlias(t, inventory.Aliases, "quoted")
	if quoted.Port != "22" {
		t.Fatalf("quoted.Port = %q, want default from Host *", quoted.Port)
	}

	conditional := findAlias(t, inventory.Aliases, "conditional")
	if !conditional.IsConditional {
		t.Fatalf("conditional.IsConditional = false, want true")
	}
}

func TestBuildAllIncludesPatternsAndGitUsers(t *testing.T) {
	graph := loadMatrix(t)

	inventory := Build(graph, Options{All: true, IgnoreGitUser: false})

	names := aliasNames(inventory.Aliases)
	for _, name := range []string{"gitbox", "*", "*.example.com", "!blocked.example.com"} {
		if !contains(names, name) {
			t.Fatalf("aliases = %#v, want %q", names, name)
		}
	}
}

// TestBuildSkipsDashAliasesWithWarning is the inventory half of the WP1
// argument-injection fix: a Host pattern beginning with "-" (which OpenSSH
// would parse as an option, not a hostname) must never become a selectable
// alias — not even with --all — and the skip must surface as a warning
// diagnostic so `list --json` explains why the entry is missing.
func TestBuildSkipsDashAliasesWithWarning(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "config")
	contents := "Host -oProxyCommand=evil\n" +
		"  HostName attacker.example.com\n" +
		"\n" +
		"Host prod\n" +
		"  HostName prod.example.com\n"
	if err := os.WriteFile(root, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	graph, err := sshconfig.Load(sshconfig.LoadOptions{RootPath: root, HomeDir: dir})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	for _, opts := range []Options{{}, {All: true}} {
		inventory := Build(graph, opts)

		names := aliasNames(inventory.Aliases)
		if contains(names, "-oProxyCommand=evil") {
			t.Fatalf("aliases = %#v, dash alias must be skipped (opts %+v)", names, opts)
		}
		if !contains(names, "prod") {
			t.Fatalf("aliases = %#v, want prod (opts %+v)", names, opts)
		}

		found := false
		for _, diag := range inventory.Diagnostics {
			if diag.Severity == sshconfig.SeverityWarning && strings.Contains(diag.Message, `"-oProxyCommand=evil"`) {
				if !strings.Contains(diag.Message, "ssh option") {
					t.Fatalf("diagnostic %q does not mention the ssh option rationale", diag.Message)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("Diagnostics = %#v, want warning about the skipped dash alias", inventory.Diagnostics)
		}
	}
}

func TestBuildFilterAndUser(t *testing.T) {
	graph := loadMatrix(t)

	inventory := Build(graph, Options{Filter: "quote", User: "bob", IgnoreGitUser: true})

	names := aliasNames(inventory.Aliases)
	want := []string{"quoted"}
	if !sameStrings(names, want) {
		t.Fatalf("aliases = %#v, want %#v", names, want)
	}
}

func loadMatrix(t *testing.T) *sshconfig.Graph {
	t.Helper()
	root := filepath.Join("..", "sshconfig", "testdata", "matrix", "config")
	graph, err := sshconfig.Load(sshconfig.LoadOptions{RootPath: root, HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	return graph
}

func aliasNames(aliases []Alias) []string {
	names := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		names = append(names, alias.Name)
	}
	return names
}

func findAlias(t *testing.T, aliases []Alias, name string) Alias {
	t.Helper()
	for _, alias := range aliases {
		if alias.Name == name {
			return alias
		}
	}
	t.Fatalf("alias %q not found in %#v", name, aliases)
	return Alias{}
}

func sameStrings(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
