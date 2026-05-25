package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestBuildItemsPrependsSyntheticRows(t *testing.T) {
	items := BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}})

	if len(items) != 8 {
		t.Fatalf("len(items) = %d, want 8", len(items))
	}

	want := []ItemKind{ItemAdd, ItemEdit, ItemJump, ItemProxy, ItemAuthkeys, ItemSessions, ItemTheme, ItemAlias}
	for i, kind := range want {
		if items[i].Kind != kind {
			t.Fatalf("items[%d].Kind = %q, want %q", i, items[i].Kind, kind)
		}
	}
	if items[7].Token != "prod" || items[7].Description != "prod.example.com" || items[7].Group != "Hosts" {
		t.Fatalf("alias item = %#v", items[7])
	}
}

func TestBuildItemsIncludesSessionCounts(t *testing.T) {
	items := BuildItemsWithOptions(nil, BuildItemsOptions{SessionCount: 4, ActiveSessionCount: 2})

	session := items[5]
	if session.Kind != ItemSessions {
		t.Fatalf("items[5].Kind = %q, want sessions", session.Kind)
	}
	if session.Description != "2 active sessions (4 recorded)" {
		t.Fatalf("session description = %q", session.Description)
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
