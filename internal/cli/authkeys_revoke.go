package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/authkeys"
	"github.com/0xbenc/ssherpa/internal/hostlist"
	"github.com/0xbenc/ssherpa/internal/sshcmd"
	"github.com/0xbenc/ssherpa/internal/ui"
)

type authkeysRevokeFlags struct {
	inventoryFlags
	DryRun        bool
	Yes           bool
	Key           string
	KeyFile       string
	SSHKeygenPath string
	SSHBinary     string
	Timeout       time.Duration
	Targets       []string
	Hops          map[string][]string
}

type authkeysRevokeOutput struct {
	OK      bool                   `json:"ok"`
	DryRun  bool                   `json:"dry_run"`
	Key     authkeysKeyOutput      `json:"key"`
	Results []authkeysRevokeResult `json:"results"`
	Summary authkeysSeedSummary    `json:"summary"`
}

type authkeysRevokeResult struct {
	Target              string   `json:"target"`
	Route               []string `json:"route,omitempty"`
	Status              string   `json:"status"`
	Removed             int      `json:"removed"`
	NotFound            int      `json:"not_found"`
	VerificationStatus  string   `json:"verification_status,omitempty"`
	Absent              int      `json:"absent,omitempty"`
	Present             int      `json:"present,omitempty"`
	ExitCode            int      `json:"exit_code,omitempty"`
	Message             string   `json:"message,omitempty"`
	VerificationMessage string   `json:"verification_message,omitempty"`
}

func runAuthkeysRevoke(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}
	flags, ok := parseAuthkeysRevokeFlags(args, stderr)
	if !ok {
		return 1
	}
	if flags.Timeout <= 0 {
		flags.Timeout = defaultAuthkeysSeedTimeout
	}
	if len(args) == 0 {
		return runAuthkeysRevokeInteractive(flags, stdout, stderr)
	}
	return runAuthkeysRevokeWithFlags(flags, stdout, stderr)
}

func runAuthkeysRevokeWithFlags(flags authkeysRevokeFlags, stdout io.Writer, stderr io.Writer) int {
	key, code, ok := prepareAuthkeysRevoke(flags, stderr)
	if !ok {
		return code
	}
	if len(flags.Targets) == 0 {
		fmt.Fprintln(stderr, "ssherpa: authkeys revoke requires at least one --target")
		return 1
	}
	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 3
	}
	targets, err := resolveAuthkeysSeedTargets(authkeysSeedFlags{Targets: flags.Targets, Hops: flags.Hops}, inventory)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !flags.DryRun && !flags.Yes {
		ok, err := confirmDeleteChoice(stderr, "Remove remote authorized key", authkeysRevokeConfirmMessage(key, targets, false))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: revoke confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys revoke cancelled")
			return 0
		}
	}
	out := executeAuthkeysRevoke(flags, key, targets, stdout)
	if flags.JSON {
		writeJSON(stdout, out)
	}
	if out.Summary.Failed > 0 {
		return 2
	}
	return 0
}

func prepareAuthkeysRevoke(flags authkeysRevokeFlags, stderr io.Writer) (authkeys.AuthorizedKey, int, bool) {
	if !validateExplicitSSHKeygen(authkeysFlags{SSHKeygenPath: flags.SSHKeygenPath}, stderr) {
		return authkeys.AuthorizedKey{}, 1, false
	}
	if err := validateAuthkeysRevokeKeySource(flags); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return authkeys.AuthorizedKey{}, 1, false
	}
	key, diagnostics, err := authkeysRevokeKey(flags)
	for _, diagnostic := range diagnostics {
		printAuthkeysDiagnostic(stderr, diagnostic)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return authkeys.AuthorizedKey{}, 1, false
	}
	return key, 0, true
}

