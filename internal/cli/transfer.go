package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/session"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/state"
	"github.com/0xbenc/ssherpa/internal/ui"
)

const (
	filePickerHere   ui.ItemKind = "file_here"
	filePickerParent ui.ItemKind = "file_parent"
	filePickerDir    ui.ItemKind = "file_dir"
	filePickerFile   ui.ItemKind = "file"
	remotePickerHere ui.ItemKind = "remote_here"
	remotePickerDir  ui.ItemKind = "remote_dir"
	remotePickerUp   ui.ItemKind = "remote_up"
	remotePickerFile ui.ItemKind = "remote_file"

	transferTransportEnv = "SSHERPA_TRANSFER_TRANSPORT"
)

type transferFlags struct {
	inventoryFlags
	Print            bool
	Force            bool
	Select           string
	LocalPath        string
	RemotePath       string
	Hops             []string
	ControlPath      string
	PickLocalDir     bool
	ConfirmOverwrite bool
	SFTPBinary       string
	NoColor          bool
	ThemeName        string
	ThemeFile        string
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
		case arg == "--force":
			flags.Force = true
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
			value, ok = requireBinaryFlagValue(value, "--sftp-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SFTPBinary = value
		case strings.HasPrefix(arg, "--sftp-binary="):
			value, ok := requireBinaryFlagValue(strings.TrimPrefix(arg, "--sftp-binary="), "--sftp-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SFTPBinary = value
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
	code, _, _ = executeSFTPTransfer(direction, flags, alias, stdout, stderr)
	return code
}

func executeSFTPTransfer(direction sshcmd.SFTPTransferDirection, flags transferFlags, alias hostlist.Alias, stdout io.Writer, stderr io.Writer) (int, sshcmd.SFTPTransfer, bool) {
	transfer, ok, code := resolveTransferSpec(direction, flags, alias, stderr)
	if !ok {
		return code, sshcmd.SFTPTransfer{}, false
	}
	if err := sshcmd.ValidateSFTPTransfer(transfer); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1, transfer, false
	}
	cmd := sshcmd.BuildSFTP(resolveSFTPBinary(flags), transfer)
	if flags.Print {
		transfer.Batch = sshcmd.BuildSFTPBatch(transfer)
		fmt.Fprintf(stdout, "[print] %s\n", sshcmd.QuoteArgv(cmd.Argv))
		fmt.Fprintf(stdout, "[batch]\n%s", transfer.Batch)
		return 0, transfer, true
	}
	if !validateSFTPCommandBinary(cmd, flags, stderr) {
		return 1, transfer, false
	}
	proceed, code := confirmTransferSafety(flags, transfer, cmd, stderr)
	if !proceed {
		return code, transfer, false
	}
	if transfer.Direction == sshcmd.SFTPTransferReceive {
		return runSFTPReceive(cmd, &transfer, stdout, stderr), transfer, true
	}
	transfer.Batch = sshcmd.BuildSFTPBatch(transfer)
	return runSFTPCommand(cmd, transfer.Batch, stdout, stderr), transfer, true
}

// runSFTPReceive downloads into a temporary file beside the destination
// and renames it into place only after sftp exits cleanly. sftp's get
// truncates its target on open, so writing the final path directly meant
// an interrupted download destroyed the original file the user had just
// confirmed overwriting.
func runSFTPReceive(cmd sshcmd.Command, transfer *sshcmd.SFTPTransfer, stdout io.Writer, stderr io.Writer) int {
	finalPath := transfer.LocalPath
	temp, err := os.CreateTemp(filepath.Dir(finalPath), "."+filepath.Base(finalPath)+".ssherpa.*.part")
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: create temporary download file: %v\n", err)
		return 1
	}
	tempPath := temp.Name()
	_ = temp.Close()
	staged := *transfer
	staged.LocalPath = tempPath
	transfer.Batch = sshcmd.BuildSFTPBatch(staged)
	code := runSFTPCommand(cmd, transfer.Batch, stdout, stderr)
	if code != 0 {
		_ = os.Remove(tempPath)
		return code
	}
	// The rename replaces the destination inode outright: a hardlinked
	// or symlinked destination is detached rather than written through.
	// That is intended hardening — a link planted at the destination
	// cannot redirect the download elsewhere. Because os.CreateTemp
	// leaves the temp 0600, carry the destination's prior mode across
	// the overwrite (the user confirmed replacing that file, not
	// tightening it) or apply the umask-derived default for a fresh
	// download; on probe failure the temp's restrictive 0600 stands.
	if mode, err := downloadFileMode(finalPath, tempPath); err == nil {
		_ = os.Chmod(tempPath, mode)
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		fmt.Fprintf(stderr, "ssherpa: move downloaded file into place: %v\n", err)
		return 1
	}
	return 0
}

