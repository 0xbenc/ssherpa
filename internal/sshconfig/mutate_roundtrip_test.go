package sshconfig

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRenderValueQuotesShellAndQuoteCharacters(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain", value: "plain", want: "plain"},
		{name: "empty", value: "", want: `""`},
		{name: "space", value: "a b", want: `"a b"`},
		{name: "hash", value: "a#b", want: `"a#b"`},
		{name: "single quote", value: "o'brien", want: `"o'brien"`},
		{name: "double quote", value: `say"hi`, want: `"say\"hi"`},
		{name: "both quotes", value: `o'br"ien`, want: `"o'br\"ien"`},
		{name: "backslash", value: `a\b`, want: `"a\\b"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := renderValue(tt.value); got != tt.want {
				t.Fatalf("renderValue(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestPlanAddOrUpdateQuotesApostropheValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")

	plan, err := PlanAddOrUpdate(path, AliasSpec{
		Alias:        "obrien",
		HostName:     "h.example.com",
		User:         "o'brien",
		IdentityFile: "/Users/o'brien/.ssh/id",
	})
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}
	got := string(plan.NewData)
	for _, want := range []string{
		`  User "o'brien"`,
		`  IdentityFile "/Users/o'brien/.ssh/id"`,
	} {
		if !strings.Contains(got, want+"\n") {
			t.Fatalf("NewData missing quoted value %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "User o'brien") {
		t.Fatalf("NewData contains unquoted apostrophe value:\n%s", got)
	}
}

func TestValidateAliasSpecRejectsQuoteAndBackslashAliases(t *testing.T) {
	tests := []struct {
		name  string
		alias string
	}{
		{name: "single quote", alias: "o'brien"},
		{name: "double quote", alias: `say"hi`},
		{name: "backslash", alias: `a\b`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateAliasSpec(AliasSpec{Alias: tt.alias, HostName: "h.example.com"}, false)
			if err == nil {
				t.Fatalf("ValidateAliasSpec(%q) returned nil error", tt.alias)
			}
			if !strings.Contains(err.Error(), "quote or backslash") {
				t.Fatalf("error = %v, want quote/backslash message", err)
			}
		})
	}
}

func TestValidateAliasSpecRejectsControlCharacters(t *testing.T) {
	tests := []struct {
		name string
		spec AliasSpec
	}{
		{name: "NUL in value", spec: AliasSpec{Alias: "prod", HostName: "a\x00b"}},
		{name: "vertical tab in value", spec: AliasSpec{Alias: "prod", HostName: "h", User: "a\vb"}},
		{name: "NUL in alias", spec: AliasSpec{Alias: "pr\x00od", HostName: "h"}},
		{name: "form feed in alias", spec: AliasSpec{Alias: "pr\fod", HostName: "h"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAliasSpec(tt.spec, false); err == nil {
				t.Fatalf("ValidateAliasSpec(%#v) returned nil error", tt.spec)
			}
		})
	}
}

func TestPlanAddOrUpdateUpdatesMixedCaseAliasInPlace(t *testing.T) {
	path := writeTempConfig(t, "Host Prod\n  HostName old.example.com\n  User old\n")

	spec, ok, err := ExistingAliasSpec(path, "Prod")
	if err != nil || !ok {
		t.Fatalf("ExistingAliasSpec = %v, %t", err, ok)
	}
	if spec.Alias != "Prod" {
		t.Fatalf("spec.Alias = %q, want original casing", spec.Alias)
	}
	spec.HostName = "new.example.com"

	plan, err := PlanAddOrUpdate(path, spec)
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}
	if plan.Action != "updated" {
		t.Fatalf("plan.Action = %q, want updated (not an appended duplicate)", plan.Action)
	}
	want := "Host Prod\n  HostName new.example.com\n  User old\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
	if plan.Alias != "Prod" {
		t.Fatalf("plan.Alias = %q, want matched casing", plan.Alias)
	}
}

func TestPlanAddOrUpdateFoldsCaseWhenNoExactMatch(t *testing.T) {
	path := writeTempConfig(t, "Host Prod\n  HostName old.example.com\n")

	plan, err := PlanAddOrUpdate(path, AliasSpec{Alias: "PROD", HostName: "new.example.com"})
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}
	if plan.Action != "updated" {
		t.Fatalf("plan.Action = %q, want updated", plan.Action)
	}
	want := "Host Prod\n  HostName new.example.com\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
}

