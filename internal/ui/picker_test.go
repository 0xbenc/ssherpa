package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestBuildItemsPrependsActiveTunnelsAndSavedForwards(t *testing.T) {
	items := BuildItemsWithOptions([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}, BuildItemsOptions{
		ActiveTunnels: []ActiveTunnelItem{
			{SessionID: "sess-1", Title: "pngwin-pg-tunnel", Description: "127.0.0.1:5432 -> 127.0.0.1:5432 · up 2m"},
		},
		SavedForwards: []SavedForwardItem{
			{Name: "pngwin-pg-tunnel", Description: "127.0.0.1:5432 -> 127.0.0.1:5432  (alias pgbox)"},
		},
	})

	// Expected order: Active Tunnels, Saved Forwards, Actions (12), Hosts.
	if len(items) != 1+1+12+1 {
		t.Fatalf("len(items) = %d, want %d", len(items), 1+1+12+1)
	}
	want := []ItemKind{
		ItemForwardActive, // active tunnel row
		ItemForwardSaved,  // saved forward row
		ItemAdd, ItemEdit, ItemJump, ItemProxy, ItemForward, ItemSendFile, ItemReceiveFile, ItemCheck, ItemAuthkeys, ItemSessions, ItemTheme, ItemDocs,
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

func TestBuildItemsIncludesStopAllActiveAction(t *testing.T) {
	items := BuildItemsWithOptions(nil, BuildItemsOptions{StopAllActiveCount: 3})
	if len(items) == 0 {
		t.Fatalf("BuildItemsWithOptions returned no items")
	}
	if items[0].Kind != ItemStopAllActive || items[0].Badge != "stop all" {
		t.Fatalf("items[0] = %#v, want stop-all action first", items[0])
	}
	if !strings.Contains(items[0].Description, "3 tracked") {
		t.Fatalf("stop-all description = %q", items[0].Description)
	}
}

func TestBuildItemsPrependsSyntheticRows(t *testing.T) {
	items := BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}})

	if len(items) != 13 {
		t.Fatalf("len(items) = %d, want 13", len(items))
	}

	want := []ItemKind{ItemAdd, ItemEdit, ItemJump, ItemProxy, ItemForward, ItemSendFile, ItemReceiveFile, ItemCheck, ItemAuthkeys, ItemSessions, ItemTheme, ItemDocs, ItemAlias}
	for i, kind := range want {
		if items[i].Kind != kind {
			t.Fatalf("items[%d].Kind = %q, want %q", i, items[i].Kind, kind)
		}
	}
	if items[12].Token != "prod" || items[12].Description != "prod.example.com" || items[12].Group != "Hosts" {
		t.Fatalf("alias item = %#v", items[12])
	}
}

func TestBuildItemsIncludesSessionCounts(t *testing.T) {
	items := BuildItemsWithOptions(nil, BuildItemsOptions{SessionCount: 4, ActiveSessionCount: 2})

	session := items[9]
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

func TestPickerRefreshKeyReturnsRefreshResult(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		Refreshable: true,
	})

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	picker := updated.(pickerModel)
	if !picker.refresh {
		t.Fatalf("refresh = false, want true after pressing R on home page")
	}
	if picker.canceled {
		t.Fatalf("canceled = true, want false")
	}
}

func TestPickerRefreshKeyIsPlainFilterWhenNotRefreshable(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
	})

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'R', Text: "R"})
	picker := updated.(pickerModel)
	if picker.refresh {
		t.Fatalf("refresh = true, want false when picker is not refreshable")
	}
	if picker.query != "R" {
		t.Fatalf("query = %q, want %q (R types into the filter)", picker.query, "R")
	}
}

func TestPickerCapitalQQuits(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		Refreshable: true,
	})

	updated, _ := model.Update(tea.KeyPressMsg{Code: 'Q', Text: "Q"})
	picker := updated.(pickerModel)
	if !picker.canceled {
		t.Fatalf("canceled = false, want true after pressing Q")
	}

	// Lowercase q is now a filter character, not a quit key.
	typed, _ := model.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	if typedModel := typed.(pickerModel); typedModel.canceled || typedModel.query != "q" {
		t.Fatalf("lowercase q: canceled=%v query=%q, want canceled=false query=%q", typedModel.canceled, typedModel.query, "q")
	}
}