// downloadFileMode returns the permissions the renamed download should
// carry: the existing destination's mode when overwriting, otherwise
// the 0o666-minus-umask default a plain create would have produced. The
// umask is probed by creating a sibling file rather than via
// syscall.Umask, which would race other threads by mutating
// process-global state.
func downloadFileMode(finalPath string, tempPath string) (os.FileMode, error) {
	info, err := os.Stat(finalPath)
	if err == nil {
		return info.Mode().Perm(), nil
	}
	if !os.IsNotExist(err) {
		return 0, err
	}
	probePath := tempPath + ".mode"
	probe, err := os.OpenFile(probePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		return 0, err
	}
	probeInfo, err := probe.Stat()
	_ = probe.Close()
	_ = os.Remove(probePath)
	if err != nil {
		return 0, err
	}
	return probeInfo.Mode().Perm(), nil
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

func resolveTransferSpec(direction sshcmd.SFTPTransferDirection, flags transferFlags, alias hostlist.Alias, stderr io.Writer) (sshcmd.SFTPTransfer, bool, int) {
	localPath := strings.TrimSpace(flags.LocalPath)
	remotePath := strings.TrimSpace(flags.RemotePath)
	switch direction {
	case sshcmd.SFTPTransferSend:
		if localPath == "" {
			picked, ok, err := pickLocalFile(stderr, transferFilePickerOptions(flags), ".")
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: file picker failed: %v\n", err)
				return sshcmd.SFTPTransfer{}, false, 1
			}
			if !ok {
				fmt.Fprintln(stderr, "[skipped] send cancelled")
				return sshcmd.SFTPTransfer{}, false, 0
			}
			localPath = picked
		}
		expanded, err := expandLocalPath(localPath)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return sshcmd.SFTPTransfer{}, false, 1
		}
		info, err := os.Stat(expanded)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: local file %s: %v\n", expanded, err)
			return sshcmd.SFTPTransfer{}, false, 1
		}
		if info.IsDir() {
			fmt.Fprintf(stderr, "ssherpa: local path %s is a directory; directory transfer is not supported yet. Choose a file or archive the directory first.\n", expanded)
			return sshcmd.SFTPTransfer{}, false, 1
		}
		localPath = expanded
		if remotePath == "" {
			if flags.Print {
				remotePath = filepath.Base(localPath)
			} else {
				dir, ok, err := pickRemoteDirectory(stderr, transferFilePickerOptions(flags), flags, alias.Name, ".")
				if err != nil {
					fmt.Fprintf(stderr, "ssherpa: remote folder picker failed: %v\n", err)
					return sshcmd.SFTPTransfer{}, false, 1
				}
				if !ok {
					fmt.Fprintln(stderr, "[skipped] send cancelled")
					return sshcmd.SFTPTransfer{}, false, 0
				}
				remotePath = remoteJoin(dir, filepath.Base(localPath))
			}
		}
	case sshcmd.SFTPTransferReceive:
		if remotePath == "" {
			if flags.Print {
				fmt.Fprintln(stderr, "ssherpa: receive requires a remote file path")
				return sshcmd.SFTPTransfer{}, false, 1
			}
			picked, ok, err := pickRemoteFile(stderr, transferFilePickerOptions(flags), flags, alias.Name, ".")
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: remote file picker failed: %v\n", err)
				return sshcmd.SFTPTransfer{}, false, 1
			}
			if !ok {
				fmt.Fprintln(stderr, "[skipped] receive cancelled")
				return sshcmd.SFTPTransfer{}, false, 0
			}
			remotePath = picked
		}
		if localPath == "" {
			if flags.PickLocalDir && !flags.Print {
				dir, ok, err := pickLocalDirectory(stderr, transferFilePickerOptions(flags), ".")
				if err != nil {
					fmt.Fprintf(stderr, "ssherpa: local folder picker failed: %v\n", err)
					return sshcmd.SFTPTransfer{}, false, 1
				}
				if !ok {
					fmt.Fprintln(stderr, "[skipped] receive cancelled")
					return sshcmd.SFTPTransfer{}, false, 0
				}
				localPath = filepath.Join(dir, localNameForRemote(remotePath))
			} else {
				localPath = localNameForRemote(remotePath)
			}
		}
		expanded, err := expandLocalPath(localPath)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return sshcmd.SFTPTransfer{}, false, 1
		}
		if strings.HasSuffix(localPath, string(os.PathSeparator)) {
			expanded = filepath.Join(expanded, localNameForRemote(remotePath))
		} else if info, err := os.Stat(expanded); err == nil && info.IsDir() {
			expanded = filepath.Join(expanded, localNameForRemote(remotePath))
		}
		localPath = expanded
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown transfer direction %q\n", direction)
		return sshcmd.SFTPTransfer{}, false, 1
	}
	return sshcmd.SFTPTransfer{
		Direction:   direction,
		Alias:       alias.Name,
		Config:      flags.Config,
		Hops:        append([]string(nil), flags.Hops...),
		ControlPath: flags.ControlPath,
		LocalPath:   localPath,
		RemotePath:  remotePath,
	}, true, 0
}

func runSFTPCommand(cmd sshcmd.Command, batch string, stdout io.Writer, stderr io.Writer) int {
	if len(cmd.Argv) == 0 {
		fmt.Fprintln(stderr, "ssherpa: empty SFTP command")
		return 1
	}
	if err := sshcmd.ValidateCommandBinary(cmd, sshcmd.BinaryRequirement{Name: "sftp", Role: "SFTP client", Hint: sshcmd.OpenSFTPInstallHint}); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
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

func confirmTransferSafety(flags transferFlags, transfer sshcmd.SFTPTransfer, cmd sshcmd.Command, stderr io.Writer) (bool, int) {
	switch transfer.Direction {
	case sshcmd.SFTPTransferSend:
		info, err := remotePathInfo(cmd, transfer.RemotePath)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: cannot verify remote destination %s:%s: %v\n", transfer.Alias, transfer.RemotePath, err)
			return false, 1
		}
		if info.Exists && info.IsDir {
			// sftp's put writes <dir>/<basename> when the destination is
			// a directory, so the overwrite gate must check the real
			// final file path, not the directory itself.
			finalPath := remoteJoin(transfer.RemotePath, filepath.Base(transfer.LocalPath))
			finalInfo, err := remotePathInfo(cmd, finalPath)
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: cannot verify remote destination %s:%s: %v\n", transfer.Alias, finalPath, err)
				return false, 1
			}
			if finalInfo.Exists && finalInfo.IsDir {
				fmt.Fprintf(stderr, "ssherpa: remote destination %s:%s is a directory\n", transfer.Alias, finalPath)
				return false, 1
			}
			if finalInfo.Exists {
				return confirmOverwrite(flags, stderr, fmt.Sprintf("Remote destination %s:%s already exists. Overwrite it?", transfer.Alias, finalPath))
			}
			return true, 0
		}
		if info.Exists {
			return confirmOverwrite(flags, stderr, fmt.Sprintf("Remote destination %s:%s already exists. Overwrite it?", transfer.Alias, transfer.RemotePath))
		}
	case sshcmd.SFTPTransferReceive:
		info, err := remotePathInfo(cmd, transfer.RemotePath)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: cannot verify remote source %s:%s: %v\n", transfer.Alias, transfer.RemotePath, err)
			return false, 1
		}
		if !info.Exists {
			fmt.Fprintf(stderr, "ssherpa: remote source %s:%s does not exist\n", transfer.Alias, transfer.RemotePath)
			return false, 1
		}
		if info.IsDir {
			fmt.Fprintf(stderr, "ssherpa: remote path %s:%s is a directory; directory transfer is not supported yet. Choose a file or archive the directory first.\n", transfer.Alias, transfer.RemotePath)
			return false, 1
		}
		if _, err := os.Stat(transfer.LocalPath); err == nil {
			return confirmOverwrite(flags, stderr, fmt.Sprintf("Local destination %s already exists. Overwrite it?", transfer.LocalPath))
		} else if err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(stderr, "ssherpa: cannot verify local destination %s: %v\n", transfer.LocalPath, err)
			return false, 1
		}
	}
	return true, 0
}

