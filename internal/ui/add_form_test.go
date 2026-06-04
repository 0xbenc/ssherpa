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
	if m.step != addStepIdentitiesOnly {
		t.Fatalf("step = %d, want identities-only", m.step)
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
