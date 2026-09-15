package hostlist

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/0xbenc/ssherpa/internal/sshconfig"
)

type Options struct {
	All           bool
	Filter        string
	User          string
	IgnoreGitUser bool
}

type Inventory struct {
	Aliases     []Alias                `json:"aliases"`
	Diagnostics []sshconfig.Diagnostic `json:"diagnostics,omitempty"`
}

type Alias struct {
	Name          string   `json:"name"`
	SourcePath    string   `json:"source_path"`
	SourceLine    int      `json:"source_line"`
	RawPatterns   []string `json:"raw_patterns"`
	IsPattern     bool     `json:"is_pattern"`
	IsNegatedOnly bool     `json:"is_negated_only,omitempty"`
	IsConditional bool     `json:"is_conditional,omitempty"`
	HostName      string   `json:"hostname,omitempty"`
	User          string   `json:"user,omitempty"`
	Port          string   `json:"port,omitempty"`
	IdentityFiles []string `json:"identity_files,omitempty"`
	Warnings      []string `json:"warnings,omitempty"`
}

func Build(graph *sshconfig.Graph, opts Options) Inventory {
	if graph == nil {
		return Inventory{}
	}

	blockIndex := newBlockIndex(graph.Blocks)

	aliases := make([]Alias, 0, len(graph.Blocks))
	byName := map[string]int{}
	diagnostics := append([]sshconfig.Diagnostic(nil), graph.Diagnostics...)

	for _, block := range graph.Blocks {
		for _, pattern := range block.Patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
				continue
			}

			if strings.HasPrefix(pattern, "-") {
				// A name beginning with "-" can never be used as an SSH
				// destination — OpenSSH would parse it as an option (the
				// argument-injection vector from the stability audit) —
				// so it is dropped from the inventory entirely rather
				// than offered in pickers or matched by --select.
				diagnostics = append(diagnostics, sshconfig.Diagnostic{
					Severity: sshconfig.SeverityWarning,
					Path:     block.SourcePath,
					Line:     block.SourceLine,
					Message: fmt.Sprintf(
						"skipping host alias %q: a name beginning with \"-\" would be parsed as an ssh option",
						pattern,
					),
				})
				continue
			}

			alias := Alias{
				Name:          pattern,
				SourcePath:    block.SourcePath,
				SourceLine:    block.SourceLine,
				RawPatterns:   append([]string(nil), block.Patterns...),
				IsPattern:     IsPattern(pattern),
				IsNegatedOnly: strings.HasPrefix(pattern, "!"),
				IsConditional: block.Conditional,
			}
			applyParsedEffectiveValues(&alias, graph, blockIndex)

			if alias.IsConditional {
				alias.Warnings = append(alias.Warnings, "alias came from a conditional Include or Match scope")
			}

			if first, ok := byName[alias.Name]; ok {
				aliases[first].Warnings = append(aliases[first].Warnings, fmt.Sprintf(
					"duplicate alias also found at %s:%d; first occurrence is used",
					block.SourcePath,
					block.SourceLine,
				))
				continue
			}

			byName[alias.Name] = len(aliases)
			aliases = append(aliases, alias)
		}
	}

	filtered := aliases[:0]
	for _, alias := range aliases {
		if includeAlias(alias, opts) {
			filtered = append(filtered, alias)
		}
	}

	return Inventory{
		Aliases:     filtered,
		Diagnostics: diagnostics,
	}
}

func IsPattern(name string) bool {
	return strings.HasPrefix(name, "!") || strings.ContainsAny(name, "*?")
}

func includeAlias(alias Alias, opts Options) bool {
	if !opts.All && (alias.IsPattern || alias.IsNegatedOnly) {
		return false
	}

	if opts.IgnoreGitUser && opts.User == "" && strings.EqualFold(alias.User, "git") {
		return false
	}

	if opts.User != "" && alias.User != "" && alias.User != opts.User {
		return false
	}

	if opts.Filter != "" && !strings.Contains(searchText(alias), opts.Filter) {
		return false
	}

	return true
}

