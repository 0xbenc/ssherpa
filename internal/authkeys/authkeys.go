package authkeys

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
)

const (
	DefaultFileMode = 0o600
	DefaultDirMode  = 0o700
)

var ErrNoKey = errors.New("line does not contain an authorized key")

type AuthorizedKey struct {
	Options string `json:"options,omitempty"`
	Type    string `json:"type"`
	Blob    string `json:"blob"`
	Comment string `json:"comment,omitempty"`
	Source  string `json:"source,omitempty"`
	Line    int    `json:"line,omitempty"`
}

func (k AuthorizedKey) Identity() string {
	return k.Type + " " + k.Blob
}

func (k AuthorizedKey) Render() string {
	var b strings.Builder
	if k.Options != "" {
		b.WriteString(k.Options)
		b.WriteByte(' ')
	}
	b.WriteString(k.Type)
	b.WriteByte(' ')
	b.WriteString(k.Blob)
	if k.Comment != "" {
		b.WriteByte(' ')
		b.WriteString(k.Comment)
	}
	return b.String()
}

func (k AuthorizedKey) SHA256Fingerprint() (string, error) {
	blob, err := decodeBlob(k.Blob)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]), nil
}

type Line struct {
	Raw string
	Key *AuthorizedKey
}

type Diagnostic struct {
	Path     string `json:"path,omitempty"`
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type Document struct {
	Path               string
	Lines              []Line
	Newline            string
	HadTrailingNewline bool
	Missing            bool
	Diagnostics        []Diagnostic
	data               []byte
}

func ReadDocument(path string) (Document, error) {
	path = filepath.Clean(path)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Document{
				Path:    path,
				Newline: "\n",
				Missing: true,
			}, nil
		}
		return Document{}, fmt.Errorf("read %s: %w", path, err)
	}
	return ParseDocument(path, data), nil
}

func ParseDocument(path string, data []byte) Document {
	lines, newline, trailing := splitLines(data)
	doc := Document{
		Path:               filepath.Clean(path),
		Newline:            newline,
		HadTrailingNewline: trailing,
		data:               append([]byte(nil), data...),
	}
	for i, raw := range lines {
		key, err := ParsePublicKeyLine(raw)
		line := Line{Raw: raw}
		switch {
		case err == nil:
			key.Source = doc.Path
			key.Line = i + 1
			line.Key = &key
		case errors.Is(err, ErrNoKey):
			// Comments and blank lines are preserved but are not diagnostics.
		default:
			doc.Diagnostics = append(doc.Diagnostics, Diagnostic{
				Path:     doc.Path,
				Line:     i + 1,
				Severity: "warning",
				Message:  err.Error(),
			})
		}
		doc.Lines = append(doc.Lines, line)
	}
	return doc
}

func (d Document) OldData() []byte {
	return append([]byte(nil), d.data...)
}

func (d Document) Keys() []AuthorizedKey {
	var keys []AuthorizedKey
	for _, line := range d.Lines {
		if line.Key != nil {
			keys = append(keys, *line.Key)
		}
	}
	return keys
}

func (d Document) RawLines() []string {
	lines := make([]string, 0, len(d.Lines))
	for _, line := range d.Lines {
		lines = append(lines, line.Raw)
	}
	return lines
}

type Validator struct {
	SSHKeygenPath string
	SkipSSHKeygen bool
}

type ValidationResult struct {
	UsedSSHKeygen  bool
	StructuralOnly bool
}

