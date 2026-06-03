package ui

import (
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/termstyle"
)

func TestWorkflowProgressKeepsFullRailWhenItFits(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}

	got := workflowProgress(theme, addStepLabels(), 2, 200)

	for _, want := range []string{"✓ host", "✓ alias", "● user", "○ port", "○ review"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress missing %q: %q", want, got)
		}
	}
}

func TestWorkflowProgressTruncatesCompletedStepsBeforeFutureSteps(t *testing.T) {
	theme := pickerTheme{theme: termstyle.TerminalTheme().WithNoColor(true)}

	got := workflowProgress(theme, addStepLabels(), 6, 56)

	for _, want := range []string{"…", "● auth", "○ review"} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "✓ host") {
		t.Fatalf("oldest completed step should be collapsed first: %q", got)
	}
	if strings.Index(got, "● auth") > strings.Index(got, "○ review") {
		t.Fatalf("active step should remain before future step: %q", got)
	}
}
