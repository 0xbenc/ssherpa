package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/0xbenc/ssherpa/internal/portable"
	"github.com/0xbenc/ssherpa/internal/state"
)

const exportUsage = `Usage:
  ssherpa export --output PATH [--include LIST] [--all]
                 [--alias NAME]... [--forward NAME]... [--proxy NAME]...
                 [--config PATH] [--state-dir PATH] [--json]

Export SSH aliases and saved presets (forwards/proxies) to a single JSON
bundle that can be imported on another machine.

  --output, -o PATH  Write the bundle here (required)
  --include LIST     Comma list of categories: aliases,forwards,proxies
  --all              Include wildcard/negated alias patterns
  --alias NAME       Export only this alias (repeatable; implies cherry-pick)
  --forward NAME     Export only this saved forward (repeatable)
  --proxy NAME       Export only this saved proxy (repeatable)

With no selectors and no --include, every alias and preset is exported.
`

const importUsage = `Usage:
  ssherpa import PATH [--include LIST] [--force]
                 [--alias NAME]... [--forward NAME]... [--proxy NAME]...
                 [--config PATH] [--state-dir PATH] [--json]

Import SSH aliases and saved presets from a JSON bundle. Existing items are
skipped unless --force is given. Aliases are written before presets so a
self-contained bundle resolves its references on a single run.

  PATH / --input PATH  Bundle file to import
  --force              Overwrite existing aliases/presets (default: skip)
  --include LIST       Comma list of categories: aliases,forwards,proxies
  --alias/--forward/--proxy NAME  Import only the named items (repeatable)
`

type exportFlags struct {
	inventoryFlags
	Output   string
	StateDir string
	Include  string
	Aliases  []string
	Forwards []string
	Proxies  []string
	JSON     bool
}

type importFlags struct {
	Config   string
	StateDir string
	Input    string
	Include  string
	Aliases  []string
	Forwards []string
	Proxies  []string
	Force    bool
	JSON     bool
}

type exportJSON struct {
	SchemaVersion int    `json:"schema_version"`
	Output        string `json:"output"`
	Aliases       int    `json:"aliases"`
	Forwards      int    `json:"forwards"`
	Proxies       int    `json:"proxies"`
}

type importJSON struct {
	SchemaVersion int      `json:"schema_version"`
	Aliases       int      `json:"aliases"`
	Forwards      int      `json:"forwards"`
	Proxies       int      `json:"proxies"`
	Skipped       []string `json:"skipped,omitempty"`
	Errors        []string `json:"errors,omitempty"`
}

func runExport(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, exportUsage)
		return 0
	}
	flags, ok := parseExportFlags(args, stderr)
	if !ok {
		return 1
	}
	if strings.TrimSpace(flags.Output) == "" {
		fmt.Fprintln(stderr, "ssherpa: export requires --output PATH")
		return 1
	}

	src, err := loadPortingSources(flags.inventoryFlags, flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	want, err := resolveExportWant(src, flags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	bundle, warnings := buildExportBundle(src, want)
	for _, w := range warnings {
		fmt.Fprintf(stderr, "ssherpa: warning: %s\n", w)
	}
	if bundle.IsEmpty() {
		fmt.Fprintln(stderr, "ssherpa: nothing matched; no bundle written")
		return 2
	}
	if err := writeBundleFile(flags.Output, bundle); err != nil {
		fmt.Fprintf(stderr, "ssherpa: write bundle: %v\n", err)
		return 1
	}

	if flags.JSON {
		writeJSON(stdout, exportJSON{
			SchemaVersion: portable.BundleSchemaVersion,
			Output:        flags.Output,
			Aliases:       len(bundle.Aliases),
			Forwards:      len(bundle.Forwards),
			Proxies:       len(bundle.Proxies),
		})
		return 0
	}
	fmt.Fprintf(stdout, "exported %d alias(es), %d forward(s), %d proxy(ies) to %s\n", len(bundle.Aliases), len(bundle.Forwards), len(bundle.Proxies), flags.Output)
	return 0
}

func runImport(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, importUsage)
		return 0
	}
	flags, ok := parseImportFlags(args, stderr)
	if !ok {
		return 1
	}
	if strings.TrimSpace(flags.Input) == "" {
		fmt.Fprintln(stderr, "ssherpa: import requires a bundle path")
		return 1
	}

	data, err := os.ReadFile(flags.Input)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read bundle: %v\n", err)
		return 1
	}
	bundle, err := portable.Unmarshal(data)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	want, err := resolveImportWant(bundle, flags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1
	}

	// Non-interactive conflict policy: --force overwrites, otherwise skip.
	resolve := func(kind, name string) bool { return flags.Force }

	res := applyBundleImport(bundle, want, flags.Config, stateDir, resolve, stdout, stderr)

	if flags.JSON {
		writeJSON(stdout, importJSON{
			SchemaVersion: portable.BundleSchemaVersion,
			Aliases:       res.aliases,
			Forwards:      res.forwards,
			Proxies:       res.proxies,
			Skipped:       res.skipped,
			Errors:        res.failed,
		})
	} else {
		reportImportResult(res, stdout, stderr)
	}
	if len(res.failed) > 0 && res.total() == 0 {
		return 1
	}
	return 0
}