func (v Validator) Validate(key AuthorizedKey) (ValidationResult, error) {
	if err := ValidateStructural(key); err != nil {
		return ValidationResult{}, err
	}
	if v.SkipSSHKeygen {
		return ValidationResult{StructuralOnly: true}, nil
	}

	path := v.SSHKeygenPath
	if path == "" {
		found, err := exec.LookPath("ssh-keygen")
		if err != nil {
			return ValidationResult{StructuralOnly: true}, nil
		}
		path = found
	} else if err := sshcmd.ValidateBinary(sshcmd.BinaryRequirement{
		Name:    "ssh-keygen",
		Role:    "ssh-keygen",
		Program: path,
		Flag:    "--ssh-keygen",
		Hint:    sshcmd.SSHKeygenInstallHint,
	}); err != nil {
		return ValidationResult{}, err
	}

	cmd := exec.Command(path, "-lf", "-")
	cmd.Stdin = strings.NewReader(key.Render() + "\n")
	output, err := cmd.CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return ValidationResult{UsedSSHKeygen: true}, fmt.Errorf("ssh-keygen rejected key: %s", message)
	}
	return ValidationResult{UsedSSHKeygen: true}, nil
}

func ValidateStructural(key AuthorizedKey) error {
	if !IsSupportedKeyType(key.Type) {
		return fmt.Errorf("unsupported SSH public key type %q", key.Type)
	}
	if strings.TrimSpace(key.Blob) == "" {
		return errors.New("SSH public key blob is required")
	}
	if strings.ContainsAny(key.Blob, " \t\r\n") {
		return errors.New("SSH public key blob cannot contain whitespace")
	}
	if _, err := decodeBlob(key.Blob); err != nil {
		return fmt.Errorf("SSH public key blob is not valid base64: %w", err)
	}
	return nil
}

func ParsePublicKeyLine(line string) (AuthorizedKey, error) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return AuthorizedKey{}, ErrNoKey
	}
	trimmed := strings.TrimLeft(line, " \t")
	if strings.HasPrefix(trimmed, "#") {
		return AuthorizedKey{}, ErrNoKey
	}

	fields := scanFields(line)
	if len(fields) < 2 {
		return AuthorizedKey{}, fmt.Errorf("not a recognized SSH public key line")
	}

	keyIndex := -1
	for i, field := range fields {
		if IsSupportedKeyType(field.Value) {
			keyIndex = i
			break
		}
	}
	if keyIndex < 0 {
		return AuthorizedKey{}, fmt.Errorf("not a recognized SSH public key line")
	}
	if keyIndex+1 >= len(fields) {
		return AuthorizedKey{}, fmt.Errorf("SSH public key line is missing key data")
	}

	options := ""
	if keyIndex > 0 {
		options = strings.TrimSpace(line[fields[0].Start:fields[keyIndex].Start])
	}

	comment := strings.TrimLeft(line[fields[keyIndex+1].End:], " \t")
	comment = strings.TrimRight(comment, "\r")

	key := AuthorizedKey{
		Options: options,
		Type:    fields[keyIndex].Value,
		Blob:    fields[keyIndex+1].Value,
		Comment: comment,
	}
	if err := ValidateStructural(key); err != nil {
		return AuthorizedKey{}, err
	}
	return key, nil
}

type field struct {
	Value string
	Start int
	End   int
}

func scanFields(line string) []field {
	var fields []field
	for i := 0; i < len(line); {
		for i < len(line) && isSpace(line[i]) {
			i++
		}
		if i >= len(line) {
			break
		}

		start := i
		inQuote := false
		escaped := false
		for i < len(line) {
			c := line[i]
			if inQuote {
				switch {
				case escaped:
					escaped = false
				case c == '\\':
					escaped = true
				case c == '"':
					inQuote = false
				}
				i++
				continue
			}
			if c == '"' {
				inQuote = true
				i++
				continue
			}
			if isSpace(c) {
				break
			}
			i++
		}
		fields = append(fields, field{Value: line[start:i], Start: start, End: i})
	}
	return fields
}

func IsSupportedKeyType(value string) bool {
	switch value {
	case "ssh-ed25519",
		"ssh-ed25519-cert-v01@openssh.com",
		"ssh-rsa",
		"ssh-rsa-cert-v01@openssh.com",
		"rsa-sha2-256",
		"rsa-sha2-512",
		"sk-ssh-ed25519@openssh.com",
		"sk-ssh-ed25519-cert-v01@openssh.com",
		"sk-ecdsa-sha2-nistp256@openssh.com",
		"sk-ecdsa-sha2-nistp256-cert-v01@openssh.com":
		return true
	}
	if strings.HasPrefix(value, "ecdsa-sha2-") {
		return true
	}
	return false
}