func TestPlanAddOrUpdatePrefersExactCaseMatch(t *testing.T) {
	path := writeTempConfig(t, "Host prod\n  HostName lower.example.com\n\nHost Prod\n  HostName upper.example.com\n")

	plan, err := PlanAddOrUpdate(path, AliasSpec{Alias: "prod", HostName: "new.example.com"})
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}
	want := "Host prod\n  HostName new.example.com\n\nHost Prod\n  HostName upper.example.com\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
}

func TestExistingAliasSpecFoldsCase(t *testing.T) {
	path := writeTempConfig(t, "Host Prod\n  HostName x.example.com\n")

	spec, ok, err := ExistingAliasSpec(path, "prod")
	if err != nil {
		t.Fatalf("ExistingAliasSpec returned error: %v", err)
	}
	if !ok {
		t.Fatalf("ExistingAliasSpec ok = false, want case-insensitive match")
	}
	if spec.Alias != "Prod" {
		t.Fatalf("spec.Alias = %q, want matched casing Prod", spec.Alias)
	}
}

func TestPlanDeleteAliasFoldsCaseWhenNoExactMatch(t *testing.T) {
	path := writeTempConfig(t, "Host Prod\n  HostName x.example.com\n")

	plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err != nil {
		t.Fatalf("PlanDeleteAlias returned error: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("plan.Changed = false, want case-insensitive delete")
	}
	if len(plan.NewData) != 0 {
		t.Fatalf("NewData = %q, want empty", string(plan.NewData))
	}
}

func TestPlanDeleteAliasRefusesMultipleDistinctCasings(t *testing.T) {
	path := writeTempConfig(t, "Host Prod\n  HostName one.example.com\n\nHost PROD\n  HostName two.example.com\n")

	_, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err == nil {
		t.Fatal("PlanDeleteAlias returned nil error, want multi-casing refusal")
	}
	if !strings.Contains(err.Error(), "different casings") || !strings.Contains(err.Error(), "Prod") || !strings.Contains(err.Error(), "PROD") {
		t.Fatalf("error = %v, want refusal naming each casing", err)
	}
}

func TestPlanDeleteAliasPrefersExactCaseMatch(t *testing.T) {
	path := writeTempConfig(t, "Host prod\n  HostName lower.example.com\n\nHost Prod\n  HostName upper.example.com\n")

	plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err != nil {
		t.Fatalf("PlanDeleteAlias returned error: %v", err)
	}
	want := "Host Prod\n  HostName upper.example.com\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want only the exact-case stanza deleted (%q)", string(plan.NewData), want)
	}
}

func TestFindAliasOccurrencesFoldsCase(t *testing.T) {
	graph := &Graph{Blocks: []HostBlock{
		{SourcePath: "a", Patterns: []string{"Prod"}},
		{SourcePath: "b", Patterns: []string{"other"}},
	}}

	occurrences := FindAliasOccurrences(graph, "prod")
	if len(occurrences) != 1 || occurrences[0].Path != "a" {
		t.Fatalf("occurrences = %#v, want one folded match in a", occurrences)
	}

	graph.Blocks = append(graph.Blocks, HostBlock{SourcePath: "c", Patterns: []string{"prod"}})
	occurrences = FindAliasOccurrences(graph, "prod")
	if len(occurrences) != 1 || occurrences[0].Path != "c" {
		t.Fatalf("occurrences = %#v, want exact match in c only", occurrences)
	}
}

