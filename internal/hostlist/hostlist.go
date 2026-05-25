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

	aliases := make([]Alias, 0, len(graph.Blocks))
	byName := map[string]int{}

	for _, block := range graph.Blocks {
		for _, pattern := range block.Patterns {
			pattern = strings.TrimSpace(pattern)
			if pattern == "" {
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
			applyParsedEffectiveValues(&alias, graph)

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
		Diagnostics: append([]sshconfig.Diagnostic(nil), graph.Diagnostics...),
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

func applyParsedEffectiveValues(alias *Alias, graph *sshconfig.Graph) {
	seenIdentity := map[string]bool{}

	for _, block := range graph.Blocks {
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
