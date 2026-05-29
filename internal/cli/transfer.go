package cli

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
)

type transferFlags struct {
	inventoryFlags
	Print      bool
	Select     string
	LocalPath  string
	RemotePath string
	SFTPBinary string
	NoColor    bool
	ThemeName  string
	ThemeFile  string
}

func runSend(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}
	flags, ok := parseTransferFlags(args, stderr, sshcmd.SFTPTransferSend)
	if !ok {
		return 1
	}
	return runTransfer(sshcmd.SFTPTransferSend, flags, stdout, stderr)
}

func runReceive(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}
	flags, ok := parseTransferFlags(args, stderr, sshcmd.SFTPTransferReceive)
	if !ok {
		return 1
	}
	return runTransfer(sshcmd.SFTPTransferReceive, flags, stdout, stderr)
}

func parseTransferFlags(args []string, stderr io.Writer, direction sshcmd.SFTPTransferDirection) (transferFlags, bool) {
	var flags transferFlags
	var positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			positional = append(positional, args[i+1:]...)
			i = len(args)
		case arg == "--print":
			flags.Print = true
		case arg == "--select":
			value, ok := nextArg(args, &i, stderr, "--select")
			if !ok {
				return flags, false
			}
			flags.Select = value
		case strings.HasPrefix(arg, "--select="):
			flags.Select = strings.TrimPrefix(arg, "--select=")
		case arg == "--config":
			value, ok := nextArg(args, &i, stderr, "--config")
			if !ok {
				return flags, false
			}
			flags.Config = value
		case strings.HasPrefix(arg, "--config="):
			flags.Config = strings.TrimPrefix(arg, "--config=")
		case arg == "--remote":
			value, ok := nextArg(args, &i, stderr, "--remote")
			if !ok {
				return flags, false
			}
			flags.RemotePath = value
		case strings.HasPrefix(arg, "--remote="):
			flags.RemotePath = strings.TrimPrefix(arg, "--remote=")
		case arg == "--local":
			value, ok := nextArg(args, &i, stderr, "--local")
			if !ok {
				return flags, false
			}
			flags.LocalPath = value
		case strings.HasPrefix(arg, "--local="):
			flags.LocalPath = strings.TrimPrefix(arg, "--local=")
		case arg == "--sftp-binary":
			value, ok := nextArg(args, &i, stderr, "--sftp-binary")
			if !ok {
				return flags, false
			}
			flags.SFTPBinary = value
		case strings.HasPrefix(arg, "--sftp-binary="):
			flags.SFTPBinary = strings.TrimPrefix(arg, "--sftp-binary=")
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
		case arg == "--no-color":
			flags.NoColor = true
		case arg == "--theme":
			value, ok := nextArg(args, &i, stderr, "--theme")
			if !ok {
				return flags, false
			}
			flags.ThemeName = value
		case strings.HasPrefix(arg, "--theme="):
			flags.ThemeName = strings.TrimPrefix(arg, "--theme=")
		case arg == "--theme-file":
			value, ok := nextArg(args, &i, stderr, "--theme-file")
			if !ok {
				return flags, false
			}
			flags.ThemeFile = value
		case strings.HasPrefix(arg, "--theme-file="):
			flags.ThemeFile = strings.TrimPrefix(arg, "--theme-file=")
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown transfer flag %q\n", arg)
			return flags, false
		default:
			positional = append(positional, arg)
		}
	}

	if len(positional) > 2 {
		fmt.Fprintf(stderr, "ssherpa: transfer accepts at most a path and alias: %s\n", strings.Join(positional[2:], " "))
		return flags, false
	}
	if len(positional) >= 1 {
		if direction == sshcmd.SFTPTransferReceive {
			flags.RemotePath = positional[0]
		} else {
			flags.LocalPath = positional[0]
		}
	}
	if len(positional) == 2 {
		flags.Select = positional[1]
	}
	return flags, true
}

func runTransfer(direction sshcmd.SFTPTransferDirection, flags transferFlags, stdout io.Writer, stderr io.Writer) int {
	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 2
	}
	alias, ok, code := resolveTransferAlias(flags, inventory, stderr)
	if !ok {
		return code
	}
	transfer, ok := resolveTransferSpec(direction, flags, alias, stderr)
	if !ok {
		return 1
	}
	if err := sshcmd.ValidateSFTPTransfer(transfer); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	transfer.Batch = sshcmd.BuildSFTPBatch(transfer)
	cmd := sshcmd.BuildSFTP(resolveSFTPBinary(flags), transfer)
	if flags.Print {
		fmt.Fprintf(stdout, "[print] %s\n", sshcmd.QuoteArgv(cmd.Argv))
		fmt.Fprintf(stdout, "[batch]\n%s", transfer.Batch)
		return 0
	}
	return runSFTPCommand(cmd, transfer.Batch, stdout, stderr)
}

func resolveTransferAlias(flags transferFlags, inventory hostlist.Inventory, stderr io.Writer) (hostlist.Alias, bool, int) {
	if flags.Select != "" {
		alias := findAlias(inventory.Aliases, flags.Select)
		if alias == nil {
			fmt.Fprintf(stderr, "ssherpa: alias %q not found\n", flags.Select)
			return hostlist.Alias{}, false, 2
		}
		return *alias, true, 0
	}
	alias, ok, err := pickAlias(inventory.Aliases, flags.NoColor, flags.ThemeName, flags.ThemeFile, "Transfer: pick host", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return hostlist.Alias{}, false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] transfer cancelled")
		return hostlist.Alias{}, false, 0
	}
	return alias, true, 0
}

