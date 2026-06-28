package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/tailscale"
	"github.com/0xbenc/ssherpa/internal/ui"
)

type mutationFlags struct {
	Config string
	DryRun bool
	Yes    bool
}

type addFlags struct {
	mutationFlags
	Alias          string
	HostName       string
	User           string
	Port           string
	IdentityFile   string
	IdentitiesOnly bool
	ForcePassword  bool
}

type editSetFlags struct {
	mutationFlags
	HostName          string
	User              string
	Port              string
	IdentityFile      string
	ClearUser         bool
	ClearPort         bool
	ClearIdentity     bool
	IdentitiesOnly    bool
	IdentitiesOnlySet bool
	ForcePassword     bool
	ForcePasswordSet  bool
}

type deleteFlags struct {
	mutationFlags
	AllSources    bool
	DeletePattern bool
	StateDir      string
}

type deleteAllFlags struct {
	inventoryFlags
	DryRun        bool
	Yes           bool
	Confirm       string
	DeletePattern bool
	StateDir      string
}

type catalogDeletePlan struct {
	StateDir string
	Forwards []state.StoredForward
	Proxies  []state.StoredProxy
}

type editInteractiveFlags struct {
	inventoryFlags
	StateDir      string
	DeletePattern bool
	NoColor       bool
	ThemeName     string
	ThemeFile     string
}

const editItemDeleteAll = ui.ItemKind("delete_all")

func runAdd(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseAddFlags(args, stderr)
	if !ok {
		return 1
	}

	spec := sshconfig.AliasSpec{
		Alias:          flags.Alias,
		HostName:       flags.HostName,
		User:           flags.User,
		Port:           flags.Port,
		IdentityFile:   flags.IdentityFile,
		IdentitiesOnly: flags.IdentitiesOnly,
		ForcePassword:  flags.ForcePassword,
	}

	var err error
	confirmedByForm := false
	promptOptional := flags.Alias == "" || flags.HostName == ""
	if promptOptional {
		tsLoggedIn, tsDevices := discoverTailscaleDevices(flags.HostName)
		result, ok, err := ui.AddAliasForm(context.Background(), ui.AddAliasOptions{
			Input:             os.Stdin,
			Output:            stderr,
			NoAltScreen:       envBool("SSHERPA_NO_ALT_SCREEN"),
			Initial:           addAliasResultFromSpec(spec),
			IdentityFiles:     discoverIdentityFiles(),
			TailscaleLoggedIn: tsLoggedIn,
			TailscaleDevices:  tsDevices,
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: add form failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] add cancelled")
			return 0
		}
		spec = aliasSpecFromAddResult(result)
		confirmedByForm = true
	} else {
		reader := bufio.NewReader(os.Stdin)
		spec, err = promptMissingAddFields(spec, reader, stderr, promptOptional)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
	}
	if err := sshconfig.ValidateAliasSpec(spec, false); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	warnMissingIdentity(spec.IdentityFile, stderr)

	target, err := chooseAddTarget(flags.Config, spec.Alias)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	plan, err := sshconfig.PlanAddOrUpdate(target, spec)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	if !flags.DryRun && !flags.Yes && !confirmedByForm {
		ok, err := confirmActionChoice(stderr, "Write SSH alias", fmt.Sprintf("%s to %s", spec.Alias, target))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: write confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] cancelled")
			return 0
		}
	}

	return applyMutationPlans([]sshconfig.MutationPlan{plan}, flags.mutationFlags, stdout, stderr)
}

func addAliasResultFromSpec(spec sshconfig.AliasSpec) ui.AddAliasResult {
	return ui.AddAliasResult{
		Alias:          spec.Alias,
		HostName:       spec.HostName,
		User:           spec.User,
		Port:           spec.Port,
		IdentityFile:   spec.IdentityFile,
		IdentitiesOnly: spec.IdentitiesOnly,
		ForcePassword:  spec.ForcePassword,
	}
}

func aliasSpecFromAddResult(result ui.AddAliasResult) sshconfig.AliasSpec {
	return sshconfig.AliasSpec{
		Alias:          result.Alias,
		HostName:       result.HostName,
		User:           result.User,
		Port:           result.Port,
		IdentityFile:   result.IdentityFile,
		IdentitiesOnly: result.IdentitiesOnly,
		ForcePassword:  result.ForcePassword,
	}
}

func runEdit(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}
	if len(args) > 0 {
		switch args[0] {
		case "set":
			return runEditSet(args[1:], stdout, stderr)
		case "delete", "remove":
			return runEditDelete(args[1:], stdout, stderr)
		case "delete-all":
			return runEditDeleteAll(args[1:], stdout, stderr)
		}
	}
	return runEditInteractive(args, stdout, stderr)
}

func runEditSet(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "ssherpa: edit set requires an alias")
		return 1
	}
	alias := args[0]
	flags, ok := parseEditSetFlags(args[1:], stderr)
	if !ok {
		return 1
	}

	targets, err := chooseExistingTargets(flags.Config, alias, false)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if len(targets) == 0 {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", alias)
		return 2
	}

	current, ok, err := sshconfig.ExistingAliasSpec(targets[0], alias)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found in %s\n", alias, targets[0])
		return 2
	}

	spec := applyEditSetFlags(current, flags)
	if err := sshconfig.ValidateAliasSpec(spec, false); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	warnMissingIdentity(spec.IdentityFile, stderr)

	plan, err := sshconfig.PlanAddOrUpdate(targets[0], spec)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	if !flags.DryRun && !flags.Yes {
		ok, err := confirmActionChoice(stderr, "Save SSH alias changes", fmt.Sprintf("%s in %s", alias, targets[0]))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: edit confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] edit cancelled")
			return 0
		}
	}

	return applyMutationPlans([]sshconfig.MutationPlan{plan}, flags.mutationFlags, stdout, stderr)
}

func runEditDelete(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(stderr, "ssherpa: edit delete requires an alias")
		return 1
	}
	alias := args[0]
	flags, ok := parseDeleteFlags(args[1:], stderr)
	if !ok {
		return 1
	}

	targets, err := chooseExistingTargets(flags.Config, alias, flags.AllSources)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if len(targets) == 0 {
		fmt.Fprintf(stdout, "[skipped] alias %q not found\n", alias)
		return 0
	}

	plans := make([]sshconfig.MutationPlan, 0, len(targets))
	for _, target := range targets {
		plan, err := sshconfig.PlanDeleteAlias(target, alias, sshconfig.DeleteOptions{AllowPatterns: flags.DeletePattern})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if plan.Changed {
			plan.Alias = alias
			plans = append(plans, plan)
		}
	}
	if len(plans) == 0 {
		fmt.Fprintf(stdout, "[skipped] alias %q not found\n", alias)
		return 0
	}

	catalogPlan, err := planCatalogDeletesForAliases(flags.StateDir, aliasesFromPlans(plans))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	printPlanWarnings(stderr, plans)
	warnCatalogDeletes(stderr, fmt.Sprintf("alias %q", alias), catalogPlan)

	if !flags.DryRun && !flags.Yes {
		ok, err := confirmDeleteChoice(stderr, "Delete SSH alias", deleteAliasDescription(alias, len(plans), catalogPlan))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: delete confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] delete cancelled")
			return 0
		}
	}

	code := applyMutationPlans(plans, flags.mutationFlags, stdout, stderr)
	if code != 0 {
		return code
	}
	return applyCatalogDeletePlan(catalogPlan, flags.DryRun, stdout, stderr)
}

