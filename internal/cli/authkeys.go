package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/0xbenc/ssherpa/internal/authkeys"
	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/ui"
)

type authkeysFlags struct {
	Path          string
	DryRun        bool
	Yes           bool
	JSON          bool
	Key           string
	KeyFile       string
	FromDir       string
	Fingerprints  []string
	SSHKeygenPath string
}

type authkeysListOutput struct {
	Path        string                `json:"path"`
	Keys        []authkeysKeyOutput   `json:"keys"`
	Diagnostics []authkeys.Diagnostic `json:"diagnostics,omitempty"`
}

type authkeysKeyOutput struct {
	Options     string `json:"options,omitempty"`
	Type        string `json:"type"`
	Blob        string `json:"blob"`
	Comment     string `json:"comment,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Source      string `json:"source,omitempty"`
	Line        int    `json:"line,omitempty"`
}

func runAuthkeys(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		flags, ok := parseAuthkeysMenuFlags(args, stderr)
		if !ok {
			return 1
		}
		return runAuthkeysInteractive(flags, stdout, stderr)
	}

	subcommand := args[0]
	switch subcommand {
	case "list":
		return runAuthkeysList(args[1:], stdout, stderr)
	case "add":
		return runAuthkeysAdd(args[1:], stdout, stderr)
	case "merge":
		return runAuthkeysMerge(args[1:], stdout, stderr)
	case "replace":
		return runAuthkeysReplace(args[1:], stdout, stderr)
	case "delete", "remove":
		return runAuthkeysDelete(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown authkeys command %q\n", subcommand)
		return 1
	}
}

func runAuthkeysList(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseAuthkeysListFlags(args, stderr)
	if !ok {
		return 1
	}
	path, err := authorizedKeysPath(flags.Path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	doc, err := authkeys.ReadDocument(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	keys := authkeysKeyOutputs(doc.Keys())
	if flags.JSON {
		writeJSON(stdout, authkeysListOutput{
			Path:        path,
			Keys:        keys,
			Diagnostics: doc.Diagnostics,
		})
		return 0
	}

	for _, diagnostic := range doc.Diagnostics {
		printAuthkeysDiagnostic(stderr, diagnostic)
	}
	if len(keys) == 0 {
		fmt.Fprintf(stdout, "[empty] no keys found in %s\n", path)
		return 0
	}
	for _, key := range keys {
		comment := key.Comment
		if comment == "" {
			comment = "-"
		}
		if key.Options != "" {
			fmt.Fprintf(stdout, "%s\t%s\t%s\toptions=%s\n", key.Fingerprint, key.Type, comment, key.Options)
		} else {
			fmt.Fprintf(stdout, "%s\t%s\t%s\n", key.Fingerprint, key.Type, comment)
		}
	}
	return 0
}

func runAuthkeysAdd(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseAuthkeysAddFlags(args, stderr)
	if !ok {
		return 1
	}
	if (flags.Key == "") == (flags.KeyFile == "") {
		fmt.Fprintln(stderr, "ssherpa: authkeys add requires exactly one of --key or --key-file")
		return 1
	}
	if !validateExplicitSSHKeygen(flags, stderr) {
		return 1
	}

	path, err := authorizedKeysPath(flags.Path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	key, diagnostics, err := authkeysKeyFromFlags(flags)
	for _, diagnostic := range diagnostics {
		printAuthkeysDiagnostic(stderr, diagnostic)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	plan, err := authkeys.PlanAdd(path, key, authkeys.PlanOptions{Validator: authkeysValidator(flags)})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	printAuthkeysDiagnostics(stderr, plan.Diagnostics)
	if !flags.DryRun && !flags.Yes && plan.Changed {
		ok, err := confirmActionChoice(stderr, "Add authorized key", "1 key to "+path)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: add confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys add cancelled")
			return 0
		}
	}
	return applyAuthkeysPlan(plan, flags, stdout, stderr)
}

func runAuthkeysMerge(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseAuthkeysDirFlags(args, stderr, "merge")
	if !ok {
		return 1
	}
	if flags.FromDir == "" {
		fmt.Fprintln(stderr, "ssherpa: authkeys merge requires --from-dir")
		return 1
	}
	if !validateExplicitSSHKeygen(flags, stderr) {
		return 1
	}

	path, err := authorizedKeysPath(flags.Path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	plan, err := authkeys.PlanMerge(path, flags.FromDir, authkeys.PlanOptions{Validator: authkeysValidator(flags)})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	printAuthkeysDiagnostics(stderr, plan.Diagnostics)
	if !flags.DryRun && !flags.Yes && plan.Changed {
		ok, err := confirmActionChoice(stderr, "Merge authorized_keys", fmt.Sprintf("%d new key(s) into %s", plan.Stats.Added, path))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: merge confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys merge cancelled")
			return 0
		}
	}
	return applyAuthkeysPlan(plan, flags, stdout, stderr)
}

func runAuthkeysReplace(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseAuthkeysDirFlags(args, stderr, "replace")
	if !ok {
		return 1
	}
	if flags.FromDir == "" {
		fmt.Fprintln(stderr, "ssherpa: authkeys replace requires --from-dir")
		return 1
	}
	if !validateExplicitSSHKeygen(flags, stderr) {
		return 1
	}

	path, err := authorizedKeysPath(flags.Path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	plan, err := authkeys.PlanReplace(path, flags.FromDir, authkeys.PlanOptions{Validator: authkeysValidator(flags)})
	if err != nil {
		printAuthkeysDiagnostics(stderr, plan.Diagnostics)
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	printAuthkeysDiagnostics(stderr, plan.Diagnostics)
	if !flags.DryRun && !flags.Yes && plan.Changed {
		ok, err := confirmActionChoice(stderr, "Replace authorized_keys", fmt.Sprintf("%s with %d key(s)", path, len(plan.Keys)))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: replace confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys replace cancelled")
			return 0
		}
	}
	return applyAuthkeysPlan(plan, flags, stdout, stderr)
}

func runAuthkeysDelete(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseAuthkeysDeleteFlags(args, stderr)
	if !ok {
		return 1
	}
	if len(flags.Fingerprints) == 0 {
		fmt.Fprintln(stderr, "ssherpa: authkeys delete requires --fingerprint")
		return 1
	}

	path, err := authorizedKeysPath(flags.Path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	plan, err := authkeys.PlanDelete(path, flags.Fingerprints)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	printAuthkeysDiagnostics(stderr, plan.Diagnostics)
	for _, fp := range plan.NotFound {
		fmt.Fprintf(stderr, "Warning: fingerprint not found: %s\n", fp)
	}
	if !flags.DryRun && !flags.Yes && plan.Changed {
		ok, err := confirmDeleteChoice(stderr, "Delete authorized_keys entries", fmt.Sprintf("%d key(s) from %s", plan.Stats.Deleted, path))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: delete confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys delete cancelled")
			return 0
		}
	}
	return applyAuthkeysPlan(plan, flags, stdout, stderr)
}

func runAuthkeysInteractive(flags authkeysFlags, stdout io.Writer, stderr io.Writer) int {
	if !validateExplicitSSHKeygen(flags, stderr) {
		return 1
	}

	path, err := authorizedKeysPath(flags.Path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	for {
		item, ok, err := ui.ChooseManagement(context.Background(), authkeysMenuItems(), ui.ManagementChooserOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			Title:       "authorized_keys manager",
			Mode:        "manage authorized_keys",
			Steps:       []string{"action", "input", "confirm"},
			CurrentStep: 0,
			Summary:     path,
			Footer:      "enter select  /  type filter  /  arrows move  /  shift+arrows section  /  Q back",
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: authkeys picker failed: %v\n", err)
			return 1
		}
		if !ok || item.Token == "back" {
			fmt.Fprintln(stdout, "[skipped] authkeys cancelled")
			return 0
		}

		switch item.Token {
		case "add":
			line, ok, err := promptText(stderr, "Add authorized key", "key", "", validateNonEmpty("SSH public key"))
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: %v\n", err)
				return 1
			}
			if !ok {
				fmt.Fprintln(stdout, "[skipped] authkeys add cancelled")
				continue
			}
			if strings.TrimSpace(line) == "" {
				fmt.Fprintln(stdout, "[skipped] empty key; nothing added")
				continue
			}
			runFlags := []string{"--path", path, "--key", line}
			if flags.SSHKeygenPath != "" {
				runFlags = append(runFlags, "--ssh-keygen", flags.SSHKeygenPath)
			}
			if code := runAuthkeysAdd(runFlags, stdout, stderr); code != 0 {
				return code
			}
		case "merge":
			dir, ok, err := promptText(stderr, "Merge authorized_keys", "directory", ".", validateNonEmpty("Directory"))
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: %v\n", err)
				return 1
			}
			if !ok {
				fmt.Fprintln(stdout, "[skipped] authkeys merge cancelled")
				continue
			}
			if code := runAuthkeysMerge(authkeysInteractiveDirArgs(path, dir, flags), stdout, stderr); code != 0 {
				return code
			}
		case "replace":
			dir, ok, err := promptText(stderr, "Replace authorized_keys", "directory", ".", validateNonEmpty("Directory"))
			if err != nil {
				fmt.Fprintf(stderr, "ssherpa: %v\n", err)
				return 1
			}
			if !ok {
				fmt.Fprintln(stdout, "[skipped] authkeys replace cancelled")
				continue
			}
			if code := runAuthkeysReplace(authkeysInteractiveDirArgs(path, dir, flags), stdout, stderr); code != 0 {
				return code
			}
		case "delete":
			fingerprint, ok, code := pickAuthkeysFingerprint(path, stderr)
			if !ok {
				if code != 0 {
					return code
				}
				continue
			}
			runFlags := []string{"--path", path, "--fingerprint", fingerprint}
			if code := runAuthkeysDelete(runFlags, stdout, stderr); code != 0 {
				return code
			}
		}
	}
}

func authkeysInteractiveDirArgs(path string, dir string, flags authkeysFlags) []string {
	args := []string{"--path", path, "--from-dir", dir}
	if flags.SSHKeygenPath != "" {
		args = append(args, "--ssh-keygen", flags.SSHKeygenPath)
	}
	return args
}

func pickAuthkeysFingerprint(path string, stderr io.Writer) (string, bool, int) {
	doc, err := authkeys.ReadDocument(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return "", false, 1
	}
	items := authkeysFingerprintItems(doc.Keys())
	if len(items) == 0 {
		fmt.Fprintf(stderr, "[skipped] no keys found in %s\n", path)
		return "", false, 0
	}

	item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Select key to delete",
		Mode:        "choose key fingerprint",
		Steps:       []string{"key", "confirm"},
		CurrentStep: 0,
		Summary:     authkeysCountLabel(len(items), "key", "keys"),
		Footer:      "enter select  /  type filter  /  arrows move  /  shift+arrows section  /  Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: authkeys picker failed: %v\n", err)
		return "", false, 1
	}
	if !ok {
		fmt.Fprintln(stderr, "[skipped] authkeys delete cancelled")
		return "", false, 0
	}
	return item.Token, true, 0
}

func authkeysMenuItems() []ui.ManagementItem {
	return []ui.ManagementItem{
		{Kind: ui.ItemAuthkeys, Token: "add", Title: "Add single key (paste)", Description: "append one public key", Group: "Add Keys", Badge: "add", Action: "Paste one public key and append it"},
		{Kind: ui.ItemAuthkeys, Token: "merge", Title: "Add keys from directory (merge)", Description: "read authorized_keys/ or *.pub", Group: "Add Keys", Badge: "merge", Action: "Merge keys from a directory without removing existing keys"},
		{Kind: ui.ItemConfirmDelete, Token: "replace", Title: "Replace keys from directory (overwrite)", Description: "backup, then replace file contents", Group: "Overwrite", Badge: "repl", Action: "Replace authorized_keys after confirmation"},
		{Kind: ui.ItemConfirmDelete, Token: "delete", Title: "Delete keys", Description: "select one key to remove", Group: "Remove", Badge: "delete", Action: "Choose one key fingerprint to delete"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to the previous menu", Group: "Navigation", Badge: "back", Action: "Return without changing authorized_keys"},
	}
}

func authkeysFingerprintItems(keys []authkeys.AuthorizedKey) []ui.ManagementItem {
	items := make([]ui.ManagementItem, 0, len(keys))
	for _, key := range keys {
		fp, err := key.SHA256Fingerprint()
		if err != nil {
			continue
		}
		title := fp
		if key.Comment != "" {
			title = key.Comment
		}
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemConfirmDelete,
			Token:       fp,
			Title:       title,
			Description: key.Type + "  " + fp,
			Detail:      authkeysKeyDetail(key),
			Group:       "Authorized Keys",
			Badge:       authkeysKeyBadge(key.Type),
			Action:      "Delete this key after confirmation",
		})
	}
	return items
}

func authkeysKeyDetail(key authkeys.AuthorizedKey) string {
	var parts []string
	if key.Source != "" {
		if key.Line > 0 {
			parts = append(parts, fmt.Sprintf("%s:%d", key.Source, key.Line))
		} else {
			parts = append(parts, key.Source)
		}
	}
	if key.Options != "" {
		parts = append(parts, "options="+key.Options)
	}
	if key.Comment != "" {
		parts = append(parts, "comment="+key.Comment)
	}
	return strings.Join(parts, "  ")
}

func authkeysKeyBadge(keyType string) string {
	keyType = strings.TrimPrefix(strings.TrimSpace(keyType), "ssh-")
	keyType = strings.TrimPrefix(keyType, "sk-")
	switch {
	case strings.Contains(keyType, "ed25519"):
		return "ed255"
	case strings.Contains(keyType, "ecdsa"):
		return "ecdsa"
	case strings.Contains(keyType, "rsa"):
		return "rsa"
	default:
		return "key"
	}
}

func authkeysCountLabel(count int, singular string, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func parseAuthkeysMenuFlags(args []string, stderr io.Writer) (authkeysFlags, bool) {
	var flags authkeysFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--path":
			value, ok := nextArg(args, &i, stderr, "--path")
			if !ok {
				return flags, false
			}
			flags.Path = value
		case strings.HasPrefix(arg, "--path="):
			flags.Path = strings.TrimPrefix(arg, "--path=")
		case arg == "--ssh-keygen":
			value, ok := nextArg(args, &i, stderr, "--ssh-keygen")
			if !ok {
				return flags, false
			}
			value, ok = requireBinaryFlagValue(value, "--ssh-keygen", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHKeygenPath = value
		case strings.HasPrefix(arg, "--ssh-keygen="):
			value, ok := requireBinaryFlagValue(strings.TrimPrefix(arg, "--ssh-keygen="), "--ssh-keygen", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHKeygenPath = value
		default:
			fmt.Fprintf(stderr, "ssherpa: unknown authkeys argument %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

func parseAuthkeysListFlags(args []string, stderr io.Writer) (authkeysFlags, bool) {
	var flags authkeysFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			flags.JSON = true
		case arg == "--path":
			value, ok := nextArg(args, &i, stderr, "--path")
			if !ok {
				return flags, false
			}
			flags.Path = value
		case strings.HasPrefix(arg, "--path="):
			flags.Path = strings.TrimPrefix(arg, "--path=")
		default:
			fmt.Fprintf(stderr, "ssherpa: unknown authkeys list argument %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

func parseAuthkeysAddFlags(args []string, stderr io.Writer) (authkeysFlags, bool) {
	var flags authkeysFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--key":
			value, ok := nextArg(args, &i, stderr, "--key")
			if !ok {
				return flags, false
			}
			flags.Key = value
		case strings.HasPrefix(arg, "--key="):
			flags.Key = strings.TrimPrefix(arg, "--key=")
		case arg == "--key-file":
			value, ok := nextArg(args, &i, stderr, "--key-file")
			if !ok {
				return flags, false
			}
			flags.KeyFile = value
		case strings.HasPrefix(arg, "--key-file="):
			flags.KeyFile = strings.TrimPrefix(arg, "--key-file=")
		default:
			handled, ok := parseAuthkeysMutationFlag(arg, args, &i, stderr, &flags)
			if handled {
				if !ok {
					return flags, false
				}
				continue
			}
			fmt.Fprintf(stderr, "ssherpa: unknown authkeys add argument %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

func parseAuthkeysDirFlags(args []string, stderr io.Writer, subcommand string) (authkeysFlags, bool) {
	var flags authkeysFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--from-dir":
			value, ok := nextArg(args, &i, stderr, "--from-dir")
			if !ok {
				return flags, false
			}
			flags.FromDir = value
		case strings.HasPrefix(arg, "--from-dir="):
			flags.FromDir = strings.TrimPrefix(arg, "--from-dir=")
		default:
			handled, ok := parseAuthkeysMutationFlag(arg, args, &i, stderr, &flags)
			if handled {
				if !ok {
					return flags, false
				}
				continue
			}
			fmt.Fprintf(stderr, "ssherpa: unknown authkeys %s argument %q\n", subcommand, arg)
			return flags, false
		}
	}
	return flags, true
}

func parseAuthkeysDeleteFlags(args []string, stderr io.Writer) (authkeysFlags, bool) {
	var flags authkeysFlags
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--fingerprint":
			value, ok := nextArg(args, &i, stderr, "--fingerprint")
			if !ok {
				return flags, false
			}
			flags.Fingerprints = append(flags.Fingerprints, value)
		case strings.HasPrefix(arg, "--fingerprint="):
			flags.Fingerprints = append(flags.Fingerprints, strings.TrimPrefix(arg, "--fingerprint="))
		case !strings.HasPrefix(arg, "-"):
			flags.Fingerprints = append(flags.Fingerprints, arg)
		default:
			handled, ok := parseAuthkeysMutationFlag(arg, args, &i, stderr, &flags)
			if handled {
				if !ok {
					return flags, false
				}
				continue
			}
			fmt.Fprintf(stderr, "ssherpa: unknown authkeys delete argument %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

func parseAuthkeysMutationFlag(arg string, args []string, i *int, stderr io.Writer, flags *authkeysFlags) (bool, bool) {
	switch {
	case arg == "--path":
		value, ok := nextArg(args, i, stderr, "--path")
		if !ok {
			return true, false
		}
		flags.Path = value
		return true, true
	case strings.HasPrefix(arg, "--path="):
		flags.Path = strings.TrimPrefix(arg, "--path=")
		return true, true
	case arg == "--dry-run":
		flags.DryRun = true
		return true, true
	case arg == "--yes" || arg == "-y":
		flags.Yes = true
		return true, true
	case arg == "--ssh-keygen":
		value, ok := nextArg(args, i, stderr, "--ssh-keygen")
		if !ok {
			return true, false
		}
		value, ok = requireBinaryFlagValue(value, "--ssh-keygen", stderr)
		if !ok {
			return true, false
		}
		flags.SSHKeygenPath = value
		return true, true
	case strings.HasPrefix(arg, "--ssh-keygen="):
		value, ok := requireBinaryFlagValue(strings.TrimPrefix(arg, "--ssh-keygen="), "--ssh-keygen", stderr)
		if !ok {
			return true, false
		}
		flags.SSHKeygenPath = value
		return true, true
	default:
		return false, true
	}
}

func authkeysKeyFromFlags(flags authkeysFlags) (authkeys.AuthorizedKey, []authkeys.Diagnostic, error) {
	validator := authkeysValidator(flags)
	if flags.Key != "" {
		key, err := authkeys.ParsePublicKeyLine(flags.Key)
		if err != nil {
			return authkeys.AuthorizedKey{}, nil, err
		}
		_, err = validator.Validate(key)
		return key, nil, err
	}

	path := flags.KeyFile
	data, err := os.ReadFile(path)
	if err != nil {
		return authkeys.AuthorizedKey{}, nil, fmt.Errorf("read %s: %w", path, err)
	}
	return authkeys.ParseFirstKey(data, path, validator)
}

func authkeysValidator(flags authkeysFlags) authkeys.Validator {
	return authkeys.Validator{SSHKeygenPath: flags.SSHKeygenPath}
}

func applyAuthkeysPlan(plan authkeys.Plan, flags authkeysFlags, stdout io.Writer, stderr io.Writer) int {
	if !flags.DryRun {
		if err := assertAuthkeysUnchangedSincePlan(plan); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
	}

	result, err := fsutil.AtomicWriteFile(plan.Path, plan.NewData, fsutil.WriteOptions{
		DryRun: flags.DryRun,
		Backup: true,
		Mode:   authkeys.DefaultFileMode,
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: write %s: %v\n", plan.Path, err)
		return 1
	}

	printAuthkeysResult(stdout, plan, result)
	return 0
}

func assertAuthkeysUnchangedSincePlan(plan authkeys.Plan) error {
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

func printAuthkeysResult(stdout io.Writer, plan authkeys.Plan, result fsutil.WriteResult) {
	action := plan.Action
	if !result.Changed {
		action = "unchanged"
	} else if result.DryRun {
		action = "would-" + action
	}

	target := plan.Target
	if target == "" {
		target = fmt.Sprintf("%d key(s)", len(plan.Keys))
	}
	fmt.Fprintf(stdout, "[%s] %s in %s\n", action, target, plan.Path)
	if result.DryRun && result.Changed {
		fmt.Fprint(stdout, result.Diff)
	}
	printAuthkeysStats(stdout, plan.Stats)
	if result.BackupPath != "" {
		fmt.Fprintf(stdout, "[backup] %s\n", result.BackupPath)
	}
}

func printAuthkeysStats(stdout io.Writer, stats authkeys.ImportStats) {
	if stats == (authkeys.ImportStats{}) {
		return
	}
	fmt.Fprintf(stdout, "[summary] valid=%d added=%d deleted=%d invalid=%d duplicate=%d already-present=%d ignored=%d\n",
		stats.Valid,
		stats.Added,
		stats.Deleted,
		stats.Invalid,
		stats.Duplicate,
		stats.AlreadyPresent,
		stats.Ignored,
	)
}

func printAuthkeysDiagnostics(stderr io.Writer, diagnostics []authkeys.Diagnostic) {
	for _, diagnostic := range diagnostics {
		printAuthkeysDiagnostic(stderr, diagnostic)
	}
}

func printAuthkeysDiagnostic(stderr io.Writer, diagnostic authkeys.Diagnostic) {
	prefix := "Warning"
	if diagnostic.Severity != "" {
		prefix = strings.ToUpper(diagnostic.Severity[:1]) + diagnostic.Severity[1:]
	}
	if diagnostic.Path != "" && diagnostic.Line > 0 {
		fmt.Fprintf(stderr, "%s: %s:%d: %s\n", prefix, diagnostic.Path, diagnostic.Line, diagnostic.Message)
		return
	}
	if diagnostic.Path != "" {
		fmt.Fprintf(stderr, "%s: %s: %s\n", prefix, diagnostic.Path, diagnostic.Message)
		return
	}
	fmt.Fprintf(stderr, "%s: %s\n", prefix, diagnostic.Message)
}

func authkeysKeyOutputs(keys []authkeys.AuthorizedKey) []authkeysKeyOutput {
	out := make([]authkeysKeyOutput, 0, len(keys))
	for _, key := range keys {
		fingerprint, _ := key.SHA256Fingerprint()
		out = append(out, authkeysKeyOutput{
			Options:     key.Options,
			Type:        key.Type,
			Blob:        key.Blob,
			Comment:     key.Comment,
			Fingerprint: fingerprint,
			Source:      key.Source,
			Line:        key.Line,
		})
	}
	return out
}

func authorizedKeysPath(flagPath string) (string, error) {
	path := strings.TrimSpace(flagPath)
	if path == "" {
		path = strings.TrimSpace(os.Getenv("SSHERPA_AUTHORIZED_KEYS_PATH"))
	}
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		path = filepath.Join(home, ".ssh", "authorized_keys")
	}
	return expandUserPath(path)
}

func expandUserPath(path string) (string, error) {
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
			return "", fmt.Errorf("resolve path %s: %w", path, err)
		}
		path = abs
	}
	return filepath.Clean(path), nil
}
