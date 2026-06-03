package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestCheckResultsViewRendersFramedOKSummary(t *testing.T) {
	model := checkResultsModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		checkedAt:   time.Date(2026, 6, 3, 14, 22, 9, 0, time.Local),
		ok:          true,
		results: []CheckResult{
			{Kind: "alias", Name: "prod", Status: "ok", SSHRttMillis: 42, ICMPStatus: "ok", ICMPRttMillis: 11, LocalBindStatus: "skipped"},
			{Kind: "alias", Name: "bastion", Status: "ok", SSHRttMillis: 87, ICMPStatus: "skipped", LocalBindStatus: "skipped"},
		},
		width:  96,
		height: 24,
	}

	view := model.View()
	text := view.Content
	for _, want := range []string{
		"SSHERPA CHECK RESULTS",
		"Status",
		"OK",
		"2 checked",
		"14:22:09",
		"RESULTS",
		"prod",
		"ssh 42ms",
		"icmp 11ms",
		"bastion",
		"ssh 87ms",
		"press any key to return",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"bind skipped", "\x1b["} {
		if strings.Contains(text, notWant) {
			t.Fatalf("view contains unwanted %q:\n%s", notWant, text)
		}
	}
	if view.AltScreen {
		t.Fatalf("AltScreen = true, want false")
	}
}

func TestCheckResultsViewEmphasizesFailuresAndMessages(t *testing.T) {
	model := checkResultsModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		checkedAt:   time.Date(2026, 6, 3, 14, 22, 9, 0, time.Local),
		ok:          false,
		results: []CheckResult{
			{Kind: "alias", Name: "prod", Status: "failed", SSHError: "permission denied", ICMPStatus: "ok", ICMPRttMillis: 12, LocalBindStatus: "skipped"},
			{Kind: "saved_forward", Name: "pg", Status: "failed", SSHRttMillis: 20, ICMPStatus: "skipped", LocalBindStatus: "busy"},
			{Kind: "alias", Name: "missing", Status: "invalid", ICMPStatus: "skipped", LocalBindStatus: "skipped", Message: "alias not found"},
		},
		width:  104,
		height: 26,
	}

	text := model.View().Content
	for _, want := range []string{
		"FAILED",
		"3 issues / 3 checked",
		"prod",
		"ssh failed",
		"icmp 12ms",
		"permission denied",
		"forward",
		"pg",
		"ssh 20ms",
		"bind busy",
		"missing",
		"invalid",
		"alias not found",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}

func TestCheckResultsViewRendersEmptyState(t *testing.T) {
	model := checkResultsModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		ok:          true,
		width:       72,
		height:      18,
	}

	text := model.View().Content
	for _, want := range []string{"SSHERPA CHECK RESULTS", "0 checked", "No results to show."} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
}

func TestCheckResultsViewStaysInsideNarrowFrame(t *testing.T) {
	model := checkResultsModel{
		noAltScreen: true,
		theme:       termstyle.TerminalTheme().WithNoColor(true),
		ok:          false,
		results: []CheckResult{{
			Kind:            "saved_forward",
			Name:            "very-long-forward-name-for-prod-postgres",
			Status:          "failed",
			SSHRttMillis:    1234,
			ICMPStatus:      "failed",
			LocalBindStatus: "already-in-use-on-this-machine",
			Message:         "local bind failed because the configured port is already occupied by another process",
		}},
		width:  60,
		height: 18,
	}

	text := model.View().Content
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if got := termstyle.VisibleWidth(line); got > 60 {
			t.Fatalf("line width = %d, want <= 60: %q\n%s", got, line, text)
		}
	}
	for _, want := range []string{"SSHERPA CHECK RESULTS", "FAILED", "forward"} {
		if !strings.Contains(text, want) {
			t.Fatalf("view missing %q:\n%s", want, text)
		}
	}
	for _, notWant := range []string{"already-in-use-on-this-machine local bind", "DETAILS"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("view contains unwanted %q:\n%s", notWant, text)
		}
	}
}