func runEditDeleteAll(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseDeleteAllFlags(args, stderr)
	if !ok {
		return 1
	}

	graph, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	if len(inventory.Aliases) == 0 {
		fmt.Fprintln(stdout, "[skipped] no aliases available to delete")
		return 0
	}

	for _, alias := range inventory.Aliases {
		if alias.IsPattern && !flags.DeletePattern {
			fmt.Fprintf(stderr, "ssherpa: delete-all includes pattern alias %q; pass --all and --delete-patterns to allow this\n", alias.Name)
			return 1
		}
	}

	aliasesByPath := map[string][]string{}
	for _, alias := range inventory.Aliases {
		occurrences := sshconfig.FindAliasOccurrences(graph, alias.Name)
		if len(occurrences) == 0 {
			aliasesByPath[alias.SourcePath] = append(aliasesByPath[alias.SourcePath], alias.Name)
			continue
		}
		for _, occurrence := range occurrences {
			aliasesByPath[occurrence.Path] = append(aliasesByPath[occurrence.Path], alias.Name)
		}
	}

	paths := sortedMapKeys(aliasesByPath)
	plans := make([]sshconfig.MutationPlan, 0, len(paths))
	for _, path := range paths {
		plan, err := sshconfig.PlanDeleteAliases(path, uniqueStrings(aliasesByPath[path]), sshconfig.DeleteOptions{AllowPatterns: flags.DeletePattern})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if plan.Changed {
			plans = append(plans, plan)
		}
	}
	if len(plans) == 0 {
		fmt.Fprintln(stdout, "[skipped] no matching aliases found")
		return 0
	}

	catalogPlan, err := planCatalogDeletesForAliases(flags.StateDir, aliasesFromPlans(plans))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	printPlanWarnings(stderr, plans)
	warnCatalogDeletes(stderr, fmt.Sprintf("%d aliases", len(aliasesFromPlans(plans))), catalogPlan)

	wantConfirm := fmt.Sprintf("delete %d aliases", len(inventory.Aliases))
	reader := bufio.NewReader(os.Stdin)
	if !flags.DryRun && !exactConfirm(reader, stderr, flags.Confirm, wantConfirm) {
		fmt.Fprintf(stdout, "[skipped] confirmation did not match %q\n", wantConfirm)
		return 0
	}

	code := applyMutationPlans(plans, mutationFlags{Config: flags.Config, DryRun: flags.DryRun, Yes: flags.Yes}, stdout, stderr)
	if code != 0 {
		return code
	}
	return applyCatalogDeletePlan(catalogPlan, flags.DryRun, stdout, stderr)
}

