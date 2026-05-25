package sshconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type AliasSpec struct {
	Alias          string
	HostName       string
	User           string
	Port           string
	IdentityFile   string
	IdentitiesOnly bool
}

type DeleteOptions struct {
	AllowPatterns bool
}

type MutationPlan struct {
	Path      string
	Action    string
	Alias     string
	Aliases   []string
	OldData   []byte
	NewData   []byte
	Changed   bool
	Warnings  []string
	NotFound  []string
	Line      int
	LineCount int
}

type AliasOccurrence struct {
	Path        string
	Line        int
	Patterns    []string
	Conditional bool
}

type Document struct {
	Path               string
	Lines              []string
	Newline            string
	HadTrailingNewline bool
	Missing            bool
	Blocks             []DocumentBlock
	data               []byte
}

type DocumentBlock struct {
	Start       int
	End         int
	SourceLine  int
	Patterns    []string
	Conditional bool
}

func PlanAddOrUpdate(path string, spec AliasSpec) (MutationPlan, error) {
	path = filepath.Clean(path)
	spec = normalizeAliasSpec(spec)
	if err := ValidateAliasSpec(spec, false); err != nil {
		return MutationPlan{}, err
	}

	doc, err := ReadDocument(path)
	if err != nil {
		return MutationPlan{}, err
	}

	matches := doc.findExactAliasBlocks(spec.Alias)
	if len(matches) > 1 {
		return MutationPlan{}, fmt.Errorf("alias %q appears %d times in %s; delete duplicates or choose one source before updating", spec.Alias, len(matches), path)
	}

	action := "added"
	line := len(doc.Lines) + 1
	if len(matches) == 0 {
		doc.appendStanza(spec)
	} else {
		action = "updated"
		match := matches[0]
		line = match.block.SourceLine
		if len(match.block.Patterns) == 1 {
			doc.replaceBlockFields(match.block, spec)
		} else {
			if containsPattern(match.block.Patterns) {
				return MutationPlan{}, fmt.Errorf("alias %q is in a multi-pattern Host stanza with wildcard or negated patterns at %s:%d; edit that stanza manually", spec.Alias, path, match.block.SourceLine)
			}
			doc.splitAliasBlock(match.block, match.index, spec)
		}
	}

	newData := doc.Render()
	if err := validateRenderedDocument(path, newData); err != nil {
		return MutationPlan{}, err
	}

	return MutationPlan{
		Path:    path,
		Action:  action,
		Alias:   spec.Alias,
		Aliases: []string{spec.Alias},
		OldData: append([]byte(nil), doc.data...),
		NewData: newData,
		Changed: !bytes.Equal(doc.data, newData),
		Line:    line,
	}, nil
}

func PlanDeleteAlias(path string, alias string, opts DeleteOptions) (MutationPlan, error) {
	return PlanDeleteAliases(path, []string{alias}, opts)
}

func PlanDeleteAliases(path string, aliases []string, opts DeleteOptions) (MutationPlan, error) {
	path = filepath.Clean(path)
	doc, err := ReadDocument(path)
	if err != nil {
		return MutationPlan{}, err
	}

	seen := map[string]bool{}
	removed := make([]string, 0, len(aliases))
	notFound := make([]string, 0)
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true

		if err := validateAliasName(alias, opts.AllowPatterns); err != nil {
			return MutationPlan{}, err
		}

		changed, err := doc.deleteAlias(alias, opts)
		if err != nil {
			return MutationPlan{}, err
		}
		if changed {
			removed = append(removed, alias)
		} else {
			notFound = append(notFound, alias)
		}
	}

	newData := doc.Render()
	if len(removed) > 0 {
		if err := validateRenderedDocument(path, newData); err != nil {
			return MutationPlan{}, err
		}
	}

	action := "removed"
	if len(removed) == 0 {
		action = "unchanged"
	}
	return MutationPlan{
		Path:      path,
		Action:    action,
		Aliases:   removed,
		OldData:   append([]byte(nil), doc.data...),
		NewData:   newData,
		Changed:   !bytes.Equal(doc.data, newData),
		NotFound:  notFound,
		LineCount: len(removed),
	}, nil
}