type ImportStats struct {
	Files          int `json:"files"`
	Valid          int `json:"valid"`
	Invalid        int `json:"invalid"`
	Duplicate      int `json:"duplicate"`
	AlreadyPresent int `json:"already_present"`
	Ignored        int `json:"ignored"`
	Added          int `json:"added"`
	Deleted        int `json:"deleted"`
}

type ImportResult struct {
	Keys        []AuthorizedKey
	Stats       ImportStats
	Diagnostics []Diagnostic
}

func CollectFromDir(dir string, validator Validator) (ImportResult, error) {
	dir = filepath.Clean(dir)
	files, err := importFiles(dir)
	if err != nil {
		return ImportResult{}, err
	}

	result := ImportResult{Stats: ImportStats{Files: len(files)}}
	seen := map[string]AuthorizedKey{}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return result, fmt.Errorf("read %s: %w", path, err)
		}
		lines, _, _ := splitLines(data)
		for i, raw := range lines {
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				result.Stats.Ignored++
				continue
			}

			key, err := ParsePublicKeyLine(raw)
			if err != nil {
				result.Stats.Invalid++
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Path:     path,
					Line:     i + 1,
					Severity: "warning",
					Message:  err.Error(),
				})
				continue
			}
			key.Source = path
			key.Line = i + 1
			if _, err := validator.Validate(key); err != nil {
				result.Stats.Invalid++
				result.Diagnostics = append(result.Diagnostics, Diagnostic{
					Path:     path,
					Line:     i + 1,
					Severity: "warning",
					Message:  err.Error(),
				})
				continue
			}

			identity := key.Identity()
			if existing, ok := seen[identity]; ok {
				result.Stats.Duplicate++
				if existing.Options != key.Options {
					result.Diagnostics = append(result.Diagnostics, duplicateOptionsDiagnostic(key, existing))
				}
				continue
			}

			seen[identity] = key
			result.Keys = append(result.Keys, key)
			result.Stats.Valid++
		}
	}
	return result, nil
}

func ParseFirstKey(data []byte, source string, validator Validator) (AuthorizedKey, []Diagnostic, error) {
	lines, _, _ := splitLines(data)
	var diagnostics []Diagnostic
	for i, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		key, err := ParsePublicKeyLine(raw)
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Path:     source,
				Line:     i + 1,
				Severity: "warning",
				Message:  err.Error(),
			})
			continue
		}
		key.Source = source
		key.Line = i + 1
		if _, err := validator.Validate(key); err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Path:     source,
				Line:     i + 1,
				Severity: "warning",
				Message:  err.Error(),
			})
			continue
		}
		return key, diagnostics, nil
	}
	if len(diagnostics) > 0 {
		return AuthorizedKey{}, diagnostics, fmt.Errorf("no valid SSH public key found in %s", source)
	}
	return AuthorizedKey{}, nil, fmt.Errorf("no SSH public key found in %s", source)
}

func importFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}

	special := filepath.Join(dir, "authorized_keys")
	if stat, err := os.Stat(special); err == nil && stat.IsDir() {
		entries, err := os.ReadDir(special)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", special, err)
		}
		var files []string
		for _, entry := range entries {
			if entry.Type().IsRegular() {
				files = append(files, filepath.Join(special, entry.Name()))
			}
		}
		sort.Strings(files)
		if len(files) == 0 {
			return nil, fmt.Errorf("no files found in %s", special)
		}
		return files, nil
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.pub"))
	if err != nil {
		return nil, fmt.Errorf("scan %s/*.pub: %w", dir, err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("no authorized_keys directory or *.pub files found in %s", dir)
	}
	return files, nil
}

type PlanOptions struct {
	Validator Validator
}

type Plan struct {
	Path        string
	Action      string
	Target      string
	OldData     []byte
	NewData     []byte
	Changed     bool
	Stats       ImportStats
	Diagnostics []Diagnostic
	Keys        []AuthorizedKey
	NotFound    []string
}

