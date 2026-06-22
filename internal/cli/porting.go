package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/portable"
	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/ui"
)

// Porting moves SSH aliases and saved presets (forwards/proxies) between
// machines as a single JSON bundle. The interactive flows live here; the
// non-interactive `export`/`import` commands live in porting_cli.go and share
// the bundle-build and import-apply cores below.

const (
	portingKindAlias   = "alias"
	portingKindForward = "forward"
	portingKindProxy   = "proxy"
)

// portingToken namespaces a selection token by category so an alias and a
// preset that share a name never collide in the multi-select picker.
func portingToken(kind, name string) string { return kind + ":" + name }

func splitPortingToken(token string) (kind, name string) {
	if i := strings.IndexByte(token, ':'); i >= 0 {
		return token[:i], token[i+1:]
	}
	return "", token
}

func importBrowseStartDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return "."
}

func defaultBundleFilePath() string {
	name := "ssherpa-export-" + time.Now().Format("20060102") + ".json"
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, name)
	}
	return name
}

// portingSources holds everything export needs from this machine.
type portingSources struct {
	inventory hostlist.Inventory
	forwards  []state.StoredForward
	proxies   []state.StoredProxy
	stateDir  string
}

func loadPortingSources(invFlags inventoryFlags, stateDirOverride string) (portingSources, error) {
	_, inventory, err := loadInventory(invFlags)
	if err != nil {
		return portingSources{}, err
	}
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		return portingSources{}, err
	}
	forwards, err := state.ListForwards(stateDir)
	if err != nil {
		return portingSources{}, err
	}
	proxies, err := state.ListProxies(stateDir)
	if err != nil {
		return portingSources{}, err
	}
	return portingSources{inventory: inventory, forwards: forwards, proxies: proxies, stateDir: stateDir}, nil
}

// portingExportItems projects the available aliases and presets into grouped,
// prefixed-token management items for the multi-select picker.
func portingExportItems(src portingSources) []ui.ManagementItem {
	var items []ui.ManagementItem
	for _, alias := range src.inventory.Aliases {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemAlias,
			Token:       portingToken(portingKindAlias, alias.Name),
			Title:       alias.Name,
			Description: displayAlias(alias),
			Group:       "SSH Aliases",
			Badge:       "host",
		})
	}
	for _, fwd := range src.forwards {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemForwardSaved,
			Token:       portingToken(portingKindForward, fwd.Name),
			Title:       fwd.Name,
			Description: savedForwardSummary(fwd),
			Group:       "Saved Forwards",
			Badge:       "fwd",
		})
	}
	for _, proxy := range src.proxies {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemProxySaved,
			Token:       portingToken(portingKindProxy, proxy.Name),
			Title:       proxy.Name,
			Description: savedProxySummary(proxy),
			Group:       "Saved Proxies",
			Badge:       "proxy",
		})
	}
	return items
}

// exportAliasEntry reads an alias's managed fields back into a portable entry.
// ExistingAliasSpec captures HostName/User/Port/IdentityFile/IdentitiesOnly/
// ProxyJump/ForcePassword exactly as the import write path expects them.
func exportAliasEntry(alias hostlist.Alias) portable.AliasEntry {
	if alias.SourcePath != "" {
		if spec, ok, err := sshconfig.ExistingAliasSpec(alias.SourcePath, alias.Name); err == nil && ok {
			return portable.AliasEntryFromSpec(spec)
		}
	}
	// Fallback to the resolved inventory view when the source can't be read.
	entry := portable.AliasEntry{Alias: alias.Name, HostName: alias.HostName, User: alias.User, Port: alias.Port}
	if len(alias.IdentityFiles) > 0 {
		entry.IdentityFile = alias.IdentityFiles[0]
	}
	return entry
}

// buildExportBundle assembles the bundle for the given prefixed tokens. A nil
// want set selects everything available in src.
func buildExportBundle(src portingSources, want map[string]bool) (portable.Bundle, []string) {
	var bundle portable.Bundle
	var warnings []string

	aliasByName := map[string]hostlist.Alias{}
	for _, a := range src.inventory.Aliases {
		aliasByName[a.Name] = a
	}
	fwdByName := map[string]state.StoredForward{}
	for _, f := range src.forwards {
		fwdByName[f.Name] = f
	}
	proxyByName := map[string]state.StoredProxy{}
	for _, p := range src.proxies {
		proxyByName[p.Name] = p
	}

	selected := func(kind, name string) bool {
		if want == nil {
			return true
		}
		return want[portingToken(kind, name)]
	}

	for _, alias := range src.inventory.Aliases {
		if selected(portingKindAlias, alias.Name) {
			bundle.Aliases = append(bundle.Aliases, exportAliasEntry(alias))
		}
	}
	for _, fwd := range src.forwards {
		if selected(portingKindForward, fwd.Name) {
			bundle.Forwards = append(bundle.Forwards, fwd)
		}
	}
	for _, proxy := range src.proxies {
		if selected(portingKindProxy, proxy.Name) {
			bundle.Proxies = append(bundle.Proxies, proxy)
		}
	}
	return bundle, warnings
}