func ExistingAliasSpec(path string, alias string) (AliasSpec, bool, error) {
	doc, err := ReadDocument(path)
	if err != nil {
		return AliasSpec{}, false, err
	}

	matches := doc.findExactAliasBlocks(alias)
	if len(matches) == 0 {
		return AliasSpec{}, false, nil
	}
	spec := doc.specFromBlock(matches[0].block, alias)
	return spec, true, nil
}

func FindAliasOccurrences(graph *Graph, alias string) []AliasOccurrence {
	if graph == nil {
		return nil
	}
	var occurrences []AliasOccurrence
	for _, block := range graph.Blocks {
		for _, pattern := range block.Patterns {
			if pattern == alias {
				occurrences = append(occurrences, AliasOccurrence{
					Path:        block.SourcePath,
					Line:        block.SourceLine,
					Patterns:    append([]string(nil), block.Patterns...),
					Conditional: block.Conditional,
				})
				break
			}
		}
	}
	return occurrences
}

func ValidateAliasSpec(spec AliasSpec, allowPattern bool) error {
	if err := validateAliasName(spec.Alias, allowPattern); err != nil {
		return err
	}
	if spec.HostName == "" {
		return errors.New("HostName is required")
	}
	for name, value := range map[string]string{
		"HostName":     spec.HostName,
		"User":         spec.User,
		"Port":         spec.Port,
		"IdentityFile": spec.IdentityFile,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s cannot contain a newline", name)
		}
	}
	if spec.Port != "" {
		port, err := strconv.Atoi(spec.Port)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("Port must be an integer from 1 to 65535")
		}
	}
	return nil
}

func validateAliasName(alias string, allowPattern bool) error {
	if alias == "" {
		return errors.New("alias is required")
	}
	if strings.ContainsAny(alias, " \t\r\n") {
		return errors.New("alias cannot contain whitespace")
	}
	if strings.ContainsAny(alias, "\x00") {
		return errors.New("alias cannot contain NUL")
	}
	if !allowPattern && (strings.HasPrefix(alias, "!") || strings.ContainsAny(alias, "*?")) {
		return errors.New("alias cannot be a wildcard or negated pattern")
	}
	return nil
}

func normalizeAliasSpec(spec AliasSpec) AliasSpec {
	spec.Alias = strings.TrimSpace(spec.Alias)
	spec.HostName = strings.TrimSpace(spec.HostName)
	spec.User = strings.TrimSpace(spec.User)
	spec.Port = strings.TrimSpace(spec.Port)
	spec.IdentityFile = strings.TrimSpace(spec.IdentityFile)
	return spec
}

