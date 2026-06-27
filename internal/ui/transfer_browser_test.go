package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/0xbenc/ssherpa/internal/termstyle"
	"github.com/0xbenc/termnav"
)

// transferRows is the sample listing the transfer-browser render tests use.
func transferRows() []termnav.Row {
	return []termnav.Row{
		{Kind: "file_parent", Token: "/srv", Title: "..", Description: "/srv", Group: "Directories", Badge: "up", Intent: termnav.IntentAscend, Selectable: true, IsContainer: true},
		{Kind: "file_dir", Token: "/srv/app/logs", Title: "logs/", Description: "/srv/app/logs", Group: "Directories", Badge: "dir", Intent: termnav.IntentDescend, Selectable: true, IsContainer: true},
		{Kind: "file", Token: "/srv/app/release.tar.gz", Title: "release.tar.gz", Description: "42 MB  modified 2026-06-03 12:00", Detail: "/srv/app/release.tar.gz", Group: "Files", Badge: "file", Intent: termnav.IntentSelectLeaf, Selectable: true},
	}
}

// transferModel drives a termnav model into Ready with the given rows at w×h,
// using the same Options BrowseTransfer configures.
func transferModel(rows []termnav.Row, dir string, w, h int) termnav.Model {
	m := termnav.New(termnav.Options{
		Matcher:            termnav.Fuzzy{},
		MatchText:          transferMatchText,
		ReserveRows:        12,
		MinRows:            4,
		KeepCursorOnFilter: true,
	})
	m, _ = m.Load(dir)
	m, _ = termnav.Update(m, termnav.ResizeEvent{W: w, H: h})
	m, _ = termnav.Update(m, termnav.ListLoadedEvent{Gen: 1, Listing: termnav.Listing{Dir: dir, Parent: "/srv", Rows: rows}})
	return m
}

func sendSourceOpts() TransferBrowserOptions {
	return TransferBrowserOptions{
		Title:         "SSHERPA SEND SOURCE",
		Mode:          "local-file",
		LocationLabel: "LOCAL",
		Start:         "/srv/app",
		Steps:         []string{"direction", "local", "host", "remote", "run"},
		CurrentStep:   1,
	}
}

func TestTransferBrowserRendersWorkflowShellAndPreview(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}
	m := transferModel(transferRows(), "/srv/app", 120, 24)
	// move cursor to the file row
	m, _ = termnav.Update(m, termnav.KeyEvent{Key: "down"})
	m, _ = termnav.Update(m, termnav.KeyEvent{Key: "down"})

	text := renderTransferBrowser(m, sendSourceOpts(), theme)
	for _, want := range []string{
		"SSHERPA SEND SOURCE", "✓ direction", "● local", "LOCAL", "/srv/app",
		"FILTER", "SELECTION", "release.tar.gz", "Select this file",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}

func TestTransferBrowserFiltersByPathAndSelects(t *testing.T) {
	m := transferModel(transferRows(), "/srv/app", 100, 24)
	for _, ch := range "release" {
		m, _ = termnav.Update(m, termnav.KeyEvent{Text: string(ch)})
	}
	if len(m.Filtered()) != 1 {
		t.Fatalf("filtered = %d, want one match", len(m.Filtered()))
	}
	m, _ = termnav.Update(m, termnav.KeyEvent{Key: "enter"})
	out, ok := m.Outcome()
	if !ok || out.Token() != "/srv/app/release.tar.gz" {
		t.Fatalf("outcome = %#v ok=%v, want the release file", out, ok)
	}
}

func TestTransferBrowserKeyMap(t *testing.T) {
	// Uppercase Q cancels; lowercase q is filter text.
	if ev, ok := transferKeyMap(tea.KeyPressMsg{Code: 'Q', Text: "Q"}); !ok || ev.Key != "cancel" {
		t.Fatalf("Q -> %+v, want cancel", ev)
	}
	if ev, ok := transferKeyMap(tea.KeyPressMsg{Code: 'q', Text: "q"}); !ok || ev.Key != "" || ev.Text != "q" {
		t.Fatalf("q -> %+v, want filter text", ev)
	}
}

func TestTransferBrowserRendersEmptyDirectory(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}
	m := transferModel(nil, "/empty", 100, 24)
	opts := TransferBrowserOptions{Title: "SSHERPA RECEIVE SOURCE", Mode: "remote-file", LocationLabel: "prod", Start: "/empty"}
	text := renderTransferBrowser(m, opts, theme)
	for _, want := range []string{"SSHERPA RECEIVE SOURCE", "/empty", "0/0", "empty folder"} {
		if !strings.Contains(text, want) {
			t.Fatalf("empty browser missing %q:\n%s", want, text)
		}
	}
}

func TestTransferBrowserMetaLineBoundsLongLocationLabels(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}
	m := transferModel(nil, "/srv/releases", 100, 24)
	opts := TransferBrowserOptions{Mode: "remote-file", LocationLabel: "production-bastion-with-a-very-long-alias"}
	line := transferMetaLine(m, opts, 72, theme)
	if termstyle.VisibleWidth(line) > 72 {
		t.Fatalf("meta line width = %d, want <= 72: %q", termstyle.VisibleWidth(line), line)
	}
	if !strings.Contains(line, "/srv/releases") {
		t.Fatalf("meta line should preserve path context: %q", line)
	}
}

func TestTransferBrowserShiftArrowsJumpGroups(t *testing.T) {
	m := transferModel(transferRows(), "/srv/app", 100, 24)
	m, _ = termnav.Update(m, termnav.KeyEvent{Key: "down"})
	if m.Cursor() != 1 {
		t.Fatalf("cursor = %d, want second directory", m.Cursor())
	}
	m, _ = termnav.Update(m, termnav.KeyEvent{Key: "section-down"})
	if m.Cursor() != 2 {
		t.Fatalf("cursor after section-down = %d, want first file", m.Cursor())
	}
	m, _ = termnav.Update(m, termnav.KeyEvent{Key: "section-up"})
	if m.Cursor() != 0 {
		t.Fatalf("cursor after section-up = %d, want first directory", m.Cursor())
	}
}

func TestTransferBrowserNarrowRowsDoNotExceedFrame(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}
	row := termnav.Row{
		Kind: "file", Token: "/home/xbenc/builds/very-long-directory-name/really-long-artifact-name.tar.gz",
		Title: "really-long-artifact-name.tar.gz", Description: "928 MB  modified 2026-06-03 12:00",
		Detail: "/home/xbenc/builds/very-long-directory-name/really-long-artifact-name.tar.gz",
		Group:  "Files", Badge: "file", Intent: termnav.IntentSelectLeaf, Selectable: true,
	}
	line := transferRow(row, true, 44, theme)
	if termstyle.VisibleWidth(line) > 44 {
		t.Fatalf("row width = %d, want <= 44: %q", termstyle.VisibleWidth(line), line)
	}
	if !strings.Contains(line, "[FILE]") {
		t.Fatalf("row missing file badge: %q", line)
	}
}
