package cli

// Drift guard for the shipped shell completions (audit follow-up): the
// files in completions/ must mention every command the dispatcher
// handles. The expected lists below mirror the Run switch in cli.go and
// the subcommand dispatchers; TestCompletionCommandsDispatch and
// TestCompletionCommandListMatchesGlobalHelp pin the lists to the real
// dispatch surface, so adding a command without updating this file
// fails. When this test fails after a CLI change, update the lists here
// AND regenerate completions/ssherpa.{bash,zsh,fish} together.

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// completionTopLevelCommands mirrors the Run dispatch switch in cli.go,
// including the recv alias.
var completionTopLevelCommands = []string{
	"add", "edit", "jump", "proxy", "forward", "send", "receive", "recv",
	"check", "incoming", "authkeys", "key", "theme", "session", "export", "import",
	"list", "show", "version", "help",
}

// completionSessionSubcommands mirrors the runSession dispatch in
// session.go (browse also answers to the transcripts alias).
var completionSessionSubcommands = []string{
	"list", "map", "show", "log", "replay", "grep", "export", "bundle",
	"identity", "browse", "stop-all", "prune",
}

// completionSavedSubcommands mirrors runForwardSaved and runProxySaved
// in forward_saved.go and proxy_saved.go.
var completionSavedSubcommands = []string{"list", "show", "save", "edit", "delete", "rename"}

// Remaining nested dispatchers: session bundle (session.go), edit
// (mutate.go), authkeys (authkeys.go), and incoming (incoming.go).
var (
	completionBundleSubcommands   = []string{"export", "import"}
	completionEditSubcommands     = []string{"set", "delete", "delete-all"}
	completionAuthkeysSubcommands = []string{"list", "add", "merge", "replace", "delete"}
	completionIncomingSubcommands = []string{"list", "mark", "hook"}
)

// completionRequiredFlags spot-checks the newer flags the stability
// audit found missing from the shipped completions. Names are written
// without leading dashes because fish spells flags as "-l name".
var completionRequiredFlags = []string{
	"latency-warn", "latency-disconnect", "overlay-key", "composer-key",
	"record-max-bytes", "reconnect-max", "no-reconnect", "no-muxer-guard",
	"saved-forward", "saved-forwards", "all-matching", "theme-file",
	"force-password",
}

var completionFiles = []string{"ssherpa.bash", "ssherpa.zsh", "ssherpa.fish"}

func completionsDir(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "completions")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("completions directory not found: %v", err)
	}
	return dir
}

// assertMentions requires each name to appear as a standalone token in
// the completion file. The boundary check rejects "delete" matching
// inside "delete-all" and "reconnect-max" inside "reconnect-max-backoff",
// while still matching "--flag", "-l flag", and "'flag:desc'" spellings.
func assertMentions(t *testing.T, fileName string, content string, kind string, names []string) {
	t.Helper()
	for _, name := range names {
		re := regexp.MustCompile(`(^|[^\w])` + regexp.QuoteMeta(name) + `([^-\w]|$)`)
		if !re.MatchString(content) {
			t.Errorf("%s: missing %s %q; regenerate completions/ against the CLI surface", fileName, kind, name)
		}
	}
}

func TestCompletionsMentionCLISurface(t *testing.T) {
	dir := completionsDir(t)
	for _, fileName := range completionFiles {
		t.Run(fileName, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, fileName))
			if err != nil {
				t.Fatalf("read completion file: %v", err)
			}
			content := string(data)
			assertMentions(t, fileName, content, "command", completionTopLevelCommands)
			assertMentions(t, fileName, content, "session subcommand", completionSessionSubcommands)
			assertMentions(t, fileName, content, "saved subcommand", completionSavedSubcommands)
			assertMentions(t, fileName, content, "bundle subcommand", completionBundleSubcommands)
			assertMentions(t, fileName, content, "edit subcommand", completionEditSubcommands)
			assertMentions(t, fileName, content, "authkeys subcommand", completionAuthkeysSubcommands)
			assertMentions(t, fileName, content, "incoming subcommand", completionIncomingSubcommands)
			assertMentions(t, fileName, content, "flag", completionRequiredFlags)
		})
	}
}

// TestCompletionCommandsDispatch proves every command in the canonical
// list is really handled by the dispatcher: "<cmd> --help" must exit 0
// and print the command's own usage topic. The topic comparison guards
// against the connect fallback, which also exits 0 for unknown words.
func TestCompletionCommandsDispatch(t *testing.T) {
	for _, cmd := range completionTopLevelCommands {
		t.Run(cmd, func(t *testing.T) {
			want, ok := helpTopicUsage(cmd)
			if !ok {
				t.Fatalf("no help topic for %q; is it still dispatched in Run?", cmd)
			}
			var stdout, stderr bytes.Buffer
			code := Run([]string{cmd, "--help"}, &stdout, &stderr, BuildInfo{})
			if code != 0 {
				t.Fatalf("Run(%s --help) = %d, want 0; stderr=%q", cmd, code, stderr.String())
			}
			if stdout.String() != want {
				t.Fatalf("Run(%s --help) did not print the %s usage topic:\n%s", cmd, cmd, stdout.String())
			}
		})
	}
}

// TestCompletionCommandListMatchesGlobalHelp pins the canonical list to
// the "Available Commands" block of the global help. A command added to
// the dispatcher shows up there first, so this test reminds the
// developer to update completionTopLevelCommands and the completion
// files in the same change.
func TestCompletionCommandListMatchesGlobalHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run([]string{"help"}, &stdout, &stderr, BuildInfo{}); code != 0 {
		t.Fatalf("Run(help) = %d, want 0; stderr=%q", code, stderr.String())
	}

	helpCommands := map[string]bool{}
	inBlock := false
	for _, line := range strings.Split(stdout.String(), "\n") {
		if strings.TrimSpace(line) == "Available Commands:" {
			inBlock = true
			continue
		}
		if !inBlock {
			continue
		}
		if strings.TrimSpace(line) == "" {
			break
		}
		if fields := strings.Fields(line); len(fields) > 0 {
			helpCommands[fields[0]] = true
		}
	}
	if len(helpCommands) == 0 {
		t.Fatal("global help has no Available Commands block")
	}

	expected := map[string]bool{}
	for _, cmd := range completionTopLevelCommands {
		if cmd == "recv" {
			// Alias: the overview lists it as "(alias: recv)" under receive.
			continue
		}
		expected[cmd] = true
	}
	for cmd := range expected {
		if !helpCommands[cmd] {
			t.Errorf("global help is missing command %q from the completion drift list", cmd)
		}
	}
	for cmd := range helpCommands {
		if !expected[cmd] {
			t.Errorf("global help lists %q but completionTopLevelCommands does not; update this test and completions/", cmd)
		}
	}
}
