package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
)

type forwardSavedFlags struct {
	StateDir         string
	Config           string
	JSON             bool
	Yes              bool
	Name             string
	NewName          string
	Select           string
	LocalBind        string
	LocalPort        int
	LocalSet         bool
	RemoteHost       string
	RemotePort       int
	RemoteSet        bool
	Through          string
	ThroughSet       bool
	ClearThrough     bool
	Description      string
	DescriptionSet   bool
	ClearDescription bool
}

func runForwardSaved(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}
	switch args[0] {
	case "list":
		return runForwardSavedList(args[1:], stdout, stderr)
	case "show":
		return runForwardSavedShow(args[1:], stdout, stderr)
	case "save":
		return runForwardSavedSave(args[1:], stdout, stderr)
	case "edit":
		return runForwardSavedEdit(args[1:], stdout, stderr)
	case "delete", "remove":
		return runForwardSavedDelete(args[1:], stdout, stderr)
	case "rename":
		return runForwardSavedRename(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown forward saved command %q\n", args[0])
		return 1
	}
}

func runForwardSavedList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardSavedFlags(args, stderr, "list")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	specs, skipped, err := state.ListForwardsDetailed(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list saved forwards: %v\n", err)
		return 1
	}
	warnSkippedFiles(stderr, "saved forward", skipped)
	if flags.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(specs)
		return 0
	}
	if len(specs) == 0 {
		fmt.Fprintln(stdout, "No saved forwards.")
		return 0
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSSH ALIAS\tLOCAL\tREMOTE\tTHROUGH\tDESCRIPTION")
	for _, spec := range specs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			spec.Name, spec.SSHAlias, forwardSavedLocal(spec), forwardSavedRemote(spec), spec.Through, spec.Description)
	}
	_ = tw.Flush()
	return 0
}

func runForwardSavedShow(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardSavedFlags(args, stderr, "show")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadForward(stateDir, flags.Name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved forward %q: %v\n", flags.Name, err)
		return 2
	}
	if flags.JSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(spec)
		return 0
	}
	printSavedForward(stdout, spec)
	return 0
}

func runForwardSavedSave(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardSavedFlags(args, stderr, "save")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	if err := validateSavedForwardAgainstInventory(flags.Config, flags.Select, flags.Through); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	spec := state.StoredForward{
		Name:        flags.Name,
		SSHAlias:    flags.Select,
		LocalBind:   flags.LocalBind,
		LocalPort:   flags.LocalPort,
		RemoteHost:  flags.RemoteHost,
		RemotePort:  flags.RemotePort,
		Through:     flags.Through,
		Description: flags.Description,
	}
	if err := validateStoredForward(spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if _, err := state.ReadForward(stateDir, flags.Name); err == nil && !flags.Yes {
		ok, err := confirmActionChoice(stderr, "Overwrite saved forward", flags.Name)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: overwrite confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] save cancelled")
			return 0
		}
	}
	if err := state.WriteForward(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: save forward: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: forward saved as %q\n", spec.Name)
	return 0
}

func runForwardSavedEdit(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardSavedFlags(args, stderr, "edit")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadForward(stateDir, flags.Name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved forward %q: %v\n", flags.Name, err)
		return 2
	}
	if flags.Select != "" {
		spec.SSHAlias = flags.Select
	}
	if flags.LocalSet {
		spec.LocalBind = flags.LocalBind
		spec.LocalPort = flags.LocalPort
	}
	if flags.RemoteSet {
		spec.RemoteHost = flags.RemoteHost
		spec.RemotePort = flags.RemotePort
	}
	if flags.ClearThrough {
		spec.Through = ""
	} else if flags.ThroughSet {
		spec.Through = flags.Through
	}
	if flags.ClearDescription {
		spec.Description = ""
	} else if flags.DescriptionSet {
		spec.Description = flags.Description
	}
	if err := validateSavedForwardAgainstInventory(flags.Config, spec.SSHAlias, spec.Through); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	if err := validateStoredForward(spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if err := state.WriteForward(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: edit forward: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: forward %q updated\n", spec.Name)
	return 0
}

func runForwardSavedDelete(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardSavedFlags(args, stderr, "delete")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	if _, err := state.ReadForward(stateDir, flags.Name); err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved forward %q: %v\n", flags.Name, err)
		return 2
	}
	if !flags.Yes {
		ok, err := confirmDeleteChoice(stderr, "Delete saved forward", flags.Name)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: delete confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] delete cancelled")
			return 0
		}
	}
	if err := state.DeleteForward(stateDir, flags.Name); err != nil {
		fmt.Fprintf(stderr, "ssherpa: delete saved forward: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: forward %q deleted\n", flags.Name)
	return 0
}

