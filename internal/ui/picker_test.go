package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestBuildItemsPrependsActiveTunnelsAndSavedForwards(t *testing.T) {
	items := BuildItemsWithOptions([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}, BuildItemsOptions{
		ActiveTunnels: []ActiveTunnelItem{
			{SessionID: "sess-1", Title: "pngwin-pg-tunnel", Description: "127.0.0.1:5432 -> 127.0.0.1:5432 · up 2m · pid 31337"},
		},
		SavedForwards: []SavedForwardItem{
			{Name: "pngwin-pg-tunnel", Description: "127.0.0.1:5432 -> 127.0.0.1:5432  (alias pgbox)"},
		},
	})

	// Expected order: Active Tunnels, Saved Forwards, Actions (8), Hosts.
	if len(items) != 1+1+8+1 {
		t.Fatalf("len(items) = %d, want %d", len(items), 1+1+8+1)
	}
	want := []ItemKind{
		ItemForwardActive, // active tunnel row
		ItemForwardSaved,  // saved forward row
		ItemAdd, ItemEdit, ItemJump, ItemProxy, ItemForward, ItemAuthkeys, ItemSessions, ItemTheme,
		ItemAlias, // host
	}
	for i, kind := range want {
		if items[i].Kind != kind {
			t.Fatalf("items[%d].Kind = %q, want %q", i, items[i].Kind, kind)
		}
	}
	if items[0].Token != "sess-1" {
		t.Fatalf("active-tunnel token = %q, want session ID 'sess-1'", items[0].Token)
	}
	if items[0].Group != "Active Tunnels" {
		t.Fatalf("active-tunnel group = %q", items[0].Group)
	}
	if items[1].Group != "Saved Forwards" {
		t.Fatalf("saved-forward group = %q", items[1].Group)
	}
}

func TestBuildItemsPrependsSyntheticRows(t *testing.T) {
	items := BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}})

	if len(items) != 9 {
		t.Fatalf("len(items) = %d, want 9", len(items))
	}

	want := []ItemKind{ItemAdd, ItemEdit, ItemJump, ItemProxy, ItemForward, ItemAuthkeys, ItemSessions, ItemTheme, ItemAlias}
	for i, kind := range want {
		if items[i].Kind != kind {
			t.Fatalf("items[%d].Kind = %q, want %q", i, items[i].Kind, kind)
		}
	}
	if items[8].Token != "prod" || items[8].Description != "prod.example.com" || items[8].Group != "Hosts" {
		t.Fatalf("alias item = %#v", items[8])
	}
}

func TestBuildItemsIncludesSessionCounts(t *testing.T) {
	items := BuildItemsWithOptions(nil, BuildItemsOptions{SessionCount: 4, ActiveSessionCount: 2})

	session := items[6]
	if session.Kind != ItemSessions {
		t.Fatalf("items[6].Kind = %q, want sessions", session.Kind)
	}
	if session.Description != "" {
		t.Fatalf("session action description = %q, want empty", session.Description)
	}
}

func TestFuzzyMatch(t *testing.T) {
	tests := []struct {
		value string
		query string
		want  bool
	}{
		{value: "prod-web\talice@example.com", query: "prd", want: true},
		{value: "prod-web\talice@example.com", query: "pwe", want: true},
		{value: "prod-web\talice@example.com", query: "zzz", want: false},
	}

	for _, tt := range tests {
		if got := fuzzyMatch(tt.value, tt.query); got != tt.want {
			t.Fatalf("fuzzyMatch(%q, %q) = %t, want %t", tt.value, tt.query, got, tt.want)
		}
	}
}

func TestPickerViewHonorsNoAltScreen(t *testing.T) {
	model := newPickerModel([]Item{{Kind: ItemAlias, Token: "prod", Title: "prod"}}, PickOptions{NoAltScreen: true})

	if model.View().AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
}

func TestPickerViewRendersHeaderGroupsAndRows(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
		Title:       "ssherpa",
		Subtitle:    "exec mode",
		Summary:     []string{"1 host  0 warnings"},
	})
	view := model.View()
	text := view.Content

	for _, want := range []string{"SSHERPA", "EXEC MODE", "1 host  0 warnings", "FILTER", "ACTIONS", "Sessions and route map", "Theme and colors", "HOSTS", "prod"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("view contains ANSI escapes with NoColor: %q", text)
	}
}

func TestPickerViewOmitsActionRowDescriptions(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
	})
	model.width = 120

	text := model.View().Content
	for _, unwanted := range []string{
		"write a safe Host stanza",
		"add, merge, replace, or delete login keys",
		"preview and save UI palette",
	} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("view contains action row description %q:\n%s", unwanted, text)
		}
	}
	if !strings.Contains(text, "Adds a new SSH alias to your config.") {
		t.Fatalf("selection detail missing:\n%s", text)
	}
}

