package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/hostlist"
)

func TestBuildItemsPrependsSyntheticRows(t *testing.T) {
	items := BuildItems([]hostlist.Alias{{Name: "prod", HostName: "prod.example.com"}})

	if len(items) != 7 {
		t.Fatalf("len(items) = %d, want 7", len(items))
	}

	want := []ItemKind{ItemAdd, ItemEdit, ItemJump, ItemProxy, ItemAuthkeys, ItemSessions, ItemAlias}
	for i, kind := range want {
		if items[i].Kind != kind {
			t.Fatalf("items[%d].Kind = %q, want %q", i, items[i].Kind, kind)
		}
	}
	if items[6].Token != "prod" || items[6].Description != "prod.example.com" || items[6].Group != "Hosts" {
		t.Fatalf("alias item = %#v", items[6])
	}
}

func TestBuildItemsIncludesSessionCounts(t *testing.T) {
	items := BuildItemsWithOptions(nil, BuildItemsOptions{SessionCount: 4, ActiveSessionCount: 2})

	session := items[5]
	if session.Kind != ItemSessions {
		t.Fatalf("items[5].Kind = %q, want sessions", session.Kind)
	}
	if session.Description != "2 active / 4 recorded sessions" {
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
		Title:       "ssherpa",
		Subtitle:    "exec mode",
		Summary:     []string{"1 host  0 warnings"},
	})
	view := model.View()
	text := view.Content

	for _, want := range []string{"ssherpa  exec mode", "1 host  0 warnings", "Actions", "Sessions and route map", "Hosts", "prod"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
}