func writeBundleFile(path string, bundle portable.Bundle) error {
	data, err := portable.Marshal(bundle, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	expanded, err := expandLocalPath(path)
	if err != nil {
		return err
	}
	// 0600: an exported bundle records identity-file paths and host details.
	if _, err := fsutil.AtomicWriteFile(expanded, data, fsutil.WriteOptions{Mode: 0o600}); err != nil {
		return err
	}
	return nil
}

// importResult tallies what an import did for reporting.
type importResult struct {
	aliases  int
	forwards int
	proxies  int
	skipped  []string
	failed   []string
}

func (r importResult) total() int { return r.aliases + r.forwards + r.proxies }

// applyBundleImport writes the selected items from a bundle. want==nil imports
// everything. resolveOverwrite decides, per existing item, whether to replace
// it (false = skip). Aliases are written first so a self-contained bundle's
// presets can resolve their SSHAlias on the same run. A single invalid or
// failing item is recorded and skipped rather than aborting the whole import.
func applyBundleImport(bundle portable.Bundle, want map[string]bool, configPath, stateDir string, resolveOverwrite func(kind, name string) bool, stdout, stderr io.Writer) importResult {
	var res importResult
	selected := func(kind, name string) bool {
		if want == nil {
			return true
		}
		return want[portingToken(kind, name)]
	}

	// Names already present in the target config, extended as we import, so
	// the missing-alias warning for presets is accurate within this run.
	knownAliases := map[string]bool{}
	if _, inv, err := loadInventory(inventoryFlags{Config: configPath, All: true}); err == nil {
		for _, a := range inv.Aliases {
			knownAliases[a.Name] = true
		}
	}

	for _, entry := range bundle.Aliases {
		if !selected(portingKindAlias, entry.Alias) {
			continue
		}
		spec := entry.ToSpec()
		if err := sshconfig.ValidateAliasSpec(spec, false); err != nil {
			res.failed = append(res.failed, fmt.Sprintf("alias %q: %v", entry.Alias, err))
			continue
		}
		if filepath.IsAbs(spec.IdentityFile) {
			fmt.Fprintf(stderr, "ssherpa: warning: alias %q uses an absolute identity path %q that may not exist on this machine\n", spec.Alias, spec.IdentityFile)
		}
		target, err := chooseAddTarget(configPath, spec.Alias)
		if err != nil {
			res.failed = append(res.failed, fmt.Sprintf("alias %q: %v", entry.Alias, err))
			continue
		}
		if _, found, _ := sshconfig.ExistingAliasSpec(target, spec.Alias); found {
			if !resolveOverwrite(portingKindAlias, spec.Alias) {
				res.skipped = append(res.skipped, fmt.Sprintf("alias %q (exists)", spec.Alias))
				continue
			}
		}
		plan, err := sshconfig.PlanAddOrUpdate(target, spec)
		if err != nil {
			res.failed = append(res.failed, fmt.Sprintf("alias %q: %v", entry.Alias, err))
			continue
		}
		// Plan-then-apply per alias so each plans against current disk state
		// (multiple aliases may target the same file).
		if code := applyMutationPlans([]sshconfig.MutationPlan{plan}, mutationFlags{Config: configPath, Yes: true}, stdout, stderr); code != 0 {
			res.failed = append(res.failed, fmt.Sprintf("alias %q: write failed", entry.Alias))
			continue
		}
		knownAliases[spec.Alias] = true
		res.aliases++
	}

	for _, fwd := range bundle.Forwards {
		if !selected(portingKindForward, fwd.Name) {
			continue
		}
		if err := validateStoredForward(fwd); err != nil {
			res.failed = append(res.failed, fmt.Sprintf("forward %q: %v", fwd.Name, err))
			continue
		}
		if _, err := state.ReadForward(stateDir, fwd.Name); err == nil {
			if !resolveOverwrite(portingKindForward, fwd.Name) {
				res.skipped = append(res.skipped, fmt.Sprintf("forward %q (exists)", fwd.Name))
				continue
			}
		}
		if err := state.WriteForward(stateDir, fwd); err != nil {
			res.failed = append(res.failed, fmt.Sprintf("forward %q: %v", fwd.Name, err))
			continue
		}
		if fwd.SSHAlias != "" && !knownAliases[fwd.SSHAlias] {
			fmt.Fprintf(stderr, "ssherpa: warning: saved forward %q references alias %q which is not in this machine's SSH config\n", fwd.Name, fwd.SSHAlias)
		}
		res.forwards++
	}

	for _, proxy := range bundle.Proxies {
		if !selected(portingKindProxy, proxy.Name) {
			continue
		}
		if err := validateStoredProxy(proxy); err != nil {
			res.failed = append(res.failed, fmt.Sprintf("proxy %q: %v", proxy.Name, err))
			continue
		}
		if _, err := state.ReadProxy(stateDir, proxy.Name); err == nil {
			if !resolveOverwrite(portingKindProxy, proxy.Name) {
				res.skipped = append(res.skipped, fmt.Sprintf("proxy %q (exists)", proxy.Name))
				continue
			}
		}
		if err := state.WriteProxy(stateDir, proxy); err != nil {
			res.failed = append(res.failed, fmt.Sprintf("proxy %q: %v", proxy.Name, err))
			continue
		}
		if proxy.SSHAlias != "" && !knownAliases[proxy.SSHAlias] {
			fmt.Fprintf(stderr, "ssherpa: warning: saved proxy %q references alias %q which is not in this machine's SSH config\n", proxy.Name, proxy.SSHAlias)
		}
		res.proxies++
	}

	return res
}

func reportImportResult(res importResult, stdout, stderr io.Writer) {
	fmt.Fprintf(stdout, "imported %d alias(es), %d forward(s), %d proxy(ies)\n", res.aliases, res.forwards, res.proxies)
	for _, s := range res.skipped {
		fmt.Fprintf(stdout, "[skipped] %s\n", s)
	}
	for _, f := range res.failed {
		fmt.Fprintf(stderr, "ssherpa: skipped %s\n", f)
	}
}

// runPortingPicker is the interactive Import / Export sub-menu.
func runPortingPicker(flags connectFlags, stdout io.Writer, stderr io.Writer, build BuildInfo) (int, bool) {
	items := []ui.ManagementItem{
		{Kind: ui.ItemKind("export"), Token: "export", Title: "Export...", Description: "write selected aliases and presets to a JSON bundle", Group: "Porting", Badge: "export", Action: "Export aliases and presets"},
		{Kind: ui.ItemKind("import"), Token: "import", Title: "Import...", Description: "read aliases and presets from a JSON bundle", Group: "Porting", Badge: "import", Action: "Import aliases and presets"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to the home screen", Group: "Navigation", Badge: "back", Action: "Return without porting"},
	}
	item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Import / Export",
		Mode:        "choose porting action",
		Footer:      "enter select / arrows move / Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: porting picker failed: %v\n", err)
		return 1, false
	}
	if !ok || item.Token == "back" {
		return 0, true
	}
	switch item.Token {
	case "export":
		return runPortingExportTUI(flags, stdout, stderr)
	case "import":
		return runPortingImportTUI(flags, stdout, stderr)
	default:
		return 0, true
	}
}