func PlanAdd(path string, key AuthorizedKey, opts PlanOptions) (Plan, error) {
	if _, err := opts.Validator.Validate(key); err != nil {
		return Plan{}, err
	}

	doc, err := ReadDocument(path)
	if err != nil {
		return Plan{}, err
	}

	plan := basePlan(doc, "added", "1 key")
	plan.Keys = []AuthorizedKey{key}
	existing := existingByIdentity(doc)
	if found, ok := existing[key.Identity()]; ok {
		plan.Action = "unchanged"
		plan.Stats.AlreadyPresent = 1
		if found.Options != key.Options {
			plan.Diagnostics = append(plan.Diagnostics, duplicateOptionsDiagnostic(key, found))
		}
		plan.NewData = append([]byte(nil), doc.data...)
		return finishPlan(plan), nil
	}

	plan.Stats.Valid = 1
	plan.Stats.Added = 1
	plan.NewData = renderAppend(doc, []AuthorizedKey{key})
	return finishPlan(plan), nil
}

func PlanMerge(path string, dir string, opts PlanOptions) (Plan, error) {
	imported, err := CollectFromDir(dir, opts.Validator)
	if err != nil {
		return Plan{}, err
	}
	doc, err := ReadDocument(path)
	if err != nil {
		return Plan{}, err
	}

	plan := basePlan(doc, "merged", dir)
	plan.Stats = imported.Stats
	plan.Diagnostics = append(plan.Diagnostics, imported.Diagnostics...)

	existing := existingByIdentity(doc)
	var add []AuthorizedKey
	for _, key := range imported.Keys {
		if found, ok := existing[key.Identity()]; ok {
			plan.Stats.AlreadyPresent++
			if found.Options != key.Options {
				plan.Diagnostics = append(plan.Diagnostics, duplicateOptionsDiagnostic(key, found))
			}
			continue
		}
		add = append(add, key)
		existing[key.Identity()] = key
	}

	plan.Keys = add
	plan.Stats.Added = len(add)
	if len(add) == 0 {
		plan.Action = "unchanged"
		plan.NewData = append([]byte(nil), doc.data...)
		return finishPlan(plan), nil
	}

	plan.NewData = renderAppend(doc, add)
	return finishPlan(plan), nil
}

func PlanReplace(path string, dir string, opts PlanOptions) (Plan, error) {
	imported, err := CollectFromDir(dir, opts.Validator)
	if err != nil {
		return Plan{}, err
	}
	if len(imported.Keys) == 0 {
		return Plan{
			Path:        filepath.Clean(path),
			Action:      "unchanged",
			Target:      dir,
			Stats:       imported.Stats,
			Diagnostics: imported.Diagnostics,
		}, fmt.Errorf("no valid SSH public keys found in %s", filepath.Clean(dir))
	}
	doc, err := ReadDocument(path)
	if err != nil {
		return Plan{}, err
	}

	plan := basePlan(doc, "replaced", dir)
	plan.Stats = imported.Stats
	plan.Stats.Added = len(imported.Keys)
	plan.Diagnostics = append(plan.Diagnostics, imported.Diagnostics...)
	plan.Keys = append([]AuthorizedKey(nil), imported.Keys...)

	lines := []string{"# Managed by ssherpa authkeys - replaced from directory"}
	for _, key := range imported.Keys {
		lines = append(lines, key.Render())
	}
	plan.NewData = renderLines(lines, doc.Newline, true)
	return finishPlan(plan), nil
}