func TestPlanDeleteAliasPreservesTrailingContent(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "comment documenting next stanza",
			in:   "Host prod\n  HostName x.example.com\n\n# keep's documentation comment\nHost keep\n  HostName y.example.com\n",
			want: "# keep's documentation comment\nHost keep\n  HostName y.example.com\n",
		},
		{
			name: "end of file comment",
			in:   "Host keep\n  HostName y.example.com\n\nHost prod\n  HostName x.example.com\n\n# TODO migrate bastions next quarter\n",
			want: "Host keep\n  HostName y.example.com\n\n# TODO migrate bastions next quarter\n",
		},
		{
			name: "trailing include",
			in:   "Host prod\n  HostName x.example.com\n\nInclude work.conf\n",
			want: "Include work.conf\n",
		},
		{
			name: "include between stanzas",
			in:   "Host prod\n  HostName x.example.com\n\nInclude work.conf\n\nHost keep\n  HostName y.example.com\n",
			want: "Include work.conf\n\nHost keep\n  HostName y.example.com\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeTempConfig(t, tt.in)
			plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
			if err != nil {
				t.Fatalf("PlanDeleteAlias returned error: %v", err)
			}
			if string(plan.NewData) != tt.want {
				t.Fatalf("NewData = %q, want %q", string(plan.NewData), tt.want)
			}
		})
	}
}