func TestPickerHomeFooterAdvertisesRefreshAndCapitalQuit(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
		Refreshable: true,
	})
	text := model.View().Content
	if !strings.Contains(text, "R refresh") {
		t.Fatalf("home footer missing 'R refresh':\n%s", text)
	}
	if !strings.Contains(text, "Q quit") {
		t.Fatalf("home footer missing 'Q quit':\n%s", text)
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
	model.height = 40
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

func TestSavedRowsKeepFullDetailsOutOfLeftList(t *testing.T) {
	model := newPickerModel(BuildItemsWithOptions(nil, BuildItemsOptions{
		SavedForwards: []SavedForwardItem{{
			Name:        "pg",
			Description: ":15432 -> :5432",
			Detail:      "alias pgbox · 127.0.0.1:15432 -> 127.0.0.1:5432",
		}},
	}), PickOptions{NoAltScreen: true, NoColor: true})
	model.width = 88

	text := model.View().Content
	if strings.Contains(text, "pgbox") || strings.Contains(text, "127.0.0.1") {
		t.Fatalf("saved row leaked full details into left list:\n%s", text)
	}
	if !strings.Contains(text, ":15432 -> :5432") {
		t.Fatalf("saved row missing compact endpoints:\n%s", text)
	}
}

func TestSavedRowsShowFullDetailsInPreview(t *testing.T) {
	model := newPickerModel(BuildItemsWithOptions(nil, BuildItemsOptions{
		SavedForwards: []SavedForwardItem{{
			Name:        "pg",
			Description: ":15432 -> :5432",
			Detail:      "alias pgbox · 127.0.0.1:15432 -> 127.0.0.1:5432",
		}},
	}), PickOptions{NoAltScreen: true, NoColor: true})
	model.width = 120

	text := model.View().Content
	if !strings.Contains(text, "Details") || !strings.Contains(text, "alias pgbox") || !strings.Contains(text, "127.0.0.1:15432") {
		t.Fatalf("saved preview missing full details:\n%s", text)
	}
}

func TestPickerHeaderCombinesSummaryWhenItFits(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
		Title:       "ssherpa",
		Version:     "dev",
		Subtitle:    "supervised mode",
		Summary:     []string{"1 host  0 warnings  0 sessions  0 tunnels"},
	})
	model.width = 140

	lines := strings.Split(model.View().Content, "\n")
	if !strings.Contains(lines[0], "SSHERPA dev") || !strings.Contains(lines[0], "SUPERVISED MODE") || !strings.HasSuffix(lines[0], "1 host  0 warnings  0 sessions  0 tunnels") {
		t.Fatalf("first header line did not combine summary:\n%s", model.View().Content)
	}
	if strings.Contains(lines[1], "1 host") {
		t.Fatalf("summary should not be repeated on second line:\n%s", model.View().Content)
	}
}

func TestPickerHeaderKeepsSummarySeparateWhenCombinedLineWouldNotFit(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
		Title:       "ssherpa",
		Version:     "dev",
		Subtitle:    "supervised mode",
		Summary:     []string{"1 host  0 warnings  0 sessions  0 tunnels"},
	})
	model.width = 71

	lines := strings.Split(model.View().Content, "\n")
	if strings.Contains(lines[0], "1 host") {
		t.Fatalf("summary should stay separate when it would not fit:\n%s", model.View().Content)
	}
	if !strings.Contains(lines[1], "1 host  0 warnings") {
		t.Fatalf("summary missing from second line:\n%s", model.View().Content)
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

func TestPickerHostRowsOnlyShowNickname(t *testing.T) {
	model := newPickerModel(BuildItems([]hostlist.Alias{{Name: "prod", User: "alice", HostName: "prod.example.com", Port: "2222"}}), PickOptions{
		NoAltScreen: true,
		NoColor:     true,
	})
	model.width = 120
	model.cursor = 12

	text := model.View().Content
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "[HOST]") {
			left, _, _ := strings.Cut(line, "|")
			if !strings.Contains(left, "prod") {
				t.Fatalf("host row missing nickname:\n%s", text)
			}
			if strings.Contains(left, "prod.example.com") || strings.Contains(left, "alice@") || strings.Contains(left, "2222") {
				t.Fatalf("host row leaked details:\n%s", text)
			}
			if !strings.Contains(text, "alice@prod.example.com:2222") {
				t.Fatalf("selection pane missing target details:\n%s", text)
			}
			return
		}
	}
	t.Fatalf("host row not found:\n%s", text)
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

func TestPickerActionBadgeRolesAreIntentional(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme()}
	tests := []struct {
		kind ItemKind
		code string
	}{
		{ItemAdd, "\x1b[32m"},           // create
		{ItemEdit, "\x1b[33m"},          // mutation/caution
		{ItemJump, "\x1b[35m"},          // route builder
		{ItemProxy, "\x1b[31m"},         // exposed local proxy
		{ItemForward, "\x1b[36m"},       // tunnel builder
		{ItemSendFile, "\x1b[36m"},      // file transfer
		{ItemReceiveFile, "\x1b[36m"},   // file transfer
		{ItemForwardSaved, "\x1b[36m"},  // tunnel launch
		{ItemForwardActive, "\x1b[31m"}, // stop running tunnel
		{ItemProxyActive, "\x1b[31m"},   // stop running proxy
		{ItemStopAllActive, "\x1b[31m"}, // stop all running sessions
		{ItemAuthkeys, "\x1b[33m"},      // security-sensitive
		{ItemSessions, "\x1b[34m"},      // inspection
		{ItemTheme, "\x1b[33m"},         // appearance/config
	}

	for _, tt := range tests {
		got := theme.badge(tt.kind, "[X]")
		if !strings.Contains(got, tt.code) {
			t.Fatalf("badge(%s) = %q, want ANSI code %q", tt.kind, got, tt.code)
		}
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
		"Edit aliases and forwards",
		"Jump via intermediate hops",
		"Open port-forward tunnel",
		"Send file",
		"Receive file",
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