func runForwardSavedRename(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseForwardSavedFlags(args, stderr, "rename")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadForward(stateDir, flags.Name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved forward %q: %v\n", flags.Name, err)
		return 2
	}
	if flags.Name == flags.NewName {
		fmt.Fprintln(stderr, "ssherpa: OLD and NEW names must differ")
		return 1
	}
	if _, err := state.ReadForward(stateDir, flags.NewName); err == nil && !flags.Yes {
		fmt.Fprintf(stderr, "ssherpa: saved forward %q already exists; pass --yes to replace it\n", flags.NewName)
		return 1
	}
	if err := state.ValidateForwardName(flags.NewName); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	oldName := spec.Name
	spec.Name = flags.NewName
	if err := state.WriteForward(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: rename saved forward: %v\n", err)
		return 1
	}
	if err := state.DeleteForward(stateDir, oldName); err != nil {
		fmt.Fprintf(stderr, "ssherpa: remove old saved forward: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: forward %q renamed to %q\n", oldName, spec.Name)
	return 0
}

func parseForwardSavedFlags(args []string, stderr io.Writer, mode string) (forwardSavedFlags, bool) {
	var flags forwardSavedFlags
	positionals := []string{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			flags.JSON = true
		case arg == "--yes":
			flags.Yes = true
		case arg == "--state-dir":
			value, ok := nextArg(args, &i, stderr, "--state-dir")
			if !ok {
				return flags, false
			}
			flags.StateDir = value
		case strings.HasPrefix(arg, "--state-dir="):
			flags.StateDir = strings.TrimPrefix(arg, "--state-dir=")
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--select":
			value, ok := nextArg(args, &i, stderr, "--select")
			if !ok {
				return flags, false
			}
			flags.Select = value
		case strings.HasPrefix(arg, "--select="):
			flags.Select = strings.TrimPrefix(arg, "--select=")
		case arg == "--local":
			value, ok := nextArg(args, &i, stderr, "--local")
			if !ok {
				return flags, false
			}
			bind, port, ok := parseForwardLocal(value, stderr)
			if !ok {
				return flags, false
			}
			flags.LocalBind, flags.LocalPort, flags.LocalSet = bind, port, true
		case strings.HasPrefix(arg, "--local="):
			bind, port, ok := parseForwardLocal(strings.TrimPrefix(arg, "--local="), stderr)
			if !ok {
				return flags, false
			}
			flags.LocalBind, flags.LocalPort, flags.LocalSet = bind, port, true
		case arg == "--remote":
			value, ok := nextArg(args, &i, stderr, "--remote")
			if !ok {
				return flags, false
			}
			host, port, ok := parseForwardRemote(value, stderr)
			if !ok {
				return flags, false
			}
			flags.RemoteHost, flags.RemotePort, flags.RemoteSet = host, port, true
		case strings.HasPrefix(arg, "--remote="):
			host, port, ok := parseForwardRemote(strings.TrimPrefix(arg, "--remote="), stderr)
			if !ok {
				return flags, false
			}
			flags.RemoteHost, flags.RemotePort, flags.RemoteSet = host, port, true
		case arg == "--through":
			value, ok := nextArg(args, &i, stderr, "--through")
			if !ok {
				return flags, false
			}
			flags.Through, flags.ThroughSet = value, true
		case strings.HasPrefix(arg, "--through="):
			flags.Through, flags.ThroughSet = strings.TrimPrefix(arg, "--through="), true
		case arg == "--clear-through":
			flags.ClearThrough = true
		case arg == "--description":
			value, ok := nextArg(args, &i, stderr, "--description")
			if !ok {
				return flags, false
			}
			flags.Description, flags.DescriptionSet = value, true
		case strings.HasPrefix(arg, "--description="):
			flags.Description, flags.DescriptionSet = strings.TrimPrefix(arg, "--description="), true
		case arg == "--clear-description":
			flags.ClearDescription = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown forward saved %s flag %q\n", mode, arg)
			return flags, false
		default:
			positionals = append(positionals, arg)
		}
	}
	switch mode {
	case "list":
		if len(positionals) != 0 {
			fmt.Fprintf(stderr, "ssherpa: forward saved list does not accept positional arguments: %s\n", strings.Join(positionals, " "))
			return flags, false
		}
	case "show", "save", "edit", "delete":
		if len(positionals) != 1 {
			fmt.Fprintf(stderr, "ssherpa: forward saved %s requires exactly one name\n", mode)
			return flags, false
		}
		flags.Name = positionals[0]
	case "rename":
		if len(positionals) != 2 {
			fmt.Fprintln(stderr, "ssherpa: forward saved rename requires OLD and NEW names")
			return flags, false
		}
		flags.Name, flags.NewName = positionals[0], positionals[1]
	}
	if mode == "save" {
		if flags.Select == "" || !flags.LocalSet || !flags.RemoteSet {
			fmt.Fprintln(stderr, "ssherpa: forward saved save requires NAME, --select, --local, and --remote")
			return flags, false
		}
	}
	if flags.ClearThrough && flags.ThroughSet {
		fmt.Fprintln(stderr, "ssherpa: --through and --clear-through are mutually exclusive")
		return flags, false
	}
	if flags.ClearDescription && flags.DescriptionSet {
		fmt.Fprintln(stderr, "ssherpa: --description and --clear-description are mutually exclusive")
		return flags, false
	}
	return flags, true
}

