package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/ui"
)

const (
	defaultProxyBind = "127.0.0.1"
	defaultProxyPort = 1080
)

type jumpFlags struct {
	connectFlags
	Destination   string
	Hops          []string
	RouteProvided bool
}

type proxyFlags struct {
	connectFlags
	Bind    string
	Port    int
	PortSet bool
}

func runJump(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseJumpFlags(args, stderr)
	if !ok {
		return 1
	}
	if flags.JSON {
		fmt.Fprintln(stderr, "ssherpa: --json is not supported for jump")
		return 1
	}

	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	destination, hops, ok, code := resolveJumpRoute(flags, inventory, stderr)
	if !ok {
		return code
	}

	if err := sshcmd.ValidateJumpRoute(destination, hops); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	base := resolveSSHCommand(flags.connectFlags)
	cmd := sshcmd.BuildJump(base, destination, hops, flags.SSHArgs)
	return printOrRunSSH(cmd, flags.Print, stdout, stderr)
}

func runProxy(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseProxyFlags(args, stderr)
	if !ok {
		return 1
	}
	if flags.JSON {
		fmt.Fprintln(stderr, "ssherpa: --json is not supported for proxy")
		return 1
	}

	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}

	if !flags.PortSet && flags.Select == "" {
		port, ok := promptProxyPort(stderr)
		if !ok {
			return 1
		}
		flags.Port = port
	}

	alias, ok, code := resolveProxyAlias(flags, inventory, stderr)
	if !ok {
		return code
	}

	if err := sshcmd.ValidateProxy(alias.Name, flags.Bind, flags.Port); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	base := resolveSSHCommand(flags.connectFlags)
	cmd := sshcmd.BuildProxy(base, alias.Name, flags.Bind, flags.Port, flags.SSHArgs)
	return printOrRunSSH(cmd, flags.Print, stdout, stderr)
}

func parseJumpFlags(args []string, stderr io.Writer) (jumpFlags, bool) {
	flags := jumpFlags{}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			flags.SSHArgs = append(flags.SSHArgs, args[i+1:]...)
			return flags, true
		case arg == "--json":
			flags.JSON = true
		case arg == "--all":
			flags.All = true
		case arg == "--print":
			flags.Print = true
		case arg == "--exec":
			flags.Print = false
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
		case arg == "--ssh-binary":
			value, ok := nextArg(args, &i, stderr, "--ssh-binary")
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case strings.HasPrefix(arg, "--ssh-binary="):
			flags.SSHBinary = strings.TrimPrefix(arg, "--ssh-binary=")
		case arg == "--dest" || arg == "--destination":
			value, ok := nextArg(args, &i, stderr, arg)
			if !ok {
				return flags, false
			}
			flags.Destination = value
			flags.RouteProvided = true
		case strings.HasPrefix(arg, "--dest="):
			flags.Destination = strings.TrimPrefix(arg, "--dest=")
			flags.RouteProvided = true
		case strings.HasPrefix(arg, "--destination="):
			flags.Destination = strings.TrimPrefix(arg, "--destination=")
			flags.RouteProvided = true
		case arg == "--hop":
			value, ok := nextArg(args, &i, stderr, "--hop")
			if !ok {
				return flags, false
			}
			flags.Hops = append(flags.Hops, splitHopArg(value)...)
			flags.RouteProvided = true
		case strings.HasPrefix(arg, "--hop="):
			flags.Hops = append(flags.Hops, splitHopArg(strings.TrimPrefix(arg, "--hop="))...)
			flags.RouteProvided = true
		case arg == "--no-kitty":
			flags.NoKitty = true
		case arg == "--no-color":
			flags.NoColor = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown jump flag %q\n", arg)
			return flags, false
		default:
			if flags.Destination == "" {
				flags.Destination = arg
			} else {
				flags.Hops = append(flags.Hops, arg)
			}
			flags.RouteProvided = true
		}
	}

	return flags, true
}