// resolveExportWant computes the selection set for export. Returns an explicit
// token set; an unknown named selector is an error (exit 2 upstream).
func resolveExportWant(src portingSources, flags exportFlags) (map[string]bool, error) {
	aliasNames := nameSet(aliasNamesOf(src))
	fwdNames := nameSet(forwardNamesOf(src))
	proxyNames := nameSet(proxyNamesOf(src))

	if len(flags.Aliases) > 0 || len(flags.Forwards) > 0 || len(flags.Proxies) > 0 {
		want := map[string]bool{}
		for _, name := range flags.Aliases {
			if !aliasNames[name] {
				return nil, fmt.Errorf("no such alias: %s", name)
			}
			want[portingToken(portingKindAlias, name)] = true
		}
		for _, name := range flags.Forwards {
			if !fwdNames[name] {
				return nil, fmt.Errorf("no such saved forward: %s", name)
			}
			want[portingToken(portingKindForward, name)] = true
		}
		for _, name := range flags.Proxies {
			if !proxyNames[name] {
				return nil, fmt.Errorf("no such saved proxy: %s", name)
			}
			want[portingToken(portingKindProxy, name)] = true
		}
		return want, nil
	}

	includes, err := parseIncludeCategories(flags.Include)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	if includes[portingKindAlias] {
		for name := range aliasNames {
			want[portingToken(portingKindAlias, name)] = true
		}
	}
	if includes[portingKindForward] {
		for name := range fwdNames {
			want[portingToken(portingKindForward, name)] = true
		}
	}
	if includes[portingKindProxy] {
		for name := range proxyNames {
			want[portingToken(portingKindProxy, name)] = true
		}
	}
	return want, nil
}

func resolveImportWant(bundle portable.Bundle, flags importFlags) (map[string]bool, error) {
	aliasNames := map[string]bool{}
	for _, a := range bundle.Aliases {
		aliasNames[a.Alias] = true
	}
	fwdNames := map[string]bool{}
	for _, f := range bundle.Forwards {
		fwdNames[f.Name] = true
	}
	proxyNames := map[string]bool{}
	for _, p := range bundle.Proxies {
		proxyNames[p.Name] = true
	}

	if len(flags.Aliases) > 0 || len(flags.Forwards) > 0 || len(flags.Proxies) > 0 {
		want := map[string]bool{}
		for _, name := range flags.Aliases {
			if !aliasNames[name] {
				return nil, fmt.Errorf("bundle has no alias: %s", name)
			}
			want[portingToken(portingKindAlias, name)] = true
		}
		for _, name := range flags.Forwards {
			if !fwdNames[name] {
				return nil, fmt.Errorf("bundle has no saved forward: %s", name)
			}
			want[portingToken(portingKindForward, name)] = true
		}
		for _, name := range flags.Proxies {
			if !proxyNames[name] {
				return nil, fmt.Errorf("bundle has no saved proxy: %s", name)
			}
			want[portingToken(portingKindProxy, name)] = true
		}
		return want, nil
	}

	includes, err := parseIncludeCategories(flags.Include)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	if includes[portingKindAlias] {
		for name := range aliasNames {
			want[portingToken(portingKindAlias, name)] = true
		}
	}
	if includes[portingKindForward] {
		for name := range fwdNames {
			want[portingToken(portingKindForward, name)] = true
		}
	}
	if includes[portingKindProxy] {
		for name := range proxyNames {
			want[portingToken(portingKindProxy, name)] = true
		}
	}
	return want, nil
}