func executeAuthkeysRevoke(flags authkeysRevokeFlags, key authkeys.AuthorizedKey, targets []authkeysSeedTarget, stdout io.Writer) authkeysRevokeOutput {
	base := sshcmd.Resolve(sshcmd.ResolveOptions{SSHBinary: flags.SSHBinary, NoKitty: true, Env: sshcmd.Env()})
	sshBinaryErr := sshcmd.ValidateCommandBinary(base, sshBinaryRequirement(flags.SSHBinary))
	keys := []authkeys.AuthorizedKey{key}
	keyOutputs := authkeysKeyOutputs(keys)
	out := authkeysRevokeOutput{OK: true, DryRun: flags.DryRun, Key: keyOutputs[0]}
	for _, target := range targets {
		result := authkeysRevokeResult{Target: target.Alias, Route: append([]string(nil), target.Hops...)}
		if sshBinaryErr != nil {
			result.Status = "failed"
			result.ExitCode = 1
			result.Message = sshBinaryErr.Error()
		} else {
			cmd := buildAuthkeysSeedSSHCommand(base, target, flags.Timeout)
			result = runAuthkeysRevokeSSH(cmd, target, key, flags.DryRun)
			if !flags.DryRun && result.Status != "failed" {
				verifyCmd := buildAuthkeysSeedSSHCommand(base, target, flags.Timeout)
				verified := runAuthkeysRevokeVerifySSH(verifyCmd, target, key)
				result.VerificationStatus = verified.VerificationStatus
				result.Absent = verified.Absent
				result.Present = verified.Present
				result.VerificationMessage = verified.VerificationMessage
				if verified.ExitCode != 0 {
					result.ExitCode = verified.ExitCode
				}
			} else if flags.DryRun && result.Status != "failed" {
				result.VerificationStatus = "skipped"
				result.VerificationMessage = "dry-run did not mutate the remote file"
			}
		}
		out.Results = append(out.Results, result)
		if !flags.JSON {
			printAuthkeysRevokeResult(stdout, result, flags.DryRun)
		}
	}
	out.Summary = summarizeAuthkeysRevokeResults(out.Results)
	out.OK = out.Summary.Failed == 0
	if !flags.JSON {
		printAuthkeysSeedSummary(stdout, out.Summary)
	}
	return out
}

func runAuthkeysRevokeSSH(cmd sshcmd.Command, target authkeysSeedTarget, key authkeys.AuthorizedKey, dryRun bool) authkeysRevokeResult {
	result := authkeysRevokeResult{Target: target.Alias, Route: append([]string(nil), target.Hops...)}
	if len(cmd.Argv) == 0 {
		result.Status = "failed"
		result.ExitCode = 1
		result.Message = "empty SSH command"
		return result
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	proc := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	proc.Stdin = strings.NewReader(authkeysRevokeRemoteScript(key, dryRun))
	proc.Stdout = &stdout
	proc.Stderr = &stderr
	err := proc.Run()
	if err != nil {
		result.Status = "failed"
		result.ExitCode = commandExitCode(err)
		result.Message = firstNonEmptyLine(stderr.String(), stdout.String(), err.Error())
		return result
	}
	parsed, ok := parseAuthkeysRevokeRemoteResult(stdout.String())
	if !ok {
		result.Status = "failed"
		result.ExitCode = 1
		result.Message = firstNonEmptyLine(stdout.String(), "remote revoke command did not return a result")
		return result
	}
	parsed.Target = target.Alias
	parsed.Route = append([]string(nil), target.Hops...)
	return parsed
}

func runAuthkeysRevokeVerifySSH(cmd sshcmd.Command, target authkeysSeedTarget, key authkeys.AuthorizedKey) authkeysRevokeResult {
	result := authkeysRevokeResult{Target: target.Alias, Route: append([]string(nil), target.Hops...), VerificationStatus: "failed"}
	if len(cmd.Argv) == 0 {
		result.ExitCode = 1
		result.VerificationMessage = "empty SSH command"
		return result
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	proc := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	proc.Stdin = strings.NewReader(authkeysRevokeVerifyRemoteScript(key))
	proc.Stdout = &stdout
	proc.Stderr = &stderr
	err := proc.Run()
	if err != nil {
		result.ExitCode = commandExitCode(err)
		result.VerificationMessage = firstNonEmptyLine(stderr.String(), stdout.String(), err.Error())
		return result
	}
	parsed, ok := parseAuthkeysRevokeVerifyResult(stdout.String())
	if !ok {
		result.ExitCode = 1
		result.VerificationMessage = firstNonEmptyLine(stdout.String(), "remote revoke verification command did not return a result")
		return result
	}
	parsed.Target = target.Alias
	parsed.Route = append([]string(nil), target.Hops...)
	return parsed
}

func parseAuthkeysRevokeRemoteResult(output string) (authkeysRevokeResult, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SSHERPA_AUTHKEYS_REVOKE ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "SSHERPA_AUTHKEYS_REVOKE "))
		result := authkeysRevokeResult{}
		for _, field := range fields {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "status":
				result.Status = value
			case "removed":
				result.Removed, _ = strconv.Atoi(value)
			case "not_found":
				result.NotFound, _ = strconv.Atoi(value)
			}
		}
		if result.Status == "" {
			if result.Removed > 0 {
				result.Status = "removed"
			} else {
				result.Status = "unchanged"
			}
		}
		return result, true
	}
	return authkeysRevokeResult{}, false
}

