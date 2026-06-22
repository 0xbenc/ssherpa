package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func testManagementItems() []ManagementItem {
	return []ManagementItem{
		{
			Kind:        ItemForwardSaved,
			Token:       "pg",
			Title:       "pg",
			Description: "pgbox: :15432 -> :5432 via bastion",
			Detail:      "alias pgbox  127.0.0.1:15432 -> 127.0.0.1:5432",
			Group:       "Saved Forwards",
			Badge:       "fwd",
			Action:      "Choose an action for this saved forward",
		},
		{
			Kind:        ItemProxySaved,
			Token:       "corp",
			Title:       "corp",
			Description: "bastion: SOCKS :1080",
			Detail:      "alias bastion  127.0.0.1:1080",
			Group:       "Saved Proxies",
			Badge:       "proxy",
			Action:      "Choose an action for this saved proxy",
		},
		{
			Kind:        ItemAlias,
			Token:       "prod",
			Title:       "prod",
			Description: "deploy@prod.example.com:2222",
			Detail:      "/home/test/.ssh/config:12",
			Group:       "SSH Aliases",
			Badge:       "host",
			Action:      "Choose an action for this SSH alias",
		},
	}
}

func newTestManagementChooser(t *testing.T, items []ManagementItem, opts hostChooserBaseOptions) hostChooserModel {
	t.Helper()
	opts.NoAltScreen = true
	opts.NoColor = true
	opts.Theme = termstyle.TerminalTheme().WithNoColor(true)
	opts.EmptyLabel = "No matching choices"
	model, err := newHostChooserModel(managementChooserItems(items), opts)
	if err != nil {
		t.Fatalf("newHostChooserModel: %v", err)
	}
	return model
}

func TestManagementChooserViewRendersEditTargets(t *testing.T) {
	model := newTestManagementChooser(t, testManagementItems(), hostChooserBaseOptions{
		Title:       "Edit: pick an alias or saved preset",
		Mode:        "choose item to edit",
		Steps:       []string{"target", "action", "editor"},
		CurrentStep: 0,
		Summary:     "1 alias  1 forward  1 proxy",
		Footer:      "enter select / type filter / arrows move / Q back",
	})
	model.width = 112
	model.height = 26

	view := model.View()
	text := view.Content
	for _, want := range []string{
		"SSHERPA EDIT AN ALIAS OR SAVED PRESET",
		"● target",
		"○ action",
		"choose item to edit",
		"1 alias  1 forward  1 proxy",
		"SAVED FORWARDS",
		"[FWD]",
		"pg",
		"SAVED PROXIES",
		"[PROXY]",
		"corp",
		"SSH ALIASES",
		"[HOST]",
		"prod",
		"SELECTION",
		"Choose an action for this saved",
		"forward",
		"enter select",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("view contains ANSI escapes with NoColor:\n%s", text)
	}
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
}

func TestManagementChooserFilteringAndSelection(t *testing.T) {
	model := newTestManagementChooser(t, testManagementItems(), hostChooserBaseOptions{})

	model = updateHostChooser(model, keyPress('c', "c"), keyPress('o', "o"), keyPress('r', "r"), keyPress('p', "p"))
	if model.canceled {
		t.Fatalf("filter key canceled chooser")
	}
	if model.query != "corp" {
		t.Fatalf("query = %q, want corp", model.query)
	}
	if len(model.filtered) != 1 || model.items[model.filtered[0]].Token != "corp" {
		t.Fatalf("filtered = %#v, want corp", model.filtered)
	}

	model = updateHostChooser(model, keyPress(tea.KeyEnter, ""))
	if model.selected < 0 || model.items[model.selected].Token != "corp" {
		t.Fatalf("selected = %d, want corp", model.selected)
	}

	model = newTestManagementChooser(t, testManagementItems(), hostChooserBaseOptions{})
	model = updateHostChooser(model, keyPress('Q', "Q"))
	if !model.canceled {
		t.Fatalf("uppercase Q did not cancel chooser")
	}
}

func TestManagementChooserViewRendersGroupedActions(t *testing.T) {
	items := []ManagementItem{
		{Kind: ItemKind("edit_details"), Token: "edit", Title: "Edit tunnel", Description: "open the forward editor", Group: "Actions", Badge: "edit", Action: "Open the editor for this item"},
		{Kind: ItemKind("rename"), Token: "rename", Title: "Rename saved forward", Description: "change the catalog handle", Group: "Actions", Badge: "rename", Action: "Rename this saved catalog item"},
		{Kind: ItemKind("delete"), Token: "delete", Title: "Delete saved forward", Description: "remove from ssherpa catalog", Group: "Danger", Badge: "delete", Action: "Delete this item after confirmation"},
		{Kind: ItemKind("back"), Token: "back", Title: "Back", Description: "leave unchanged", Group: "Navigation", Badge: "back", Action: "Return without changing this item"},
	}
	model := newTestManagementChooser(t, items, hostChooserBaseOptions{
		Title:       "Edit saved forward: pg",
		Mode:        "choose action for pg",
		Steps:       []string{"target", "action", "editor"},
		CurrentStep: 1,
		Summary:     "4 actions",
	})
	model.width = 120
	model.height = 24

	text := model.View().Content
	for _, want := range []string{
		"SSHERPA EDIT SAVED FORWARD PG",
		"✓ target",
		"● action",
		"choose action for pg",
		"4 actions",
		"ACTIONS",
		"[EDIT]",
		"Rename saved forward",
		"DANGER",
		"[DELETE]",
		"Delete saved forward",
		"NAVIGATION",
		"[BACK]",
		"leave unchanged",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}

func TestManagementChooserViewStaysInsideNarrowFrame(t *testing.T) {
	model := newTestManagementChooser(t, testManagementItems(), hostChooserBaseOptions{
		Title:   "Edit: pick an alias or saved preset",
		Mode:    "choose item to edit",
		Summary: "1 alias  1 forward  1 proxy",
	})
	model.width = 54
	model.height = 14

	text := model.View().Content
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if got := termstyle.VisibleWidth(line); got > 54 {
			t.Fatalf("line width = %d, want <= 54: %q\n%s", got, line, text)
		}
	}
	for _, want := range []string{"SSHERPA EDIT AN ALIAS", "[FWD]", "pg"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}
