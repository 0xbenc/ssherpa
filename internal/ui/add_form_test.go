package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func updateAddAlias(m addAliasModel, msgs ...tea.Msg) addAliasModel {
	for _, msg := range msgs {
		newModel, _ := m.Update(msg)
		m = newModel.(addAliasModel)
	}
	return m
}

func TestAddAliasFormCanChooseNoIdentity(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{
		Initial:       AddAliasResult{HostName: "prod.example.com", Alias: "prod"},
		IdentityFiles: []string{"~/.ssh/id_ed25519"},
	}, termstyle.Theme{})
	m.step = addStepIdentity

	m = updateAddAlias(m, keyPress(tea.KeyEnter, ""))

	if m.idBuf != "" {
		t.Fatalf("idBuf = %q, want empty", m.idBuf)
	}
	if m.step != addStepReview {
		t.Fatalf("step = %d, want review", m.step)
	}
}

func TestAddAliasFormCanChooseDiscoveredIdentity(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{
		Initial:       AddAliasResult{HostName: "prod.example.com", Alias: "prod"},
		IdentityFiles: []string{"~/.ssh/id_ed25519"},
	}, termstyle.Theme{})
	m.step = addStepIdentity

	m = updateAddAlias(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyEnter, ""))

	if m.idBuf != "~/.ssh/id_ed25519" {
		t.Fatalf("idBuf = %q, want discovered identity", m.idBuf)
	}
	if m.step != addStepIdentitiesOnly {
		t.Fatalf("step = %d, want identities-only", m.step)
	}
	if !m.idsOnly {
		t.Fatalf("idsOnly = false, want true after choosing an identity file")
	}
}

func TestAddAliasFormCustomIdentityPath(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{
		Initial:       AddAliasResult{HostName: "prod.example.com", Alias: "prod"},
		IdentityFiles: []string{"~/.ssh/id_ed25519"},
	}, termstyle.Theme{})
	m.step = addStepIdentity

	m = updateAddAlias(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyDown, ""), keyPress(tea.KeyEnter, ""))
	if m.step != addStepIdentityCustom {
		t.Fatalf("step = %d, want custom identity", m.step)
	}
	m = updateAddAlias(m, typeText("~/.ssh/prod_key")...)
	m = updateAddAlias(m, keyPress(tea.KeyEnter, ""))

	if m.idBuf != "~/.ssh/prod_key" {
		t.Fatalf("idBuf = %q, want custom path", m.idBuf)
	}
	if m.step != addStepIdentitiesOnly {
		t.Fatalf("step = %d, want identities-only", m.step)
	}
	if !m.idsOnly {
		t.Fatalf("idsOnly = false, want true after choosing a custom identity file")
	}
}

func TestAddAliasFormSkipsAuthModeForEmptyCustomIdentityPath(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{
		Initial:       AddAliasResult{HostName: "prod.example.com", Alias: "prod"},
		IdentityFiles: []string{"~/.ssh/id_ed25519"},
	}, termstyle.Theme{})
	m.step = addStepIdentityCustom

	m = updateAddAlias(m, keyPress(tea.KeyEnter, ""))

	if m.idBuf != "" {
		t.Fatalf("idBuf = %q, want empty", m.idBuf)
	}
	if m.idsOnly {
		t.Fatalf("idsOnly = true, want false without an identity file")
	}
	if m.step != addStepReview {
		t.Fatalf("step = %d, want review", m.step)
	}
}

func TestAddAliasAuthModeUsesExplicitChoices(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{
		Initial: AddAliasResult{
			HostName:     "prod.example.com",
			Alias:        "prod",
			IdentityFile: "~/.ssh/id_ed25519",
		},
	}, termstyle.TerminalTheme().WithNoColor(true))
	m.step = addStepIdentitiesOnly

	text := m.View().Content

	for _, want := range []string{
		"How strictly should SSH use the selected identity file?",
		"Normal SSH authentication",
		"Only this identity file",
		"Write IdentitiesOnly yes for this alias",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "[ ]") || strings.Contains(text, "[x]") {
		t.Fatalf("auth mode view should not render fake checkbox:\n%s", text)
	}
}

func TestAddAliasAuthModeCanMoveFromOnlyToNormal(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{
		Initial:       AddAliasResult{HostName: "prod.example.com", Alias: "prod"},
		IdentityFiles: []string{"~/.ssh/id_ed25519"},
	}, termstyle.TerminalTheme().WithNoColor(true))
	m.step = addStepIdentity

	m = updateAddAlias(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyEnter, ""))

	if !m.idsOnly {
		t.Fatalf("idsOnly = false, want only-this-identity default")
	}
	if text := m.View().Content; !strings.Contains(text, "> Only this identity file") {
		t.Fatalf("auth mode should select only-this-identity by default:\n%s", text)
	}

	m = updateAddAlias(m, keyPress(tea.KeyLeft, ""))

	if m.idsOnly {
		t.Fatalf("idsOnly = true, want normal authentication after moving left")
	}
	if text := m.View().Content; !strings.Contains(text, "> Normal SSH authentication") {
		t.Fatalf("auth mode should move selection to normal authentication:\n%s", text)
	}
}

func TestAddAliasFormPastesIntoActiveTextField(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{}, termstyle.Theme{})

	m = updateAddAlias(m, tea.PasteMsg{Content: "prod.example.com\n"})

	if m.hostBuf != "prod.example.com" {
		t.Fatalf("hostBuf = %q, want pasted host", m.hostBuf)
	}
	if m.hostCursor != len("prod.example.com") {
		t.Fatalf("hostCursor = %d, want end", m.hostCursor)
	}
}

func TestAddAliasFormPastesCustomIdentityPath(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{}, termstyle.Theme{})
	m.step = addStepIdentityCustom

	m = updateAddAlias(m, tea.PasteMsg{Content: "~/.ssh/prod key\n"})

	if m.idBuf != "~/.ssh/prod key" {
		t.Fatalf("idBuf = %q, want pasted identity path", m.idBuf)
	}
}

func TestAddAliasIdentityChoicesKeepLongPathsInLabelColumn(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}
	longPath := "~/.ssh/0xbenc_id_ed25519_d01p0MOh"

	line := identityChoiceLine(longPath, false, 80, theme)

	if termstyle.VisibleWidth(line) > 72 {
		t.Fatalf("identity choice line width = %d, want <= 72: %q", termstyle.VisibleWidth(line), line)
	}
	if !strings.Contains(line, "write IdentityFile") {
		t.Fatalf("identity choice line missing action description: %q", line)
	}
	if strings.Contains(line, "write IdentityFile "+longPath) {
		t.Fatalf("identity choice line duplicated long path in description: %q", line)
	}
}

func TestAddAliasFormUsesAccentForBreadcrumbAndLabels(t *testing.T) {
	m := newAddAliasModel(AddAliasOptions{
		Initial: AddAliasResult{HostName: "prod.example.com", Alias: "prod"},
	}, termstyle.TerminalTheme())

	text := m.View().Content
	for _, want := range []string{
		"\x1b[39;4m● host\x1b[0m",
		"\x1b[90m○ alias\x1b[0m",
		"\x1b[33mHostName\x1b[0m",
		"\x1b[39mprod.example.com|\x1b[0m",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing accented text %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\x1b[39;4mprod.example.com|") {
		t.Fatalf("input value should not be underlined:\n%s", text)
	}
}