func parseAuthkeysRevokeVerifyResult(output string) (authkeysRevokeResult, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SSHERPA_AUTHKEYS_REVOKE_VERIFY ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "SSHERPA_AUTHKEYS_REVOKE_VERIFY "))
		result := authkeysRevokeResult{}
		for _, field := range fields {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "status":
				result.VerificationStatus = value
			case "absent":
				result.Absent, _ = strconv.Atoi(value)
			case "present":
				result.Present, _ = strconv.Atoi(value)
			}
		}
		if result.VerificationStatus == "" {
			if result.Present > 0 {
				result.VerificationStatus = "present"
			} else {
				result.VerificationStatus = "absent"
			}
		}
		if result.VerificationStatus != "absent" {
			result.ExitCode = 1
			result.VerificationMessage = "removed key was still present after reconnecting"
		}
		return result, true
	}
	return authkeysRevokeResult{}, false
}

func authkeysRevokeRemoteScript(key authkeys.AuthorizedKey, dryRun bool) string {
	var b strings.Builder
	b.WriteString("dry_run=")
	if dryRun {
		b.WriteString("1\n")
	} else {
		b.WriteString("0\n")
	}
	b.WriteString(authkeysRemoteIdentityFunction())
	b.WriteString("target_identity=")
	b.WriteString(singleQuoteShell(key.Identity()))
	b.WriteString(`
ak_file="${HOME:?}/.ssh/authorized_keys"
tmp="${TMPDIR:-/tmp}/ssherpa-authkeys-revoke.$$"
trap 'rm -f "$tmp"' EXIT HUP INT TERM
if [ ! -e "$ak_file" ]; then
  printf 'SSHERPA_AUTHKEYS_REVOKE status=unchanged removed=0 not_found=1\n'
  exit 0
fi
if [ -d "$ak_file" ]; then
  printf '%s\n' 'authorized_keys is a directory' >&2
  exit 40
fi
if [ ! -r "$ak_file" ]; then
  printf '%s\n' 'authorized_keys is not readable by the login user' >&2
  exit 41
fi
removed=0
not_found=0
while IFS= read -r existing_line || [ -n "$existing_line" ]; do
  identity=$(key_identity <<EOF
$existing_line
EOF
)
  if [ "$identity" = "$target_identity" ]; then
    removed=$((removed + 1))
    continue
  fi
  printf '%s\n' "$existing_line" >> "$tmp" || exit 42
done < "$ak_file"
if [ "$removed" -eq 0 ]; then
  not_found=1
fi
if [ "$dry_run" != "1" ] && [ "$removed" -gt 0 ]; then
  cat "$tmp" > "$ak_file" || exit 43
  chmod 600 "$ak_file" || exit 44
fi
status=unchanged
if [ "$removed" -gt 0 ]; then
  if [ "$dry_run" = "1" ]; then
    status=would-remove
  else
    status=removed
  fi
fi
printf 'SSHERPA_AUTHKEYS_REVOKE status=%s removed=%s not_found=%s\n' "$status" "$removed" "$not_found"
`)
	return b.String()
}