func runPortingExportTUI(flags connectFlags, stdout io.Writer, stderr io.Writer) (int, bool) {
	src, err := loadPortingSources(flags.inventoryFlags, flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1, false
	}
	items := portingExportItems(src)
	if len(items) == 0 {
		fmt.Fprintln(stdout, "[skipped] nothing to export")
		return 0, true
	}
	tokens, ok, err := ui.ChooseManagementMulti(context.Background(), items, ui.ManagementMultiChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Export: choose items",
		Mode:        "select items to export",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: export selection failed: %v\n", err)
		return 1, false
	}
	if !ok || len(tokens) == 0 {
		fmt.Fprintln(stdout, "[skipped] nothing selected")
		return 0, true
	}

	want := map[string]bool{}
	for _, t := range tokens {
		want[t] = true
	}
	bundle, warnings := buildExportBundle(src, want)
	for _, w := range warnings {
		fmt.Fprintf(stderr, "ssherpa: warning: %s\n", w)
	}
	if bundle.IsEmpty() {
		fmt.Fprintln(stdout, "[skipped] nothing to export")
		return 0, true
	}

	path, ok, err := promptText(stderr, "Export bundle", "path", defaultBundleFilePath(), validateNonEmpty("path"))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: export path prompt failed: %v\n", err)
		return 1, false
	}
	if !ok || strings.TrimSpace(path) == "" {
		fmt.Fprintln(stdout, "[skipped] export cancelled")
		return 0, true
	}
	path = strings.TrimSpace(path)

	if expanded, err := expandLocalPath(path); err == nil {
		if _, statErr := os.Stat(expanded); statErr == nil {
			confirmed, err := confirmActionChoice(stderr, "Overwrite existing file", path)
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: overwrite confirmation failed: %v\n", err)
				return 1, false
			}
			if !confirmed {
				fmt.Fprintln(stdout, "[skipped] export cancelled")
				return 0, true
			}
		}
	}

	if err := writeBundleFile(path, bundle); err != nil {
		fmt.Fprintf(stderr, "ssherpa: write bundle: %v\n", err)
		return 1, false
	}
	fmt.Fprintf(stdout, "exported %d alias(es), %d forward(s), %d proxy(ies) to %s\n", len(bundle.Aliases), len(bundle.Forwards), len(bundle.Proxies), path)
	return 0, true
}

