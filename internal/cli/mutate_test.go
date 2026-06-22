package cli

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/ui"
)

func TestEditManagementItemsPreserveTargetKindsAndDetails(t *testing.T) {
	aliases := []hostlist.Alias{{
		Name:       "prod",
		HostName:   "prod.example.com",
		User:       "deploy",
		Port:       "2222",
		SourcePath: "/home/test/.ssh/config",
		SourceLine: 12,
	}}
	forwards := []state.StoredForward{{
		Name:       "pg",
		SSHAlias:   "prod",
		LocalBind:  "127.0.0.1",
		LocalPort:  15432,
		RemoteHost: "127.0.0.1",
		RemotePort: 5432,
		Through:    "bastion",
	}}
	proxies := []state.StoredProxy{{
		Name:     "corp",
		SSHAlias: "bastion",
		Bind:     "127.0.0.1",
		Port:     1080,
	}}

	items := editManagementItems(aliases, forwards, proxies)
	if len(items) != 4 {
		t.Fatalf("len(items) = %d, want 4", len(items))
	}
	if items[0].Kind != editItemDeleteAll || items[0].Token != "delete-all" || items[0].Group != "Bulk Actions" {
		t.Fatalf("bulk delete item = %+v", items[0])
	}
	if !strings.Contains(items[0].Description, "1 visible alias") || !strings.Contains(items[0].Detail, "saved forwards/proxies") {
		t.Fatalf("bulk delete description/detail = %q / %q", items[0].Description, items[0].Detail)
	}
	if items[1].Kind != ui.ItemForwardSaved || items[1].Token != "pg" || items[1].Badge != "fwd" {
		t.Fatalf("forward item = %+v", items[1])
	}
	if !strings.Contains(items[1].Description, "prod:") || !strings.Contains(items[1].Detail, "alias prod") {
		t.Fatalf("forward description/detail = %q / %q", items[1].Description, items[1].Detail)
	}
	if items[2].Kind != ui.ItemProxySaved || items[2].Token != "corp" || items[2].Group != "Saved Proxies" {
		t.Fatalf("proxy item = %+v", items[2])
	}
	if items[3].Kind != ui.ItemAlias || items[3].Token != "prod" || items[3].Group != "SSH Aliases" {
		t.Fatalf("alias item = %+v", items[3])
	}
	if !strings.Contains(items[3].Description, "deploy@prod.example.com:2222") || items[3].Detail != "/home/test/.ssh/config:12" {
		t.Fatalf("alias description/detail = %q / %q", items[3].Description, items[3].Detail)
	}
}

func TestEditDeleteAllArgsPreserveVisibleAliasScope(t *testing.T) {
	args := editDeleteAllArgs(editInteractiveFlags{
		inventoryFlags: inventoryFlags{
			All:    true,
			Filter: "scratch",
			User:   "deploy",
			Config: "/tmp/ssh-config",
		},
		StateDir:      "/tmp/ssherpa-state",
		DeletePattern: true,
	})
	want := "--all\x00--filter\x00scratch\x00--user\x00deploy\x00--config\x00/tmp/ssh-config\x00--state-dir\x00/tmp/ssherpa-state\x00--delete-patterns"
	if strings.Join(args, "\x00") != want {
		t.Fatalf("editDeleteAllArgs = %#v", args)
	}
}