func authkeysRevokeVerifyRemoteScript(key authkeys.AuthorizedKey) string {
	var b strings.Builder
	b.WriteString(authkeysRemoteIdentityFunction())
	b.WriteString("target_identity=")
	b.WriteString(singleQuoteShell(key.Identity()))
	b.WriteString(`
ak_file="${HOME:?}/.ssh/authorized_keys"
present=0
absent=1
if [ -f "$ak_file" ] && [ -r "$ak_file" ]; then
  while IFS= read -r existing_line || [ -n "$existing_line" ]; do
    identity=$(key_identity <<EOF
$existing_line
EOF
)
    if [ "$identity" = "$target_identity" ]; then
      present=1
      absent=0
      break
    fi
  done < "$ak_file"
elif [ -e "$ak_file" ] && [ ! -r "$ak_file" ]; then
  printf '%s\n' 'authorized_keys is not readable by the login user' >&2
  exit 45
fi
status=absent
if [ "$present" -gt 0 ]; then
  status=present
fi
printf 'SSHERPA_AUTHKEYS_REVOKE_VERIFY status=%s absent=%s present=%s\n' "$status" "$absent" "$present"
`)
	return b.String()
}

func authkeysRemoteIdentityFunction() string {
	return `key_identity() {
  awk '{
    for (i = 1; i < NF; i++) {
      if ($i == "ssh-ed25519" || $i == "ssh-rsa" || $i == "rsa-sha2-256" || $i == "rsa-sha2-512" || $i ~ /^ecdsa-sha2-/ || $i ~ /^sk-ssh-ed25519/ || $i ~ /^sk-ecdsa-sha2-/) {
        print $i " " $(i + 1)
        exit
      }
    }
  }'
}
`
}

func validateAuthkeysRevokeKeySource(flags authkeysRevokeFlags) error {
	sources := 0
	for _, value := range []string{flags.Key, flags.KeyFile} {
		if strings.TrimSpace(value) != "" {
			sources++
		}
	}
	if sources != 1 {
		return errors.New("authkeys revoke requires exactly one of --key or --key-file")
	}
	return nil
}

func authkeysRevokeKey(flags authkeysRevokeFlags) (authkeys.AuthorizedKey, []authkeys.Diagnostic, error) {
	validator := authkeys.Validator{SSHKeygenPath: flags.SSHKeygenPath}
	if flags.Key != "" {
		key, err := authkeys.ParsePublicKeyLine(flags.Key)
		if err != nil {
			return authkeys.AuthorizedKey{}, nil, err
		}
		_, err = validator.Validate(key)
		return key, nil, err
	}
	data, err := os.ReadFile(flags.KeyFile)
	if err != nil {
		return authkeys.AuthorizedKey{}, nil, fmt.Errorf("read %s: %w", flags.KeyFile, err)
	}
	return authkeys.ParseFirstKey(data, flags.KeyFile, validator)
}