func ReadDocument(path string) (*Document, error) {
	path = filepath.Clean(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			doc := &Document{
				Path:               path,
				Newline:            "\n",
				HadTrailingNewline: true,
				Missing:            true,
			}
			doc.reparse()
			return doc, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseDocument(path, data)
}

func ParseDocument(path string, data []byte) (*Document, error) {
	doc := &Document{
		Path:    filepath.Clean(path),
		data:    append([]byte(nil), data...),
		Newline: detectNewline(data),
	}

	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	doc.HadTrailingNewline = strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	if text != "" {
		doc.Lines = strings.Split(text, "\n")
	}
	doc.reparse()
	return doc, nil
}

func (d *Document) Render() []byte {
	if len(d.Lines) == 0 {
		return nil
	}
	text := strings.Join(d.Lines, d.Newline)
	if d.HadTrailingNewline {
		text += d.Newline
	}
	return []byte(text)
}

func (d *Document) appendStanza(spec AliasSpec) {
	if d.Missing || len(d.Lines) == 0 {
		d.Lines = []string{"# Created by ssherpa"}
	}
	if len(d.Lines) > 0 && strings.TrimSpace(d.Lines[len(d.Lines)-1]) != "" {
		d.Lines = append(d.Lines, "")
	}
	d.Lines = append(d.Lines, renderStanzaLines(spec)...)
	d.HadTrailingNewline = true
	d.Missing = false
	d.reparse()
}

func (d *Document) replaceBlockFields(block DocumentBlock, spec AliasSpec) {
	indent := d.blockIndent(block)
	managed := renderManagedLines(spec, indent)
	lines := make([]string, 0, len(d.Lines)+len(managed))
	lines = append(lines, d.Lines[:block.Start+1]...)

	inserted := false
	for i := block.Start + 1; i < block.End; i++ {
		parsed, err := parseLine(d.Lines[i])
		if err == nil && isManagedOption(parsed.Keyword) {
			if !inserted {
				lines = append(lines, managed...)
				inserted = true
			}
			continue
		}
		lines = append(lines, d.Lines[i])
	}
	if !inserted {
		lines = append(lines, managed...)
	}
	lines = append(lines, d.Lines[block.End:]...)
	d.Lines = lines
	d.HadTrailingNewline = true
	d.reparse()
}

func (d *Document) splitAliasBlock(block DocumentBlock, aliasIndex int, spec AliasSpec) {
	remaining := append([]string(nil), block.Patterns[:aliasIndex]...)
	remaining = append(remaining, block.Patterns[aliasIndex+1:]...)
	d.Lines[block.Start] = renderHostLine(d.Lines[block.Start], remaining)

	insert := make([]string, 0, len(renderStanzaLines(spec))+2)
	if block.End > 0 && strings.TrimSpace(d.Lines[block.End-1]) != "" {
		insert = append(insert, "")
	}
	insert = append(insert, renderStanzaLines(spec)...)
	if block.End < len(d.Lines) && strings.TrimSpace(d.Lines[block.End]) != "" {
		insert = append(insert, "")
	}
	lines := make([]string, 0, len(d.Lines)+len(insert))
	lines = append(lines, d.Lines[:block.End]...)
	lines = append(lines, insert...)
	lines = append(lines, d.Lines[block.End:]...)
	d.Lines = lines
	d.HadTrailingNewline = true
	d.reparse()
}

func (d *Document) deleteAlias(alias string, opts DeleteOptions) (bool, error) {
	changed := false
	for {
		matches := d.findExactAliasBlocks(alias)
		if len(matches) == 0 {
			break
		}
		match := matches[len(matches)-1]
		if containsPattern(match.block.Patterns) && !opts.AllowPatterns {
			return false, fmt.Errorf("alias %q is in a Host stanza with wildcard or negated patterns at %s:%d; use explicit pattern deletion or edit manually", alias, d.Path, match.block.SourceLine)
		}
		if len(match.block.Patterns) == 1 {
			start, end := d.removalRange(match.block)
			d.Lines = append(d.Lines[:start], d.Lines[end:]...)
			changed = true
			d.reparse()
			continue
		}
		remaining := append([]string(nil), match.block.Patterns[:match.index]...)
		remaining = append(remaining, match.block.Patterns[match.index+1:]...)
		d.Lines[match.block.Start] = renderHostLine(d.Lines[match.block.Start], remaining)
		changed = true
		d.reparse()
	}
	if !changed {
		return false, nil
	}
	d.HadTrailingNewline = true
	d.reparse()
	return true, nil
}

func (d *Document) removalRange(block DocumentBlock) (int, int) {
	start := block.Start
	end := block.End
	if start > 0 && strings.TrimSpace(d.Lines[start-1]) == "" {
		start--
	} else if end < len(d.Lines) && strings.TrimSpace(d.Lines[end]) == "" {
		end++
	}
	return start, end
}

func (d *Document) specFromBlock(block DocumentBlock, alias string) AliasSpec {
	spec := AliasSpec{Alias: alias}
	for i := block.Start + 1; i < block.End; i++ {
		parsed, err := parseLine(d.Lines[i])
		if err != nil || len(parsed.Values) == 0 {
			continue
		}
		value := parsed.Values[0]
		switch parsed.Keyword {
		case "hostname":
			if spec.HostName == "" {
				spec.HostName = value
			}
		case "user":
			if spec.User == "" {
				spec.User = value
			}
		case "port":
			if spec.Port == "" {
				spec.Port = value
			}
		case "identityfile":
			if spec.IdentityFile == "" {
				spec.IdentityFile = value
			}
		case "identitiesonly":
			if strings.EqualFold(value, "yes") || strings.EqualFold(value, "true") {
				spec.IdentitiesOnly = true
			}
		}
	}
	return spec
}

type aliasBlockMatch struct {
	block DocumentBlock
	index int
}

func (d *Document) findExactAliasBlocks(alias string) []aliasBlockMatch {
	var matches []aliasBlockMatch
	for _, block := range d.Blocks {
		for i, pattern := range block.Patterns {
			if pattern == alias {
				matches = append(matches, aliasBlockMatch{block: block, index: i})
			}
		}
	}
	return matches
}

func (d *Document) blockIndent(block DocumentBlock) string {
	for i := block.Start + 1; i < block.End; i++ {
		line := d.Lines[i]
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
	}
	return "  "
}

func (d *Document) reparse() {
	d.Blocks = d.Blocks[:0]
	section := sectionGlobal
	current := -1

	for i, raw := range d.Lines {
		line, err := parseLine(raw)
		if err != nil || line.Keyword == "" {
			continue
		}

		switch line.Keyword {
		case "host":
			if current >= 0 {
				d.Blocks[current].End = i
			}
			block := DocumentBlock{
				Start:      i,
				End:        len(d.Lines),
				SourceLine: i + 1,
				Patterns:   append([]string(nil), line.Values...),
			}
			d.Blocks = append(d.Blocks, block)
			current = len(d.Blocks) - 1
			section = sectionHost
		case "match":
			if current >= 0 {
				d.Blocks[current].End = i
				current = -1
			}
			section = sectionMatch
		default:
			_ = section
		}
	}
	if current >= 0 {
		d.Blocks[current].End = len(d.Lines)
	}
}

func renderStanzaLines(spec AliasSpec) []string {
	lines := []string{"Host " + renderValue(spec.Alias)}
	lines = append(lines, renderManagedLines(spec, "  ")...)
	return lines
}

func renderManagedLines(spec AliasSpec, indent string) []string {
	lines := []string{indent + "HostName " + renderValue(spec.HostName)}
	if spec.User != "" {
		lines = append(lines, indent+"User "+renderValue(spec.User))
	}
	if spec.Port != "" {
		lines = append(lines, indent+"Port "+renderValue(spec.Port))
	}
	if spec.IdentityFile != "" {
		lines = append(lines, indent+"IdentityFile "+renderValue(spec.IdentityFile))
	}
	if spec.IdentitiesOnly {
		lines = append(lines, indent+"IdentitiesOnly yes")
	}
	return lines
}

func renderHostLine(original string, patterns []string) string {
	leading := original[:len(original)-len(strings.TrimLeft(original, " \t"))]
	_, comment := splitInlineComment(original)
	var b strings.Builder
	b.WriteString(leading)
	b.WriteString("Host")
	for _, pattern := range patterns {
		b.WriteByte(' ')
		b.WriteString(renderValue(pattern))
	}
	if comment != "" {
		b.WriteByte(' ')
		b.WriteString(comment)
	}
	return b.String()
}

func splitInlineComment(line string) (string, string) {
	inQuote := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		switch {
		case r == '\\' && inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
		case r == '#' && !inQuote:
			return strings.TrimRight(line[:i], " \t"), line[i:]
		}
	}
	return strings.TrimRight(line, " \t"), ""
}

func renderValue(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t#\"\\") {
		return value
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func isManagedOption(keyword string) bool {
	switch keyword {
	case "hostname", "user", "port", "identityfile", "identitiesonly":
		return true
	default:
		return false
	}
}

func containsPattern(patterns []string) bool {
	for _, pattern := range patterns {
		if strings.HasPrefix(pattern, "!") || strings.ContainsAny(pattern, "*?") {
			return true
		}
	}
	return false
}

func detectNewline(data []byte) string {
	if bytes.Contains(data, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func validateRenderedDocument(path string, data []byte) error {
	doc, err := ParseDocument(path, data)
	if err != nil {
		return err
	}
	for _, line := range doc.Lines {
		if _, err := parseLine(line); err != nil {
			return fmt.Errorf("rendered config does not parse: %w", err)
		}
	}
	return nil
}