func confirmOverwrite(flags transferFlags, stderr io.Writer, message string) (bool, int) {
	if flags.Force {
		return true, 0
	}
	if !flags.ConfirmOverwrite {
		fmt.Fprintf(stderr, "ssherpa: %s Use --force to overwrite.\n", strings.TrimSuffix(message, " Overwrite it?"))
		return false, 1
	}
	confirmed, answered, err := ui.Confirm(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Title:       "Overwrite destination",
		Message:     message,
		Danger:      true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: overwrite confirmation failed: %v\n", err)
		return false, 1
	}
	if !answered || !confirmed {
		fmt.Fprintln(stderr, "[skipped] transfer cancelled")
		return false, 0
	}
	return true, 0
}

type remotePathStat struct {
	Exists bool
	IsDir  bool
}

func remotePathInfo(cmd sshcmd.Command, remotePath string) (remotePathStat, error) {
	remotePath = strings.TrimSpace(remotePath)
	if remotePath == "/" {
		return remotePathStat{Exists: true, IsDir: true}, nil
	}
	parent := remotePathParent(remotePath)
	base := remoteBase(remotePath)
	if base == "" {
		return remotePathStat{}, fmt.Errorf("cannot resolve remote path name")
	}
	batch := fmt.Sprintf("cd %s\npwd\nls -la\n", quoteSFTPBatchPath(parent))
	var stdout, stderr strings.Builder
	code := runSFTPCommand(cmd, batch, &stdout, &stderr)
	if code != 0 {
		msg := strings.TrimSpace(stderr.String())
		if isRemoteMissingMessage(msg) {
			return remotePathStat{}, nil
		}
		if msg == "" {
			msg = fmt.Sprintf("sftp exited with code %d", code)
		}
		return remotePathStat{}, fmt.Errorf("%s", msg)
	}
	listing := parseRemoteListing(stdout.String())
	if listing.CWD == "" && len(listing.Entries) == 0 {
		return remotePathStat{}, fmt.Errorf("sftp did not return a parseable listing")
	}
	for _, entry := range listing.Entries {
		if entry.Name == base {
			return remotePathStat{Exists: true, IsDir: entry.IsDir}, nil
		}
	}
	return remotePathStat{}, nil
}

func isRemoteMissingMessage(message string) bool {
	message = strings.ToLower(message)
	return strings.Contains(message, "no such file") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "couldn't stat") ||
		strings.Contains(message, "cannot stat")
}

func remotePathParent(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return "."
	}
	cleaned := pathpkg.Clean(path)
	if cleaned == "/" {
		return "/"
	}
	parent := pathpkg.Dir(cleaned)
	if parent == "" {
		return "."
	}
	return parent
}

func runTransferFileBuilder(flags connectFlags, inventory hostlist.Inventory, stdout io.Writer, stderr io.Writer) (int, bool) {
	direction, ok, err := chooseTransferDirection(flags, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: transfer direction picker failed: %v\n", err)
		return 1, false
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] file transfer cancelled")
		return 0, true
	}
	switch direction {
	case sshcmd.SFTPTransferSend:
		return runSendFileBuilder(flags, inventory, stdout, stderr)
	case sshcmd.SFTPTransferReceive:
		return runReceiveFileBuilder(flags, inventory, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown transfer direction %q\n", direction)
		return 1, false
	}
}

func chooseTransferDirection(flags connectFlags, stderr io.Writer) (sshcmd.SFTPTransferDirection, bool, error) {
	direction, ok, err := ui.ChooseTransferDirection(context.Background(), ui.TransferDirectionOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
	})
	if err != nil {
		return "", false, err
	}
	if !ok {
		return "", false, nil
	}
	switch direction {
	case ui.TransferDirectionSend:
		return sshcmd.SFTPTransferSend, true, nil
	case ui.TransferDirectionReceive:
		return sshcmd.SFTPTransferReceive, true, nil
	default:
		return "", false, fmt.Errorf("unexpected transfer direction %q", direction)
	}
}