func searchText(alias Alias) string {
	parts := []string{alias.Name, alias.HostName, alias.User, alias.Port}
	parts = append(parts, alias.IdentityFiles...)
	return strings.Join(parts, "\t")
}

// blockIndex partitions the graph's blocks once per Build so each alias
// consults only the blocks that can possibly match it. Literal-only blocks
// are looked up by exact pattern; every other block (any negation or glob)
// is kept in an ordered scan list.
type blockIndex struct {
	literal map[string][]int
	scan    []int
}

func newBlockIndex(blocks []sshconfig.HostBlock) blockIndex {
	idx := blockIndex{literal: make(map[string][]int, len(blocks))}
	for i, block := range blocks {
		if isLiteralOnlyBlock(block) {
			for _, pattern := range block.Patterns {
				if pattern == "" {
					continue
				}
				idx.literal[pattern] = append(idx.literal[pattern], i)
			}
		} else {
			idx.scan = append(idx.scan, i)
		}
	}
	return idx
}

// isLiteralOnlyBlock reports whether every pattern in block can match only
// by exact equality, so the block can be indexed by pattern instead of
// scanned per alias. filepath.Match treats "*", "?", "[" and "\" as
// metacharacters (character classes and escapes), so any of those — plus a
// "!" prefix — forces the block into the scan list. IsPattern is
// deliberately not reused here: it only looks at "*?" and drives the
// Alias.IsPattern display semantics elsewhere.
func isLiteralOnlyBlock(block sshconfig.HostBlock) bool {
	for _, pattern := range block.Patterns {
		if pattern == "" {
			continue
		}
		if strings.HasPrefix(pattern, "!") || strings.ContainsAny(pattern, "*?[\\") {
			return false
		}
	}
	return true
}

// candidates returns the block indices that can possibly match name, in
// ascending block order: the literal map hit merged with the scan list and
// de-duplicated, so per-block application order matches a full rescan.
func (idx blockIndex) candidates(name string) []int {
	hits := idx.literal[name]
	scan := idx.scan
	if len(hits) == 0 {
		return scan
	}
	if len(scan) == 0 {
		return hits
	}
	merged := make([]int, 0, len(hits)+len(scan))
	i, j := 0, 0
	for i < len(hits) || j < len(scan) {
		var next int
		switch {
		case j >= len(scan) || (i < len(hits) && hits[i] < scan[j]):
			next = hits[i]
			i++
		case i >= len(hits) || scan[j] < hits[i]:
			next = scan[j]
			j++
		default:
			next = hits[i]
			i++
			j++
		}
		if n := len(merged); n == 0 || merged[n-1] != next {
			merged = append(merged, next)
		}
	}
	return merged
}

func applyParsedEffectiveValues(alias *Alias, graph *sshconfig.Graph, idx blockIndex) {
	seenIdentity := map[string]bool{}

	for _, blockIdx := range idx.candidates(alias.Name) {
		block := graph.Blocks[blockIdx]
		if !blockMatchesName(block, alias.Name) {
			continue
		}

		for _, option := range block.Options {
			value := firstValue(option.Values)
			if value == "" {
				continue
			}

			switch option.Keyword {
			case "hostname":
				if alias.HostName == "" {
					alias.HostName = value
				}
			case "user":
				if alias.User == "" {
					alias.User = value
				}
			case "port":
				if alias.Port == "" {
					alias.Port = value
				}
			case "identityfile":
				if !seenIdentity[value] {
					alias.IdentityFiles = append(alias.IdentityFiles, value)
					seenIdentity[value] = true
				}
			}
		}
	}
}

func firstValue(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func blockMatchesName(block sshconfig.HostBlock, name string) bool {
	matched := false
	for _, pattern := range block.Patterns {
		if pattern == "" {
			continue
		}

		negated := strings.HasPrefix(pattern, "!")
		matchPattern := strings.TrimPrefix(pattern, "!")
		if patternMatches(matchPattern, name) {
			if negated {
				return false
			}
			matched = true
		}
	}
	return matched
}

func patternMatches(pattern string, name string) bool {
	if pattern == name {
		return true
	}
	ok, err := filepath.Match(pattern, name)
	return err == nil && ok
}