func parseAuthkeysRevokeFlags(args []string, stderr io.Writer) (authkeysRevokeFlags, bool) {
	flags := authkeysRevokeFlags{Timeout: defaultAuthkeysSeedTimeout, Hops: map[string][]string{}}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--json":
			flags.JSON = true
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
		case arg == "--target":
			value, ok := nextArg(args, &i, stderr, "--target")
			if !ok {
				return flags, false
			}
			flags.Targets = append(flags.Targets, value)
		case strings.HasPrefix(arg, "--target="):
			flags.Targets = append(flags.Targets, strings.TrimPrefix(arg, "--target="))
		case arg == "--hop":
			value, ok := nextArg(args, &i, stderr, "--hop")
			if !ok {
				return flags, false
			}
			if !parseAuthkeysSeedHop(value, flags.Hops, stderr) {
				return flags, false
			}
		case strings.HasPrefix(arg, "--hop="):
			if !parseAuthkeysSeedHop(strings.TrimPrefix(arg, "--hop="), flags.Hops, stderr) {
				return flags, false
			}
		case arg == "--dry-run":
			flags.DryRun = true
		case arg == "--yes" || arg == "-y":
			flags.Yes = true
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
		case arg == "--ssh-binary":
			value, ok := nextArg(args, &i, stderr, "--ssh-binary")
			if !ok {
				return flags, false
			}
			value, ok = requireBinaryFlagValue(value, "--ssh-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case strings.HasPrefix(arg, "--ssh-binary="):
			value, ok := requireBinaryFlagValue(strings.TrimPrefix(arg, "--ssh-binary="), "--ssh-binary", stderr)
			if !ok {
				return flags, false
			}
			flags.SSHBinary = value
		case arg == "--timeout":
			value, ok := nextArg(args, &i, stderr, "--timeout")
			if !ok {
				return flags, false
			}
			d, ok := parseDuration(value, stderr, "--timeout")
			if !ok {
				return flags, false
			}
			flags.Timeout = d
		case strings.HasPrefix(arg, "--timeout="):
			d, ok := parseDuration(strings.TrimPrefix(arg, "--timeout="), stderr, "--timeout")
			if !ok {
				return flags, false
			}
			flags.Timeout = d
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(stderr, "ssherpa: unknown authkeys revoke argument %q\n", arg)
			return flags, false
		default:
			flags.Targets = append(flags.Targets, arg)
		}
	}
	return flags, true
}

func printAuthkeysRevokeResult(stdout io.Writer, result authkeysRevokeResult, dryRun bool) {
	status := result.Status
	if status == "" {
		status = "failed"
	}
	route := "direct"
	if len(result.Route) > 0 {
		route = strings.Join(result.Route, ",")
	}
	if result.Status == "failed" {
		fmt.Fprintf(stdout, "[failed] %s route=%s message=%s\n", result.Target, route, result.Message)
		return
	}
	if dryRun && status == "removed" {
		status = "would-remove"
	}
	verify := ""
	if result.VerificationStatus != "" {
		verify = fmt.Sprintf(" verify=%s", authkeysRevokeVerifyLabel(result))
	}
	fmt.Fprintf(stdout, "[%s] %s removed=%d not-found=%d route=%s%s\n", status, result.Target, result.Removed, result.NotFound, route, verify)
}

func summarizeAuthkeysRevokeResults(results []authkeysRevokeResult) authkeysSeedSummary {
	var summary authkeysSeedSummary
	for _, result := range results {
		switch result.Status {
		case "failed":
			summary.Failed++
		case "unchanged":
			if authkeysRevokeVerificationFailed(result) {
				summary.Failed++
			} else {
				summary.OK++
				summary.Unchanged++
			}
		default:
			if authkeysRevokeVerificationFailed(result) {
				summary.Failed++
			} else {
				summary.OK++
				summary.Changed++
			}
		}
	}
	return summary
}

func authkeysRevokeVerificationFailed(result authkeysRevokeResult) bool {
	return result.VerificationStatus == "failed" || result.VerificationStatus == "present"
}

func authkeysRevokeVerifyLabel(result authkeysRevokeResult) string {
	switch result.VerificationStatus {
	case "absent":
		return "absent"
	case "present":
		return "present"
	case "failed":
		return "failed"
	case "skipped":
		return "skipped"
	default:
		return result.VerificationStatus
	}
}

func singleQuoteShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func runAuthkeysRevokeInteractive(flags authkeysRevokeFlags, stdout io.Writer, stderr io.Writer) int {
	if flags.Hops == nil {
		flags.Hops = map[string][]string{}
	}
	item, ok, err := ui.ChooseManagement(context.Background(), authkeysRevokeSourceItems(), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Remove remote authorized key",
		Mode:        "choose key source",
		Steps:       []string{"source", "key", "hosts", "routes", "confirm", "report"},
		CurrentStep: 0,
		Footer:      "enter select / type filter / arrows move / shift+arrows section / esc back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: revoke picker failed: %v\n", err)
		return 1
	}
	if !ok || item.Token == "back" {
		fmt.Fprintln(stdout, "[skipped] authkeys revoke cancelled")
		return 0
	}
	switch item.Token {
	case "paste":
		line, ok, err := promptText(stderr, "Remove remote authorized key", "key", "", validateNonEmpty("SSH public key"))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys revoke cancelled")
			return 0
		}
		flags.Key = line
	case "file":
		path, ok, err := pickAuthkeysRevokeFile(stderr)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys revoke cancelled")
			return 0
		}
		flags.KeyFile = path
	}
	return runAuthkeysRevokeInteractiveTargets(flags, stdout, stderr)
}

func authkeysRevokeSourceItems() []ui.ManagementItem {
	return []ui.ManagementItem{
		{Kind: ui.ItemConfirmDelete, Token: "paste", Title: "Paste one public key", Description: "enter the public key to remove", Group: "Key Source", Badge: "key", Action: "Remove this key from selected remote hosts"},
		{Kind: ui.ItemConfirmDelete, Token: "file", Title: "Use public key file", Description: "read the first valid key from a .pub file", Group: "Key Source", Badge: "file", Action: "Load one public key from a local file path"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to authorized_keys actions", Group: "Navigation", Badge: "back", Action: "Return without removing remote keys"},
	}
}

func pickAuthkeysRevokeFile(stderr io.Writer) (string, bool, error) {
	opts := transferBrowserOptions(stderr, filePickerOptions{}, "SSHERPA AUTHKEYS REMOVE FILE", "local-file", "LOCAL", ".", []string{"source", "key", "hosts", "routes", "confirm", "report"}, 1)
	opts.Footer = "enter open/use / type filter / arrows move / shift+arrows section / esc cancel"
	out, ok, err := ui.BrowseTransfer(context.Background(), localFileSource(), opts)
	if err != nil || !ok {
		return "", ok, err
	}
	return out.Token(), true, nil
}

func runAuthkeysRevokeInteractiveTargets(flags authkeysRevokeFlags, stdout io.Writer, stderr io.Writer) int {
	key, code, ok := prepareAuthkeysRevoke(flags, stderr)
	if !ok {
		return code
	}
	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 3
	}
	selected, ok, err := ui.ChooseHosts(context.Background(), inventory.Aliases, ui.HostMultiChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Remove key: pick remote hosts",
		Mode:        "choose remote removal targets",
		Steps:       []string{"source", "key", "hosts", "routes", "confirm", "report"},
		CurrentStep: 2,
		Footer:      "space toggle / enter continue / type filter / arrows move / shift+arrows section / esc back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: host picker failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] authkeys revoke cancelled")
		return 0
	}
	flags.Targets = selected
	if flags.Hops == nil {
		flags.Hops = map[string][]string{}
	}
	routed, ok, code := configureAuthkeysRevokeRoutes(flags, inventory, stderr)
	if !ok {
		if code != 0 {
			return code
		}
		fmt.Fprintln(stdout, "[skipped] authkeys revoke cancelled")
		return 0
	}
	flags = routed
	targets, err := resolveAuthkeysSeedTargets(authkeysSeedFlags{Targets: flags.Targets, Hops: flags.Hops}, inventory)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	yes, answered, err := ui.ConfirmDelete(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Confirm remote key removal",
		Message:     authkeysRevokeConfirmMessage(key, targets, flags.DryRun),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: revoke confirmation failed: %v\n", err)
		return 1
	}
	if !answered || !yes {
		fmt.Fprintln(stdout, "[skipped] authkeys revoke cancelled")
		return 0
	}
	flags.Yes = true
	out := executeAuthkeysRevoke(flags, key, targets, stdout)
	if err := showAuthkeysRevokeReportScreen(out, stderr); err != nil {
		fmt.Fprintf(stderr, "ssherpa: revoke report failed: %v\n", err)
		return 1
	}
	if out.Summary.Failed > 0 {
		return 2
	}
	return 0
}

