package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/0xbenc/ssherpa/internal/ui"
)

func TestCheckModeItemsWithoutSavedForwards(t *testing.T) {
	items := checkModeItems(false)
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	want := []struct {
		token string
		group string
		badge string
	}{
		{"host", "Hosts", "host"},
		{"hosts", "Hosts", "all"},
		{"back", "Navigation", "back"},
	}
	for i, w := range want {
		if items[i].Token != w.token || items[i].Group != w.group || items[i].Badge != w.badge {
			t.Fatalf("item %d = %+v, want token %q group %q badge %q", i, items[i], w.token, w.group, w.badge)
		}
	}
	if items[0].Kind != ui.ItemCheck || items[2].Kind != ui.ItemKind("back") {
		t.Fatalf("unexpected item kinds: %+v", items)
	}
}

func TestCheckModeItemsWithSavedForwards(t *testing.T) {
	items := checkModeItems(true)
	if len(items) != 5 {
		t.Fatalf("len(items) = %d, want 5", len(items))
	}
	if items[2].Token != "forward" || items[2].Group != "Saved Forwards" || items[2].Badge != "fwd" {
		t.Fatalf("one-forward item = %+v", items[2])
	}
	if items[3].Token != "forwards" || items[3].Group != "Saved Forwards" || items[3].Badge != "all" {
		t.Fatalf("all-forwards item = %+v", items[3])
	}
	if !strings.Contains(items[2].Action, "saved forward") {
		t.Fatalf("one-forward action = %q", items[2].Action)
	}
}

func TestCheckSavedForwardItemsPreserveDetails(t *testing.T) {
	items := checkSavedForwardItems([]ui.SavedForwardItem{{
		Name:        "pg",
		Description: ":15432 -> :5432",
		Detail:      "alias pgbox  127.0.0.1:15432 -> 127.0.0.1:5432",
	}})
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(items))
	}
	item := items[0]
	if item.Kind != ui.ItemForwardSaved || item.Token != "pg" || item.Badge != "fwd" || item.Group != "Saved Forwards" {
		t.Fatalf("saved forward item = %+v", item)
	}
	if item.Description != ":15432 -> :5432" || !strings.Contains(item.Detail, "alias pgbox") {
		t.Fatalf("description/detail = %q / %q", item.Description, item.Detail)
	}
}

func TestCheckSummaries(t *testing.T) {
	if got, want := checkModeSummary(2, 0), "2 aliases"; got != want {
		t.Fatalf("checkModeSummary without forwards = %q, want %q", got, want)
	}
	if got, want := checkModeSummary(1, 3), "1 alias  3 forwards"; got != want {
		t.Fatalf("checkModeSummary with forwards = %q, want %q", got, want)
	}
	if got, want := checkCountLabel(1, "forward", "forwards"), "1 forward"; got != want {
		t.Fatalf("checkCountLabel singular = %q, want %q", got, want)
	}
}

func TestDocsArtifactItems(t *testing.T) {
	items := docsArtifactItems()
	if len(items) != 5 {
		t.Fatalf("len(items) = %d, want 5", len(items))
	}
	want := []struct {
		token string
		group string
		badge string
		path  string
	}{
		{"bash", "Completions", "bash", "completions/ssherpa.bash"},
		{"zsh", "Completions", "zsh", "completions/ssherpa.zsh"},
		{"fish", "Completions", "fish", "completions/ssherpa.fish"},
		{"man", "Manual", "man", "man/ssherpa.1"},
		{"back", "Navigation", "back", "return to the home screen"},
	}
	for i, w := range want {
		item := items[i]
		if item.Token != w.token || item.Group != w.group || item.Badge != w.badge || item.Description != w.path {
			t.Fatalf("item %d = %+v, want token %q group %q badge %q description %q", i, item, w.token, w.group, w.badge, w.path)
		}
	}
	if items[0].Kind != ui.ItemDocs || items[4].Kind != ui.ItemKind("back") {
		t.Fatalf("unexpected docs item kinds: %+v", items)
	}
	if !strings.Contains(items[0].Action, "Print") {
		t.Fatalf("artifact action = %q", items[0].Action)
	}
}

func TestDocsArtifactByTokenAndPrintInfo(t *testing.T) {
	artifact, ok := docsArtifactByToken("zsh")
	if !ok {
		t.Fatalf("docsArtifactByToken zsh returned false")
	}
	if artifact.RelPath != "completions/ssherpa.zsh" || !strings.Contains(artifact.Hint, "fpath") {
		t.Fatalf("artifact = %+v", artifact)
	}
	if _, ok := docsArtifactByToken("missing"); ok {
		t.Fatalf("docsArtifactByToken missing returned true")
	}

	var out bytes.Buffer
	printArtifactInfo(&out, "man")
	text := out.String()
	for _, want := range []string{"man/ssherpa.1", "view with: man ./man/ssherpa.1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("artifact output missing %q:\n%s", want, text)
		}
	}

	out.Reset()
	printArtifactInfo(&out, "missing")
	if out.Len() != 0 {
		t.Fatalf("missing artifact output = %q, want empty", out.String())
	}
}