func runSendFileBuilder(flags connectFlags, inventory hostlist.Inventory, stdout io.Writer, stderr io.Writer) (int, bool) {
	if len(inventory.Aliases) == 0 {
		fmt.Fprintln(stderr, "[skipped] no aliases available for file transfer")
		return 0, true
	}
	localPath, ok, err := pickLocalFile(stderr, connectFilePickerOptions(flags), ".")
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: file picker failed: %v\n", err)
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
	var localInfo os.FileInfo
	if info, err := os.Stat(expanded); err != nil {
		fmt.Fprintf(stderr, "ssherpa: local file %s: %v\n", expanded, err)
		return 1, true
	} else if info.IsDir() {
		fmt.Fprintf(stderr, "ssherpa: local path %s is a directory; directory transfer is not supported yet. Choose a file or archive the directory first.\n", expanded)
		return 1, true
	} else {
		localInfo = info
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

	transferFlags := transferFlagsFromConnect(flags)
	transferFlags.ConfirmOverwrite = true
	transferFlags.LocalPath = expanded
	code, transfer, attempted := executeSFTPTransfer(sshcmd.SFTPTransferSend, transferFlags, alias, stdout, stderr)
	if code == 0 && attempted && !transferFlags.Print {
		if err := showSendComplete(stderr, flags, transfer, localInfo.Size()); err != nil {
			fmt.Fprintf(stderr, "ssherpa: send confirmation failed: %v\n", err)
			return 1, false
		}
	}
	return code, true
}

func runReceiveFileBuilder(flags connectFlags, inventory hostlist.Inventory, stdout io.Writer, stderr io.Writer) (int, bool) {
	if len(inventory.Aliases) == 0 {
		fmt.Fprintln(stderr, "[skipped] no aliases available for file transfer")
		return 0, true
	}
	alias, ok, err := pickAlias(inventory.Aliases, flags.NoColor, flags.ThemeName, flags.ThemeFile, "Receive file: pick source", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return 1, false
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] receive file cancelled")
		return 0, true
	}

	transferFlags := transferFlagsFromConnect(flags)
	transferFlags.PickLocalDir = true
	transferFlags.ConfirmOverwrite = true
	code, transfer, attempted := executeSFTPTransfer(sshcmd.SFTPTransferReceive, transferFlags, alias, stdout, stderr)
	if code == 0 && attempted && !transferFlags.Print {
		size := receivedSize(transfer)
		if err := showTransferComplete(stderr, transferCompleteOptions{
			NoColor:     flags.NoColor,
			ThemeName:   flags.ThemeName,
			ThemeFile:   flags.ThemeFile,
			Transfer:    transfer,
			Size:        size,
			Direction:   "receive",
			ReturnLabel: "press any key to return home",
		}); err != nil {
			fmt.Fprintf(stderr, "ssherpa: receive confirmation failed: %v\n", err)
			return 1, false
		}
	}
	return code, true
}

func transferFlagsFromConnect(flags connectFlags) transferFlags {
	return transferFlags{
		inventoryFlags:   flags.inventoryFlags,
		Print:            flags.Print,
		ConfirmOverwrite: true,
		NoColor:          flags.NoColor,
		ThemeName:        flags.ThemeName,
		ThemeFile:        flags.ThemeFile,
	}
}

func showSendComplete(stderr io.Writer, flags connectFlags, transfer sshcmd.SFTPTransfer, size int64) error {
	return showTransferComplete(stderr, transferCompleteOptions{
		NoColor:     flags.NoColor,
		ThemeName:   flags.ThemeName,
		ThemeFile:   flags.ThemeFile,
		Transfer:    transfer,
		Size:        humanBytes(size),
		Direction:   "send",
		ReturnLabel: "press any key to return home",
	})
}

type transferCompleteOptions struct {
	NoColor     bool
	ThemeName   string
	ThemeFile   string
	Transfer    sshcmd.SFTPTransfer
	Size        string
	Direction   string
	ReturnLabel string
}

func showTransferComplete(stderr io.Writer, opts transferCompleteOptions) error {
	return ui.ShowTransferComplete(context.Background(), ui.TransferCompleteOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:     opts.NoColor,
		ThemeName:   opts.ThemeName,
		ThemeFile:   opts.ThemeFile,
		LocalPath:   opts.Transfer.LocalPath,
		Alias:       opts.Transfer.Alias,
		RemotePath:  opts.Transfer.RemotePath,
		Size:        opts.Size,
		Direction:   opts.Direction,
		ReturnLabel: opts.ReturnLabel,
	})
}

func overlayTransferOptions(options connectOptions, metadata session.Metadata, stdout io.Writer, stderr io.Writer) session.OverlayOptions {
	overlay := session.OverlayOptions{
		Key:     options.OverlayKey,
		KeyName: options.OverlayKeyName,
	}
	if metadata.TargetAlias == "" || metadata.Kind != "" {
		return overlay
	}
	overlay.Send = func(req session.OverlayTransferRequest) int {
		return runOverlaySend(options, req, stdout, stderr)
	}
	overlay.Receive = func(req session.OverlayTransferRequest) int {
		return runOverlayReceive(options, req, stdout, stderr)
	}
	return overlay
}