func runEditInteractive(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, rest, ok := parseEditInteractiveFlags(args, stderr)
	if !ok {
		return 1
	}
	if len(rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: unknown edit arguments: %s\n", strings.Join(rest, " "))
		return 1
	}

	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	savedForwards := editSavedForwards(flags.StateDir)
	savedProxies := editSavedProxies(flags.StateDir)
	if len(inventory.Aliases) == 0 && len(savedForwards) == 0 && len(savedProxies) == 0 {
		fmt.Fprintln(stdout, "[skipped] no aliases or saved presets available to edit")
		return 0
	}

	item, ok, err := ui.ChooseManagement(context.Background(), editManagementItems(inventory.Aliases, savedForwards, savedProxies), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Edit: pick an alias or saved preset",
		Mode:        "choose item to edit",
		Steps:       []string{"target", "action", "editor"},
		CurrentStep: 0,
		Summary:     editTargetSummary(len(inventory.Aliases), len(savedForwards), len(savedProxies)),
		Footer:      "enter select / type filter / arrows move / shift+arrows section / esc back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}

	switch item.Kind {
	case editItemDeleteAll:
		return runEditDeleteAll(editDeleteAllArgs(flags), stdout, stderr)
	case ui.ItemAlias:
		return runEditAliasTUI(item.Token, flags, stdout, stderr)
	case ui.ItemForwardSaved:
		return runEditSavedForwardTUI(item.Token, flags, stdout, stderr)
	case ui.ItemProxySaved:
		return runEditSavedProxyTUI(item.Token, flags, stdout, stderr)
	}
	fmt.Fprintln(stdout, "[skipped] edit cancelled")
	return 0
}

func runEditAliasTUI(alias string, flags editInteractiveFlags, stdout io.Writer, stderr io.Writer) int {
	action, ok, err := pickEditAction("Edit alias: "+alias, []ui.Item{
		{Kind: ui.ItemKind("edit_details"), Token: "edit", Title: "Edit details", Description: "open the alias form"},
		{Kind: ui.ItemKind("delete"), Token: "delete", Title: "Delete alias", Description: "remove from SSH config"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "leave unchanged"},
	}, flags, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return 1
	}
	if !ok || action.Token == "back" {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
	if action.Token == "delete" {
		args := append([]string{alias}, configArg(flags.Config)...)
		if flags.StateDir != "" {
			args = append(args, "--state-dir", flags.StateDir)
		}
		return runEditDelete(args, stdout, stderr)
	}

	targets, err := chooseExistingTargets(flags.Config, alias, false)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if len(targets) == 0 {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", alias)
		return 2
	}

	spec, ok, err := sshconfig.ExistingAliasSpec(targets[0], alias)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found in %s\n", alias, targets[0])
		return 2
	}

	result, ok, err := ui.AddAliasForm(context.Background(), ui.AddAliasOptions{
		Input:         os.Stdin,
		Output:        stderr,
		NoAltScreen:   envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:       flags.NoColor,
		ThemeName:     flags.ThemeName,
		ThemeFile:     flags.ThemeFile,
		Initial:       addAliasResultFromSpec(spec),
		IdentityFiles: discoverIdentityFiles(),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: edit form failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
	spec = aliasSpecFromAddResult(result)
	newAlias := strings.TrimSpace(result.Alias)
	if newAlias == "" {
		newAlias = alias
	}
	spec.Alias = newAlias

	if err := sshconfig.ValidateAliasSpec(spec, false); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	renamed := newAlias != alias
	if renamed && !strings.EqualFold(newAlias, alias) {
		// Reject a rename that collides with an alias anywhere in the config
		// (PlanRenameAndUpdate only sees the single target file). A case-only
		// change folds back to the same alias, so skip the check for it.
		if graph, err := loadGraph(flags.Config); err == nil {
			if occ := sshconfig.FindAliasOccurrences(graph, newAlias); len(occ) > 0 {
				fmt.Fprintf(stderr, "ssherpa: alias %q already exists; choose another name\n", newAlias)
				return 1
			}
		}
	}

	plan, err := sshconfig.PlanRenameAndUpdate(targets[0], alias, spec)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	code := applyMutationPlans([]sshconfig.MutationPlan{plan}, mutationFlags{Config: flags.Config, Yes: true}, stdout, stderr)
	if code == 0 && renamed {
		repointPresetsReferencingAlias(flags.StateDir, alias, newAlias, stdout, stderr)
	}
	return code
}

// repointPresetsReferencingAlias updates saved forwards/proxies that pointed at
// an alias's old name to its new name after a rename, so they keep working
// instead of dangling. Each change is reported. Best-effort: a missing or
// unreadable state dir is silently skipped, and a per-preset write failure is
// surfaced as a warning without aborting the others.
func repointPresetsReferencingAlias(stateDirOverride string, oldAlias string, newAlias string, stdout io.Writer, stderr io.Writer) {
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		return
	}
	forwards, _ := state.ListForwards(stateDir)
	for _, f := range forwards {
		if f.SSHAlias != oldAlias {
			continue
		}
		f.SSHAlias = newAlias
		if err := state.WriteForward(stateDir, f); err != nil {
			fmt.Fprintf(stderr, "ssherpa: warning: could not repoint saved forward %q to %q: %v\n", f.Name, newAlias, err)
			continue
		}
		fmt.Fprintf(stdout, "[updated] saved forward %s now uses alias %s\n", f.Name, newAlias)
	}
	proxies, _ := state.ListProxies(stateDir)
	for _, p := range proxies {
		if p.SSHAlias != oldAlias {
			continue
		}
		p.SSHAlias = newAlias
		if err := state.WriteProxy(stateDir, p); err != nil {
			fmt.Fprintf(stderr, "ssherpa: warning: could not repoint saved proxy %q to %q: %v\n", p.Name, newAlias, err)
			continue
		}
		fmt.Fprintf(stdout, "[updated] saved proxy %s now uses alias %s\n", p.Name, newAlias)
	}
}

func runEditSavedForwardTUI(name string, flags editInteractiveFlags, stdout io.Writer, stderr io.Writer) int {
	action, ok, err := pickEditAction("Edit saved forward: "+name, []ui.Item{
		{Kind: ui.ItemKind("edit_details"), Token: "edit", Title: "Edit tunnel", Description: "open the forward editor"},
		{Kind: ui.ItemKind("rename"), Token: "rename", Title: "Rename saved forward", Description: "change the catalog handle"},
		{Kind: ui.ItemKind("delete"), Token: "delete", Title: "Delete saved forward", Description: "remove from ssherpa catalog"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "leave unchanged"},
	}, flags, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return 1
	}
	if !ok || action.Token == "back" {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
	if action.Token == "rename" {
		return runRenameSavedForwardTUI(name, flags, stdout, stderr)
	}
	if action.Token == "delete" {
		args := []string{"saved", "delete", name, "--yes"}
		if flags.StateDir != "" {
			args = append(args, "--state-dir", flags.StateDir)
		}
		return runForward(args, stdout, stderr)
	}

	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadForward(stateDir, name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved forward %q: %v\n", name, err)
		return 2
	}
	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	if findAlias(inventory.Aliases, spec.SSHAlias) == nil {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", spec.SSHAlias)
		return 2
	}
	if spec.Through != "" && findAlias(inventory.Aliases, spec.Through) == nil {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", spec.Through)
		return 2
	}
	aliases := make([]ui.ForwardAlias, 0, len(inventory.Aliases))
	for _, a := range inventory.Aliases {
		aliases = append(aliases, ui.ForwardAlias{Name: a.Name, Description: displayAlias(a)})
	}
	if len(aliases) == 0 {
		fmt.Fprintln(stdout, "[skipped] no aliases available for forward edit")
		return 0
	}
	result, ok, err := ui.BuildForward(context.Background(), ui.BuildForwardOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Aliases:     aliases,
		Initial: ui.ForwardResult{
			Alias:      spec.SSHAlias,
			LocalBind:  spec.LocalBind,
			LocalPort:  spec.LocalPort,
			RemoteHost: spec.RemoteHost,
			RemotePort: spec.RemotePort,
			Through:    spec.Through,
		},
		EditMode: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: forward editor failed: %v\n", err)
		return 1
	}
	if !ok || result.Action != ui.ForwardActionSaveChanges {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
	updated := spec
	updated.SSHAlias = result.Alias
	updated.LocalBind = result.LocalBind
	updated.LocalPort = result.LocalPort
	updated.RemoteHost = result.RemoteHost
	updated.RemotePort = result.RemotePort
	updated.Through = result.Through
	if err := validateStoredForward(updated); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if err := state.WriteForward(stateDir, updated); err != nil {
		fmt.Fprintf(stderr, "ssherpa: edit forward: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: forward %q updated\n", updated.Name)
	return 0
}

func runRenameSavedForwardTUI(name string, flags editInteractiveFlags, stdout io.Writer, stderr io.Writer) int {
	newName, ok, err := ui.PromptText(context.Background(), ui.TextPromptOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Rename saved forward",
		Label:       "name",
		Initial:     name,
		Validate:    state.ValidateForwardName,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: rename prompt failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] rename cancelled")
		return 0
	}
	if newName == name {
		fmt.Fprintln(stdout, "[skipped] name unchanged")
		return 0
	}
	args := []string{"saved", "rename", name, newName}
	if flags.StateDir != "" {
		args = append(args, "--state-dir", flags.StateDir)
	}
	return runForward(args, stdout, stderr)
}

func runEditSavedProxyTUI(name string, flags editInteractiveFlags, stdout io.Writer, stderr io.Writer) int {
	action, ok, err := pickEditAction("Edit saved proxy: "+name, []ui.Item{
		{Kind: ui.ItemKind("edit_details"), Token: "edit", Title: "Edit proxy", Description: "open the proxy editor"},
		{Kind: ui.ItemKind("rename"), Token: "rename", Title: "Rename saved proxy", Description: "change the catalog handle"},
		{Kind: ui.ItemKind("delete"), Token: "delete", Title: "Delete saved proxy", Description: "remove from ssherpa catalog"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "leave unchanged"},
	}, flags, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return 1
	}
	if !ok || action.Token == "back" {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
	if action.Token == "rename" {
		return runRenameSavedProxyTUI(name, flags, stdout, stderr)
	}
	if action.Token == "delete" {
		args := []string{"saved", "delete", name, "--yes"}
		if flags.StateDir != "" {
			args = append(args, "--state-dir", flags.StateDir)
		}
		return runProxy(args, stdout, stderr)
	}

	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadProxy(stateDir, name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved proxy %q: %v\n", name, err)
		return 2
	}
	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	if findAlias(inventory.Aliases, spec.SSHAlias) == nil {
		fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", spec.SSHAlias)
		return 2
	}
	aliases := make([]ui.ForwardAlias, 0, len(inventory.Aliases))
	for _, a := range inventory.Aliases {
		aliases = append(aliases, ui.ForwardAlias{Name: a.Name, Description: displayAlias(a)})
	}
	result, ok, err := ui.BuildProxy(context.Background(), ui.BuildProxyOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Aliases:     aliases,
		Initial: ui.ProxyResult{
			Alias: spec.SSHAlias,
			Bind:  spec.Bind,
			Port:  spec.Port,
		},
		EditMode: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: proxy editor failed: %v\n", err)
		return 1
	}
	if !ok || result.Action != ui.ForwardActionSaveChanges {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
	updated := spec
	updated.SSHAlias = result.Alias
	updated.Bind = result.Bind
	updated.Port = result.Port
	if err := validateStoredProxy(updated); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if err := state.WriteProxy(stateDir, updated); err != nil {
		fmt.Fprintf(stderr, "ssherpa: edit proxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: proxy %q updated\n", updated.Name)
	return 0
}

func runRenameSavedProxyTUI(name string, flags editInteractiveFlags, stdout io.Writer, stderr io.Writer) int {
	newName, ok, err := ui.PromptText(context.Background(), ui.TextPromptOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Rename saved proxy",
		Label:       "name",
		Initial:     name,
		Validate:    state.ValidateProxyName,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: rename prompt failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] rename cancelled")
		return 0
	}
	if newName == name {
		fmt.Fprintln(stdout, "[skipped] name unchanged")
		return 0
	}
	args := []string{"saved", "rename", name, newName}
	if flags.StateDir != "" {
		args = append(args, "--state-dir", flags.StateDir)
	}
	return runProxy(args, stdout, stderr)
}

func pickEditAction(title string, items []ui.Item, flags editInteractiveFlags, stderr io.Writer) (ui.Item, bool, error) {
	item, ok, err := ui.ChooseManagement(context.Background(), editActionItems(items), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       title,
		Mode:        editActionMode(title),
		Steps:       []string{"target", "action", "editor"},
		CurrentStep: 1,
		Summary:     editActionSummary(len(items)),
		Footer:      "enter select / type filter / arrows move / shift+arrows section / esc back",
	})
	if err != nil || !ok {
		return ui.Item{}, ok, err
	}
	return ui.Item{
		Kind:        item.Kind,
		Token:       item.Token,
		Title:       item.Title,
		Description: item.Description,
		Detail:      item.Detail,
		Badge:       item.Badge,
		Group:       item.Group,
	}, true, nil
}

func editSavedForwards(stateDirOverride string) []state.StoredForward {
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		return nil
	}
	forwards, err := state.ListForwards(stateDir)
	if err != nil {
		return nil
	}
	return forwards
}

func editSavedProxies(stateDirOverride string) []state.StoredProxy {
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		return nil
	}
	proxies, err := state.ListProxies(stateDir)
	if err != nil {
		return nil
	}
	return proxies
}

func editManagementItems(aliases []hostlist.Alias, forwards []state.StoredForward, proxies []state.StoredProxy) []ui.ManagementItem {
	items := make([]ui.ManagementItem, 0, len(aliases)+len(forwards)+len(proxies)+1)
	if len(aliases) > 0 {
		items = append(items, ui.ManagementItem{
			Kind:        editItemDeleteAll,
			Token:       "delete-all",
			Title:       "Delete all aliases",
			Description: fmt.Sprintf("remove %s from SSH config", editCountLabel(len(aliases), "visible alias", "visible aliases")),
			Detail:      "also removes saved forwards/proxies that depend on those aliases",
			Group:       "Bulk Actions",
			Badge:       "delete",
			Action:      "Delete every visible SSH alias after exact confirmation",
		})
	}
	for _, forward := range forwards {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemForwardSaved,
			Token:       forward.Name,
			Title:       forward.Name,
			Description: savedForwardDescription(forward),
			Detail:      savedForwardDetail(forward),
			Group:       "Saved Forwards",
			Badge:       "fwd",
			Action:      "Choose an action for this saved forward",
		})
	}
	for _, proxy := range proxies {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemProxySaved,
			Token:       proxy.Name,
			Title:       proxy.Name,
			Description: savedProxyDescription(proxy),
			Detail:      savedProxyDetail(proxy),
			Group:       "Saved Proxies",
			Badge:       "proxy",
			Action:      "Choose an action for this saved proxy",
		})
	}
	for _, alias := range aliases {
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemAlias,
			Token:       alias.Name,
			Title:       alias.Name,
			Description: displayAlias(alias),
			Group:       "SSH Aliases",
			Badge:       "host",
			Detail:      editAliasDetail(alias),
			Action:      "Choose an action for this SSH alias",
		})
	}
	return items
}