func resolveTransferSpec(direction sshcmd.SFTPTransferDirection, flags transferFlags, alias hostlist.Alias, stderr io.Writer) (sshcmd.SFTPTransfer, bool) {
	localPath := strings.TrimSpace(flags.LocalPath)
	remotePath := strings.TrimSpace(flags.RemotePath)
	switch direction {
	case sshcmd.SFTPTransferSend:
		if localPath == "" {
			fmt.Fprintln(stderr, "ssherpa: send requires a local file path")
			return sshcmd.SFTPTransfer{}, false
		}
		expanded, err := expandLocalPath(localPath)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return sshcmd.SFTPTransfer{}, false
		}
		info, err := os.Stat(expanded)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: local file %s: %v\n", expanded, err)
			return sshcmd.SFTPTransfer{}, false
		}
		if info.IsDir() {
			fmt.Fprintf(stderr, "ssherpa: local path %s is a directory; directory transfer is not implemented yet\n", expanded)
			return sshcmd.SFTPTransfer{}, false
		}
		localPath = expanded
		if remotePath == "" {
			remotePath = filepath.Base(localPath)
		}
	case sshcmd.SFTPTransferReceive:
		if remotePath == "" {
			fmt.Fprintln(stderr, "ssherpa: receive requires a remote file path")
			return sshcmd.SFTPTransfer{}, false
		}
		if localPath == "" {
			localPath = filepath.Base(remotePath)
		}
		expanded, err := expandLocalPath(localPath)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return sshcmd.SFTPTransfer{}, false
		}
		if strings.HasSuffix(localPath, string(os.PathSeparator)) {
			expanded = filepath.Join(expanded, filepath.Base(remotePath))
		} else if info, err := os.Stat(expanded); err == nil && info.IsDir() {
			expanded = filepath.Join(expanded, filepath.Base(remotePath))
		}
		localPath = expanded
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown transfer direction %q\n", direction)
		return sshcmd.SFTPTransfer{}, false
	}
	return sshcmd.SFTPTransfer{
		Direction:  direction,
		Alias:      alias.Name,
		Config:     flags.Config,
		LocalPath:  localPath,
		RemotePath: remotePath,
	}, true
}

func runSFTPCommand(cmd sshcmd.Command, batch string, stdout io.Writer, stderr io.Writer) int {
	if len(cmd.Argv) == 0 {
		fmt.Fprintln(stderr, "ssherpa: empty SFTP command")
		return 1
	}
	proc := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	proc.Stdin = strings.NewReader(batch)
	proc.Stdout = stdout
	proc.Stderr = stderr
	err := proc.Run()
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		code := exitErr.ExitCode()
		if code >= 0 {
			return code
		}
		return 1
	}
	fmt.Fprintf(stderr, "ssherpa: run %s: %v\n", sshcmd.QuoteArgv(cmd.Argv), err)
	return 1
}

func runSendFileBuilder(flags connectFlags, inventory hostlist.Inventory, stdout io.Writer, stderr io.Writer) (int, bool) {
	if len(inventory.Aliases) == 0 {
		fmt.Fprintln(stderr, "[skipped] no aliases available for file transfer")
		return 0, true
	}
	localPath, ok, err := promptText(stderr, "Send file", "local file", "", validateNonEmpty("Local file"))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: file prompt failed: %v\n", err)
		return 1, false
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] send file cancelled")
		return 0, true
	}
	expanded, err := expandLocalPath(localPath)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1, true
	}
	if info, err := os.Stat(expanded); err != nil {
		fmt.Fprintf(stderr, "ssherpa: local file %s: %v\n", expanded, err)
		return 1, true
	} else if info.IsDir() {
		fmt.Fprintf(stderr, "ssherpa: local path %s is a directory; directory transfer is not implemented yet\n", expanded)
		return 1, true
	}

	alias, ok, err := pickAlias(inventory.Aliases, flags.NoColor, flags.ThemeName, flags.ThemeFile, "Send file: pick target", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return 1, false
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] send file cancelled")
		return 0, true
	}

	remoteDefault := filepath.Base(expanded)
	remotePath, ok, err := promptText(stderr, "Send file", "remote path", remoteDefault, validateNonEmpty("Remote path"))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: remote path prompt failed: %v\n", err)
		return 1, false
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] send file cancelled")
		return 0, true
	}

	args := []string{"--select", alias.Name, "--remote", remotePath}
	args = append(args, connectFlagsAsTransferArgs(flags)...)
	args = append(args, expanded)
	return runSend(args, stdout, stderr), true
}

func connectFlagsAsTransferArgs(flags connectFlags) []string {
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
	if flags.Print {
		args = append(args, "--print")
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

func resolveSFTPBinary(flags transferFlags) string {
	if strings.TrimSpace(flags.SFTPBinary) != "" {
		return flags.SFTPBinary
	}
	return strings.TrimSpace(os.Getenv("SSHERPA_SFTP_BINARY"))
}

func expandLocalPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("local path is required")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("resolve local path %s: %w", path, err)
		}
		path = abs
	}
	return filepath.Clean(path), nil
}
