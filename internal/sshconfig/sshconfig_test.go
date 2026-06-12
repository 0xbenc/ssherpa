package sshconfig

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadParsesFixtureMatrix(t *testing.T) {
	root := filepath.Join("testdata", "matrix", "config")

	graph, err := Load(LoadOptions{RootPath: root, HomeDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if got := len(graph.Blocks); got != 7 {
		t.Fatalf("len(graph.Blocks) = %d, want 7", got)
	}
	assertBlock(t, graph, "prod", "prod.example.com", "alice", "2222", []string{"~/.ssh/prod key", "~/.ssh/prod_ed25519"}, false)
	assertBlock(t, graph, "quoted", "quoted.example.com", "bob", "", nil, false)
	assertBlock(t, graph, "conditional", "conditional.example.com", "matchuser", "", nil, true)

	if len(graph.Includes) != 4 {
		t.Fatalf("len(graph.Includes) = %d, want 4", len(graph.Includes))
	}

	if !hasDiagnostic(graph.Diagnostics, "cyclic Include") {
		t.Fatalf("expected cyclic Include diagnostic, got %#v", graph.Diagnostics)
	}

	if !hasConditionalInclude(graph) {
		t.Fatalf("expected conditional Include edge, got %#v", graph.Includes)
	}
}

func TestLoadMissingRootIsDiagnosticNotHardError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")

	graph, err := Load(LoadOptions{RootPath: root})
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if len(graph.Blocks) != 0 {
		t.Fatalf("len(graph.Blocks) = %d, want 0", len(graph.Blocks))
	}
	if len(graph.Files) != 1 || !graph.Files[0].Missing {
		t.Fatalf("Files = %#v, want one missing file", graph.Files)
	}
	if !hasDiagnostic(graph.Diagnostics, "does not exist") {
		t.Fatalf("expected missing-file diagnostic, got %#v", graph.Diagnostics)
	}
}

func TestParseLineSupportsEqualsQuotesAndComments(t *testing.T) {
	line, err := parseLine(`HostName="server # not comment" # comment`)
	if err != nil {
		t.Fatalf("parseLine returned error: %v", err)
	}

	if line.Keyword != "hostname" {
		t.Fatalf("Keyword = %q, want hostname", line.Keyword)
	}
	if len(line.Values) != 1 || line.Values[0] != "server # not comment" {
		t.Fatalf("Values = %#v, want quoted value", line.Values)
	}
}

func TestSplitFieldsRecognizesSingleQuotes(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    []string
		wantErr bool
	}{
		{name: "single-quoted value", raw: "User 'o brien'", want: []string{"User", "o brien"}},
		{name: "double quote inside single quotes", raw: `User 'say"hi'`, want: []string{"User", `say"hi`}},
		{name: "single quote inside double quotes", raw: `User "o'brien"`, want: []string{"User", "o'brien"}},
		// Modern OpenSSH fatals on an unmatched single quote
		// ("invalid quotes"); read parity must reject it too so the
		// rendered-document safety net catches it.
		{name: "bare apostrophe", raw: "User o'brien", wantErr: true},
		{name: "unclosed single quote", raw: "User 'open", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fields, err := splitFields(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("splitFields(%q) = %#v, want error", tt.raw, fields)
				}
				return
			}
			if err != nil {
				t.Fatalf("splitFields(%q) returned error: %v", tt.raw, err)
			}
			if strings.Join(fields, "\x00") != strings.Join(tt.want, "\x00") {
				t.Fatalf("splitFields(%q) = %#v, want %#v", tt.raw, fields, tt.want)
			}
		})
	}
}

func assertBlock(t *testing.T, graph *Graph, pattern string, host string, user string, port string, identities []string, conditional bool) {
	t.Helper()

	block := findBlock(graph, pattern)
	if block == nil {
		t.Fatalf("block %q not found", pattern)
	}
	if block.Conditional != conditional {
		t.Fatalf("block %q Conditional = %t, want %t", pattern, block.Conditional, conditional)
	}

	values := map[string][]string{}
	for _, option := range block.Options {
		values[option.Keyword] = append(values[option.Keyword], option.Values...)
	}

	assertFirstValue(t, values, "hostname", host)
	assertFirstValue(t, values, "user", user)
	assertFirstValue(t, values, "port", port)

	if got := values["identityfile"]; strings.Join(got, "\x00") != strings.Join(identities, "\x00") {
		t.Fatalf("identityfile values = %#v, want %#v", got, identities)
	}
}

func findBlock(graph *Graph, pattern string) *HostBlock {
	for i := range graph.Blocks {
		for _, blockPattern := range graph.Blocks[i].Patterns {
			if blockPattern == pattern {
				return &graph.Blocks[i]
			}
		}
	}
	return nil
}

func assertFirstValue(t *testing.T, values map[string][]string, keyword string, want string) {
	t.Helper()
	got := ""
	if len(values[keyword]) > 0 {
		got = values[keyword][0]
	}
	if got != want {
		t.Fatalf("%s = %q, want %q", keyword, got, want)
	}
}

func hasDiagnostic(diagnostics []Diagnostic, substring string) bool {
	for _, diagnostic := range diagnostics {
		if strings.Contains(diagnostic.Message, substring) {
			return true
		}
	}
	return false
}

func hasConditionalInclude(graph *Graph) bool {
	for _, include := range graph.Includes {
		if include.Conditional {
			return true
		}
	}
	return false
}
