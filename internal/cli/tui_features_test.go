package cli

import (
	"bytes"
	"os"
	"path/filepath"
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
	printArtifactInfo(&out, "missing")
	if out.Len() != 0 {
		t.Fatalf("missing artifact output = %q, want empty", out.String())
	}
}

// TestPrintArtifactInfoInRepoPrintsExistingPath covers the source
// checkout / extracted archive layout: the repo-relative file exists,
// so its real absolute path is printed.
func TestPrintArtifactInfoInRepoPrintsExistingPath(t *testing.T) {
	t.Chdir("../..")

	var out bytes.Buffer
	printArtifactInfo(&out, "man")
	text := out.String()

	lines := strings.SplitN(text, "\n", 2)
	if len(lines) < 2 {
		t.Fatalf("artifact output = %q, want path line plus hint", text)
	}
	path := lines[0]
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, filepath.Join("man", "ssherpa.1")) {
		t.Fatalf("path line = %q, want absolute man/ssherpa.1 path", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("printed path does not exist: %v", err)
	}
	if !strings.Contains(text, "view with: man") {
		t.Fatalf("artifact output missing hint:\n%s", text)
	}
}

// TestPrintArtifactInfoInstalledFallsBackToInstallPaths covers the
// installed-binary case (audit: the picker printed cwd-relative paths
// that do not exist for brew/deb/rpm installs). With no repo-relative
// file present, the package install locations are printed instead.
func TestPrintArtifactInfoInstalledFallsBackToInstallPaths(t *testing.T) {
	t.Chdir(t.TempDir())

	cases := []struct {
		token string
		want  []string
	}{
		{"bash", []string{
			"completions/ssherpa.bash is not present next to this binary",
			"/usr/share/bash-completion/completions/ssherpa (deb/rpm)",
			"$(brew --prefix)/etc/bash_completion.d/ssherpa (Homebrew)",
		}},
		{"man", []string{
			"man/ssherpa.1 is not present next to this binary",
			"/usr/share/man/man1/ssherpa.1 (deb/rpm)",
			"$(brew --prefix)/share/man/man1/ssherpa.1 (Homebrew)",
		}},
	}
	for _, c := range cases {
		t.Run(c.token, func(t *testing.T) {
			var out bytes.Buffer
			printArtifactInfo(&out, c.token)
			for _, want := range c.want {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("artifact output missing %q:\n%s", want, out.String())
				}
			}
		})
	}
}

func TestLocateRepoArtifact(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "completions"), 0o755); err != nil {
		t.Fatalf("os.MkdirAll: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "completions", "ssherpa.bash"), []byte("# completion"), 0o644); err != nil {
		t.Fatalf("os.WriteFile: %v", err)
	}
	t.Chdir(dir)

	path, ok := locateRepoArtifact("completions/ssherpa.bash")
	if !ok {
		t.Fatalf("locateRepoArtifact returned !ok for existing file")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("returned path does not exist: %v", err)
	}

	if _, ok := locateRepoArtifact("completions/ssherpa.zsh"); ok {
		t.Fatalf("locateRepoArtifact returned ok for missing file")
	}
}