func runOverlaySend(options connectOptions, req session.OverlayTransferRequest, stdout io.Writer, stderr io.Writer) int {
	alias := strings.TrimSpace(req.TargetAlias)
	if alias == "" {
		fmt.Fprintln(stderr, "ssherpa: overlay send requires a target alias")
		return 1
	}

	flags := transferFlags{
		inventoryFlags: inventoryFlags{Config: options.Config},
		Hops:           append([]string(nil), req.Hops...),
		NoColor:        options.NoColor,
		ThemeName:      options.ThemeName,
		ThemeFile:      options.ThemeFile,
		ControlPath:    req.ControlPath,
	}
	localPath, ok, err := pickLocalFile(stderr, transferFilePickerOptions(flags), ".")
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: file picker failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] send cancelled")
		return 0
	}
	expanded, err := expandLocalPath(localPath)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	localInfo, err := os.Stat(expanded)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: local file %s: %v\n", expanded, err)
		return 1
	}
	if localInfo.IsDir() {
		fmt.Fprintf(stderr, "ssherpa: local path %s is a directory; directory transfer is not supported yet. Choose a file or archive the directory first.\n", expanded)
		return 1
	}

	start := strings.TrimSpace(req.RemoteCWD)
	if start == "" {
		start = "."
	}
	forceInband := forceOverlayInbandSend()
	remotePath := ""
	directAvailable := !forceInband
	if forceInband {
		remotePath = remoteJoin(start, filepath.Base(expanded))
		fmt.Fprintf(stderr, "ssherpa: %s=inband set; skipping direct SFTP and using %s\n", transferTransportEnv, remotePath)
	} else {
		dir, ok, err := pickRemoteDirectory(stderr, transferFilePickerOptions(flags), flags, alias, start)
		if err != nil {
			directAvailable = false
			remotePath = remoteJoin(start, filepath.Base(expanded))
			fmt.Fprintf(stderr, "ssherpa: direct SFTP folder picker failed: %v\n", err)
		} else if !ok {
			fmt.Fprintln(stderr, "[skipped] send cancelled")
			return 0
		} else {
			remotePath = remoteJoin(dir, filepath.Base(expanded))
		}
	}

	var code int
	var transfer sshcmd.SFTPTransfer
	var attempted bool
	if directAvailable {
		flags.LocalPath = expanded
		flags.RemotePath = remotePath
		flags.ConfirmOverwrite = true
		code, transfer, attempted = executeSFTPTransfer(sshcmd.SFTPTransferSend, flags, hostlist.Alias{Name: alias}, stdout, stderr)
	}
	if code == 0 && attempted {
		recordTransferEvent(req.StateDir, req.SessionID, transfer)
		if err := showTransferComplete(stderr, transferCompleteOptions{
			NoColor:     options.NoColor,
			ThemeName:   options.ThemeName,
			ThemeFile:   options.ThemeFile,
			Transfer:    transfer,
			Size:        humanBytes(localInfo.Size()),
			Direction:   "send",
			ReturnLabel: "press any key to return to session",
		}); err != nil {
			fmt.Fprintf(stderr, "ssherpa: send confirmation failed: %v\n", err)
			return 1
		}
	}
	if code == 0 && attempted {
		return 0
	}
	if directAvailable && !attempted {
		return code
	}
	if req.InbandSend == nil {
		return defaultNonZero(code)
	}
	if err := validateOverlayInbandSend(req, forceInband); err != nil {
		fmt.Fprintf(stderr, "ssherpa: in-band send unavailable: %v\n", err)
		return defaultNonZero(code)
	}
	fmt.Fprintf(stderr, "ssherpa: using in-band PTY transfer to %s\n", remotePath)
	result, err := req.InbandSend(session.InbandSendRequest{
		LocalPath:  expanded,
		RemotePath: remotePath,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: in-band send failed: %v\n", err)
		return 1
	}
	recordInbandTransferEvent(req.StateDir, req.SessionID, result)
	if err := showTransferComplete(stderr, transferCompleteOptions{
		NoColor:   options.NoColor,
		ThemeName: options.ThemeName,
		ThemeFile: options.ThemeFile,
		Transfer: sshcmd.SFTPTransfer{
			Direction:  sshcmd.SFTPTransferSend,
			Alias:      alias,
			LocalPath:  result.LocalPath,
			RemotePath: result.RemotePath,
		},
		Size:        humanBytes(result.Size),
		Direction:   "send",
		ReturnLabel: "press any key to return to session",
	}); err != nil {
		fmt.Fprintf(stderr, "ssherpa: send confirmation failed: %v\n", err)
		return 1
	}
	return 0
}

func runOverlayReceive(options connectOptions, req session.OverlayTransferRequest, stdout io.Writer, stderr io.Writer) int {
	alias := strings.TrimSpace(req.TargetAlias)
	if alias == "" {
		fmt.Fprintln(stderr, "ssherpa: overlay receive requires a target alias")
		return 1
	}
	start := strings.TrimSpace(req.RemoteCWD)
	if start == "" {
		start = "."
	}
	flags := transferFlags{
		inventoryFlags: inventoryFlags{Config: options.Config},
		Hops:           append([]string(nil), req.Hops...),
		ControlPath:    req.ControlPath,
		NoColor:        options.NoColor,
		ThemeName:      options.ThemeName,
		ThemeFile:      options.ThemeFile,
	}
	remotePath, ok, err := pickRemoteFile(stderr, transferFilePickerOptions(flags), flags, alias, start)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: remote file picker failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] receive cancelled")
		return 0
	}
	dir, ok, err := pickLocalDirectory(stderr, transferFilePickerOptions(flags), ".")
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: local folder picker failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] receive cancelled")
		return 0
	}
	flags.RemotePath = remotePath
	flags.LocalPath = filepath.Join(dir, localNameForRemote(remotePath))
	flags.ConfirmOverwrite = true
	code, transfer, attempted := executeSFTPTransfer(sshcmd.SFTPTransferReceive, flags, hostlist.Alias{Name: alias}, stdout, stderr)
	if code == 0 && attempted {
		recordTransferEvent(req.StateDir, req.SessionID, transfer)
		if err := showTransferComplete(stderr, transferCompleteOptions{
			NoColor:     options.NoColor,
			ThemeName:   options.ThemeName,
			ThemeFile:   options.ThemeFile,
			Transfer:    transfer,
			Size:        receivedSize(transfer),
			Direction:   "receive",
			ReturnLabel: "press any key to return to session",
		}); err != nil {
			fmt.Fprintf(stderr, "ssherpa: receive confirmation failed: %v\n", err)
			return 1
		}
	}
	return code
}

func validateOverlayInbandSend(req session.OverlayTransferRequest, forcedMode bool) error {
	if req.InbandSend == nil {
		return fmt.Errorf("in-band sender is not available")
	}
	prompt := strings.TrimSpace(req.RemotePrompt)
	if prompt != state.RemotePromptPrompt {
		if forcedMode && prompt == state.RemotePromptPromptStart {
			// Forced in-band mode exists for live Transport C testing on shells
			// that emit OSC 133 prompt-start but never publish prompt-complete.
		} else if prompt == "" {
			return fmt.Errorf("remote prompt state is unknown; open a normal shell prompt and try again")
		} else {
			return fmt.Errorf("remote prompt state is %q, not idle", req.RemotePrompt)
		}
	}
	if strings.TrimSpace(req.RemoteCWD) == "" && !forcedMode {
		return fmt.Errorf("remote cwd is unknown")
	}
	return nil
}

func forceOverlayInbandSend() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(transferTransportEnv))) {
	case "inband", "in-band", "pty", "transport-c", "c":
		return true
	default:
		return false
	}
}

func defaultNonZero(code int) int {
	if code == 0 {
		return 1
	}
	return code
}