func editDeleteAllArgs(flags editInteractiveFlags) []string {
	var args []string
	if flags.All {
		args = append(args, "--all")
	}
	if flags.Filter != "" {
		args = append(args, "--filter", flags.Filter)
	}
	if flags.User != "" {
		args = append(args, "--user", flags.User)
	}
	if flags.Config != "" {
		args = append(args, "--config", flags.Config)
	}
	if flags.StateDir != "" {
		args = append(args, "--state-dir", flags.StateDir)
	}
	if flags.DeletePattern {
		args = append(args, "--delete-patterns")
	}
	return args
}

func editActionItems(items []ui.Item) []ui.ManagementItem {
	out := make([]ui.ManagementItem, 0, len(items))
	for _, item := range items {
		out = append(out, ui.ManagementItem{
			Kind:        item.Kind,
			Token:       item.Token,
			Title:       item.Title,
			Description: item.Description,
			Detail:      item.Detail,
			Badge:       editActionBadge(item),
			Group:       editActionGroup(item.Token),
			Action:      editActionHelp(item.Token, item.Title),
		})
	}
	return out
}

func editActionBadge(item ui.Item) string {
	if strings.TrimSpace(item.Badge) != "" {
		return item.Badge
	}
	switch item.Token {
	case "edit":
		return "edit"
	case "rename":
		return "rename"
	case "delete":
		return "delete"
	case "back":
		return "back"
	default:
		return "action"
	}
}

func editActionGroup(token string) string {
	switch token {
	case "delete":
		return "Danger"
	case "back":
		return "Navigation"
	default:
		return "Actions"
	}
}

func editActionHelp(token string, title string) string {
	switch token {
	case "edit":
		return "Open the editor for this item"
	case "rename":
		return "Rename this saved catalog item"
	case "delete":
		return "Delete this item after confirmation"
	case "back":
		return "Return without changing this item"
	default:
		return title
	}
}

func editActionMode(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "choose edit action"
	}
	if _, target, ok := strings.Cut(title, ":"); ok {
		target = strings.TrimSpace(target)
		if target != "" {
			return "choose action for " + target
		}
	}
	return "choose edit action"
}

func editActionSummary(count int) string {
	return editCountLabel(count, "action", "actions")
}