func TestEditActionItemsGroupDangerAndNavigation(t *testing.T) {
	items := editActionItems([]ui.Item{
		{Kind: ui.ItemKind("edit_details"), Token: "edit", Title: "Edit details"},
		{Kind: ui.ItemKind("rename"), Token: "rename", Title: "Rename saved forward"},
		{Kind: ui.ItemKind("delete"), Token: "delete", Title: "Delete saved forward"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back"},
	})

	wantGroups := []string{"Actions", "Actions", "Danger", "Navigation"}
	wantBadges := []string{"edit", "rename", "delete", "back"}
	for i := range items {
		if items[i].Group != wantGroups[i] || items[i].Badge != wantBadges[i] {
			t.Fatalf("item %d = %+v, want group %q badge %q", i, items[i], wantGroups[i], wantBadges[i])
		}
	}
	if !strings.Contains(items[2].Action, "Delete") {
		t.Fatalf("delete action help = %q", items[2].Action)
	}
}

func TestEditTargetSummary(t *testing.T) {
	got := editTargetSummary(2, 1, 3)
	want := "2 aliases  1 forward  3 proxies"
	if got != want {
		t.Fatalf("editTargetSummary = %q, want %q", got, want)
	}
}

func TestParseAddFlagsForcePassword(t *testing.T) {
	var stderr strings.Builder
	flags, ok := parseAddFlags([]string{"--alias", "pwbox", "--host", "h", "--force-password"}, &stderr)
	if !ok {
		t.Fatalf("parseAddFlags ok = false; stderr = %q", stderr.String())
	}
	if !flags.ForcePassword {
		t.Fatalf("flags.ForcePassword = false, want true")
	}
}

func TestParseAddFlagsForcePasswordRejectsIdentity(t *testing.T) {
	var stderr strings.Builder
	_, ok := parseAddFlags([]string{"--alias", "pwbox", "--host", "h", "--force-password", "--identity", "~/.ssh/id"}, &stderr)
	if ok {
		t.Fatalf("parseAddFlags ok = true, want rejection")
	}
	if !strings.Contains(stderr.String(), "--force-password cannot be combined with --identity") {
		t.Fatalf("stderr = %q, want conflict message", stderr.String())
	}
}

func TestParseAddFlagsForcePasswordRejectsIdentitiesOnly(t *testing.T) {
	var stderr strings.Builder
	_, ok := parseAddFlags([]string{"--alias", "pwbox", "--host", "h", "--force-password", "--identities-only"}, &stderr)
	if ok {
		t.Fatalf("parseAddFlags ok = true, want rejection")
	}
	if !strings.Contains(stderr.String(), "--identities-only") {
		t.Fatalf("stderr = %q, want conflict message", stderr.String())
	}
}

func TestAddResultSpecRoundTripForcePassword(t *testing.T) {
	spec := aliasSpecFromAddResult(ui.AddAliasResult{Alias: "pwbox", HostName: "h", ForcePassword: true})
	if !spec.ForcePassword {
		t.Fatalf("aliasSpecFromAddResult dropped ForcePassword")
	}
	back := addAliasResultFromSpec(spec)
	if !back.ForcePassword {
		t.Fatalf("addAliasResultFromSpec dropped ForcePassword")
	}
}

func TestRepointPresetsReferencingAlias(t *testing.T) {
	dir := t.TempDir()
	if err := state.WriteForward(dir, state.StoredForward{Name: "db", SSHAlias: "prod", LocalPort: 5432, RemoteHost: "db.internal", RemotePort: 5432}); err != nil {
		t.Fatalf("WriteForward: %v", err)
	}
	if err := state.WriteForward(dir, state.StoredForward{Name: "other", SSHAlias: "web", LocalPort: 8080, RemoteHost: "h", RemotePort: 80}); err != nil {
		t.Fatalf("WriteForward other: %v", err)
	}
	if err := state.WriteProxy(dir, state.StoredProxy{Name: "socks", SSHAlias: "prod", Port: 1080}); err != nil {
		t.Fatalf("WriteProxy: %v", err)
	}

	var stdout, stderr strings.Builder
	repointPresetsReferencingAlias(dir, "prod", "prod2", &stdout, &stderr)

	if f, err := state.ReadForward(dir, "db"); err != nil || f.SSHAlias != "prod2" {
		t.Fatalf("forward db SSHAlias = %q err=%v, want prod2", f.SSHAlias, err)
	}
	if p, err := state.ReadProxy(dir, "socks"); err != nil || p.SSHAlias != "prod2" {
		t.Fatalf("proxy socks SSHAlias = %q err=%v, want prod2", p.SSHAlias, err)
	}
	// A preset referencing a different alias must be untouched.
	if f, err := state.ReadForward(dir, "other"); err != nil || f.SSHAlias != "web" {
		t.Fatalf("forward other SSHAlias = %q err=%v, want web (unchanged)", f.SSHAlias, err)
	}
	if !strings.Contains(stdout.String(), "saved forward db now uses alias prod2") {
		t.Fatalf("stdout missing forward report: %q", stdout.String())
	}
}

func TestApplyEditSetFlagsForcePassword(t *testing.T) {
	// Setting force-password clears any identity state.
	spec := applyEditSetFlags(
		sshconfig.AliasSpec{Alias: "pwbox", HostName: "h", IdentityFile: "~/.ssh/id", IdentitiesOnly: true},
		editSetFlags{ForcePassword: true, ForcePasswordSet: true},
	)
	if !spec.ForcePassword || spec.IdentityFile != "" || spec.IdentitiesOnly {
		t.Fatalf("force-password edit set leaked identity state: %#v", spec)
	}

	// Setting an identity on a force-password alias clears force-password.
	spec = applyEditSetFlags(
		sshconfig.AliasSpec{Alias: "pwbox", HostName: "h", ForcePassword: true},
		editSetFlags{IdentityFile: "~/.ssh/id"},
	)
	if spec.ForcePassword || spec.IdentityFile != "~/.ssh/id" {
		t.Fatalf("identity edit set did not clear force-password: %#v", spec)
	}
}