func recordTransferEvent(stateDir string, sessionID string, transfer sshcmd.SFTPTransfer) {
	stateDir = strings.TrimSpace(stateDir)
	sessionID = strings.TrimSpace(sessionID)
	if stateDir == "" || sessionID == "" {
		return
	}
	record, err := state.ReadRecord(stateDir, sessionID)
	if err != nil {
		return
	}
	hash, size := transferDigest(transfer)
	remote := transfer.Alias + ":" + transfer.RemotePath
	message := fmt.Sprintf("transport=sftp local=%q remote=%q bytes=%d sha256=%s", transfer.LocalPath, remote, size, hash)
	record.Events = append(record.Events, state.SessionEvent{
		Time:    time.Now().UTC(),
		Type:    "transfer_" + string(transfer.Direction),
		Message: message,
	})
	_ = state.WriteRecord(stateDir, record)
}

func recordInbandTransferEvent(stateDir string, sessionID string, result session.InbandSendResult) {
	stateDir = strings.TrimSpace(stateDir)
	sessionID = strings.TrimSpace(sessionID)
	if stateDir == "" || sessionID == "" {
		return
	}
	record, err := state.ReadRecord(stateDir, sessionID)
	if err != nil {
		return
	}
	message := fmt.Sprintf("transport=inband local=%q remote=%q bytes=%d sha256=%s", result.LocalPath, result.RemotePath, result.Size, result.SHA256)
	record.Events = append(record.Events, state.SessionEvent{
		Time:    time.Now().UTC(),
		Type:    "transfer_send",
		Message: message,
	})
	_ = state.WriteRecord(stateDir, record)
}

func transferDigest(transfer sshcmd.SFTPTransfer) (string, int64) {
	path := transfer.LocalPath
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", 0
	}
	file, err := os.Open(path)
	if err != nil {
		return "", info.Size()
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", info.Size()
	}
	return hex.EncodeToString(hash.Sum(nil)), info.Size()
}

func receivedSize(transfer sshcmd.SFTPTransfer) string {
	info, err := os.Stat(transfer.LocalPath)
	if err != nil || info.IsDir() {
		return ""
	}
	return humanBytes(info.Size())
}

type filePickerOptions struct {
	NoColor   bool
	ThemeName string
	ThemeFile string
}

func transferFilePickerOptions(flags transferFlags) filePickerOptions {
	return filePickerOptions{NoColor: flags.NoColor, ThemeName: flags.ThemeName, ThemeFile: flags.ThemeFile}
}

func connectFilePickerOptions(flags connectFlags) filePickerOptions {
	return filePickerOptions{NoColor: flags.NoColor, ThemeName: flags.ThemeName, ThemeFile: flags.ThemeFile}
}

func transferBrowserOptions(output io.Writer, opts filePickerOptions, title string, mode string, locationLabel string, location string, steps []string, currentStep int) ui.TransferBrowserOptions {
	return ui.TransferBrowserOptions{
		Input:         os.Stdin,
		Output:        output,
		NoAltScreen:   envBool("SSHERPA_NO_ALT_SCREEN"),
		NoColor:       opts.NoColor,
		ThemeName:     opts.ThemeName,
		ThemeFile:     opts.ThemeFile,
		Title:         title,
		Mode:          mode,
		LocationLabel: locationLabel,
		Location:      location,
		Steps:         steps,
		CurrentStep:   currentStep,
	}
}

func sendTransferSteps() []string {
	return []string{"direction", "local", "host", "remote", "run"}
}

func receiveTransferSteps() []string {
	return []string{"direction", "host", "remote", "local", "run"}
}

func pickLocalFile(stderr io.Writer, opts filePickerOptions, start string) (string, bool, error) {
	return pickLocalFileWith(stderr, opts, start, "SSHERPA SEND SOURCE", "local-file", "LOCAL", sendTransferSteps(), 1)
}

// pickLocalFileWith drives the shared file browser to pick one local file,
// looping on directory navigation. The title/mode/steps let callers reuse it
// outside the send-transfer flow (e.g. importing a config or transcript
// bundle) without duplicating the navigation loop.
func pickLocalFileWith(stderr io.Writer, opts filePickerOptions, start string, title string, mode string, locationLabel string, steps []string, currentStep int) (string, bool, error) {
	cwd, err := expandLocalPath(start)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		cwd = filepath.Dir(cwd)
	}

	for {
		items, err := localFilePickerItems(cwd)
		if err != nil {
			return "", false, err
		}
		browserOpts := transferBrowserOptions(stderr, opts, title, mode, locationLabel, cwd, steps, currentStep)
		item, ok, err := ui.BrowseTransfer(context.Background(), items, browserOpts)
		if err != nil || !ok {
			return "", ok, err
		}
		switch item.Kind {
		case filePickerParent, filePickerDir:
			cwd = item.Token
		case filePickerFile:
			return item.Token, true, nil
		}
	}
}

func pickLocalDirectory(stderr io.Writer, opts filePickerOptions, start string) (string, bool, error) {
	cwd, err := expandLocalPath(start)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(cwd)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		cwd = filepath.Dir(cwd)
	}

	for {
		items, err := localDirectoryPickerItems(cwd)
		if err != nil {
			return "", false, err
		}
		browserOpts := transferBrowserOptions(stderr, opts, "SSHERPA RECEIVE TARGET", "local-folder", "LOCAL", cwd, receiveTransferSteps(), 3)
		browserOpts.Footer = "enter open/use / type filter / arrows move / shift+arrows section / Q cancel"
		item, ok, err := ui.BrowseTransfer(context.Background(), items, browserOpts)
		if err != nil || !ok {
			return "", ok, err
		}
		switch item.Kind {
		case filePickerHere:
			return item.Token, true, nil
		case filePickerParent, filePickerDir:
			cwd = item.Token
		}
	}
}

