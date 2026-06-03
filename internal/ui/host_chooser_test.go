package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func testHostChooserAliases() []hostlist.Alias {
	return []hostlist.Alias{
		{
			Name:          "prod",
			HostName:      "prod.example.com",
			User:          "deploy",
			Port:          "2222",
			IdentityFiles: []string{"~/.ssh/prod_ed25519"},
			SourcePath:    "/home/test/.ssh/config",
			SourceLine:    12,
		},
		{Name: "bastion", HostName: "bastion.example.com", User: "ops", SourcePath: "/home/test/.ssh/config", SourceLine: 18},
		{Name: "qa-box", HostName: "qa.internal", User: "qa", Port: "2200"},
	}
}

func newTestHostChooser(t *testing.T, opts hostChooserBaseOptions) hostChooserModel {
	t.Helper()
	opts.NoAltScreen = true
	opts.NoColor = true
	opts.Theme = termstyle.TerminalTheme().WithNoColor(true)
	model, err := newHostChooserModel(hostChooserItemsFromAliases(testHostChooserAliases()), opts)
	if err != nil {
		t.Fatalf("newHostChooserModel: %v", err)
	}
	return model
}

func updateHostChooser(m hostChooserModel, msgs ...tea.Msg) hostChooserModel {
	for _, msg := range msgs {
		next, _ := m.Update(msg)
		m = next.(hostChooserModel)
	}
	return m
}

func keyPressShift(code rune) tea.KeyPressMsg {
	return tea.KeyPressMsg(tea.Key{Code: code, Mod: tea.ModShift})
}

func TestHostChooserViewRendersFramedAliasPicker(t *testing.T) {
	model := newTestHostChooser(t, hostChooserBaseOptions{
		Title:       "Transfer: pick host",
		Mode:        "choose transfer host",
		Steps:       []string{"direction", "host", "paths", "complete"},
		CurrentStep: 1,
	})
	model.width = 108
	model.height = 24

	view := model.View()
	text := view.Content
	for _, want := range []string{
		"SSHERPA TRANSFER HOST",
		"✓ direction",
		"● host",
		"MODE",
		"choose transfer host",
		"3 hosts",
		"FILTER",
		"3/3",
		"HOSTS",
		"[HOST]",
		"prod",
		"deploy@prod.example.com:2222",
		"SELECTION",
		"Action",
		"Select this host",
		"enter select",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"\x1b["} {
		if strings.Contains(text, notWant) {
			t.Fatalf("view contains unwanted %q:\n%s", notWant, text)
		}
	}
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
}

func TestHostChooserFilteringSelectionAndCancel(t *testing.T) {
	model := newTestHostChooser(t, hostChooserBaseOptions{})

	model = updateHostChooser(model, keyPress('q', "q"))
	if model.canceled {
		t.Fatalf("lowercase q canceled picker")
	}
	if model.query != "q" {
		t.Fatalf("query = %q, want q", model.query)
	}
	if len(model.filtered) != 1 || model.items[model.filtered[0]].Token != "qa-box" {
		t.Fatalf("filtered = %#v, want qa-box", model.filtered)
	}

	model = updateHostChooser(model, keyPress(tea.KeyEnter, ""))
	if model.selected < 0 || model.items[model.selected].Token != "qa-box" {
		t.Fatalf("selected = %d, want qa-box", model.selected)
	}

	model = newTestHostChooser(t, hostChooserBaseOptions{})
	model = updateHostChooser(model, keyPress('Q', "Q"))
	if !model.canceled {
		t.Fatalf("uppercase Q did not cancel picker")
	}
}

func TestHostChooserSectionJumps(t *testing.T) {
	items := []hostChooserItem{{
		Kind:        hostChooserItemDone,
		Title:       "Finish route",
		Description: "bastion -> prod",
		Badge:       "done",
		Group:       "Action",
	}}
	items = append(items, hostChooserItemsFromAliases(testHostChooserAliases())...)
	model, err := newHostChooserModel(items, hostChooserBaseOptions{
		NoAltScreen: true,
		NoColor:     true,
		Theme:       termstyle.TerminalTheme().WithNoColor(true),
	})
	if err != nil {
		t.Fatalf("newHostChooserModel: %v", err)
	}

	model = updateHostChooser(model, keyPressShift(tea.KeyDown))
	if model.cursor != 1 {
		t.Fatalf("shift+down from action cursor = %d, want first host at 1", model.cursor)
	}

	model.cursor = 3
	model = updateHostChooser(model, keyPressShift(tea.KeyUp))
	if model.cursor != 1 {
		t.Fatalf("shift+up within hosts cursor = %d, want first host at 1", model.cursor)
	}

	model = updateHostChooser(model, keyPressShift(tea.KeyDown))
	if model.cursor != 3 {
		t.Fatalf("shift+down on last section cursor = %d, want last host at 3", model.cursor)
	}
}

func TestJumpHopChooserViewAndFinishSelection(t *testing.T) {
	items := []hostChooserItem{{
		Kind:        hostChooserItemDone,
		Title:       "Finish route",
		Description: "bastion -> prod",
		Badge:       "done",
		Group:       "Action",
	}}
	items = append(items, hostChooserItemsFromAliases(testHostChooserAliases()[:2])...)
	model, err := newHostChooserModel(items, hostChooserBaseOptions{
		NoAltScreen: true,
		NoColor:     true,
		Theme:       termstyle.TerminalTheme().WithNoColor(true),
		Title:       "SSHERPA JUMP ROUTE",
		Mode:        "route bastion -> prod",
		Steps:       []string{"destination", "first hop", "extra hops", "run"},
		CurrentStep: 2,
		Footer:      "enter select  /  type filter  /  arrows move  /  Q back",
	})
	if err != nil {
		t.Fatalf("newHostChooserModel: %v", err)
	}
	model.width = 96
	model.height = 22

	text := model.View().Content
	for _, want := range []string{
		"SSHERPA JUMP ROUTE",
		"✓ destination",
		"✓ first hop",
		"● extra hops",
		"route bastion -> prod",
		"3 choices",
		"2 hosts",
		"ACTION",
		"Finish route",
		"HOSTS",
		"prod",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}

	model = updateHostChooser(model, keyPress(tea.KeyEnter, ""))
	if model.selected != 0 {
		t.Fatalf("selected = %d, want finish route at 0", model.selected)
	}
}

func TestHostChooserViewStaysInsideNarrowFrame(t *testing.T) {
	model := newTestHostChooser(t, hostChooserBaseOptions{Title: "Check: pick host", Mode: "choose host to check"})
	model.width = 52
	model.height = 14

	text := model.View().Content
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if got := termstyle.VisibleWidth(line); got > 52 {
			t.Fatalf("line width = %d, want <= 52: %q\n%s", got, line, text)
		}
	}
	for _, want := range []string{"SSHERPA CHECK HOST", "[HOST]", "prod"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}