func configureAuthkeysRevokeRoutes(flags authkeysRevokeFlags, inventory hostlist.Inventory, stderr io.Writer) (authkeysRevokeFlags, bool, int) {
	for {
		items := authkeysRevokeRouteItems(flags)
		item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			Title:       "Remove key: configure routes",
			Mode:        "direct or ProxyJump per host",
			Steps:       []string{"source", "key", "hosts", "routes", "confirm", "report"},
			CurrentStep: 3,
			Summary:     authkeysRevokeRouteSummary(flags),
			Footer:      "enter select / type filter / arrows move / shift+arrows section / esc back",
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: route picker failed: %v\n", err)
			return flags, false, 1
		}
		if !ok {
			return flags, false, 0
		}
		if item.Token == "continue" {
			return flags, true, 0
		}
		target := strings.TrimPrefix(item.Token, "target:")
		updated, ok, code := configureAuthkeysRevokeTargetRoute(flags, inventory, target, stderr)
		if !ok {
			if code != 0 {
				return flags, false, code
			}
			continue
		}
		flags = updated
	}
}

func authkeysRevokeRouteItems(flags authkeysRevokeFlags) []ui.ManagementItem {
	items := []ui.ManagementItem{{
		Kind:        ui.ItemCheck,
		Token:       "continue",
		Title:       "Continue to review",
		Description: authkeysRevokeRouteSummary(flags),
		Group:       "Action",
		Badge:       "done",
		Action:      "Review the removal plan before running",
	}}
	for _, target := range flags.Targets {
		route := "direct"
		if len(flags.Hops[target]) > 0 {
			route = "via " + strings.Join(flags.Hops[target], ",")
		}
		items = append(items, ui.ManagementItem{
			Kind:        ui.ItemAlias,
			Token:       "target:" + target,
			Title:       target,
			Description: route,
			Detail:      "removes one key from login user's ~/.ssh/authorized_keys",
			Group:       "Targets",
			Badge:       "host",
			Action:      "Set direct or ProxyJump route for this host",
		})
	}
	return items
}

func configureAuthkeysRevokeTargetRoute(flags authkeysRevokeFlags, inventory hostlist.Inventory, target string, stderr io.Writer) (authkeysRevokeFlags, bool, int) {
	item, ok, err := ui.ChooseManagement(context.Background(), authkeysSeedTargetRouteItems(target, flags.Hops[target]), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Remove key: route for " + target,
		Mode:        "choose route type",
		Steps:       []string{"source", "key", "hosts", "routes", "confirm", "report"},
		CurrentStep: 3,
		Footer:      "enter select / type filter / arrows move / esc back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: route picker failed: %v\n", err)
		return flags, false, 1
	}
	if !ok || item.Token == "back" {
		return flags, false, 0
	}
	switch item.Token {
	case "direct":
		delete(flags.Hops, target)
		return flags, true, 0
	case "jump":
		hops, ok, code := pickAuthkeysSeedHops(target, inventory.Aliases, stderr)
		if !ok {
			return flags, false, code
		}
		flags.Hops[target] = hops
		return flags, true, 0
	default:
		return flags, false, 0
	}
}

func authkeysRevokeRouteSummary(flags authkeysRevokeFlags) string {
	routed := 0
	for _, target := range flags.Targets {
		if len(flags.Hops[target]) > 0 {
			routed++
		}
	}
	direct := len(flags.Targets) - routed
	return fmt.Sprintf("%d direct  %d routed", direct, routed)
}