func localFilePickerItems(dir string) ([]ui.Item, error) {
	dir, err := expandLocalPath(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}

	items := []ui.Item{}
	parent := filepath.Dir(dir)
	if parent != dir {
		items = append(items, ui.Item{
			Kind:        filePickerParent,
			Token:       parent,
			Title:       "..",
			Description: parent,
			Group:       "Directories",
			Badge:       "up",
		})
	}

	type fileEntry struct {
		item  ui.Item
		isDir bool
		name  string
	}
	files := make([]fileEntry, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name)
		if info.IsDir() {
			files = append(files, fileEntry{
				isDir: true,
				name:  strings.ToLower(name),
				item: ui.Item{
					Kind:        filePickerDir,
					Token:       path,
					Title:       name + string(os.PathSeparator),
					Description: path,
					Group:       "Directories",
					Badge:       "dir",
				},
			})
			continue
		}
		files = append(files, fileEntry{
			name: strings.ToLower(name),
			item: ui.Item{
				Kind:        filePickerFile,
				Token:       path,
				Title:       name,
				Description: filePickerDescription(info),
				Detail:      path,
				Group:       "Files",
				Badge:       "file",
			},
		})
	}
	sort.SliceStable(files, func(i, j int) bool {
		if files[i].isDir != files[j].isDir {
			return files[i].isDir
		}
		return files[i].name < files[j].name
	})
	for _, file := range files {
		items = append(items, file.item)
	}
	return items, nil
}

func localDirectoryPickerItems(dir string) ([]ui.Item, error) {
	dir, err := expandLocalPath(dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read directory %s: %w", dir, err)
	}

	items := []ui.Item{{
		Kind:        filePickerHere,
		Token:       dir,
		Title:       "Use this folder",
		Description: dir,
		Group:       "Current",
		Badge:       "use",
	}}
	parent := filepath.Dir(dir)
	if parent != dir {
		items = append(items, ui.Item{
			Kind:        filePickerParent,
			Token:       parent,
			Title:       "..",
			Description: parent,
			Group:       "Directories",
			Badge:       "up",
		})
	}

	type dirEntry struct {
		item ui.Item
		name string
	}
	dirs := make([]dirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		path := filepath.Join(dir, name)
		dirs = append(dirs, dirEntry{
			name: strings.ToLower(name),
			item: ui.Item{
				Kind:        filePickerDir,
				Token:       path,
				Title:       name + string(os.PathSeparator),
				Description: path,
				Group:       "Directories",
				Badge:       "dir",
			},
		})
	}
	sort.SliceStable(dirs, func(i, j int) bool {
		return dirs[i].name < dirs[j].name
	})
	for _, dir := range dirs {
		items = append(items, dir.item)
	}
	return items, nil
}

func filePickerDescription(info os.FileInfo) string {
	mod := info.ModTime()
	when := "unknown"
	if !mod.IsZero() {
		when = mod.Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("%s  modified %s", humanBytes(info.Size()), when)
}

func humanBytes(size int64) string {
	if size < 0 {
		size = 0
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	value := float64(size)
	unit := 0
	for value >= 1024 && unit < len(units)-1 {
		value /= 1024
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d %s", size, units[unit])
	}
	if value >= 10 {
		return fmt.Sprintf("%.0f %s", value, units[unit])
	}
	return fmt.Sprintf("%.1f %s", value, units[unit])
}

type remoteDirectoryListing struct {
	CWD  string
	Dirs []string
}

type remoteListing struct {
	CWD     string
	Entries []remoteEntry
}

type remoteEntry struct {
	Name  string
	IsDir bool
	Size  string
}

func pickRemoteFile(stderr io.Writer, opts filePickerOptions, flags transferFlags, alias string, start string) (string, bool, error) {
	current := strings.TrimSpace(start)
	if current == "" {
		current = "."
	}
	for {
		listing, err := listRemoteEntries(flags, alias, current)
		if err != nil {
			return "", false, err
		}
		if listing.CWD != "" {
			current = listing.CWD
		}
		items := remoteFilePickerItems(current, listing.Entries)
		browserOpts := transferBrowserOptions(stderr, opts, "SSHERPA RECEIVE SOURCE", "remote-file", alias, current, receiveTransferSteps(), 2)
		item, ok, err := ui.BrowseTransfer(context.Background(), items, browserOpts)
		if err != nil || !ok {
			return "", ok, err
		}
		switch item.Kind {
		case remotePickerFile:
			return item.Token, true, nil
		case remotePickerUp, remotePickerDir:
			current = item.Token
		}
	}
}

func pickRemoteDirectory(stderr io.Writer, opts filePickerOptions, flags transferFlags, alias string, start string) (string, bool, error) {
	current := strings.TrimSpace(start)
	if current == "" {
		current = "."
	}
	for {
		listing, err := listRemoteDirectories(flags, alias, current)
		if err != nil {
			return "", false, err
		}
		if listing.CWD != "" {
			current = listing.CWD
		}
		items := remoteDirectoryPickerItems(current, listing.Dirs)
		browserOpts := transferBrowserOptions(stderr, opts, "SSHERPA SEND TARGET", "remote-folder", alias, current, sendTransferSteps(), 3)
		browserOpts.Footer = "enter open/use / type filter / arrows move / shift+arrows section / Q cancel"
		item, ok, err := ui.BrowseTransfer(context.Background(), items, browserOpts)
		if err != nil || !ok {
			return "", ok, err
		}
		switch item.Kind {
		case remotePickerHere:
			return item.Token, true, nil
		case remotePickerUp, remotePickerDir:
			current = item.Token
		}
	}
}

func listRemoteDirectories(flags transferFlags, alias string, dir string) (remoteDirectoryListing, error) {
	listing, err := listRemoteEntries(flags, alias, dir)
	if err != nil {
		return remoteDirectoryListing{}, err
	}
	dirs := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		if entry.IsDir {
			dirs = append(dirs, entry.Name)
		}
	}
	return remoteDirectoryListing{CWD: listing.CWD, Dirs: dirs}, nil
}

func listRemoteEntries(flags transferFlags, alias string, dir string) (remoteListing, error) {
	transfer := sshcmd.SFTPTransfer{
		Alias:       alias,
		Config:      flags.Config,
		Hops:        append([]string(nil), flags.Hops...),
		ControlPath: flags.ControlPath,
	}
	cmd := sshcmd.BuildSFTP(resolveSFTPBinary(flags), transfer)
	if err := sshcmd.ValidateCommandBinary(cmd, sftpBinaryRequirement(flags)); err != nil {
		return remoteListing{}, err
	}
	batch := fmt.Sprintf("cd %s\npwd\nls -la\n", quoteSFTPBatchPath(dir))
	var stdout, stderr strings.Builder
	code := runSFTPCommand(cmd, batch, &stdout, &stderr)
	if code != 0 {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = fmt.Sprintf("sftp exited with code %d", code)
		}
		return remoteListing{}, fmt.Errorf("%s", msg)
	}
	listing := parseRemoteListing(stdout.String())
	if listing.CWD == "" {
		listing.CWD = dir
	}
	return listing, nil
}

