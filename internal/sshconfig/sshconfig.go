package sshconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"
)

type Diagnostic struct {
	Severity string `json:"severity"`
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
}

type File struct {
	Path    string `json:"path"`
	Missing bool   `json:"missing,omitempty"`
}

type IncludeEdge struct {
	FromPath    string   `json:"from_path"`
	FromLine    int      `json:"from_line"`
	Pattern     string   `json:"pattern"`
	Paths       []string `json:"paths,omitempty"`
	Conditional bool     `json:"conditional,omitempty"`
}

type Graph struct {
	RootPath    string        `json:"root_path"`
	Files       []File        `json:"files"`
	Includes    []IncludeEdge `json:"includes,omitempty"`
	Diagnostics []Diagnostic  `json:"diagnostics,omitempty"`
	Blocks      []HostBlock   `json:"-"`
}

type HostBlock struct {
	SourcePath  string
	SourceLine  int
	Patterns    []string
	Conditional bool
	Options     []Option
}

type Option struct {
	Keyword    string
	Values     []string
	SourcePath string
	SourceLine int
}

type LoadOptions struct {
	RootPath string
	HomeDir  string
}

func Load(opts LoadOptions) (*Graph, error) {
	home := opts.HomeDir
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home directory: %w", err)
		}
	}

	root := opts.RootPath
	if root == "" {
		root = filepath.Join(home, ".ssh", "config")
	}

	root, err := normalizePath(root, home, "")
	if err != nil {
		return nil, err
	}

	loader := &loader{
		home:  home,
		graph: &Graph{RootPath: root},
		seen:  map[string]bool{},
		stack: map[string]bool{},
	}
	loader.parseFile(root, false)
	return loader.graph, nil
}

type loader struct {
	home  string
	graph *Graph
	seen  map[string]bool
	stack map[string]bool
}

func (l *loader) parseFile(path string, conditional bool) {
	path = filepath.Clean(path)

	if l.stack[path] {
		l.addDiagnostic(SeverityWarning, path, 0, "skipping cyclic Include")
		return
	}
	if l.seen[path] {
		l.addDiagnostic(SeverityInfo, path, 0, "skipping duplicate Include")
		return
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.graph.Files = append(l.graph.Files, File{Path: path, Missing: true})
			l.addDiagnostic(SeverityWarning, path, 0, "config file does not exist")
			return
		}
		l.graph.Files = append(l.graph.Files, File{Path: path})
		l.addDiagnostic(SeverityError, path, 0, fmt.Sprintf("could not read config file: %v", err))
		return
	}

	l.seen[path] = true
	l.stack[path] = true
	defer delete(l.stack, path)

	l.graph.Files = append(l.graph.Files, File{Path: path})

	baseDir := filepath.Dir(path)
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")

	section := sectionGlobal
	matchConditional := false
	currentBlock := -1

	for i, raw := range lines {
		lineNo := i + 1
		line, err := parseLine(raw)
		if err != nil {
			l.addDiagnostic(SeverityWarning, path, lineNo, err.Error())
			continue
		}
		if line.Keyword == "" {
			continue
		}

		switch line.Keyword {
		case "host":
			if len(line.Values) == 0 {
				l.addDiagnostic(SeverityWarning, path, lineNo, "Host directive has no patterns")
				currentBlock = -1
				section = sectionHost
				continue
			}
			block := HostBlock{
				SourcePath:  path,
				SourceLine:  lineNo,
				Patterns:    append([]string(nil), line.Values...),
				Conditional: conditional,
			}
			l.graph.Blocks = append(l.graph.Blocks, block)
			currentBlock = len(l.graph.Blocks) - 1
			section = sectionHost
			matchConditional = false
		case "match":
			currentBlock = -1
			section = sectionMatch
			matchConditional = !isUnconditionalMatch(line.Values)
		case "include":
			includeConditional := conditional || section == sectionHost || (section == sectionMatch && matchConditional)
			l.expandInclude(path, lineNo, baseDir, line.Values, includeConditional)
		default:
			if section == sectionHost && currentBlock >= 0 {
				l.graph.Blocks[currentBlock].Options = append(l.graph.Blocks[currentBlock].Options, Option{
					Keyword:    line.Keyword,
					Values:     append([]string(nil), line.Values...),
					SourcePath: path,
					SourceLine: lineNo,
				})
			}
		}
	}
}