func TestPlanDeleteAliasWarnsWhenIncludeInsideStanzaIsRemoved(t *testing.T) {
	path := writeTempConfig(t, "Host prod\n  HostName x.example.com\n  Include extra.conf\n  User alice\n")

	plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
	if err != nil {
		t.Fatalf("PlanDeleteAlias returned error: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("plan.Changed = false, want delete")
	}
	if len(plan.Warnings) != 1 || !strings.Contains(plan.Warnings[0], "Include extra.conf") {
		t.Fatalf("Warnings = %#v, want Include removal warning", plan.Warnings)
	}
}

func TestPlanAddOrUpdateSplitCarriesUnmanagedOptions(t *testing.T) {
	path := writeTempConfig(t, "Host prod web\n  HostName shared.example.com\n  ForwardAgent yes\n  ServerAliveInterval 30\n")

	spec, ok, err := ExistingAliasSpec(path, "prod")
	if err != nil || !ok {
		t.Fatalf("ExistingAliasSpec = %v, %t", err, ok)
	}
	spec.HostName = "prod.example.com"

	plan, err := PlanAddOrUpdate(path, spec)
	if err != nil {
		t.Fatalf("PlanAddOrUpdate returned error: %v", err)
	}
	want := "Host web\n" +
		"  HostName shared.example.com\n" +
		"  ForwardAgent yes\n" +
		"  ServerAliveInterval 30\n" +
		"\n" +
		"Host prod\n" +
		"  HostName prod.example.com\n" +
		"  ForwardAgent yes\n" +
		"  ServerAliveInterval 30\n"
	if string(plan.NewData) != want {
		t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
	}
}

func TestPlanAddOrUpdatePreservesMultipleIdentityFiles(t *testing.T) {
	const in = "Host prod\n  HostName x.example.com\n  IdentityFile ~/.ssh/a # primary\n  IdentityFile ~/.ssh/b\n"

	t.Run("identity untouched keeps all lines", func(t *testing.T) {
		path := writeTempConfig(t, in)
		spec, ok, err := ExistingAliasSpec(path, "prod")
		if err != nil || !ok {
			t.Fatalf("ExistingAliasSpec = %v, %t", err, ok)
		}
		spec.User = "alice"

		plan, err := PlanAddOrUpdate(path, spec)
		if err != nil {
			t.Fatalf("PlanAddOrUpdate returned error: %v", err)
		}
		want := "Host prod\n" +
			"  HostName x.example.com\n" +
			"  User alice\n" +
			"  IdentityFile ~/.ssh/a # primary\n" +
			"  IdentityFile ~/.ssh/b\n"
		if string(plan.NewData) != want {
			t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
		}
	})

	t.Run("explicit identity change replaces all lines", func(t *testing.T) {
		path := writeTempConfig(t, in)
		spec, _, err := ExistingAliasSpec(path, "prod")
		if err != nil {
			t.Fatalf("ExistingAliasSpec returned error: %v", err)
		}
		spec.IdentityFile = "~/.ssh/new"

		plan, err := PlanAddOrUpdate(path, spec)
		if err != nil {
			t.Fatalf("PlanAddOrUpdate returned error: %v", err)
		}
		want := "Host prod\n  HostName x.example.com\n  IdentityFile ~/.ssh/new\n"
		if string(plan.NewData) != want {
			t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
		}
	})

	t.Run("explicit identity clear removes all lines", func(t *testing.T) {
		path := writeTempConfig(t, in)
		spec, _, err := ExistingAliasSpec(path, "prod")
		if err != nil {
			t.Fatalf("ExistingAliasSpec returned error: %v", err)
		}
		spec.IdentityFile = ""

		plan, err := PlanAddOrUpdate(path, spec)
		if err != nil {
			t.Fatalf("PlanAddOrUpdate returned error: %v", err)
		}
		want := "Host prod\n  HostName x.example.com\n"
		if string(plan.NewData) != want {
			t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
		}
	})

	t.Run("split carries all identity lines", func(t *testing.T) {
		path := writeTempConfig(t, "Host prod web\n  HostName shared.example.com\n  IdentityFile ~/.ssh/a\n  IdentityFile ~/.ssh/b\n")
		spec, _, err := ExistingAliasSpec(path, "prod")
		if err != nil {
			t.Fatalf("ExistingAliasSpec returned error: %v", err)
		}
		spec.User = "alice"

		plan, err := PlanAddOrUpdate(path, spec)
		if err != nil {
			t.Fatalf("PlanAddOrUpdate returned error: %v", err)
		}
		got := string(plan.NewData)
		stanza := "Host prod\n  HostName shared.example.com\n  User alice\n  IdentityFile ~/.ssh/a\n  IdentityFile ~/.ssh/b\n"
		if !strings.Contains(got, stanza) {
			t.Fatalf("NewData missing split stanza with both identities:\n%s", got)
		}
	})
}

func TestMutationRoundTripCRLF(t *testing.T) {
	t.Run("edit keeps CRLF and trailing comment", func(t *testing.T) {
		path := writeTempConfig(t, "Host prod\r\n  HostName old.example.com\r\n\r\n# tail\r\n")
		plan, err := PlanAddOrUpdate(path, AliasSpec{Alias: "prod", HostName: "new.example.com"})
		if err != nil {
			t.Fatalf("PlanAddOrUpdate returned error: %v", err)
		}
		want := "Host prod\r\n  HostName new.example.com\r\n\r\n# tail\r\n"
		if string(plan.NewData) != want {
			t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
		}
	})

	t.Run("delete keeps CRLF and trailing comment", func(t *testing.T) {
		path := writeTempConfig(t, "Host prod\r\n  HostName old.example.com\r\n\r\n# tail\r\n")
		plan, err := PlanDeleteAlias(path, "prod", DeleteOptions{})
		if err != nil {
			t.Fatalf("PlanDeleteAlias returned error: %v", err)
		}
		want := "# tail\r\n"
		if string(plan.NewData) != want {
			t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
		}
	})

	t.Run("append keeps CRLF", func(t *testing.T) {
		path := writeTempConfig(t, "Host old\r\n  HostName old.example.com\r\n")
		plan, err := PlanAddOrUpdate(path, AliasSpec{Alias: "prod", HostName: "prod.example.com"})
		if err != nil {
			t.Fatalf("PlanAddOrUpdate returned error: %v", err)
		}
		want := "Host old\r\n  HostName old.example.com\r\n\r\nHost prod\r\n  HostName prod.example.com\r\n"
		if string(plan.NewData) != want {
			t.Fatalf("NewData = %q, want %q", string(plan.NewData), want)
		}
	})
}

var (
	sshPathOnce sync.Once
	sshPath     string
)

func sshBinaryForTest() string {
	sshPathOnce.Do(func() {
		if path, err := exec.LookPath("ssh"); err == nil {
			sshPath = path
		}
	})
	return sshPath
}

func TestPlanAddOrUpdateOutputAcceptedBySSH(t *testing.T) {
	ssh := sshBinaryForTest()
	if ssh == "" {
		t.Skip("ssh not available")
	}

	tests := []struct {
		name string
		user string
	}{
		{name: "apostrophe", user: "o'brien"},
		{name: "double quote", user: `say"hi`},
		{name: "both quotes", user: `o'br"ien`},
		{name: "backslash", user: `a\b`},
		{name: "space and hash", user: "a b#c"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config")
			plan, err := PlanAddOrUpdate(path, AliasSpec{
				Alias:    "sshprobe",
				HostName: "h.example.com",
				User:     tt.user,
			})
			if err != nil {
				t.Fatalf("PlanAddOrUpdate returned error: %v", err)
			}
			if err := os.WriteFile(path, plan.NewData, 0o600); err != nil {
				t.Fatalf("os.WriteFile returned error: %v", err)
			}

			out, err := exec.Command(ssh, "-G", "-F", path, "sshprobe").CombinedOutput()
			if err != nil {
				t.Fatalf("ssh -G rejected rendered config: %v\n%s\nconfig:\n%s", err, out, plan.NewData)
			}
			if !strings.Contains(string(out), "user "+tt.user+"\n") {
				t.Fatalf("ssh -G did not round-trip user %q:\n%s", tt.user, out)
			}
		})
	}
}