func authkeysRevokeConfirmMessage(key authkeys.AuthorizedKey, targets []authkeysSeedTarget, dryRun bool) string {
	action := "Remove"
	if dryRun {
		action = "Dry-run remove"
	}
	fp, _ := key.SHA256Fingerprint()
	return fmt.Sprintf("%s key %s from %d host(s). Writes only the SSH login user's ~/.ssh/authorized_keys.", action, fp, len(targets))
}

func showAuthkeysRevokeReportScreen(out authkeysRevokeOutput, stderr io.Writer) error {
	return ui.ShowTextView(context.Background(), ui.TextViewOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Remote key removal report",
		Steps:       []string{"source", "key", "hosts", "routes", "confirm", "report"},
		CurrentStep: 5,
		Summary:     authkeysSeedReportSummary(authkeysSeedOutput{Summary: out.Summary}),
		Lines:       authkeysRevokeReportLines(out),
		Footer:      "up/down scroll / pgup/pgdn page / q close report",
	})
}

func authkeysRevokeReportLines(out authkeysRevokeOutput) []string {
	overall := "complete"
	if out.Summary.Failed > 0 {
		overall = "needs attention"
	}
	comment := out.Key.Comment
	if comment == "" {
		comment = "-"
	}
	lines := []string{
		"Remote authorized key removal report",
		"",
		"Overall: " + overall,
		fmt.Sprintf("Dry run: %s", yesNo(out.DryRun)),
		fmt.Sprintf("Summary: ok=%d changed=%d unchanged=%d failed=%d", out.Summary.OK, out.Summary.Changed, out.Summary.Unchanged, out.Summary.Failed),
		fmt.Sprintf("Verification: %s", authkeysRevokeReportVerificationSummary(out)),
		"",
		"Key:",
		fmt.Sprintf("  %s  %s  %s", out.Key.Fingerprint, out.Key.Type, comment),
		"",
		"Hosts:",
	}
	for _, result := range out.Results {
		route := "direct"
		if len(result.Route) > 0 {
			route = "via " + strings.Join(result.Route, ",")
		}
		hostStatus := result.Status
		if authkeysRevokeVerificationFailed(result) {
			hostStatus += " / verification " + result.VerificationStatus
		}
		lines = append(lines,
			fmt.Sprintf("  %s", result.Target),
			fmt.Sprintf("    route: %s", route),
			fmt.Sprintf("    status: %s", hostStatus),
			fmt.Sprintf("    write: removed=%d  not-found=%d", result.Removed, result.NotFound),
		)
		if result.Message != "" {
			lines = append(lines, "    message: "+result.Message)
		}
		if result.VerificationStatus != "" {
			lines = append(lines, fmt.Sprintf("    verify: %s  absent=%d  present=%d", result.VerificationStatus, result.Absent, result.Present))
		}
		if result.VerificationMessage != "" {
			lines = append(lines, "    verify-message: "+result.VerificationMessage)
		}
	}
	lines = append(lines, "", "Verification reconnects to each successful target and confirms the key identity is absent.")
	if out.Summary.Failed > 0 {
		lines = append(lines, "Review failed hosts above before relying on the removed key.")
	}
	return lines
}

func authkeysRevokeReportVerificationSummary(out authkeysRevokeOutput) string {
	absent := 0
	present := 0
	failed := 0
	skipped := 0
	for _, result := range out.Results {
		switch result.VerificationStatus {
		case "absent":
			absent++
		case "present":
			present++
		case "failed":
			failed++
		case "skipped":
			skipped++
		}
	}
	parts := []string{}
	if absent > 0 {
		parts = append(parts, fmt.Sprintf("absent=%d", absent))
	}
	if present > 0 {
		parts = append(parts, fmt.Sprintf("present=%d", present))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("failed=%d", failed))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("skipped=%d", skipped))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "  ")
}