func PlanDelete(path string, fingerprints []string) (Plan, error) {
	if len(fingerprints) == 0 {
		return Plan{}, errors.New("at least one fingerprint is required")
	}
	want := map[string]bool{}
	for _, fp := range fingerprints {
		fp = strings.TrimSpace(fp)
		if fp == "" {
			continue
		}
		want[fp] = true
	}
	if len(want) == 0 {
		return Plan{}, errors.New("at least one fingerprint is required")
	}

	doc, err := ReadDocument(path)
	if err != nil {
		return Plan{}, err
	}

	plan := basePlan(doc, "removed", strings.Join(sortedSet(want), ", "))
	lines := make([]string, 0, len(doc.Lines))
	found := map[string]bool{}
	for _, line := range doc.Lines {
		if line.Key == nil {
			lines = append(lines, line.Raw)
			continue
		}
		fp, err := line.Key.SHA256Fingerprint()
		if err != nil {
			plan.Diagnostics = append(plan.Diagnostics, Diagnostic{
				Path:     doc.Path,
				Line:     line.Key.Line,
				Severity: "warning",
				Message:  fmt.Sprintf("could not fingerprint key: %v", err),
			})
			lines = append(lines, line.Raw)
			continue
		}
		if want[fp] {
			found[fp] = true
			plan.Keys = append(plan.Keys, *line.Key)
			plan.Stats.Deleted++
			continue
		}
		lines = append(lines, line.Raw)
	}
	for fp := range want {
		if !found[fp] {
			plan.NotFound = append(plan.NotFound, fp)
		}
	}
	sort.Strings(plan.NotFound)

	if plan.Stats.Deleted == 0 {
		plan.Action = "unchanged"
		plan.NewData = append([]byte(nil), doc.data...)
		return finishPlan(plan), nil
	}

	plan.NewData = renderLines(lines, doc.Newline, doc.HadTrailingNewline)
	return finishPlan(plan), nil
}

func basePlan(doc Document, action string, target string) Plan {
	plan := Plan{
		Path:        doc.Path,
		Action:      action,
		Target:      target,
		OldData:     append([]byte(nil), doc.data...),
		Diagnostics: append([]Diagnostic(nil), doc.Diagnostics...),
	}
	return plan
}

func finishPlan(plan Plan) Plan {
	plan.Changed = !bytes.Equal(plan.OldData, plan.NewData)
	return plan
}

func existingByIdentity(doc Document) map[string]AuthorizedKey {
	existing := map[string]AuthorizedKey{}
	for _, key := range doc.Keys() {
		if _, ok := existing[key.Identity()]; !ok {
			existing[key.Identity()] = key
		}
	}
	return existing
}

func duplicateOptionsDiagnostic(incoming AuthorizedKey, existing AuthorizedKey) Diagnostic {
	path := incoming.Source
	line := incoming.Line
	return Diagnostic{
		Path:     path,
		Line:     line,
		Severity: "warning",
		Message:  fmt.Sprintf("duplicate key has different options than existing entry at %s:%d", existing.Source, existing.Line),
	}
}

func renderAppend(doc Document, keys []AuthorizedKey) []byte {
	lines := doc.RawLines()
	if len(lines) == 0 {
		lines = append(lines, "# Created by ssherpa authkeys")
	}
	for _, key := range keys {
		lines = append(lines, key.Render())
	}
	return renderLines(lines, doc.Newline, true)
}

func renderLines(lines []string, newline string, trailing bool) []byte {
	if newline == "" {
		newline = "\n"
	}
	if len(lines) == 0 {
		return nil
	}
	data := strings.Join(lines, newline)
	if trailing {
		data += newline
	}
	return []byte(data)
}

func splitLines(data []byte) ([]string, string, bool) {
	if len(data) == 0 {
		return nil, "\n", false
	}
	newline := "\n"
	text := string(data)
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
		text = strings.ReplaceAll(text, "\r\n", "\n")
	}
	trailing := strings.HasSuffix(text, "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return nil, newline, trailing
	}
	return strings.Split(text, "\n"), newline, trailing
}

func decodeBlob(value string) ([]byte, error) {
	if data, err := base64.StdEncoding.DecodeString(value); err == nil {
		if len(data) == 0 {
			return nil, errors.New("empty decoded key blob")
		}
		return data, nil
	}
	data, err := base64.RawStdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("empty decoded key blob")
	}
	return data, nil
}

func isSpace(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\v', '\f':
		return true
	default:
		return false
	}
}

func sortedSet(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
