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
}

type deleteFlags struct {
	mutationFlags
	AllSources    bool
	DeletePattern bool
}

type deleteAllFlags struct {
	inventoryFlags
	DryRun        bool
	Yes           bool
	Confirm       string
	DeletePattern bool
}

func runAdd(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseAddFlags(args, stderr)
	if !ok {
		return 1
	}

	reader := bufio.NewReader(os.Stdin)
	spec := sshconfig.AliasSpec{
		Alias:          flags.Alias,
		HostName:       flags.HostName,
		User:           flags.User,
		Port:           flags.Port,
		IdentityFile:   flags.IdentityFile,
		IdentitiesOnly: flags.IdentitiesOnly,
	}

	var err error
	promptOptional := flags.Alias == "" || flags.HostName == ""
	spec, err = promptMissingAddFields(spec, reader, stderr, promptOptional)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
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

	if !flags.DryRun && !flags.Yes && !confirm(reader, stderr, fmt.Sprintf("Write alias %q to %s?", spec.Alias, target)) {
		fmt.Fprintln(stdout, "[skipped] cancelled")
		return 0
	}

	return applyMutationPlans([]sshconfig.MutationPlan{plan}, flags.mutationFlags, stdout, stderr)
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

	reader := bufio.NewReader(os.Stdin)
	if !flags.DryRun && !flags.Yes && !confirm(reader, stderr, fmt.Sprintf("Save changes to alias %q in %s?", alias, targets[0])) {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
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

	reader := bufio.NewReader(os.Stdin)
	if !flags.DryRun && !flags.Yes && !confirm(reader, stderr, fmt.Sprintf("Delete alias %q from %d file(s)?", alias, len(plans))) {
		fmt.Fprintln(stdout, "[skipped] delete cancelled")
		return 0
	}

	return applyMutationPlans(plans, flags.mutationFlags, stdout, stderr)
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

	wantConfirm := fmt.Sprintf("delete %d aliases", len(inventory.Aliases))
	reader := bufio.NewReader(os.Stdin)
	if !flags.DryRun && !exactConfirm(reader, stderr, flags.Confirm, wantConfirm) {
		fmt.Fprintf(stdout, "[skipped] confirmation did not match %q\n", wantConfirm)
		return 0
	}

	return applyMutationPlans(plans, mutationFlags{Config: flags.Config, DryRun: flags.DryRun, Yes: flags.Yes}, stdout, stderr)
}

func runEditInteractive(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, rest, ok := parseInventoryFlags(args, stderr)
	if !ok {
		return 1
	}
	if len(rest) != 0 {
		fmt.Fprintf(stderr, "ssherpa: unknown edit arguments: %s\n", strings.Join(rest, " "))
		return 1
	}

	_, inventory, err := loadInventory(flags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	if len(inventory.Aliases) == 0 {
		fmt.Fprintln(stdout, "[skipped] no aliases available to edit")
		return 0
	}

	item, ok, err := ui.Pick(context.Background(), aliasItems(inventory.Aliases), ui.PickOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}

	reader := bufio.NewReader(os.Stdin)
	action, err := promptLine(reader, stderr, fmt.Sprintf("Action for %s [set/delete/back]", item.Token), "set")
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "set", "edit", "update":
		return runEditSetInteractive(item.Token, flags.Config, reader, stdout, stderr)
	case "delete", "remove":
		return runEditDelete(append([]string{item.Token}, configArg(flags.Config)...), stdout, stderr)
	default:
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
}

func runEditSetInteractive(alias string, configPath string, reader *bufio.Reader, stdout io.Writer, stderr io.Writer) int {
	targets, err := chooseExistingTargets(configPath, alias, false)
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

	if spec.HostName, err = promptLine(reader, stderr, "HostName", spec.HostName); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if spec.User, err = promptLine(reader, stderr, "User", spec.User); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if spec.Port, err = promptLine(reader, stderr, "Port", spec.Port); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if spec.IdentityFile, err = promptLine(reader, stderr, "IdentityFile", spec.IdentityFile); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if spec.IdentityFile != "" {
		spec.IdentitiesOnly = confirmDefault(reader, stderr, "IdentitiesOnly yes?", spec.IdentitiesOnly)
	} else {
		spec.IdentitiesOnly = false
	}

	if err := sshconfig.ValidateAliasSpec(spec, false); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	plan, err := sshconfig.PlanAddOrUpdate(targets[0], spec)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !confirm(reader, stderr, fmt.Sprintf("Save changes to alias %q in %s?", alias, targets[0])) {
		fmt.Fprintln(stdout, "[skipped] edit cancelled")
		return 0
	}
	return applyMutationPlans([]sshconfig.MutationPlan{plan}, mutationFlags{Config: configPath, Yes: true}, stdout, stderr)
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
	return flags, true
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
	}
	if flags.IdentitiesOnlySet {
		spec.IdentitiesOnly = flags.IdentitiesOnly
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