func editTargetSummary(aliasCount int, forwardCount int, proxyCount int) string {
	parts := make([]string, 0, 3)
	if aliasCount > 0 {
		parts = append(parts, editCountLabel(aliasCount, "alias", "aliases"))
	}
	if forwardCount > 0 {
		parts = append(parts, editCountLabel(forwardCount, "forward", "forwards"))
	}
	if proxyCount > 0 {
		parts = append(parts, editCountLabel(proxyCount, "proxy", "proxies"))
	}
	if len(parts) == 0 {
		return "0 choices"
	}
	return strings.Join(parts, "  ")
}

func editCountLabel(count int, singular string, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func editAliasDetail(alias hostlist.Alias) string {
	if alias.SourcePath == "" {
		return ""
	}
	if alias.SourceLine > 0 {
		return fmt.Sprintf("%s:%d", alias.SourcePath, alias.SourceLine)
	}
	return alias.SourcePath
}

func savedForwardDescription(spec state.StoredForward) string {
	base := fmt.Sprintf("%s: %s -> %s", spec.SSHAlias, forwardSavedLocal(spec), forwardSavedRemote(spec))
	if spec.Through != "" {
		base += " via " + spec.Through
	}
	if spec.Description != "" {
		base += " · " + spec.Description
	}
	return base
}

func savedProxyDescription(spec state.StoredProxy) string {
	base := fmt.Sprintf("%s: %s", spec.SSHAlias, proxySavedListener(spec))
	if spec.Description != "" {
		base += " · " + spec.Description
	}
	return base
}

func parseAddFlags(args []string, stderr io.Writer) (addFlags, bool) {
	var flags addFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--alias":
			value, ok := nextArg(args, &i, stderr, "--alias")
			if !ok {
				return flags, false
			}
			flags.Alias = value
		case strings.HasPrefix(arg, "--alias="):
			flags.Alias = strings.TrimPrefix(arg, "--alias=")
		case arg == "--host" || arg == "--hostname":
			value, ok := nextArg(args, &i, stderr, arg)
			if !ok {
				return flags, false
			}
			flags.HostName = value
		case strings.HasPrefix(arg, "--host="):
			flags.HostName = strings.TrimPrefix(arg, "--host=")
		case strings.HasPrefix(arg, "--hostname="):
			flags.HostName = strings.TrimPrefix(arg, "--hostname=")
		case arg == "--user":
			value, ok := nextArg(args, &i, stderr, "--user")
			if !ok {
				return flags, false
			}
			flags.User = value
		case strings.HasPrefix(arg, "--user="):
			flags.User = strings.TrimPrefix(arg, "--user=")
		case arg == "--port":
			value, ok := nextArg(args, &i, stderr, "--port")
			if !ok {
				return flags, false
			}
			flags.Port = value
		case strings.HasPrefix(arg, "--port="):
			flags.Port = strings.TrimPrefix(arg, "--port=")
		case arg == "--identity":
			value, ok := nextArg(args, &i, stderr, "--identity")
			if !ok {
				return flags, false
			}
			flags.IdentityFile = value
		case strings.HasPrefix(arg, "--identity="):
			flags.IdentityFile = strings.TrimPrefix(arg, "--identity=")
		case arg == "--identities-only":
			flags.IdentitiesOnly = true
		case arg == "--force-password":
			flags.ForcePassword = true
		default:
			handled, mutationOK := parseMutationFlag(arg, args, &i, stderr, &flags.mutationFlags)
			if handled {
				if !mutationOK {
					return flags, false
				}
				continue
			}
			fmt.Fprintf(stderr, "ssherpa: unknown add argument %q\n", arg)
			return flags, false
		}
	}
	// Force-password disables key auth, so combining it with an identity
	// selection is contradictory. Reject early with a clear message rather
	// than letting ValidateAliasSpec surface it after the form/write path.
	if flags.ForcePassword && strings.TrimSpace(flags.IdentityFile) != "" {
		fmt.Fprintln(stderr, "ssherpa: --force-password cannot be combined with --identity")
		return flags, false
	}
	if flags.ForcePassword && flags.IdentitiesOnly {
		fmt.Fprintln(stderr, "ssherpa: --force-password cannot be combined with --identities-only")
		return flags, false
	}
	return flags, true
}

func parseEditInteractiveFlags(args []string, stderr io.Writer) (editInteractiveFlags, []string, bool) {
	var flags editInteractiveFlags
	var rest []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			rest = append(rest, args[i+1:]...)
			return flags, rest, true
		case arg == "--all":
			flags.All = true
		case arg == "--filter":
			value, ok := nextArg(args, &i, stderr, "--filter")
			if !ok {
				return flags, nil, false
			}
			flags.Filter = value
		case strings.HasPrefix(arg, "--filter="):
			flags.Filter = strings.TrimPrefix(arg, "--filter=")
		case arg == "--user":
			value, ok := nextArg(args, &i, stderr, "--user")
			if !ok {
				return flags, nil, false
			}
			flags.User = value
		case strings.HasPrefix(arg, "--user="):
			flags.User = strings.TrimPrefix(arg, "--user=")
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, nil, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, nil, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--delete-patterns":
			flags.DeletePattern = true
		case arg == "--no-color":
			flags.NoColor = true
		case arg == "--theme":
			value, ok := nextArg(args, &i, stderr, "--theme")
			if !ok {
				return flags, nil, false
			}
			flags.ThemeName = value
		case strings.HasPrefix(arg, "--theme="):
			flags.ThemeName = strings.TrimPrefix(arg, "--theme=")
		case arg == "--theme-file":
			value, ok := nextArg(args, &i, stderr, "--theme-file")
			if !ok {
				return flags, nil, false
			}
			flags.ThemeFile = value
		case strings.HasPrefix(arg, "--theme-file="):
			flags.ThemeFile = strings.TrimPrefix(arg, "--theme-file=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown edit flag %q\n", arg)
			return flags, nil, false
		default:
			rest = append(rest, arg)
		}
	}
	return flags, rest, true
}