func parseRemoteDirectoryListing(output string) remoteDirectoryListing {
	listing := parseRemoteListing(output)
	dirs := make([]string, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		if entry.IsDir {
			dirs = append(dirs, entry.Name)
		}
	}
	return remoteDirectoryListing{CWD: listing.CWD, Dirs: dirs}
}

func parseRemoteListing(output string) remoteListing {
	var cwd string
	entries := []remoteEntry{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if cwdValue, ok := strings.CutPrefix(line, "Remote working directory: "); ok {
			cwd = strings.TrimSpace(cwdValue)
			continue
		}
		entry, ok := parseSFTPLongEntry(line)
		if !ok || entry.Name == "." || entry.Name == ".." {
			continue
		}
		entries = append(entries, entry)
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].IsDir != entries[j].IsDir {
			return entries[i].IsDir
		}
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	return remoteListing{CWD: cwd, Entries: entries}
}

func parseSFTPLongDirectoryName(line string) (string, bool) {
	entry, ok := parseSFTPLongEntry(line)
	if !ok || !entry.IsDir {
		return "", false
	}
	return entry.Name, true
}

func parseSFTPLongEntry(line string) (remoteEntry, bool) {
	if line == "" {
		return remoteEntry{}, false
	}
	kind := line[0]
	if kind != 'd' && kind != '-' && kind != 'l' {
		return remoteEntry{}, false
	}
	fields := strings.Fields(line)
	if len(fields) < 9 {
		return remoteEntry{}, false
	}
	name := strings.Join(fields[8:], " ")
	if target, _, ok := strings.Cut(name, " -> "); ok {
		name = target
	}
	if strings.TrimSpace(name) == "" {
		return remoteEntry{}, false
	}
	return remoteEntry{Name: name, IsDir: kind == 'd', Size: fields[4]}, true
}

func remoteFilePickerItems(cwd string, entries []remoteEntry) []ui.Item {
	items := []ui.Item{}
	parent := remoteParent(cwd)
	if parent != cwd {
		items = append(items, ui.Item{
			Kind:        remotePickerUp,
			Token:       parent,
			Title:       "..",
			Description: parent,
			Group:       "Directories",
			Badge:       "up",
		})
	}
	for _, entry := range entries {
		path := remoteJoin(cwd, entry.Name)
		if entry.IsDir {
			items = append(items, ui.Item{
				Kind:        remotePickerDir,
				Token:       path,
				Title:       entry.Name + "/",
				Description: path,
				Group:       "Directories",
				Badge:       "dir",
			})
			continue
		}
		items = append(items, ui.Item{
			Kind:        remotePickerFile,
			Token:       path,
			Title:       entry.Name,
			Description: remoteFileDescription(entry),
			Detail:      path,
			Group:       "Files",
			Badge:       "file",
		})
	}
	return items
}

func remoteFileDescription(entry remoteEntry) string {
	if entry.Size == "" {
		return ""
	}
	return entry.Size + " B"
}

// remoteBase returns the final path element, or "" when the path has no
// usable name ("/", ".", ".."). The empty string — not a literal
// placeholder — signals "unresolvable" so that genuine remote files
// named like any fallback (a file literally called "download") are never
// misclassified.
func remoteBase(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		return ""
	}
	base := pathpkg.Base(path)
	if base == "." || base == ".." || base == "/" || base == "" {
		return ""
	}
	return base
}

// localNameForRemote chooses the default local filename for a received
// remote path. "download" is only a last-resort display default here; it
// is no longer an internal sentinel anywhere.
func localNameForRemote(remotePath string) string {
	if base := remoteBase(remotePath); base != "" {
		return base
	}
	return "download"
}

func remoteDirectoryPickerItems(cwd string, dirs []string) []ui.Item {
	items := []ui.Item{{
		Kind:        remotePickerHere,
		Token:       cwd,
		Title:       "Use this folder",
		Description: cwd,
		Group:       "Current",
		Badge:       "use",
	}}
	parent := remoteParent(cwd)
	if parent != cwd {
		items = append(items, ui.Item{
			Kind:        remotePickerUp,
			Token:       parent,
			Title:       "..",
			Description: parent,
			Group:       "Directories",
			Badge:       "up",
		})
	}
	for _, dir := range dirs {
		items = append(items, ui.Item{
			Kind:        remotePickerDir,
			Token:       remoteJoin(cwd, dir),
			Title:       dir + "/",
			Description: remoteJoin(cwd, dir),
			Group:       "Directories",
			Badge:       "dir",
		})
	}
	return items
}

func remoteParent(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" || dir == "." {
		return ".."
	}
	cleaned := pathpkg.Clean(dir)
	parent := pathpkg.Dir(cleaned)
	if cleaned == "/" {
		return "/"
	}
	if parent == "." && !strings.HasPrefix(cleaned, "/") {
		return ".."
	}
	return parent
}

func remoteJoin(dir string, name string) string {
	dir = strings.TrimSpace(dir)
	name = strings.TrimSpace(name)
	if dir == "" || dir == "." {
		return name
	}
	if dir == "/" {
		return "/" + name
	}
	return pathpkg.Clean(dir + "/" + name)
}

func quoteSFTPBatchPath(path string) string {
	if path == "" {
		return `""`
	}
	if isSafeSFTPBatchPath(path) {
		return path
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(path) + `"`
}

func isSafeSFTPBatchPath(path string) bool {
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("@%_+=:,./~-", r):
		default:
			return false
		}
	}
	return true
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