// FuzzMutateNoOpEdit asserts that re-saving an alias unchanged never alters
// the document's structure: every Host pattern, comment, and Include line
// survives, the managed values read back identically, a second no-op edit is
// a byte-level fixed point, and (when ssh is available) the rendered file
// never trips OpenSSH's tokenizer ("invalid quotes").
func FuzzMutateNoOpEdit(f *testing.F) {
	matrix, err := os.ReadFile(filepath.Join("testdata", "matrix", "config"))
	if err != nil {
		f.Fatalf("read matrix seed: %v", err)
	}
	f.Add(matrix)
	f.Add([]byte("Host prod\n  HostName x.example.com\n\n# keep's documentation comment\nHost keep\n  HostName y.example.com\n"))
	f.Add([]byte("Host Prod web\n  HostName shared.example.com\n  ForwardAgent yes\n  ServerAliveInterval 30\n"))
	f.Add([]byte("Host crlf\r\n  HostName crlf.example.com\r\n  User \"o'brien\"\r\n"))
	f.Add([]byte("Host multi\n  HostName m.example.com\n  IdentityFile ~/.ssh/a\n  IdentityFile ~/.ssh/b\n\nInclude extra.conf\n# tail comment\n"))
	f.Add([]byte("Host MixedCase\n  HostName mc.example.com\n  Port 2222\n  IdentitiesOnly yes\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<15 {
			t.Skip("oversized input")
		}
		doc, err := ParseDocument("fuzz-config", data)
		if err != nil {
			t.Skip("unparseable input")
		}

		var candidates []string
		seen := map[string]bool{}
		for _, block := range doc.Blocks {
			for _, pattern := range block.Patterns {
				if seen[pattern] || validateAliasName(pattern, false) != nil {
					continue
				}
				seen[pattern] = true
				candidates = append(candidates, pattern)
			}
		}
		if len(candidates) == 0 {
			t.Skip("no editable alias")
		}

		path := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("os.WriteFile returned error: %v", err)
		}

		var spec AliasSpec
		var plan MutationPlan
		planned := false
		for _, alias := range candidates {
			candidate, ok, err := ExistingAliasSpec(path, alias)
			if err != nil || !ok {
				continue
			}
			if err := ValidateAliasSpec(candidate, false); err != nil {
				continue
			}
			candidatePlan, err := PlanAddOrUpdate(path, candidate)
			if err != nil {
				continue // legitimate refusal: duplicates or wildcard split
			}
			spec, plan, planned = candidate, candidatePlan, true
			break
		}
		if !planned {
			t.Skip("no alias produced a plan")
		}

		if err := validateRenderedDocument(path, plan.NewData); err != nil {
			t.Fatalf("no-op edit broke document: %v\noutput:\n%s", err, plan.NewData)
		}

		after, err := ParseDocument(path, plan.NewData)
		if err != nil {
			t.Fatalf("reparse failed: %v", err)
		}
		assertPatternMultisetEqual(t, doc, after)
		assertNonOptionLinesPreserved(t, doc, after)

		// Managed values must read back identically from the new data.
		path2 := filepath.Join(t.TempDir(), "config")
		if err := os.WriteFile(path2, plan.NewData, 0o600); err != nil {
			t.Fatalf("os.WriteFile returned error: %v", err)
		}
		spec2, ok2, err := ExistingAliasSpec(path2, spec.Alias)
		if err != nil || !ok2 {
			t.Fatalf("alias %q lost after no-op edit: %v, %t\noutput:\n%s", spec.Alias, err, ok2, plan.NewData)
		}
		// PlanAddOrUpdate normalizes (trims) values, so compare against
		// the normalized form of what was submitted.
		if want := normalizeAliasSpec(spec); spec2 != want {
			t.Fatalf("spec changed across no-op edit:\nsubmitted %#v\nread back %#v\noutput:\n%s", want, spec2, plan.NewData)
		}

		// A second no-op edit must be a fixed point.
		plan2, err := PlanAddOrUpdate(path2, spec2)
		if err != nil {
			t.Fatalf("second no-op edit refused: %v", err)
		}
		if !bytes.Equal(plan2.NewData, plan.NewData) {
			t.Fatalf("no-op edit not idempotent:\nfirst:\n%s\nsecond:\n%s", plan.NewData, plan2.NewData)
		}

		assertSSHTokenizerAccepts(t, after, data, plan.NewData)
	})
}