func parseEditSetFlags(args []string, stderr io.Writer) (editSetFlags, bool) {
	var flags editSetFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--host" || arg == "--hostname":
			value, ok := nextArg(args, &i, stderr, arg)
			if !ok {
				return flags, false
			}
			flags.HostName = value
		case strings.HasPrefix(arg, "--host="):
			flags.HostName = strings.TrimPrefix(arg, "--host=")
		case strings.HasPrefix(arg, "--hostname="):
			flags.HostName = strings.TrimPrefix(arg, "--hostname=")
		case arg == "--user":
			value, ok := nextArg(args, &i, stderr, "--user")
			if !ok {
				return flags, false
			}
			flags.User = value
		case strings.HasPrefix(arg, "--user="):
			flags.User = strings.TrimPrefix(arg, "--user=")
		case arg == "--clear-user":
			flags.ClearUser = true
		case arg == "--port":
			value, ok := nextArg(args, &i, stderr, "--port")
			if !ok {
				return flags, false
			}
			flags.Port = value
		case strings.HasPrefix(arg, "--port="):
			flags.Port = strings.TrimPrefix(arg, "--port=")
		case arg == "--clear-port":
			flags.ClearPort = true
		case arg == "--identity":
			value, ok := nextArg(args, &i, stderr, "--identity")
			if !ok {
				return flags, false
			}
			flags.IdentityFile = value
		case strings.HasPrefix(arg, "--identity="):
			flags.IdentityFile = strings.TrimPrefix(arg, "--identity=")
		case arg == "--clear-identity":
			flags.ClearIdentity = true
		case arg == "--identities-only":
			flags.IdentitiesOnly = true
			flags.IdentitiesOnlySet = true
		case arg == "--no-identities-only":
			flags.IdentitiesOnly = false
			flags.IdentitiesOnlySet = true
		case arg == "--force-password":
			flags.ForcePassword = true
			flags.ForcePasswordSet = true
		case arg == "--no-force-password":
			flags.ForcePassword = false
			flags.ForcePasswordSet = true
		default:
			handled, mutationOK := parseMutationFlag(arg, args, &i, stderr, &flags.mutationFlags)
			if handled {
				if !mutationOK {
					return flags, false
				}
				continue
			}
			fmt.Fprintf(stderr, "ssherpa: unknown edit set argument %q\n", arg)
			return flags, false
		}
	}
	if flags.ForcePassword && (strings.TrimSpace(flags.IdentityFile) != "" || (flags.IdentitiesOnlySet && flags.IdentitiesOnly)) {
		fmt.Fprintln(stderr, "ssherpa: --force-password cannot be combined with --identity or --identities-only")
		return flags, false
	}
	return flags, true
}

