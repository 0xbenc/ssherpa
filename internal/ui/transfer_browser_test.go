package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func newTestTransferBrowser() transferBrowserModel {
	return newTransferBrowserModel([]Item{
		{
			Kind:        "file_parent",
			Token:       "/srv",
			Title:       "..",
			Description: "/srv",
			Group:       "Directories",
			Badge:       "up",
		},
		{
			Kind:        "file_dir",
			Token:       "/srv/app/logs",
			Title:       "logs/",
			Description: "/srv/app/logs",
			Group:       "Directories",
			Badge:       "dir",
		},
		{
			Kind:        "file",
			Token:       "/srv/app/release.tar.gz",
			Title:       "release.tar.gz",
			Description: "42 MB  modified 2026-06-03 12:00",
			Detail:      "/srv/app/release.tar.gz",
			Group:       "Files",
			Badge:       "file",
		},
	}, TransferBrowserOptions{
		Title:         "SSHERPA SEND SOURCE",
		Mode:          "local-file",
		LocationLabel: "LOCAL",
		Location:      "/srv/app",
		Steps:         []string{"direction", "local", "host", "remote", "run"},
		CurrentStep:   1,
	}, termstyle.TerminalTheme().WithNoColor(true))
}

func updateTransferBrowser(m transferBrowserModel, msgs ...tea.Msg) transferBrowserModel {
	for _, msg := range msgs {
		newModel, _ := m.Update(msg)
		m = newModel.(transferBrowserModel)
	}
	return m
}

func TestTransferBrowserRendersWorkflowShellAndPreview(t *testing.T) {
	m := newTestTransferBrowser()
	m = updateTransferBrowser(m, tea.WindowSizeMsg{Width: 120, Height: 24})
	m = updateTransferBrowser(m, keyPress(tea.KeyDown, ""), keyPress(tea.KeyDown, ""))

	text := m.View().Content
	for _, want := range []string{
		"SSHERPA SEND SOURCE",
		"✓ direction",
		"● local",
		"LOCAL",
		"/srv/app",
		"FILTER",
		"SELECTION",
		"release.tar.gz",
		"Select this file",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}

func TestTransferBrowserFiltersByPathAndSelects(t *testing.T) {
	m := newTestTransferBrowser()
	m = updateTransferBrowser(m, typeText("release")...)

	if len(m.filtered) != 1 {
		t.Fatalf("filtered = %#v, want one match", m.filtered)
	}

	m = updateTransferBrowser(m, keyPress(tea.KeyEnter, ""))
	if m.selected != 2 {
		t.Fatalf("selected = %d, want release item index 2", m.selected)
	}
}

func TestTransferBrowserLowercaseQFiltersInsteadOfCanceling(t *testing.T) {
	m := newTestTransferBrowser()

	m = updateTransferBrowser(m, keyPress('q', "q"))

	if m.canceled {
		t.Fatalf("lowercase q should filter, not cancel")
	}
	if m.query != "q" {
		t.Fatalf("query = %q, want q", m.query)
	}
}

func TestTransferBrowserRendersEmptyDirectory(t *testing.T) {
	m := newTransferBrowserModel(nil, TransferBrowserOptions{
		Title:         "SSHERPA RECEIVE SOURCE",
		Mode:          "remote-file",
		LocationLabel: "prod",
		Location:      "/empty",
	}, termstyle.TerminalTheme().WithNoColor(true))

	text := m.View().Content
	for _, want := range []string{"SSHERPA RECEIVE SOURCE", "/empty", "0/0", "No matching files"} {
		if !strings.Contains(text, want) {
			t.Fatalf("empty browser missing %q:\n%s", want, text)
		}
	}
}

func TestTransferBrowserMetaLineBoundsLongLocationLabels(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}
	m := newTransferBrowserModel(nil, TransferBrowserOptions{
		Mode:          "remote-file",
		LocationLabel: "production-bastion-with-a-very-long-alias",
		Location:      "/srv/releases",
	}, termstyle.TerminalTheme().WithNoColor(true))

	line := m.metaLine(72, theme)
	if termstyle.VisibleWidth(line) > 72 {
		t.Fatalf("meta line width = %d, want <= 72: %q", termstyle.VisibleWidth(line), line)
	}
	if !strings.Contains(line, "/srv/releases") {
		t.Fatalf("meta line should preserve path context: %q", line)
	}
}

func TestTransferBrowserShiftArrowsJumpGroups(t *testing.T) {
	m := newTestTransferBrowser()

	m = updateTransferBrowser(m, keyPress(tea.KeyDown, ""))
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want second directory", m.cursor)
	}

	m.jumpSection(1)
	if m.cursor != 2 {
		t.Fatalf("cursor after down section jump = %d, want first file", m.cursor)
	}

	m.jumpSection(-1)
	if m.cursor != 0 {
		t.Fatalf("cursor after up section jump = %d, want first directory", m.cursor)
	}
}

func TestTransferBrowserNarrowRowsDoNotExceedFrame(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}
	item := Item{
		Kind:        "file",
		Token:       "/home/xbenc/builds/very-long-directory-name/really-long-artifact-name.tar.gz",
		Title:       "really-long-artifact-name.tar.gz",
		Description: "928 MB  modified 2026-06-03 12:00",
		Detail:      "/home/xbenc/builds/very-long-directory-name/really-long-artifact-name.tar.gz",
		Group:       "Files",
		Badge:       "file",
	}

	line := transferBrowserRow(item, true, 44, theme)
	if termstyle.VisibleWidth(line) > 44 {
		t.Fatalf("row width = %d, want <= 44: %q", termstyle.VisibleWidth(line), line)
	}
	if !strings.Contains(line, "[FILE]") {
		t.Fatalf("row missing file badge: %q", line)
	}
}
