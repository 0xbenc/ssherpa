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
	for _, want := range []string{"SSHERPA SEND COMPLETE", "SENT", "12 KB", "Local:", "/tmp/report.txt", "Remote:", "prod:/srv/reports/report.txt", "press any key to return home"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
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
	for _, want := range []string{"SSHERPA RECEIVE COMPLETE", "RECEIVED", "12 KB", "Local:", "/tmp/report.txt", "Remote:", "prod:/srv/reports/report.txt", "press any key to return to session"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view = %q, want substring %q", text, want)
		}
	}
}