func parseDeleteFlags(args []string, stderr io.Writer) (deleteFlags, bool) {
	var flags deleteFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--all-sources":
			flags.AllSources = true
		case arg == "--delete-patterns":
			flags.DeletePattern = true
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		default:
			handled, mutationOK := parseMutationFlag(arg, args, &i, stderr, &flags.mutationFlags)
			if handled {
				if !mutationOK {
					return flags, false
				}
				continue
			}
			fmt.Fprintf(stderr, "ssherpa: unknown edit delete argument %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

func parseDeleteAllFlags(args []string, stderr io.Writer) (deleteAllFlags, bool) {
	var flags deleteAllFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--all":
			flags.All = true
		case arg == "--filter":
			value, ok := nextArg(args, &i, stderr, "--filter")
			if !ok {
				return flags, false
			}
			flags.Filter = value
		case strings.HasPrefix(arg, "--filter="):
			flags.Filter = strings.TrimPrefix(arg, "--filter=")
		case arg == "--user":
			value, ok := nextArg(args, &i, stderr, "--user")
			if !ok {
				return flags, false
			}
			flags.User = value
		case strings.HasPrefix(arg, "--user="):
			flags.User = strings.TrimPrefix(arg, "--user=")
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
		case arg == "--dry-run":
			flags.DryRun = true
		case arg == "--yes" || arg == "-y":
			flags.Yes = true
		case arg == "--confirm":
			value, ok := nextArg(args, &i, stderr, "--confirm")
			if !ok {
				return flags, false
			}
			flags.Confirm = value
		case strings.HasPrefix(arg, "--confirm="):
			flags.Confirm = strings.TrimPrefix(arg, "--confirm=")
		case arg == "--delete-patterns":
			flags.DeletePattern = true
		default:
			fmt.Fprintf(stderr, "ssherpa: unknown delete-all argument %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

func parseMutationFlag(arg string, args []string, i *int, stderr io.Writer, flags *mutationFlags) (bool, bool) {
	switch {
	case arg == "--config":
		value, ok := nextArg(args, i, stderr, "--config")
		if !ok {
			return true, false
		}
		flags.Config = value
		return true, true
	case strings.HasPrefix(arg, "--config="):
		flags.Config = strings.TrimPrefix(arg, "--config=")
		return true, true
	case arg == "--dry-run":
		flags.DryRun = true
		return true, true
	case arg == "--yes" || arg == "-y":
		flags.Yes = true
		return true, true
	default:
		return false, true
	}
}

func promptMissingAddFields(spec sshconfig.AliasSpec, reader *bufio.Reader, stderr io.Writer, promptOptional bool) (sshconfig.AliasSpec, error) {
	var err error
	if strings.TrimSpace(spec.HostName) == "" {
		spec.HostName, err = promptLine(reader, stderr, "HostName", "")
		if err != nil {
			return spec, err
		}
	}
	if strings.TrimSpace(spec.Alias) == "" {
		spec.Alias, err = promptLine(reader, stderr, "Alias", suggestAliasFromHost(spec.HostName))
		if err != nil {
			return spec, err
		}
	}
	if promptOptional && strings.TrimSpace(spec.User) == "" {
		spec.User, err = promptLine(reader, stderr, "User", "")
		if err != nil {
			return spec, err
		}
	}
	if promptOptional && strings.TrimSpace(spec.Port) == "" {
		spec.Port, err = promptLine(reader, stderr, "Port", "22")
		if err != nil {
			return spec, err
		}
	}
	if promptOptional && strings.TrimSpace(spec.IdentityFile) == "" {
		spec.IdentityFile, err = promptLine(reader, stderr, "IdentityFile", "")
		if err != nil {
			return spec, err
		}
	}
	if promptOptional && spec.IdentityFile != "" && !spec.IdentitiesOnly {
		spec.IdentitiesOnly = confirmDefault(reader, stderr, "IdentitiesOnly yes?", false)
	}
	return spec, nil
}

func applyEditSetFlags(spec sshconfig.AliasSpec, flags editSetFlags) sshconfig.AliasSpec {
	if flags.HostName != "" {
		spec.HostName = flags.HostName
	}
	if flags.ClearUser {
		spec.User = ""
	} else if flags.User != "" {
		spec.User = flags.User
	}
	if flags.ClearPort {
		spec.Port = ""
	} else if flags.Port != "" {
		spec.Port = flags.Port
	}
	if flags.ClearIdentity {
		spec.IdentityFile = ""
		spec.IdentitiesOnly = false
	} else if flags.IdentityFile != "" {
		spec.IdentityFile = flags.IdentityFile
		// Setting an identity supersedes a prior force-password choice.
		spec.ForcePassword = false
	}
	if flags.IdentitiesOnlySet {
		spec.IdentitiesOnly = flags.IdentitiesOnly
		if flags.IdentitiesOnly {
			spec.ForcePassword = false
		}
	}
	if flags.ForcePasswordSet {
		spec.ForcePassword = flags.ForcePassword
		if spec.ForcePassword {
			spec.IdentityFile = ""
			spec.IdentitiesOnly = false
		}
	}
	return spec
}

func chooseAddTarget(configPath string, alias string) (string, error) {
	root, _, err := rootAndHome(configPath)
	if err != nil {
		return "", err
	}
	if configPath != "" {
		return root, nil
	}

	graph, err := loadGraph("")
	if err != nil {
		return "", err
	}
	paths := occurrencePaths(sshconfig.FindAliasOccurrences(graph, alias))
	switch len(paths) {
	case 0:
		return root, nil
	case 1:
		return paths[0], nil
	default:
		return "", fmt.Errorf("alias %q appears in multiple config files; pass --config PATH to choose one: %s", alias, strings.Join(paths, ", "))
	}
}

// discoverTailscaleDevices probes Tailscale for the add form, mirroring the
// discoverIdentityFiles pattern (the CLI discovers environment data and
// passes it into the pure UI). When an explicit --host was supplied the
// picker is suppressed so a pick cannot silently overwrite it.
func discoverTailscaleDevices(explicitHost string) (bool, []ui.TailscaleDevice) {
	if strings.TrimSpace(explicitHost) != "" {
		return false, nil
	}
	res := tailscale.Devices(context.Background(), tailscale.Options{})
	if !res.LoggedIn {
		return false, nil
	}
	return true, mapTailscaleDevices(res.Devices)
}

func mapTailscaleDevices(devices []tailscale.Device) []ui.TailscaleDevice {
	out := make([]ui.TailscaleDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, ui.TailscaleDevice{
			Name:   d.Name,
			IPv4:   d.IPv4,
			OS:     d.OS,
			Online: d.Online,
		})
	}
	return out
}

func discoverIdentityFiles() []string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	sshDir := filepath.Join(home, ".ssh")
	entries, err := os.ReadDir(sshDir)
	if err != nil {
		return nil
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		lower := strings.ToLower(name)
		switch {
		case strings.HasSuffix(lower, ".pub"):
			continue
		case lower == "config" || lower == "authorized_keys" || strings.HasPrefix(lower, "known_hosts"):
			continue
		}
		if strings.HasPrefix(name, "id_") || strings.Contains(lower, "key") || strings.Contains(lower, "rsa") || strings.Contains(lower, "ed25519") || strings.Contains(lower, "ecdsa") {
			out = append(out, "~/.ssh/"+name)
		}
	}
	sort.Strings(out)
	return out
}

func chooseExistingTargets(configPath string, alias string, allSources bool) ([]string, error) {
	root, _, err := rootAndHome(configPath)
	if err != nil {
		return nil, err
	}
	if configPath != "" {
		return []string{root}, nil
	}

	graph, err := loadGraph("")
	if err != nil {
		return nil, err
	}
	paths := occurrencePaths(sshconfig.FindAliasOccurrences(graph, alias))
	if len(paths) <= 1 || allSources {
		return paths, nil
	}
	return nil, fmt.Errorf("alias %q appears in multiple config files; pass --all-sources or --config PATH: %s", alias, strings.Join(paths, ", "))
}

func occurrencePaths(occurrences []sshconfig.AliasOccurrence) []string {
	set := map[string]bool{}
	for _, occurrence := range occurrences {
		set[occurrence.Path] = true
	}
	paths := sortedMapKeys(set)
	return paths
}

func applyMutationPlans(plans []sshconfig.MutationPlan, flags mutationFlags, stdout io.Writer, stderr io.Writer) int {
	for _, plan := range plans {
		if !flags.DryRun {
			if err := assertUnchangedSincePlan(plan); err != nil {
				fmt.Fprintf(stderr, "ssherpa: %v\n", err)
				return 1
			}
		}

		result, err := fsutil.AtomicWriteFile(plan.Path, plan.NewData, fsutil.WriteOptions{
			DryRun: flags.DryRun,
			Backup: true,
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: write %s: %v\n", plan.Path, err)
			return 1
		}

		printMutationResult(stdout, plan, result)
	}
	return 0
}

func planCatalogDeletesForAliases(stateDirOverride string, aliases []string) (catalogDeletePlan, error) {
	stateDir, err := state.ResolveDir(stateDirOverride)
	if err != nil {
		return catalogDeletePlan{}, fmt.Errorf("resolve state directory: %w", err)
	}
	aliasSet := stringSet(aliases)
	plan := catalogDeletePlan{StateDir: stateDir}

	forwards, err := state.ListForwards(stateDir)
	if err != nil {
		return catalogDeletePlan{}, fmt.Errorf("list saved forwards: %w", err)
	}
	for _, forward := range forwards {
		if aliasSet[forward.SSHAlias] || aliasSet[forward.Through] {
			plan.Forwards = append(plan.Forwards, forward)
		}
	}

	proxies, err := state.ListProxies(stateDir)
	if err != nil {
		return catalogDeletePlan{}, fmt.Errorf("list saved proxies: %w", err)
	}
	for _, proxy := range proxies {
		if aliasSet[proxy.SSHAlias] {
			plan.Proxies = append(plan.Proxies, proxy)
		}
	}
	return plan, nil
}

func warnCatalogDeletes(stderr io.Writer, subject string, plan catalogDeletePlan) {
	if catalogDeleteCount(plan) == 0 {
		return
	}
	fmt.Fprintf(stderr, "ssherpa: warning: deleting %s will also delete %s\n", subject, catalogDeleteSummary(plan))
}

func deleteAliasDescription(alias string, fileCount int, plan catalogDeletePlan) string {
	description := fmt.Sprintf("%s from %d file(s)", alias, fileCount)
	if catalogDeleteCount(plan) > 0 {
		description += "; also deletes " + catalogDeleteSummary(plan)
	}
	return description
}

func applyCatalogDeletePlan(plan catalogDeletePlan, dryRun bool, stdout io.Writer, stderr io.Writer) int {
	for _, forward := range plan.Forwards {
		if dryRun {
			fmt.Fprintf(stdout, "[would-delete] saved forward %s\n", forward.Name)
			continue
		}
		if err := state.DeleteForward(plan.StateDir, forward.Name); err != nil {
			fmt.Fprintf(stderr, "ssherpa: delete saved forward %q: %v\n", forward.Name, err)
			return 1
		}
		fmt.Fprintf(stdout, "ssherpa: forward %q deleted\n", forward.Name)
	}
	for _, proxy := range plan.Proxies {
		if dryRun {
			fmt.Fprintf(stdout, "[would-delete] saved proxy %s\n", proxy.Name)
			continue
		}
		if err := state.DeleteProxy(plan.StateDir, proxy.Name); err != nil {
			fmt.Fprintf(stderr, "ssherpa: delete saved proxy %q: %v\n", proxy.Name, err)
			return 1
		}
		fmt.Fprintf(stdout, "ssherpa: proxy %q deleted\n", proxy.Name)
	}
	return 0
}

func catalogDeleteCount(plan catalogDeletePlan) int {
	return len(plan.Forwards) + len(plan.Proxies)
}

func catalogDeleteSummary(plan catalogDeletePlan) string {
	var parts []string
	if len(plan.Forwards) > 0 {
		parts = append(parts, fmt.Sprintf("%s: %s", pluralize(len(plan.Forwards), "saved forward", "saved forwards"), quoteNames(forwardCatalogNames(plan.Forwards))))
	}
	if len(plan.Proxies) > 0 {
		parts = append(parts, fmt.Sprintf("%s: %s", pluralize(len(plan.Proxies), "saved proxy", "saved proxies"), quoteNames(proxyCatalogNames(plan.Proxies))))
	}
	return strings.Join(parts, "; ")
}

func forwardCatalogNames(forwards []state.StoredForward) []string {
	names := make([]string, 0, len(forwards))
	for _, forward := range forwards {
		names = append(names, forward.Name)
	}
	return names
}

func proxyCatalogNames(proxies []state.StoredProxy) []string {
	names := make([]string, 0, len(proxies))
	for _, proxy := range proxies {
		names = append(names, proxy.Name)
	}
	return names
}

func quoteNames(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, fmt.Sprintf("%q", name))
	}
	return strings.Join(quoted, ", ")
}

func pluralize(count int, singular string, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func printPlanWarnings(stderr io.Writer, plans []sshconfig.MutationPlan) {
	for _, plan := range plans {
		for _, warning := range plan.Warnings {
			fmt.Fprintf(stderr, "ssherpa: warning: %s\n", warning)
		}
	}
}

func aliasesFromPlans(plans []sshconfig.MutationPlan) []string {
	var aliases []string
	for _, plan := range plans {
		aliases = append(aliases, plan.Aliases...)
		if plan.Alias != "" {
			aliases = append(aliases, plan.Alias)
		}
	}
	return uniqueStrings(aliases)
}

func stringSet(values []string) map[string]bool {
	set := map[string]bool{}
	for _, value := range values {
		if value != "" {
			set[value] = true
		}
	}
	return set
}

func assertUnchangedSincePlan(plan sshconfig.MutationPlan) error {
	current, err := os.ReadFile(plan.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			current = nil
		} else {
			return fmt.Errorf("read %s before write: %w", plan.Path, err)
		}
	}
	if !bytes.Equal(current, plan.OldData) {
		return fmt.Errorf("%s changed while ssherpa was preparing the edit; aborting", plan.Path)
	}
	return nil
}

func printMutationResult(stdout io.Writer, plan sshconfig.MutationPlan, result fsutil.WriteResult) {
	target := plan.Alias
	if target == "" {
		switch len(plan.Aliases) {
		case 0:
			target = "0 aliases"
		case 1:
			target = plan.Aliases[0]
		default:
			target = fmt.Sprintf("%d aliases", len(plan.Aliases))
		}
	}

	action := plan.Action
	if !result.Changed {
		action = "unchanged"
	} else if result.DryRun {
		action = "would-" + action
	}
	fmt.Fprintf(stdout, "[%s] %s in %s\n", action, target, plan.Path)
	if result.DryRun && result.Changed {
		fmt.Fprint(stdout, result.Diff)
	}
	if result.BackupPath != "" {
		fmt.Fprintf(stdout, "[backup] %s\n", result.BackupPath)
	}
}

func promptLine(reader *bufio.Reader, stderr io.Writer, label string, defaultValue string) (string, error) {
	if defaultValue == "" {
		fmt.Fprintf(stderr, "%s: ", label)
	} else {
		fmt.Fprintf(stderr, "%s [%s]: ", label, defaultValue)
	}
	value, err := reader.ReadString('\n')
	if err != nil && value == "" {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultValue
	}
	return value, nil
}

func confirm(reader *bufio.Reader, stderr io.Writer, prompt string) bool {
	fmt.Fprintf(stderr, "%s [y/N]: ", prompt)
	value, err := reader.ReadString('\n')
	if err != nil && value == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}

func confirmDeleteChoice(stderr io.Writer, title string, description string) (bool, error) {
	yes, ok, err := ui.ConfirmDelete(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       title,
		Message:     description,
	})
	if err != nil || !ok {
		return false, err
	}
	return yes, nil
}