func (l *loader) expandInclude(fromPath string, fromLine int, baseDir string, patterns []string, conditional bool) {
	if len(patterns) == 0 {
		l.addDiagnostic(SeverityWarning, fromPath, fromLine, "Include directive has no paths")
		return
	}

	for _, pattern := range patterns {
		paths, err := expandPathPattern(pattern, baseDir, l.home)
		if err != nil {
			l.addDiagnostic(SeverityWarning, fromPath, fromLine, err.Error())
			continue
		}

		edge := IncludeEdge{
			FromPath:    fromPath,
			FromLine:    fromLine,
			Pattern:     pattern,
			Paths:       append([]string(nil), paths...),
			Conditional: conditional,
		}
		l.graph.Includes = append(l.graph.Includes, edge)

		for _, includePath := range paths {
			l.parseFile(includePath, conditional)
		}
	}
}

func (l *loader) addDiagnostic(severity string, path string, line int, message string) {
	l.graph.Diagnostics = append(l.graph.Diagnostics, Diagnostic{
		Severity: severity,
		Path:     path,
		Line:     line,
		Message:  message,
	})
}

type sectionKind int

const (
	sectionGlobal sectionKind = iota
	sectionHost
	sectionMatch
)

type parsedLine struct {
	Keyword string
	Values  []string
}

func parseLine(raw string) (parsedLine, error) {
	fields, err := splitFields(raw)
	if err != nil {
		return parsedLine{}, err
	}
	if len(fields) == 0 {
		return parsedLine{}, nil
	}

	key := fields[0]
	values := fields[1:]

	if eq := strings.IndexByte(key, '='); eq > 0 {
		values = fields[1:]
		if key[eq+1:] != "" {
			values = append([]string{key[eq+1:]}, values...)
		}
		key = key[:eq]
	} else if len(fields) >= 2 && fields[1] == "=" {
		values = fields[2:]
	}

	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return parsedLine{}, nil
	}
	return parsedLine{Keyword: key, Values: values}, nil
}

func splitFields(raw string) ([]string, error) {
	var fields []string
	var b strings.Builder
	inQuote := false
	escaped := false
	inField := false

	flush := func() {
		if inField {
			fields = append(fields, b.String())
			b.Reset()
			inField = false
		}
	}

	for _, r := range raw {
		if escaped {
			b.WriteRune(r)
			inField = true
			escaped = false
			continue
		}

		switch {
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
			inField = true
		case r == '#' && !inQuote:
			flush()
			if inQuote {
				return nil, errors.New("unclosed quote")
			}
			return fields, nil
		case isSpace(r) && !inQuote:
			flush()
		default:
			b.WriteRune(r)
			inField = true
		}
	}

	if escaped {
		b.WriteRune('\\')
	}
	if inQuote {
		return nil, errors.New("unclosed quote")
	}
	flush()
	return fields, nil
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func isUnconditionalMatch(values []string) bool {
	return len(values) == 1 && strings.EqualFold(values[0], "all")
}

func expandPathPattern(pattern string, baseDir string, home string) ([]string, error) {
	path, err := normalizePath(pattern, home, baseDir)
	if err != nil {
		return nil, err
	}

	if hasGlobMeta(path) {
		matches, err := filepath.Glob(path)
		if err != nil {
			return nil, fmt.Errorf("invalid Include glob %q: %w", pattern, err)
		}
		sort.Strings(matches)
		return matches, nil
	}

	return []string{path}, nil
}

func normalizePath(path string, home string, baseDir string) (string, error) {
	switch {
	case path == "~":
		path = home
	case strings.HasPrefix(path, "~/"):
		path = filepath.Join(home, path[2:])
	case strings.HasPrefix(path, "~"):
		return "", fmt.Errorf("unsupported user-qualified path %q", path)
	case !filepath.IsAbs(path):
		if baseDir != "" {
			path = filepath.Join(baseDir, path)
		}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	return filepath.Clean(abs), nil
}

func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}
