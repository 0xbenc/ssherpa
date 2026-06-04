package cli

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/hostlist"
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
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	if items[0].Kind != ui.ItemForwardSaved || items[0].Token != "pg" || items[0].Badge != "fwd" {
		t.Fatalf("forward item = %+v", items[0])
	}
	if !strings.Contains(items[0].Description, "prod:") || !strings.Contains(items[0].Detail, "alias prod") {
		t.Fatalf("forward description/detail = %q / %q", items[0].Description, items[0].Detail)
	}
	if items[1].Kind != ui.ItemProxySaved || items[1].Token != "corp" || items[1].Group != "Saved Proxies" {
		t.Fatalf("proxy item = %+v", items[1])
	}
	if items[2].Kind != ui.ItemAlias || items[2].Token != "prod" || items[2].Group != "SSH Aliases" {
		t.Fatalf("alias item = %+v", items[2])
	}
	if !strings.Contains(items[2].Description, "deploy@prod.example.com:2222") || items[2].Detail != "/home/test/.ssh/config:12" {
		t.Fatalf("alias description/detail = %q / %q", items[2].Description, items[2].Detail)
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