func confirmActionChoice(stderr io.Writer, title string, description string) (bool, error) {
	yes, ok, err := ui.Confirm(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       title,
		Message:     description,
	})
	if err != nil || !ok {
		return false, err
	}
	return yes, nil
}

func promptText(stderr io.Writer, title string, label string, initial string, validate func(string) error) (string, bool, error) {
	return ui.PromptText(context.Background(), ui.TextPromptOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       title,
		Label:       label,
		Initial:     initial,
		Validate:    validate,
	})
}

func validateNonEmpty(label string) func(string) error {
	return func(value string) error {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
		return nil
	}
}

func confirmDefault(reader *bufio.Reader, stderr io.Writer, prompt string, defaultValue bool) bool {
	suffix := "[y/N]"
	if defaultValue {
		suffix = "[Y/n]"
	}
	fmt.Fprintf(stderr, "%s %s: ", prompt, suffix)
	value, err := reader.ReadString('\n')
	if err != nil && value == "" {
		return defaultValue
	}
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return defaultValue
	}
	return value == "y" || value == "yes"
}

func exactConfirm(reader *bufio.Reader, stderr io.Writer, provided string, want string) bool {
	if provided == "" {
		fmt.Fprintf(stderr, "Type %q to confirm: ", want)
		value, err := reader.ReadString('\n')
		if err != nil && value == "" {
			return false
		}
		provided = strings.TrimSpace(value)
	}
	return provided == want
}

func warnMissingIdentity(identity string, stderr io.Writer) {
	if identity == "" || strings.HasPrefix(identity, "~") {
		return
	}
	path := identity
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err == nil {
			path = abs
		}
	}
	if _, err := os.Stat(path); err != nil && errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "Warning: IdentityFile does not exist: %s\n", identity)
	}
}

func suggestAliasFromHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	var b strings.Builder
	lastDash := false
	for _, r := range host {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func aliasItems(aliases []hostlist.Alias) []ui.Item {
	items := make([]ui.Item, 0, len(aliases))
	for _, alias := range aliases {
		items = append(items, ui.Item{
			Kind:        ui.ItemAlias,
			Token:       alias.Name,
			Title:       alias.Name,
			Description: displayAlias(alias),
		})
	}
	return items
}

func connectFlagsAsEditArgs(flags connectFlags) []string {
	var args []string
	if flags.All {
		args = append(args, "--all")
	}
	if flags.Filter != "" {
		args = append(args, "--filter", flags.Filter)
	}
	if flags.User != "" {
		args = append(args, "--user", flags.User)
	}
	if flags.Config != "" {
		args = append(args, "--config", flags.Config)
	}
	if flags.StateDir != "" {
		args = append(args, "--state-dir", flags.StateDir)
	}
	if flags.NoColor {
		args = append(args, "--no-color")
	}
	if flags.ThemeName != "" {
		args = append(args, "--theme", flags.ThemeName)
	}
	if flags.ThemeFile != "" {
		args = append(args, "--theme-file", flags.ThemeFile)
	}
	return args
}

func connectFlagsAsAddArgs(flags connectFlags) []string {
	if flags.Config == "" {
		return nil
	}
	return []string{"--config", flags.Config}
}

func configArg(configPath string) []string {
	if configPath == "" {
		return nil
	}
	return []string{"--config", configPath}
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		unique = append(unique, value)
	}
	sort.Strings(unique)
	return unique
}