// parseIncludeCategories accepts a comma list of aliases/forwards/proxies (or
// their singular forms) and returns the set keyed by the canonical singular.
// An empty list means all three.
func parseIncludeCategories(list string) (map[string]bool, error) {
	out := map[string]bool{}
	list = strings.TrimSpace(list)
	if list == "" {
		return map[string]bool{portingKindAlias: true, portingKindForward: true, portingKindProxy: true}, nil
	}
	for _, raw := range strings.Split(list, ",") {
		switch strings.TrimSpace(strings.ToLower(raw)) {
		case "alias", "aliases":
			out[portingKindAlias] = true
		case "forward", "forwards":
			out[portingKindForward] = true
		case "proxy", "proxies":
			out[portingKindProxy] = true
		case "":
			continue
		default:
			return nil, fmt.Errorf("unknown --include category %q (use aliases,forwards,proxies)", strings.TrimSpace(raw))
		}
	}
	return out, nil
}

func aliasNamesOf(src portingSources) []string {
	out := make([]string, 0, len(src.inventory.Aliases))
	for _, a := range src.inventory.Aliases {
		out = append(out, a.Name)
	}
	return out
}

func forwardNamesOf(src portingSources) []string {
	out := make([]string, 0, len(src.forwards))
	for _, f := range src.forwards {
		out = append(out, f.Name)
	}
	return out
}

func proxyNamesOf(src portingSources) []string {
	out := make([]string, 0, len(src.proxies))
	for _, p := range src.proxies {
		out = append(out, p.Name)
	}
	return out
}

func nameSet(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		out[n] = true
	}
	return out
}

func parseExportFlags(args []string, stderr io.Writer) (exportFlags, bool) {
	var flags exportFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--output" || arg == "-o":
			value, ok := nextArg(args, &i, stderr, arg)
			if !ok {
				return flags, false
			}
			flags.Output = value
		case strings.HasPrefix(arg, "--output="):
			flags.Output = strings.TrimPrefix(arg, "--output=")
		case arg == "--include":
			value, ok := nextArg(args, &i, stderr, "--include")
			if !ok {
				return flags, false
			}
			flags.Include = value
		case strings.HasPrefix(arg, "--include="):
			flags.Include = strings.TrimPrefix(arg, "--include=")
		case arg == "--alias":
			value, ok := nextArg(args, &i, stderr, "--alias")
			if !ok {
				return flags, false
			}
			flags.Aliases = append(flags.Aliases, value)
		case arg == "--forward":
			value, ok := nextArg(args, &i, stderr, "--forward")
			if !ok {
				return flags, false
			}
			flags.Forwards = append(flags.Forwards, value)
		case arg == "--proxy":
			value, ok := nextArg(args, &i, stderr, "--proxy")
			if !ok {
				return flags, false
			}
			flags.Proxies = append(flags.Proxies, value)
		case arg == "--all":
			flags.All = true
		case arg == "--json":
			flags.JSON = true
		case arg == "--yes":
			// accepted for symmetry; export never prompts non-interactively
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		default:
			fmt.Fprintf(stderr, "ssherpa: unknown export argument %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

func parseImportFlags(args []string, stderr io.Writer) (importFlags, bool) {
	var flags importFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--input":
			value, ok := nextArg(args, &i, stderr, "--input")
			if !ok {
				return flags, false
			}
			flags.Input = value
		case strings.HasPrefix(arg, "--input="):
			flags.Input = strings.TrimPrefix(arg, "--input=")
		case arg == "--include":
			value, ok := nextArg(args, &i, stderr, "--include")
			if !ok {
				return flags, false
			}
			flags.Include = value
		case strings.HasPrefix(arg, "--include="):
			flags.Include = strings.TrimPrefix(arg, "--include=")
		case arg == "--alias":
			value, ok := nextArg(args, &i, stderr, "--alias")
			if !ok {
				return flags, false
			}
			flags.Aliases = append(flags.Aliases, value)
		case arg == "--forward":
			value, ok := nextArg(args, &i, stderr, "--forward")
			if !ok {
				return flags, false
			}
			flags.Forwards = append(flags.Forwards, value)
		case arg == "--proxy":
			value, ok := nextArg(args, &i, stderr, "--proxy")
			if !ok {
				return flags, false
			}
			flags.Proxies = append(flags.Proxies, value)
		case arg == "--force":
			flags.Force = true
		case arg == "--json":
			flags.JSON = true
		case arg == "--yes":
			// accepted for symmetry; import never prompts non-interactively
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown import argument %q\n", arg)
			return flags, false
		default:
			if flags.Input != "" {
				fmt.Fprintf(stderr, "ssherpa: import accepts a single bundle path; unexpected %q\n", arg)
				return flags, false
			}
			flags.Input = arg
		}
	}
	return flags, true
}
