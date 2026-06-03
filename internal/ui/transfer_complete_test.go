package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestTransferCompleteViewRendersConfirmation(t *testing.T) {
	model := transferCompleteModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		localPath:   "/tmp/report.txt",
		alias:       "prod",
		remotePath:  "/srv/reports/report.txt",
		size:        "12 KB",
		direction:   "send",
		width:       90,
	}

	view := model.View()
	text := view.Content
	for _, want := range []string{
		"SSHERPA SEND COMPLETE",
		"✓ direction",
		"● complete",
		"Status",
		"SENT",
		"12 KB",
		"TRANSFER",
		"Source",
		"local",
		"Target",
		"prod",
		"/tmp/report.txt",
		"/srv/reports/report.txt",
		"press any key to return home",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
	for _, notWant := range []string{"DETAILS", "Flow", "prod:/srv/reports/report.txt"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("view contains redundant substring %q:\n%s", notWant, text)
		}
	}
	if strings.Contains(text, "\x1b[") {
		t.Fatalf("view contains ANSI escapes with NoColor: %q", text)
	}
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
}

func TestTransferCompleteViewRendersReceiveConfirmation(t *testing.T) {
	model := transferCompleteModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		localPath:   "/tmp/report.txt",
		alias:       "prod",
		remotePath:  "/srv/reports/report.txt",
		size:        "12 KB",
		direction:   "receive",
		returnLabel: "press any key to return to session",
		width:       90,
	}

	text := model.View().Content
	for _, want := range []string{
		"SSHERPA RECEIVE COMPLETE",
		"✓ host",
		"✓ remote",
		"✓ local",
		"● complete",
		"RECEIVED",
		"12 KB",
		"Source",
		"prod",
		"/srv/reports/report.txt",
		"Target",
		"local",
		"/tmp/report.txt",
		"press any key to return to session",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
	if strings.Contains(text, "prod      prod:") {
		t.Fatalf("source row should not duplicate remote alias:\n%s", text)
	}
	for _, notWant := range []string{"DETAILS", "Flow", "prod:/srv/reports/report.txt"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("view contains redundant substring %q:\n%s", notWant, text)
		}
	}
}

func TestTransferCompleteViewStaysInsideNarrowFrame(t *testing.T) {
	model := transferCompleteModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		localPath:   "/home/xbenc/builds/very-long-directory-name/really-long-artifact-name.tar.gz",
		alias:       "production-bastion-with-a-very-long-alias",
		remotePath:  "/srv/releases/2026/very-long-directory-name/really-long-artifact-name.tar.gz",
		size:        "928 MB",
		direction:   "send",
		width:       68,
	}

	text := model.View().Content
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if got := termstyle.VisibleWidth(line); got > 68 {
			t.Fatalf("line width = %d, want <= 68: %q\n%s", got, line, text)
		}
	}
	for _, want := range []string{"SSHERPA SEND COMPLETE", "928 MB", "Source", "Target"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}