func assertPatternMultisetEqual(t *testing.T, before *Document, after *Document) {
	t.Helper()
	count := func(doc *Document) map[string]int {
		patterns := map[string]int{}
		for _, block := range doc.Blocks {
			for _, pattern := range block.Patterns {
				patterns[pattern]++
			}
		}
		return patterns
	}
	beforePatterns := count(before)
	afterPatterns := count(after)
	if len(beforePatterns) != len(afterPatterns) {
		t.Fatalf("pattern sets differ: before %#v after %#v", beforePatterns, afterPatterns)
	}
	for pattern, n := range beforePatterns {
		if afterPatterns[pattern] != n {
			t.Fatalf("pattern %q count changed: %d -> %d", pattern, n, afterPatterns[pattern])
		}
	}
}

func assertNonOptionLinesPreserved(t *testing.T, before *Document, after *Document) {
	t.Helper()
	afterLines := map[string]bool{}
	for _, line := range after.Lines {
		afterLines[line] = true
	}
	for i, line := range before.Lines {
		trimmed := strings.TrimLeft(line, " \t")
		isComment := strings.HasPrefix(trimmed, "#")
		parsed, err := parseLine(line)
		isInclude := err == nil && parsed.Keyword == "include"
		if (isComment || isInclude) && !afterLines[line] {
			t.Fatalf("line %d (%q) lost across no-op edit", i+1, line)
		}
	}
}

// assertSSHTokenizerAccepts runs ssh -G against the rendered config and
// fails if OpenSSH's tokenizer rejects it ("invalid quotes") when it
// accepted the original input — i.e. the mutation introduced tokenizer
// breakage. Unknown keywords or bad option values make ssh exit non-zero by
// design, so only the tokenizer failure mode is asserted. Configs containing
// Match or Include are skipped: -G evaluates Match exec and reads Include
// targets.
func assertSSHTokenizerAccepts(t *testing.T, doc *Document, original []byte, rendered []byte) {
	t.Helper()
	ssh := sshBinaryForTest()
	if ssh == "" {
		return
	}
	// Control bytes elsewhere in the input (e.g. a NUL in an untouched
	// stanza) truncate lines for ssh's C-string parser and would blame the
	// writer for pre-existing breakage.
	for _, b := range rendered {
		if b < 0x20 && b != '\t' && b != '\n' && b != '\r' {
			return
		}
	}
	for _, line := range doc.Lines {
		parsed, err := parseLine(line)
		if err != nil {
			return
		}
		if parsed.Keyword == "match" || parsed.Keyword == "include" {
			return
		}
	}

	if sshReportsInvalidQuotes(t, ssh, original) {
		return // pre-existing breakage preserved verbatim, not introduced
	}
	if sshReportsInvalidQuotes(t, ssh, rendered) {
		t.Fatalf("no-op edit introduced tokenizer breakage; ssh -G reports invalid quotes on rendered config:\n%s", rendered)
	}
}

func sshReportsInvalidQuotes(t *testing.T, ssh string, config []byte) bool {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh-check-config")
	if err := os.WriteFile(path, config, 0o600); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, ssh, "-G", "-F", path, "ssherpa-fuzz-probe").CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("ssh -G timed out on config:\n%s", config)
	}
	return bytes.Contains(out, []byte("invalid quotes"))
}