func runPortingImportTUI(flags connectFlags, stdout io.Writer, stderr io.Writer) (int, bool) {
	path, ok, err := pickLocalFileWith(stderr, connectFilePickerOptions(flags), importBrowseStartDir(), "SSHERPA IMPORT BUNDLE", "local-file", "LOCAL", nil, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: import file browser failed: %v\n", err)
		return 1, false
	}
	if !ok {
		return 0, true
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read bundle: %v\n", err)
		return 1, false
	}
	bundle, err := portable.Unmarshal(data)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1, false
	}
	if bundle.IsEmpty() {
		fmt.Fprintln(stdout, "[skipped] bundle is empty")
		return 0, true
	}

	message := fmt.Sprintf("Import %s?\n\nAliases: %d\nForwards: %d\nProxies: %d\nExported: %s",
		filepath.Base(path), len(bundle.Aliases), len(bundle.Forwards), len(bundle.Proxies), defaultString(bundle.ExportedAt, "unknown"))
	confirmed, err := confirmActionChoice(stderr, "Confirm import", message)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: import confirmation failed: %v\n", err)
		return 1, false
	}
	if !confirmed {
		return 0, true
	}

	items := portingImportItems(bundle)
	preselect := map[string]bool{}
	for _, it := range items {
		preselect[it.Token] = true
	}
	tokens, ok, err := ui.ChooseManagementMulti(context.Background(), items, ui.ManagementMultiChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Import: choose items",
		Mode:        "select items to import",
		Preselect:   preselect,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: import selection failed: %v\n", err)
		return 1, false
	}
	if !ok || len(tokens) == 0 {
		fmt.Fprintln(stdout, "[skipped] nothing selected")
		return 0, true
	}
	want := map[string]bool{}
	for _, t := range tokens {
		want[t] = true
	}

	stateDir, err := state.ResolveDir(flags.StateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return 1, false
	}

	// Per-conflict prompt: existing items ask skip/overwrite. An unanswered
	// prompt (esc) defaults to skip — the safe choice.
	resolve := func(kind, name string) bool {
		confirmed, err := confirmActionChoice(stderr, "Overwrite existing "+kind, name)
		if err != nil {
			return false
		}
		return confirmed
	}

	res := applyBundleImport(bundle, want, flags.Config, stateDir, resolve, stdout, stderr)
	reportImportResult(res, stdout, stderr)
	return 0, true
}

// portingImportItems projects a parsed bundle into selectable items.
func portingImportItems(bundle portable.Bundle) []ui.ManagementItem {
	var items []ui.ManagementItem
	for _, a := range bundle.Aliases {
		desc := a.HostName
		if a.ForcePassword {
			desc += "  (password login)"
		}
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemAlias,
			Token:       portingToken(portingKindAlias, a.Alias),
			Title:       a.Alias,
			Description: desc,
			Group:       "SSH Aliases",
			Badge:       "host",
		})
	}
	for _, f := range bundle.Forwards {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemForwardSaved,
			Token:       portingToken(portingKindForward, f.Name),
			Title:       f.Name,
			Description: savedForwardSummary(f),
			Group:       "Saved Forwards",
			Badge:       "fwd",
		})
	}
	for _, p := range bundle.Proxies {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemProxySaved,
			Token:       portingToken(portingKindProxy, p.Name),
			Title:       p.Name,
			Description: savedProxySummary(p),
			Group:       "Saved Proxies",
			Badge:       "proxy",
		})
	}
	return items
}

func savedForwardSummary(f state.StoredForward) string {
	return formatEndpointBindOrLoopback(f.LocalBind, f.LocalPort) + " -> " + formatEndpointBindOrLoopback(f.RemoteHost, f.RemotePort)
}

func savedProxySummary(p state.StoredProxy) string {
	return "SOCKS " + formatEndpointBindOrLoopback(p.Bind, p.Port)
}