func parseProxyFlags(args []string, stderr io.Writer) (proxyFlags, bool) {
	flags := proxyFlags{
		Bind: defaultProxyBind,
		Port: defaultProxyPort,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			flags.SSHArgs = append(flags.SSHArgs, args[i+1:]...)
			return flags, true
		case arg == "--json":
			flags.JSON = true
		case arg == "--all":
			flags.All = true
		case arg == "--print":
			flags.Print = true
		case arg == "--exec":
			flags.Print = false
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
		case arg == "--ssh-binary":
			value, ok := nextArg(args, &i, stderr, "--ssh-binary")
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case strings.HasPrefix(arg, "--ssh-binary="):
			flags.SSHBinary = strings.TrimPrefix(arg, "--ssh-binary=")
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
			flags.Port = port
			flags.PortSet = true
		case strings.HasPrefix(arg, "--port="):
			port, ok := parseProxyPort(strings.TrimPrefix(arg, "--port="), stderr)
			if !ok {
				return flags, false
			}
			flags.Port = port
			flags.PortSet = true
		case arg == "--no-kitty":
			flags.NoKitty = true
		case arg == "--no-color":
			flags.NoColor = true
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown proxy flag %q\n", arg)
			return flags, false
		default:
			if flags.Select != "" {
				fmt.Fprintf(stderr, "ssherpa: proxy accepts only one alias before --: %s\n", arg)
				return flags, false
			}
			flags.Select = arg
		}
	}

	return flags, true
}

func resolveJumpRoute(flags jumpFlags, inventory hostlist.Inventory, stderr io.Writer) (string, []string, bool, int) {
	if flags.RouteProvided {
		if flags.Destination == "" || len(flags.Hops) == 0 {
			fmt.Fprintln(stderr, "ssherpa: jump requires --dest and at least one --hop when route flags are used")
			return "", nil, false, 1
		}
		if err := sshcmd.ValidateJumpRoute(flags.Destination, flags.Hops); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return "", nil, false, 1
		}
		if missing := missingAliases(inventory.Aliases, append([]string{flags.Destination}, flags.Hops...)); len(missing) > 0 {
			fmt.Fprintf(stderr, "ssherpa: alias not found: %s\n", strings.Join(missing, ", "))
			return "", nil, false, 2
		}
		return flags.Destination, append([]string(nil), flags.Hops...), true, 0
	}

	return pickJumpRoute(inventory.Aliases, flags.NoColor, stderr)
}

func pickJumpRoute(aliases []hostlist.Alias, noColor bool, stderr io.Writer) (string, []string, bool, int) {
	if len(aliases) == 0 {
		fmt.Fprintln(stderr, "[skipped] no aliases available for jump")
		return "", nil, false, 0
	}
	if len(aliases) < 2 {
		fmt.Fprintln(stderr, "[skipped] not enough distinct hosts for a jump")
		return "", nil, false, 0
	}

	dest, ok, err := pickAlias(aliases, noColor, "Jump: pick destination", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return "", nil, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] jump cancelled (destination)")
		return "", nil, false, 0
	}

	firstChoices := aliasesExcluding(aliases, dest.Name, nil)
	firstHop, ok, err := pickAlias(firstChoices, noColor, "Jump: pick first hop", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return "", nil, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] jump cancelled (first hop)")
		return "", nil, false, 0
	}

	hops := []string{firstHop.Name}
	for {
		choices := aliasesExcluding(aliases, dest.Name, hops)
		if len(choices) == 0 {
			break
		}

		items := []ui.Item{{
			Kind:        ui.ItemKind("done"),
			Token:       "DONE",
			Title:       "ALL DONE",
			Description: routeSummary(dest.Name, hops),
		}}
		items = append(items, aliasItems(choices)...)

		item, ok, err := ui.Pick(context.Background(), items, ui.PickOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			NoColor:     noColor,
			Title:       "Jump: add another hop or finish",
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
			return "", nil, false, 1
		}
		if !ok {
			fmt.Fprintln(stderr, "[skipped] jump cancelled (additional hops)")
			return "", nil, false, 0
		}
		if item.Token == "DONE" {
			break
		}
		hops = append(hops, item.Token)
	}

	return dest.Name, hops, true, 0
}

