package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
)

type proxySavedFlags struct {
	JSON             bool
	Yes              bool
	StateDir         string
	Config           string
	Name             string
	NewName          string
	Select           string
	Bind             string
	Port             int
	PortSet          bool
	Description      string
	DescriptionSet   bool
	ClearDescription bool
}

func runProxySaved(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) == 0 || hasHelpFlag(args) {
		fmt.Fprintln(stdout, "Usage: ssherpa proxy saved {list|show|save|edit|delete|rename} ...")
		return 0
	}
	switch args[0] {
	case "list":
		return runProxySavedList(args[1:], stdout, stderr)
	case "show":
		return runProxySavedShow(args[1:], stdout, stderr)
	case "save":
		return runProxySavedSave(args[1:], stdout, stderr)
	case "edit":
		return runProxySavedEdit(args[1:], stdout, stderr)
	case "delete":
		return runProxySavedDelete(args[1:], stdout, stderr)
	case "rename":
		return runProxySavedRename(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown proxy saved command %q\n", args[0])
		return 1
	}
}

func runProxySavedList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseProxySavedFlags(args, stderr, "list")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	specs, err := state.ListProxies(stateDir)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: list saved proxies: %v\n", err)
		return 1
	}
	if flags.JSON {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(specs)
		return 0
	}
	if len(specs) == 0 {
		fmt.Fprintln(stdout, "No saved proxies.")
		return 0
	}
	for _, spec := range specs {
		fmt.Fprintf(stdout, "%s\t%s\t%s\n", spec.Name, spec.SSHAlias, proxySavedListener(spec))
	}
	return 0
}

func runProxySavedShow(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseProxySavedFlags(args, stderr, "show")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadProxy(stateDir, flags.Name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved proxy: %v\n", err)
		return 1
	}
	if flags.JSON {
		writeJSON(stdout, spec)
		return 0
	}
	printSavedProxy(stdout, spec)
	return 0
}