func resolveForwardSavedStateDir(override string, stderr io.Writer) (string, bool) {
	stateDir, err := state.ResolveDir(override)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: resolve state directory: %v\n", err)
		return "", false
	}
	return stateDir, true
}

func validateStoredForward(spec state.StoredForward) error {
	if err := state.ValidateForwardName(spec.Name); err != nil {
		return err
	}
	return sshcmd.ValidateForward(spec.SSHAlias, defaultString(spec.LocalBind, sshcmd.DefaultForwardBind), spec.LocalPort, spec.RemoteHost, spec.RemotePort, spec.Through)
}

func validateSavedForwardAgainstInventory(config string, alias string, through string) error {
	_, inventory, err := loadInventory(inventoryFlags{Config: config})
	if err != nil {
		return err
	}
	if findAlias(inventory.Aliases, alias) == nil {
		return fmt.Errorf("alias %q not found", alias)
	}
	if through != "" && findAlias(inventory.Aliases, through) == nil {
		return fmt.Errorf("alias %q not found", through)
	}
	return nil
}

func printSavedForward(stdout io.Writer, spec state.StoredForward) {
	fmt.Fprintf(stdout, "name        %s\n", spec.Name)
	fmt.Fprintf(stdout, "ssh-alias   %s\n", spec.SSHAlias)
	fmt.Fprintf(stdout, "local       %s\n", forwardSavedLocal(spec))
	fmt.Fprintf(stdout, "remote      %s\n", forwardSavedRemote(spec))
	if spec.Through != "" {
		fmt.Fprintf(stdout, "through     %s\n", spec.Through)
	}
	if spec.Description != "" {
		fmt.Fprintf(stdout, "description %s\n", spec.Description)
	}
	fmt.Fprintf(stdout, "created     %s\n", spec.CreatedAt.Local().Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(stdout, "updated     %s\n", spec.UpdatedAt.Local().Format("2006-01-02T15:04:05Z07:00"))
}

func forwardSavedLocal(spec state.StoredForward) string {
	bind := defaultString(spec.LocalBind, sshcmd.DefaultForwardBind)
	return fmt.Sprintf("%s:%d", bind, spec.LocalPort)
}

func forwardSavedRemote(spec state.StoredForward) string {
	return fmt.Sprintf("%s:%d", spec.RemoteHost, spec.RemotePort)
}