func resolveProxyAlias(flags proxyFlags, inventory hostlist.Inventory, stderr io.Writer) (hostlist.Alias, bool, int) {
	if flags.Select != "" {
		alias := findAlias(inventory.Aliases, flags.Select)
		if alias == nil {
			fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", flags.Select)
			return hostlist.Alias{}, false, 2
		}
		return *alias, true, 0
	}

	alias, ok, err := pickAlias(inventory.Aliases, flags.NoColor, "Proxy: pick host", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return hostlist.Alias{}, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] proxy cancelled")
		return hostlist.Alias{}, false, 0
	}
	return alias, true, 0
}

func pickAlias(aliases []hostlist.Alias, noColor bool, title string, stderr io.Writer) (hostlist.Alias, bool, error) {
	if len(aliases) == 0 {
		return hostlist.Alias{}, false, nil
	}
	item, ok, err := ui.Pick(context.Background(), aliasItems(aliases), ui.PickOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     noColor,
		Title:       title,
	})
	if err != nil || !ok {
		return hostlist.Alias{}, ok, err
	}
	alias := findAlias(aliases, item.Token)
	if alias == nil {
		return hostlist.Alias{}, false, fmt.Errorf("selected alias %q disappeared", item.Token)
	}
	return *alias, true, nil
}

func promptProxyPort(stderr io.Writer) (int, bool) {
	value, err := promptLine(bufio.NewReader(os.Stdin), stderr, "Local SOCKS proxy port", strconv.Itoa(defaultProxyPort))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read proxy port: %v\n", err)
		return 0, false
	}
	return parseProxyPort(value, stderr)
}

func parseProxyPort(value string, stderr io.Writer) (int, bool) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 1 || port > 65535 {
		fmt.Fprintln(stderr, "ssherpa: proxy port must be an integer from 1 to 65535")
		return 0, false
	}
	return port, true
}

func splitHopArg(value string) []string {
	parts := strings.Split(value, ",")
	hops := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			hops = append(hops, part)
		}
	}
	return hops
}

func missingAliases(aliases []hostlist.Alias, names []string) []string {
	var missing []string
	for _, name := range names {
		if findAlias(aliases, name) == nil {
			missing = append(missing, name)
		}
	}
	return uniqueStrings(missing)
}

func aliasesExcluding(aliases []hostlist.Alias, destination string, hops []string) []hostlist.Alias {
	excluded := map[string]bool{destination: true}
	for _, hop := range hops {
		excluded[hop] = true
	}

	filtered := make([]hostlist.Alias, 0, len(aliases))
	for _, alias := range aliases {
		if !excluded[alias.Name] {
			filtered = append(filtered, alias)
		}
	}
	return filtered
}

func routeSummary(destination string, hops []string) string {
	route := append([]string(nil), hops...)
	route = append(route, destination)
	return strings.Join(route, " -> ")
}

func resolveSSHCommand(flags connectFlags) sshcmd.Command {
	return sshcmd.Resolve(sshcmd.ResolveOptions{
		SSHBinary: flags.SSHBinary,
		NoKitty:   flags.NoKitty,
		Env:       sshcmd.Env(),
	})
}

func printOrRunSSH(cmd sshcmd.Command, print bool, stdout io.Writer, stderr io.Writer) int {
	if print {
		fmt.Fprintf(stdout, "[print] %s\n", sshcmd.QuoteArgv(cmd.Argv))
		return 0
	}

	fmt.Fprintf(stderr, "[exec] %s\n", sshcmd.QuoteArgv(cmd.Argv))
	return sshcmd.RunDirect(cmd, os.Stdin, stdout, stderr)
}

func connectFlagsAsJumpArgs(flags connectFlags) []string {
	return connectFlagsAsRouteArgs(flags)
}

func connectFlagsAsProxyArgs(flags connectFlags) []string {
	return connectFlagsAsRouteArgs(flags)
}

func connectFlagsAsRouteArgs(flags connectFlags) []string {
	var args []string
	if flags.All {
		args = append(args, "--all")
	}
	if flags.Print {
		args = append(args, "--print")
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
	if flags.SSHBinary != "" {
		args = append(args, "--ssh-binary", flags.SSHBinary)
	}
	if flags.NoKitty {
		args = append(args, "--no-kitty")
	}
	if flags.NoColor {
		args = append(args, "--no-color")
	}
	if len(flags.SSHArgs) > 0 {
		args = append(args, "--")
		args = append(args, flags.SSHArgs...)
	}
	return args
}