func runProxySavedSave(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseProxySavedFlags(args, stderr, "save")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	if err := validateSavedProxyAgainstInventory(flags.Config, flags.Select); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	spec := state.StoredProxy{Name: flags.Name, SSHAlias: flags.Select, Bind: flags.Bind, Port: flags.Port, Description: flags.Description}
	if err := validateStoredProxy(spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if _, err := state.ReadProxy(stateDir, flags.Name); err == nil && !flags.Yes {
		fmt.Fprintf(stderr, "ssherpa: saved proxy %q already exists; pass --yes to overwrite\n", flags.Name)
		return 1
	}
	if err := state.WriteProxy(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: save proxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: proxy saved as %q\n", spec.Name)
	fmt.Fprintf(stdout, "  ssh alias:  %s\n", spec.SSHAlias)
	fmt.Fprintf(stdout, "  listener:   %s\n", proxySavedListener(spec))
	fmt.Fprintf(stdout, "  launch:     ssherpa proxy --select %s\n", spec.Name)
	fmt.Fprintf(stdout, "  daemonize:  ssherpa proxy --select %s --background\n", spec.Name)
	return 0
}

func runProxySavedEdit(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseProxySavedFlags(args, stderr, "edit")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadProxy(stateDir, flags.Name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved proxy: %v\n", err)
		return 1
	}
	if flags.Select != "" {
		spec.SSHAlias = flags.Select
	}
	if flags.PortSet {
		spec.Bind = flags.Bind
		spec.Port = flags.Port
	}
	if flags.DescriptionSet {
		spec.Description = flags.Description
	}
	if flags.ClearDescription {
		spec.Description = ""
	}
	if err := validateSavedProxyAgainstInventory(flags.Config, spec.SSHAlias); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	if err := validateStoredProxy(spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if err := state.WriteProxy(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: update saved proxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: proxy %q updated\n", spec.Name)
	return 0
}

func runProxySavedDelete(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseProxySavedFlags(args, stderr, "delete")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	if _, err := state.ReadProxy(stateDir, flags.Name); err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved proxy: %v\n", err)
		return 1
	}
	if !flags.Yes {
		fmt.Fprintf(stderr, "ssherpa: refusing to delete saved proxy %q without --yes\n", flags.Name)
		return 1
	}
	if err := state.DeleteProxy(stateDir, flags.Name); err != nil {
		fmt.Fprintf(stderr, "ssherpa: delete saved proxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: proxy %q deleted\n", flags.Name)
	return 0
}

func runProxySavedRename(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseProxySavedFlags(args, stderr, "rename")
	if !ok {
		return 1
	}
	stateDir, ok := resolveForwardSavedStateDir(flags.StateDir, stderr)
	if !ok {
		return 1
	}
	spec, err := state.ReadProxy(stateDir, flags.Name)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read saved proxy: %v\n", err)
		return 1
	}
	if _, err := state.ReadProxy(stateDir, flags.NewName); err == nil && !flags.Yes {
		fmt.Fprintf(stderr, "ssherpa: saved proxy %q already exists; pass --yes to overwrite\n", flags.NewName)
		return 1
	}
	old := spec.Name
	spec.Name = flags.NewName
	if err := state.WriteProxy(stateDir, spec); err != nil {
		fmt.Fprintf(stderr, "ssherpa: rename saved proxy: %v\n", err)
		return 1
	}
	if err := state.DeleteProxy(stateDir, old); err != nil {
		fmt.Fprintf(stderr, "ssherpa: remove old saved proxy: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "ssherpa: proxy %q renamed to %q\n", old, spec.Name)
	return 0
}

func parseProxySavedFlags(args []string, stderr io.Writer, mode string) (proxySavedFlags, bool) {
	flags := proxySavedFlags{Bind: defaultProxyBind, Port: defaultProxyPort}
	var positionals []string
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
		case arg == "--bind":
			value, ok := nextArg(args, &i, stderr, "--bind")
			if !ok {
				return flags, false
			}
			flags.Bind = value
		case strings.HasPrefix(arg, "--bind="):
			flags.Bind = strings.TrimPrefix(arg, "--bind=")
		case arg == "--port":
			value, ok := nextArg(args, &i, stderr, "--port")
			if !ok {
				return flags, false
			}
			port, ok := parseProxyPort(value, stderr)
			if !ok {
				return flags, false
			}
			flags.Port, flags.PortSet = port, true
		case strings.HasPrefix(arg, "--port="):
			port, ok := parseProxyPort(strings.TrimPrefix(arg, "--port="), stderr)
			if !ok {
				return flags, false
			}
			flags.Port, flags.PortSet = port, true
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
			fmt.Fprintf(stderr, "ssherpa: unknown proxy saved %s flag %q\n", mode, arg)
			return flags, false
		default:
			positionals = append(positionals, arg)
		}
	}
	switch mode {
	case "list":
		if len(positionals) != 0 {
			fmt.Fprintf(stderr, "ssherpa: proxy saved list does not accept positional arguments: %s\n", strings.Join(positionals, " "))
			return flags, false
		}
	case "show", "save", "edit", "delete":
		if len(positionals) != 1 {
			fmt.Fprintf(stderr, "ssherpa: proxy saved %s requires exactly one name\n", mode)
			return flags, false
		}
		flags.Name = positionals[0]
	case "rename":
		if len(positionals) != 2 {
			fmt.Fprintln(stderr, "ssherpa: proxy saved rename requires OLD and NEW names")
			return flags, false
		}
		flags.Name, flags.NewName = positionals[0], positionals[1]
	}
	if mode == "save" && (flags.Select == "" || !flags.PortSet) {
		fmt.Fprintln(stderr, "ssherpa: proxy saved save requires NAME, --select, and --port")
		return flags, false
	}
	if flags.ClearDescription && flags.DescriptionSet {
		fmt.Fprintln(stderr, "ssherpa: --description and --clear-description are mutually exclusive")
		return flags, false
	}
	return flags, true
}

func validateStoredProxy(spec state.StoredProxy) error {
	if err := state.ValidateProxyName(spec.Name); err != nil {
		return err
	}
	return sshcmd.ValidateProxy(spec.SSHAlias, defaultString(spec.Bind, defaultProxyBind), spec.Port)
}

func validateSavedProxyAgainstInventory(config string, alias string) error {
	_, inventory, err := loadInventory(inventoryFlags{Config: config})
	if err != nil {
		return err
	}
	if findAlias(inventory.Aliases, alias) == nil {
		return fmt.Errorf("alias %q not found", alias)
	}
	return nil
}

func printSavedProxy(stdout io.Writer, spec state.StoredProxy) {
	fmt.Fprintf(stdout, "name        %s\n", spec.Name)
	fmt.Fprintf(stdout, "ssh-alias   %s\n", spec.SSHAlias)
	fmt.Fprintf(stdout, "listener    %s\n", proxySavedListener(spec))
	if spec.Description != "" {
		fmt.Fprintf(stdout, "description %s\n", spec.Description)
	}
	fmt.Fprintf(stdout, "created     %s\n", spec.CreatedAt.Local().Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(stdout, "updated     %s\n", spec.UpdatedAt.Local().Format("2006-01-02T15:04:05Z07:00"))
}

func proxySavedListener(spec state.StoredProxy) string {
	return proxyListener(defaultString(spec.Bind, defaultProxyBind), spec.Port)
}