func TestPickerViewRendersVersionTagInHeader(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
		Title:       "ssherpa",
		Version:     "v1.1.0",
		Subtitle:    "supervised mode",
	})
	text := model.View().Content

	if !strings.Contains(text, "SSHERPA") {
		t.Fatalf("missing SSHERPA logo: %q", text)
	}
	if !strings.Contains(text, "v1.1.0") {
		t.Fatalf("version tag not rendered: %q", text)
	}
	if !strings.Contains(text, "SUPERVISED MODE") {
		t.Fatalf("subtitle missing: %q", text)
	}
	// The version sits between the logo and the subtitle pill.
	logoIdx := strings.Index(text, "SSHERPA")
	versionIdx := strings.Index(text, "v1.1.0")
	subtitleIdx := strings.Index(text, "SUPERVISED MODE")
	if !(logoIdx < versionIdx && versionIdx < subtitleIdx) {
		t.Fatalf("header order wrong: SSHERPA(%d) v1.1.0(%d) SUPERVISED MODE(%d)", logoIdx, versionIdx, subtitleIdx)
	}
}

func TestPickerViewOmitsVersionTagWhenEmpty(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
		Title:       "ssherpa",
		Subtitle:    "supervised mode",
		// Version empty — header should not include a stray "v" tag.
	})
	text := model.View().Content
	// A bare "v" surrounded by spaces would be the regression
	// signature if versionTag rendered on empty input.
	if strings.Contains(text, " v ") {
		t.Fatalf("stray version tag rendered: %q", text)
	}
}

func TestPickerViewUsesColorWhenEnabled(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		Title:       "ssherpa",
		Subtitle:    "supervised mode",
	})

	if text := model.View().Content; !strings.Contains(text, "\x1b[") {
		t.Fatalf("view = %q, want ANSI styling", text)
	}
	if text := model.View().Content; strings.Contains(text, "38;2;") {
		t.Fatalf("view = %q, want default terminal palette instead of truecolor", text)
	}
}

func TestPickerViewUsesCustomTheme(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		Theme: termstyle.Theme{
			Name: "terminal",
			Codes: map[termstyle.Role]string{
				termstyle.RoleTitle: "35",
			},
		},
		Title: "ssherpa",
	})

	if text := model.View().Content; !strings.Contains(text, "\x1b[35mSSHERPA") {
		t.Fatalf("view = %q, want custom title color", text)
	}
}

func TestPickerViewRendersWideSelectionPreview(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com", SourcePath: "/tmp/config", SourceLine: 12}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
	})
	model.width = 120

	text := model.View().Content
	for _, want := range []string{"SELECTION", "Add new alias", "Type", "ADD"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
}

func TestPickerWideLayoutGivesSelectionMoreWidth(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
	})
	model.width = 120

	text := model.View().Content
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "SELECTION") {
			if got := strings.Index(line, "|"); got != 55 {
				t.Fatalf("divider column = %d, want 55 in 120-column layout:\n%s", got, text)
			}
			return
		}
	}
	t.Fatalf("selection column not rendered:\n%s", text)
}

func TestPickerWideLayoutKeepsActionTitlesComplete(t *testing.T) {
	model := newPickerModel(BuildItems(nil), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
	})
	model.width = 100

	text := model.View().Content
	for _, title := range []string{
		"Edit aliases or delete",
		"Jump via intermediate hops",
		"Open port-forward tunnel",
		"Sessions and route map",
	} {
		if !strings.Contains(text, title) {
			t.Fatalf("missing full action title %q:\n%s", title, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "[EDIT]") || strings.Contains(line, "[JUMP]") || strings.Contains(line, "[FORWARD]") || strings.Contains(line, "[MAP]") {
			if strings.Contains(line, "~") {
				t.Fatalf("action title was truncated:\n%s", text)
			}
		}
	}
}

func TestPickerSelectionHintWrapsToTwoLines(t *testing.T) {
	model := newPickerModel(BuildItems(nil), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
	})
	model.width = 120
	model.cursor = 4 // Open port-forward tunnel

	text := model.View().Content
	if strings.Contains(text, "ports~") {
		t.Fatalf("selection hint was truncated instead of wrapped:\n%s", text)
	}
	if !strings.Contains(text, "Builds an ssh -L port-forward tunnel") || !strings.Contains(text, "optional jump hop.") {
		t.Fatalf("selection hint did not wrap across the preview pane:\n%s", text)
	}
}

func TestPickerViewUsesFullAvailableWidth(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
	})
	model.width = 180

	text := model.View().Content
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || len(lines[1]) != 180 {
		t.Fatalf("rule width = %d, want 180:\n%s", len(lines[1]), text)
	}
}
