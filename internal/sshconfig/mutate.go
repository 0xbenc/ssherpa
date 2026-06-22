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
	// ProxyJump is the ssh ProxyJump option (chain of hosts to relay
	// through). Added in Phase 2e of the `forward` feature so the
	// builder's "Save as alias" action can persist a destination-only
	// Host block whose ProxyJump captures the jump-hop choice.
	// Existing alias specs leave this empty and behave as before.
	ProxyJump string
	// ForcePassword writes PubkeyAuthentication no and
	// PreferredAuthentications keyboard-interactive,password so the alias
	// logs in with a password instead of a key. It is mutually exclusive
	// with IdentityFile/IdentitiesOnly (ValidateAliasSpec enforces this):
	// forcing password auth and pinning an identity contradict each other.
	ForcePassword bool
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

	matches := doc.findAliasBlocks(spec.Alias)
	if len(matches) > 1 {
		return MutationPlan{}, fmt.Errorf("alias %q appears %d times in %s; delete duplicates or choose one source before updating", spec.Alias, len(matches), path)
	}

	action := "added"
	line := len(doc.Lines) + 1
	if len(matches) == 0 {
		// Policy: brand-new aliases are written lowercase. Existing
		// stanzas keep their original casing (below): OpenSSH Host
		// matching is case-sensitive, so rewriting the pattern's case
		// would orphan the name the user already connects with.
		spec.Alias = strings.ToLower(spec.Alias)
		doc.appendStanza(spec)
	} else {
		action = "updated"
		match := matches[0]
		// Update the stanza we matched and adopt its original pattern
		// casing; never append a near-duplicate sibling stanza.
		spec.Alias = match.block.Patterns[match.index]
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

// PlanRenameAndUpdate updates the stanza currently named oldAlias, applying
// spec's managed fields and — when spec.Alias differs from oldAlias — renaming
// the Host pattern in place. Unmanaged options and comments are preserved.
// When the name is unchanged it behaves exactly like the update branch of
// PlanAddOrUpdate. Renaming to a name that already exists in this file is an
// error (the caller is responsible for cross-file conflict checks).
func PlanRenameAndUpdate(path string, oldAlias string, spec AliasSpec) (MutationPlan, error) {
	path = filepath.Clean(path)
	spec = normalizeAliasSpec(spec)
	if err := ValidateAliasSpec(spec, false); err != nil {
		return MutationPlan{}, err
	}
	oldAlias = strings.TrimSpace(oldAlias)

	doc, err := ReadDocument(path)
	if err != nil {
		return MutationPlan{}, err
	}

	matches := doc.findAliasBlocks(oldAlias)
	if len(matches) == 0 {
		return MutationPlan{}, fmt.Errorf("alias %q not found in %s", oldAlias, path)
	}
	if len(matches) > 1 {
		return MutationPlan{}, fmt.Errorf("alias %q appears %d times in %s; resolve duplicates before editing", oldAlias, len(matches), path)
	}
	match := matches[0]

	renaming := spec.Alias != match.block.Patterns[match.index]
	if renaming {
		// The new name must not already exist as a DIFFERENT stanza/pattern in
		// this file. A case-only rename (e.g. prod -> Prod) folds back to the
		// same occurrence we are editing, which is not a conflict.
		for _, other := range doc.findAliasBlocks(spec.Alias) {
			if other.block.SourceLine != match.block.SourceLine || other.index != match.index {
				return MutationPlan{}, fmt.Errorf("alias %q already exists in %s; choose another name", spec.Alias, path)
			}
		}
	}

	action := "updated"
	line := match.block.SourceLine
	if len(match.block.Patterns) == 1 {
		if renaming {
			// Rewrite just the Host line; replaceBlockFields keeps it as-is.
			doc.Lines[match.block.Start] = renderHostLine(doc.Lines[match.block.Start], []string{spec.Alias})
			action = "renamed"
		}
		doc.replaceBlockFields(match.block, spec)
	} else {
		if containsPattern(match.block.Patterns) {
			return MutationPlan{}, fmt.Errorf("alias %q is in a multi-pattern Host stanza with wildcard or negated patterns at %s:%d; edit that stanza manually", oldAlias, path, match.block.SourceLine)
		}
		// Split this alias out under its (possibly new) name; the remaining
		// patterns keep the original stanza and its options.
		doc.splitAliasBlock(match.block, match.index, spec)
		if renaming {
			action = "renamed"
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
	var warnings []string
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || seen[alias] {
			continue
		}
		seen[alias] = true

		if err := validateAliasName(alias, opts.AllowPatterns); err != nil {
			return MutationPlan{}, err
		}

		changed, removedNames, aliasWarnings, err := doc.deleteAlias(alias, opts)
		if err != nil {
			return MutationPlan{}, err
		}
		warnings = append(warnings, aliasWarnings...)
		if changed {
			removed = append(removed, alias)
			// Case-insensitive matching may resolve to a stanza whose
			// pattern casing differs from the requested name; report
			// the resolved names too so callers (catalog cleanup,
			// messaging) operate on what was actually removed.
			for _, name := range removedNames {
				if name != alias && !seen[name] {
					seen[name] = true
					removed = append(removed, name)
				}
			}
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
		Warnings:  warnings,
		NotFound:  notFound,
		LineCount: len(removed),
	}, nil
}

func ExistingAliasSpec(path string, alias string) (AliasSpec, bool, error) {
	doc, err := ReadDocument(path)
	if err != nil {
		return AliasSpec{}, false, err
	}

	matches := doc.findAliasBlocks(alias)
	if len(matches) == 0 {
		return AliasSpec{}, false, nil
	}
	match := matches[0]
	spec := doc.specFromBlock(match.block, match.block.Patterns[match.index])
	return spec, true, nil
}

func FindAliasOccurrences(graph *Graph, alias string) []AliasOccurrence {
	if graph == nil {
		return nil
	}
	// Exact-case matches win; when none exist, fall back to
	// case-insensitive matches so callers resolve the same stanza the
	// mutation planner will edit (and never plan against a near-duplicate).
	var exact, folded []AliasOccurrence
	for _, block := range graph.Blocks {
		isExact := false
		isFolded := false
		for _, pattern := range block.Patterns {
			if pattern == alias {
				isExact = true
				break
			}
			if strings.EqualFold(pattern, alias) {
				isFolded = true
			}
		}
		if !isExact && !isFolded {
			continue
		}
		occurrence := AliasOccurrence{
			Path:        block.SourcePath,
			Line:        block.SourceLine,
			Patterns:    append([]string(nil), block.Patterns...),
			Conditional: block.Conditional,
		}
		if isExact {
			exact = append(exact, occurrence)
		} else {
			folded = append(folded, occurrence)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return folded
}

func ValidateAliasSpec(spec AliasSpec, allowPattern bool) error {
	if err := validateAliasName(spec.Alias, allowPattern); err != nil {
		return err
	}
	// ssh rejects quote and backslash characters in destination names
	// outright ("hostname contains invalid characters", verified against
	// OpenSSH 10.2), so an alias containing them could be written but
	// never used. Refuse it here with a clear error. Deletion is not
	// gated on this so legacy stanzas can still be cleaned up.
	if strings.ContainsAny(spec.Alias, `'"\`) {
		return errors.New("alias cannot contain quote or backslash characters; ssh rejects them in destination names")
	}
	if spec.HostName == "" {
		return errors.New("HostName is required")
	}
	// Force-password auth disables key auth, so pinning an identity (or
	// IdentitiesOnly, which only narrows which key is offered) is
	// contradictory. Reject the combination centrally so every path —
	// the add form, `edit set`, CLI flags, and bundle import — is covered.
	if spec.ForcePassword && (spec.IdentityFile != "" || spec.IdentitiesOnly) {
		return errors.New("ForcePassword cannot be combined with an identity file or IdentitiesOnly")
	}
	for name, value := range map[string]string{
		"HostName":     spec.HostName,
		"User":         spec.User,
		"Port":         spec.Port,
		"IdentityFile": spec.IdentityFile,
		"ProxyJump":    spec.ProxyJump,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s cannot contain a newline", name)
		}
		// Control characters are unrepresentable in ssh_config: a NUL
		// truncates the line for ssh's parser (leaving an unbalanced
		// quote that fatals every invocation) and vertical-tab/form-feed
		// split tokens. Refuse instead of writing a broken config.
		if containsControlRune(value) {
			return fmt.Errorf("%s cannot contain control characters", name)
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
	if containsControlRune(alias) {
		return errors.New("alias cannot contain control characters")
	}
	if strings.HasPrefix(alias, "-") {
		// A leading dash makes OpenSSH parse the alias as an option, so
		// ssherpa filters such hosts from its inventory and refuses to
		// connect to them — writing one would author a stanza ssherpa
		// then silently drops from its own host list.
		return errors.New("alias cannot begin with '-' (it would be parsed as an ssh option)")
	}
	if !allowPattern && (strings.HasPrefix(alias, "!") || strings.ContainsAny(alias, "*?")) {
		return errors.New("alias cannot be a wildcard or negated pattern")
	}
	return nil
}

func normalizeAliasSpec(spec AliasSpec) AliasSpec {
	// Casing is intentionally preserved here: lookups must see the name
	// as given so an edit of "Host Prod" updates that stanza instead of
	// appending a lowercase sibling. PlanAddOrUpdate lowercases only
	// genuinely new aliases (policy) and adopts the matched stanza's
	// casing on update.
	spec.Alias = strings.TrimSpace(spec.Alias)
	spec.HostName = strings.TrimSpace(spec.HostName)
	spec.User = strings.TrimSpace(spec.User)
	spec.Port = strings.TrimSpace(spec.Port)
	spec.IdentityFile = strings.TrimSpace(spec.IdentityFile)
	spec.ProxyJump = strings.TrimSpace(spec.ProxyJump)
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
	body := d.renderBlockBody(block, spec)
	lines := make([]string, 0, block.Start+1+len(body)+len(d.Lines)-block.End)
	lines = append(lines, d.Lines[:block.Start+1]...)
	lines = append(lines, body...)
	lines = append(lines, d.Lines[block.End:]...)
	d.Lines = lines
	d.HadTrailingNewline = true
	d.reparse()
}

func (d *Document) splitAliasBlock(block DocumentBlock, aliasIndex int, spec AliasSpec) {
	remaining := append([]string(nil), block.Patterns[:aliasIndex]...)
	remaining = append(remaining, block.Patterns[aliasIndex+1:]...)
	d.Lines[block.Start] = renderHostLine(d.Lines[block.Start], remaining)

	// Carry the stanza's whole body — unmanaged options included — into
	// the split-out stanza, so editing one alias out of a shared stanza
	// cannot silently drop ForwardAgent, LocalForward, etc. for it.
	stanza := append([]string{"Host " + renderValue(spec.Alias)}, d.renderBlockBody(block, spec)...)
	insert := make([]string, 0, len(stanza)+2)
	if block.End > 0 && strings.TrimSpace(d.Lines[block.End-1]) != "" {
		insert = append(insert, "")
	}
	insert = append(insert, stanza...)
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

// renderBlockBody returns block's body lines with the managed options
// replaced by spec's values. Unmanaged options, comments, and unparseable
// lines are preserved verbatim. When spec keeps the stanza's first
// IdentityFile value, every existing IdentityFile line is preserved as-is:
// IdentityFile accumulates in OpenSSH, so an edit that does not change
// identities must not collapse multiple identities into one.
func (d *Document) renderBlockBody(block DocumentBlock, spec AliasSpec) []string {
	indent := d.blockIndent(block)
	preserveIdentities := spec.IdentityFile != "" && spec.IdentityFile == d.firstIdentityFile(block)
	managed := renderManagedLines(spec, indent, preserveIdentities)
	body := make([]string, 0, block.End-block.Start-1+len(managed))
	inserted := false
	for i := block.Start + 1; i < block.End; i++ {
		parsed, err := parseLine(d.Lines[i])
		if err == nil && isManagedOption(parsed.Keyword) {
			if preserveIdentities && parsed.Keyword == "identityfile" {
				body = append(body, d.Lines[i])
				continue
			}
			if !inserted {
				body = append(body, managed...)
				inserted = true
			}
			continue
		}
		body = append(body, d.Lines[i])
	}
	if !inserted {
		body = append(body, managed...)
	}
	return body
}

func (d *Document) firstIdentityFile(block DocumentBlock) string {
	for i := block.Start + 1; i < block.End; i++ {
		parsed, err := parseLine(d.Lines[i])
		if err == nil && parsed.Keyword == "identityfile" && len(parsed.Values) > 0 {
			return parsed.Values[0]
		}
	}
	return ""
}

func (d *Document) deleteAlias(alias string, opts DeleteOptions) (bool, []string, []string, error) {
	changed := false
	var warnings []string
	var removedNames []string
	// Decide the match mode once: exact-case matches win, and the
	// case-insensitive fallback never widens a delete that already
	// matched exactly.
	fold := len(d.matchAliasBlocks(alias, false)) == 0
	if fold {
		seen := map[string]struct{}{}
		var casings []string
		for _, m := range d.matchAliasBlocks(alias, true) {
			name := m.block.Patterns[m.index]
			if _, ok := seen[name]; !ok {
				seen[name] = struct{}{}
				casings = append(casings, name)
			}
		}
		if len(casings) > 1 {
			return false, nil, nil, fmt.Errorf("alias %q matches multiple stanzas with different casings (%s) in %s; delete each casing explicitly", alias, strings.Join(casings, ", "), d.Path)
		}
	}
	for {
		matches := d.matchAliasBlocks(alias, fold)
		if len(matches) == 0 {
			break
		}
		match := matches[len(matches)-1]
		if containsPattern(match.block.Patterns) && !opts.AllowPatterns {
			return false, nil, nil, fmt.Errorf("alias %q is in a Host stanza with wildcard or negated patterns at %s:%d; use explicit pattern deletion or edit manually", alias, d.Path, match.block.SourceLine)
		}
		removedNames = append(removedNames, match.block.Patterns[match.index])
		if len(match.block.Patterns) == 1 {
			start, end := d.removalRange(match.block)
			for i := start; i < end; i++ {
				parsed, err := parseLine(d.Lines[i])
				if err == nil && parsed.Keyword == "include" {
					warnings = append(warnings, fmt.Sprintf("deleting alias %q also removes %q (%s:%d)", alias, strings.TrimSpace(d.Lines[i]), d.Path, i+1))
				}
			}
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
		return false, nil, warnings, nil
	}
	d.HadTrailingNewline = true
	d.reparse()
	return true, removedNames, warnings, nil
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
		case "proxyjump":
			if spec.ProxyJump == "" {
				spec.ProxyJump = value
			}
		case "pubkeyauthentication":
			// PubkeyAuthentication no is the load-bearing signal that key
			// auth is disabled; PreferredAuthentications is secondary, so
			// keying on this alone is the robust detection. A re-render then
			// normalizes the stanza to the canonical directive pair.
			if strings.EqualFold(value, "no") {
				spec.ForcePassword = true
			}
		}
	}
	if spec.ForcePassword {
		// Force-password wins over any (inconsistent) identity directives in
		// the same stanza, keeping the loaded spec valid for ValidateAliasSpec.
		spec.IdentityFile = ""
		spec.IdentitiesOnly = false
	}
	return spec
}

type aliasBlockMatch struct {
	block DocumentBlock
	index int
}

// findAliasBlocks returns the blocks whose Host patterns name alias.
// Exact-case matches win; when none exist, case-insensitive matches are
// returned instead so a lookup by differently-cased name still resolves the
// stanza ssh keeps (OpenSSH Host matching is case-sensitive, so updating the
// matched stanza — never appending a near-duplicate sibling — is the only
// safe edit).
func (d *Document) findAliasBlocks(alias string) []aliasBlockMatch {
	if exact := d.matchAliasBlocks(alias, false); len(exact) > 0 {
		return exact
	}
	return d.matchAliasBlocks(alias, true)
}

func (d *Document) matchAliasBlocks(alias string, fold bool) []aliasBlockMatch {
	var matches []aliasBlockMatch
	for _, block := range d.Blocks {
		for i, pattern := range block.Patterns {
			ok := pattern == alias
			if fold {
				ok = strings.EqualFold(pattern, alias)
			}
			if ok {
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
	for i := range d.Blocks {
		d.Blocks[i].End = d.tightenBlockEnd(d.Blocks[i])
	}
}

// tightenBlockEnd excludes trailing lines that do not belong to the stanza —
// blank lines, comments (which usually document the NEXT stanza or the file
// tail), Include directives (whose hosts live beyond this block), and
// anything unparseable — so deleting a stanza can never remove unrelated
// content.
func (d *Document) tightenBlockEnd(block DocumentBlock) int {
	end := block.End
	for end > block.Start+1 {
		line, err := parseLine(d.Lines[end-1])
		if err == nil && line.Keyword != "" && line.Keyword != "include" {
			break
		}
		end--
	}
	return end
}

func renderStanzaLines(spec AliasSpec) []string {
	lines := []string{"Host " + renderValue(spec.Alias)}
	lines = append(lines, renderManagedLines(spec, "  ", false)...)
	return lines
}

// renderManagedLines renders the managed options for spec. omitIdentity
// suppresses the IdentityFile line for callers that preserve a stanza's
// existing (possibly multiple) IdentityFile lines verbatim.
func renderManagedLines(spec AliasSpec, indent string, omitIdentity bool) []string {
	lines := []string{indent + "HostName " + renderValue(spec.HostName)}
	if spec.User != "" {
		lines = append(lines, indent+"User "+renderValue(spec.User))
	}
	if spec.Port != "" {
		lines = append(lines, indent+"Port "+renderValue(spec.Port))
	}
	// ForcePassword and an identity are mutually exclusive (ValidateAliasSpec
	// enforces this), so emit one auth block or the other. The directives are
	// constants — keyboard-interactive,password has no whitespace/quote/comment
	// characters, so they render verbatim without renderValue.
	if spec.ForcePassword {
		lines = append(lines, indent+"PubkeyAuthentication no")
		lines = append(lines, indent+"PreferredAuthentications keyboard-interactive,password")
	} else {
		if spec.IdentityFile != "" && !omitIdentity {
			lines = append(lines, indent+"IdentityFile "+renderValue(spec.IdentityFile))
		}
		if spec.IdentitiesOnly {
			lines = append(lines, indent+"IdentitiesOnly yes")
		}
	}
	if spec.ProxyJump != "" {
		lines = append(lines, indent+"ProxyJump "+renderValue(spec.ProxyJump))
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
	var quote rune
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		switch {
		case r == '\\' && quote != 0:
			escaped = true
		case r == '"' || r == '\'':
			switch quote {
			case 0:
				quote = r
			case r:
				quote = 0
			}
		case r == '#' && quote == 0:
			return strings.TrimRight(line[:i], " \t"), line[i:]
		}
	}
	return strings.TrimRight(line, " \t"), ""
}

// renderValue quotes values containing whitespace, comment, or quote
// characters. Double quotes with backslash escapes are the form modern
// OpenSSH (argv_split, >= 8.7) parses for every such value — including bare
// single quotes, which MUST be quoted: an unquoted apostrophe makes ssh
// fatal with "invalid quotes" for every host in the file. Verified against
// OpenSSH 10.2: `User "o'br\"ien"` and `IdentityFile "a\\b"` round-trip to
// the intended values; bare `o'brien` terminates ssh.
func renderValue(value string) string {
	if value == "" {
		return `""`
	}
	if !strings.ContainsAny(value, " \t#\"\\'") {
		return value
	}
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return `"` + value + `"`
}

func isManagedOption(keyword string) bool {
	switch keyword {
	case "hostname", "user", "port", "identityfile", "identitiesonly", "proxyjump",
		// PubkeyAuthentication and PreferredAuthentications back the
		// ForcePassword option. They MUST be managed: otherwise toggling
		// force-password off on an edit would leave the directives behind
		// (silently still forcing password), and toggling it on would append
		// a duplicate next to a pre-existing line. The cost is that a
		// hand-written value on these keywords is normalized when ssherpa
		// next edits the alias.
		"pubkeyauthentication", "preferredauthentications":
		return true
	default:
		return false
	}
}

func containsControlRune(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
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
