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

const defaultAuthkeysSeedTimeout = 10 * time.Second

type authkeysSeedFlags struct {
	inventoryFlags
	DryRun        bool
	Yes           bool
	Key           string
	KeyFile       string
	FromDir       string
	SSHKeygenPath string
	SSHBinary     string
	Timeout       time.Duration
	Targets       []string
	Hops          map[string][]string
}

type authkeysSeedOutput struct {
	OK      bool                 `json:"ok"`
	DryRun  bool                 `json:"dry_run"`
	Keys    []authkeysKeyOutput  `json:"keys"`
	Results []authkeysSeedResult `json:"results"`
	Summary authkeysSeedSummary  `json:"summary"`
}

type authkeysSeedSummary struct {
	OK        int `json:"ok"`
	Changed   int `json:"changed"`
	Unchanged int `json:"unchanged"`
	Failed    int `json:"failed"`
}

type authkeysSeedTarget struct {
	Alias string
	Hops  []string
}

type authkeysSeedResult struct {
	Target              string   `json:"target"`
	Route               []string `json:"route,omitempty"`
	Status              string   `json:"status"`
	Added               int      `json:"added"`
	AlreadyPresent      int      `json:"already_present"`
	VerificationStatus  string   `json:"verification_status,omitempty"`
	Verified            int      `json:"verified,omitempty"`
	Missing             int      `json:"missing,omitempty"`
	ExitCode            int      `json:"exit_code,omitempty"`
	Message             string   `json:"message,omitempty"`
	VerificationMessage string   `json:"verification_message,omitempty"`
}

func runAuthkeysSeed(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		printUsage(stdout)
		return 0
	}

	flags, ok := parseAuthkeysSeedFlags(args, stderr)
	if !ok {
		return 1
	}
	if flags.Timeout <= 0 {
		flags.Timeout = defaultAuthkeysSeedTimeout
	}
	if len(args) == 0 {
		return runAuthkeysSeedInteractive(flags, stdout, stderr)
	}
	return runAuthkeysSeedWithFlags(flags, stdout, stderr)
}

func runAuthkeysSeedWithFlags(flags authkeysSeedFlags, stdout io.Writer, stderr io.Writer) int {
	if !validateExplicitSSHKeygen(authkeysFlags{SSHKeygenPath: flags.SSHKeygenPath}, stderr) {
		return 1
	}
	if err := validateAuthkeysSeedKeySource(flags); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if len(flags.Targets) == 0 {
		fmt.Fprintln(stderr, "ssherpa: authkeys seed requires at least one --target")
		return 1
	}

	keys, diagnostics, err := authkeysSeedKeys(flags)
	for _, diagnostic := range diagnostics {
		printAuthkeysDiagnostic(stderr, diagnostic)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if len(keys) == 0 {
		fmt.Fprintln(stderr, "ssherpa: no valid SSH public keys selected")
		return 1
	}

	_, inventory, err := loadInventory(flags.inventoryFlags)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 3
	}
	targets, err := resolveAuthkeysSeedTargets(flags, inventory)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	if !flags.DryRun && !flags.Yes {
		ok, err := confirmActionChoice(stderr, "Seed authorized keys", authkeysSeedConfirmMessage(keys, targets, false))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: seed confirmation failed: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
			return 0
		}
	}

	out := executeAuthkeysSeed(flags, keys, targets, stdout)
	if flags.JSON {
		writeJSON(stdout, out)
	}
	if out.Summary.Failed > 0 {
		return 2
	}
	return 0
}

func executeAuthkeysSeed(flags authkeysSeedFlags, keys []authkeys.AuthorizedKey, targets []authkeysSeedTarget, stdout io.Writer) authkeysSeedOutput {
	base := sshcmd.Resolve(sshcmd.ResolveOptions{SSHBinary: flags.SSHBinary, NoKitty: true, Env: sshcmd.Env()})
	sshBinaryErr := sshcmd.ValidateCommandBinary(base, sshBinaryRequirement(flags.SSHBinary))
	out := authkeysSeedOutput{
		OK:     true,
		DryRun: flags.DryRun,
		Keys:   authkeysKeyOutputs(keys),
	}
	for _, target := range targets {
		result := authkeysSeedResult{Target: target.Alias, Route: append([]string(nil), target.Hops...)}
		if sshBinaryErr != nil {
			result.Status = "failed"
			result.ExitCode = 1
			result.Message = sshBinaryErr.Error()
		} else {
			cmd := buildAuthkeysSeedSSHCommand(base, target, flags.Timeout)
			result = runAuthkeysSeedSSH(cmd, target, keys, flags.DryRun)
			if !flags.DryRun && result.Status != "failed" {
				verifyCmd := buildAuthkeysSeedSSHCommand(base, target, flags.Timeout)
				verified := runAuthkeysSeedVerifySSH(verifyCmd, target, keys)
				result.VerificationStatus = verified.VerificationStatus
				result.Verified = verified.Verified
				result.Missing = verified.Missing
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
			printAuthkeysSeedResult(stdout, result, flags.DryRun)
		}
	}
	out.Summary = summarizeAuthkeysSeedResults(out.Results)
	out.OK = out.Summary.Failed == 0
	if !flags.JSON {
		printAuthkeysSeedSummary(stdout, out.Summary)
	}
	return out
}

func buildAuthkeysSeedSSHCommand(base sshcmd.Command, target authkeysSeedTarget, timeout time.Duration) sshcmd.Command {
	cmd := sshcmd.WithConnectTimeout(base, int(timeout.Round(time.Second).Seconds()))
	argv := append([]string(nil), cmd.Argv...)
	if len(target.Hops) > 0 {
		argv = append(argv, "-J", strings.Join(target.Hops, ","))
	}
	argv = append(argv, target.Alias, "sh", "-s")
	return sshcmd.Command{Argv: argv}
}

func runAuthkeysSeedSSH(cmd sshcmd.Command, target authkeysSeedTarget, keys []authkeys.AuthorizedKey, dryRun bool) authkeysSeedResult {
	result := authkeysSeedResult{Target: target.Alias, Route: append([]string(nil), target.Hops...)}
	if len(cmd.Argv) == 0 {
		result.Status = "failed"
		result.ExitCode = 1
		result.Message = "empty SSH command"
		return result
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	proc := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	proc.Stdin = strings.NewReader(authkeysSeedRemoteScript(keys, dryRun))
	proc.Stdout = &stdout
	proc.Stderr = &stderr
	err := proc.Run()
	if err != nil {
		result.Status = "failed"
		result.ExitCode = commandExitCode(err)
		result.Message = firstNonEmptyLine(stderr.String(), stdout.String(), err.Error())
		return result
	}

	parsed, ok := parseAuthkeysSeedRemoteResult(stdout.String())
	if !ok {
		result.Status = "failed"
		result.ExitCode = 1
		result.Message = firstNonEmptyLine(stdout.String(), "remote seed command did not return a result")
		return result
	}
	parsed.Target = target.Alias
	parsed.Route = append([]string(nil), target.Hops...)
	return parsed
}

func runAuthkeysSeedVerifySSH(cmd sshcmd.Command, target authkeysSeedTarget, keys []authkeys.AuthorizedKey) authkeysSeedResult {
	result := authkeysSeedResult{Target: target.Alias, Route: append([]string(nil), target.Hops...), VerificationStatus: "failed"}
	if len(cmd.Argv) == 0 {
		result.ExitCode = 1
		result.VerificationMessage = "empty SSH command"
		return result
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	proc := exec.Command(cmd.Argv[0], cmd.Argv[1:]...)
	proc.Stdin = strings.NewReader(authkeysSeedVerifyRemoteScript(keys))
	proc.Stdout = &stdout
	proc.Stderr = &stderr
	err := proc.Run()
	if err != nil {
		result.ExitCode = commandExitCode(err)
		result.VerificationMessage = firstNonEmptyLine(stderr.String(), stdout.String(), err.Error())
		return result
	}

	parsed, ok := parseAuthkeysSeedVerifyResult(stdout.String())
	if !ok {
		result.ExitCode = 1
		result.VerificationMessage = firstNonEmptyLine(stdout.String(), "remote verification command did not return a result")
		return result
	}
	parsed.Target = target.Alias
	parsed.Route = append([]string(nil), target.Hops...)
	return parsed
}

func commandExitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if code := exitErr.ExitCode(); code >= 0 {
			return code
		}
	}
	return 1
}

func parseAuthkeysSeedRemoteResult(output string) (authkeysSeedResult, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SSHERPA_AUTHKEYS_SEED ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "SSHERPA_AUTHKEYS_SEED "))
		result := authkeysSeedResult{}
		for _, field := range fields {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "status":
				result.Status = value
			case "added":
				result.Added, _ = strconv.Atoi(value)
			case "already_present":
				result.AlreadyPresent, _ = strconv.Atoi(value)
			}
		}
		if result.Status == "" {
			if result.Added > 0 {
				result.Status = "added"
			} else {
				result.Status = "unchanged"
			}
		}
		return result, true
	}
	return authkeysSeedResult{}, false
}

func parseAuthkeysSeedVerifyResult(output string) (authkeysSeedResult, bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "SSHERPA_AUTHKEYS_VERIFY ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "SSHERPA_AUTHKEYS_VERIFY "))
		result := authkeysSeedResult{}
		for _, field := range fields {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "status":
				result.VerificationStatus = value
			case "verified":
				result.Verified, _ = strconv.Atoi(value)
			case "missing":
				result.Missing, _ = strconv.Atoi(value)
			}
		}
		if result.VerificationStatus == "" {
			if result.Missing > 0 {
				result.VerificationStatus = "missing"
			} else {
				result.VerificationStatus = "verified"
			}
		}
		if result.VerificationStatus != "verified" {
			result.ExitCode = 1
			result.VerificationMessage = "one or more seeded keys were not found after reconnecting"
		}
		return result, true
	}
	return authkeysSeedResult{}, false
}

func authkeysSeedRemoteScript(keys []authkeys.AuthorizedKey, dryRun bool) string {
	delimiter := authkeysSeedDelimiter(keys)
	var b strings.Builder
	b.WriteString("dry_run=")
	if dryRun {
		b.WriteString("1\n")
	} else {
		b.WriteString("0\n")
	}
	b.WriteString(`key_identity() {
  awk '{
    for (i = 1; i < NF; i++) {
      if ($i == "ssh-ed25519" || $i == "ssh-rsa" || $i == "rsa-sha2-256" || $i == "rsa-sha2-512" || $i ~ /^ecdsa-sha2-/ || $i ~ /^sk-ssh-ed25519/ || $i ~ /^sk-ecdsa-sha2-/) {
        print $i " " $(i + 1)
        exit
      }
    }
  }'
}
ak_dir="${HOME:?}/.ssh"
ak_file="$ak_dir/authorized_keys"
tmp="${TMPDIR:-/tmp}/ssherpa-authkeys-seed.$$"
trap 'rm -f "$tmp"' EXIT HUP INT TERM
cat > "$tmp" <<'`)
	b.WriteString(delimiter)
	b.WriteString("'\n")
	for _, key := range keys {
		b.WriteString(key.Render())
		b.WriteByte('\n')
	}
	b.WriteString(delimiter)
	b.WriteString(`
if [ -d "$ak_file" ]; then
  printf '%s\n' 'authorized_keys is a directory' >&2
  exit 20
fi
if [ "$dry_run" != "1" ]; then
  mkdir -p "$ak_dir" || exit 21
  chmod 700 "$ak_dir" || exit 22
  touch "$ak_file" || exit 23
  chmod 600 "$ak_file" || exit 24
fi
if [ -e "$ak_file" ] && [ ! -r "$ak_file" ]; then
  printf '%s\n' 'authorized_keys is not readable by the login user' >&2
  exit 25
fi
added=0
already_present=0
existing=""
if [ -f "$ak_file" ]; then
  existing=$(while IFS= read -r existing_line || [ -n "$existing_line" ]; do
    key_identity <<EOF
$existing_line
EOF
  done < "$ak_file")
fi
while IFS= read -r key_line || [ -n "$key_line" ]; do
  [ -n "$key_line" ] || continue
  identity=$(key_identity <<EOF
$key_line
EOF
)
  if printf '%s\n' "$existing" | grep -F -x -- "$identity" >/dev/null 2>&1; then
    already_present=$((already_present + 1))
    continue
  fi
  added=$((added + 1))
  existing=$(printf '%s\n%s\n' "$existing" "$identity")
  if [ "$dry_run" != "1" ]; then
    printf '%s\n' "$key_line" >> "$ak_file" || exit 26
  fi
done < "$tmp"
status=unchanged
if [ "$added" -gt 0 ]; then
  if [ "$dry_run" = "1" ]; then
    status=would-add
  else
    status=added
  fi
fi
printf 'SSHERPA_AUTHKEYS_SEED status=%s added=%s already_present=%s\n' "$status" "$added" "$already_present"
`)
	return b.String()
}

func authkeysSeedVerifyRemoteScript(keys []authkeys.AuthorizedKey) string {
	delimiter := authkeysSeedDelimiter(keys)
	var b strings.Builder
	b.WriteString(`key_identity() {
  awk '{
    for (i = 1; i < NF; i++) {
      if ($i == "ssh-ed25519" || $i == "ssh-rsa" || $i == "rsa-sha2-256" || $i == "rsa-sha2-512" || $i ~ /^ecdsa-sha2-/ || $i ~ /^sk-ssh-ed25519/ || $i ~ /^sk-ecdsa-sha2-/) {
        print $i " " $(i + 1)
        exit
      }
    }
  }'
}
ak_file="${HOME:?}/.ssh/authorized_keys"
tmp="${TMPDIR:-/tmp}/ssherpa-authkeys-verify.$$"
trap 'rm -f "$tmp"' EXIT HUP INT TERM
cat > "$tmp" <<'`)
	b.WriteString(delimiter)
	b.WriteString("'\n")
	for _, key := range keys {
		b.WriteString(key.Render())
		b.WriteByte('\n')
	}
	b.WriteString(delimiter)
	b.WriteString(`
if [ ! -f "$ak_file" ]; then
  printf '%s\n' 'authorized_keys does not exist after seed' >&2
  exit 30
fi
if [ ! -r "$ak_file" ]; then
  printf '%s\n' 'authorized_keys is not readable by the login user' >&2
  exit 31
fi
existing=$(while IFS= read -r existing_line || [ -n "$existing_line" ]; do
  key_identity <<EOF
$existing_line
EOF
done < "$ak_file")
verified=0
missing=0
while IFS= read -r key_line || [ -n "$key_line" ]; do
  [ -n "$key_line" ] || continue
  identity=$(key_identity <<EOF
$key_line
EOF
)
  if printf '%s\n' "$existing" | grep -F -x -- "$identity" >/dev/null 2>&1; then
    verified=$((verified + 1))
  else
    missing=$((missing + 1))
  fi
done < "$tmp"
status=verified
if [ "$missing" -gt 0 ]; then
  status=missing
fi
printf 'SSHERPA_AUTHKEYS_VERIFY status=%s verified=%s missing=%s\n' "$status" "$verified" "$missing"
`)
	return b.String()
}

func authkeysSeedDelimiter(keys []authkeys.AuthorizedKey) string {
	base := "SSHERPA_AUTHKEYS_KEYS"
	delimiter := base
	for i := 2; ; i++ {
		found := false
		for _, key := range keys {
			if strings.Contains(key.Render(), delimiter) {
				found = true
				break
			}
		}
		if !found {
			return delimiter
		}
		delimiter = fmt.Sprintf("%s_%d", base, i)
	}
}

func validateAuthkeysSeedKeySource(flags authkeysSeedFlags) error {
	sources := 0
	for _, value := range []string{flags.Key, flags.KeyFile, flags.FromDir} {
		if strings.TrimSpace(value) != "" {
			sources++
		}
	}
	if sources != 1 {
		return errors.New("authkeys seed requires exactly one of --key, --key-file, or --from-dir")
	}
	return nil
}

func authkeysSeedKeys(flags authkeysSeedFlags) ([]authkeys.AuthorizedKey, []authkeys.Diagnostic, error) {
	validator := authkeys.Validator{SSHKeygenPath: flags.SSHKeygenPath}
	switch {
	case flags.Key != "":
		key, err := authkeys.ParsePublicKeyLine(flags.Key)
		if err != nil {
			return nil, nil, err
		}
		if _, err := validator.Validate(key); err != nil {
			return nil, nil, err
		}
		return []authkeys.AuthorizedKey{key}, nil, nil
	case flags.KeyFile != "":
		data, err := os.ReadFile(flags.KeyFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read %s: %w", flags.KeyFile, err)
		}
		key, diagnostics, err := authkeys.ParseFirstKey(data, flags.KeyFile, validator)
		if err != nil {
			return nil, diagnostics, err
		}
		return []authkeys.AuthorizedKey{key}, diagnostics, nil
	default:
		imported, err := authkeys.CollectFromDir(flags.FromDir, validator)
		return imported.Keys, imported.Diagnostics, err
	}
}

func resolveAuthkeysSeedTargets(flags authkeysSeedFlags, inventory hostlist.Inventory) ([]authkeysSeedTarget, error) {
	var targets []authkeysSeedTarget
	seen := map[string]bool{}
	for _, name := range flags.Targets {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		if findAlias(inventory.Aliases, name) == nil {
			return nil, fmt.Errorf("alias %q not found", name)
		}
		hops := append([]string(nil), flags.Hops[name]...)
		if err := sshcmd.ValidateJumpRoute(name, hops); err != nil && len(hops) > 0 {
			return nil, err
		}
		for _, hop := range hops {
			if findAlias(inventory.Aliases, hop) == nil {
				return nil, fmt.Errorf("alias %q not found", hop)
			}
		}
		targets = append(targets, authkeysSeedTarget{Alias: name, Hops: hops})
	}
	if len(targets) == 0 {
		return nil, errors.New("authkeys seed requires at least one non-empty --target")
	}
	for target := range flags.Hops {
		if !seen[target] {
			return nil, fmt.Errorf("--hop target %q is not selected by --target", target)
		}
	}
	return targets, nil
}

func parseAuthkeysSeedFlags(args []string, stderr io.Writer) (authkeysSeedFlags, bool) {
	flags := authkeysSeedFlags{Timeout: defaultAuthkeysSeedTimeout, Hops: map[string][]string{}}
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
		case arg == "--from-dir":
			value, ok := nextArg(args, &i, stderr, "--from-dir")
			if !ok {
				return flags, false
			}
			flags.FromDir = value
		case strings.HasPrefix(arg, "--from-dir="):
			flags.FromDir = strings.TrimPrefix(arg, "--from-dir=")
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
			fmt.Fprintf(stderr, "ssherpa: unknown authkeys seed argument %q\n", arg)
			return flags, false
		default:
			flags.Targets = append(flags.Targets, arg)
		}
	}
	return flags, true
}

func parseAuthkeysSeedHop(value string, hops map[string][]string, stderr io.Writer) bool {
	target, route, ok := strings.Cut(value, "=")
	target = strings.TrimSpace(target)
	if !ok || target == "" {
		fmt.Fprintln(stderr, "ssherpa: --hop must be TARGET=HOP[,HOP...]")
		return false
	}
	parsed := splitHopArg(route)
	if len(parsed) == 0 {
		fmt.Fprintln(stderr, "ssherpa: --hop requires at least one hop")
		return false
	}
	hops[target] = parsed
	return true
}

func authkeysSeedConfirmMessage(keys []authkeys.AuthorizedKey, targets []authkeysSeedTarget, dryRun bool) string {
	action := "Seed"
	if dryRun {
		action = "Dry-run seed"
	}
	return fmt.Sprintf("%s %d key(s) to %d host(s). Writes only the SSH login user's ~/.ssh/authorized_keys.", action, len(keys), len(targets))
}

func printAuthkeysSeedResult(stdout io.Writer, result authkeysSeedResult, dryRun bool) {
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
	if dryRun && status == "added" {
		status = "would-add"
	}
	verify := ""
	if result.VerificationStatus != "" {
		verify = fmt.Sprintf(" verify=%s", authkeysSeedVerifyLabel(result))
	}
	fmt.Fprintf(stdout, "[%s] %s added=%d already-present=%d route=%s%s\n", status, result.Target, result.Added, result.AlreadyPresent, route, verify)
}

func printAuthkeysSeedSummary(stdout io.Writer, summary authkeysSeedSummary) {
	fmt.Fprintf(stdout, "[summary] ok=%d changed=%d unchanged=%d failed=%d\n", summary.OK, summary.Changed, summary.Unchanged, summary.Failed)
}

func summarizeAuthkeysSeedResults(results []authkeysSeedResult) authkeysSeedSummary {
	var summary authkeysSeedSummary
	for _, result := range results {
		switch result.Status {
		case "failed":
			summary.Failed++
		case "unchanged":
			if authkeysSeedVerificationFailed(result) {
				summary.Failed++
			} else {
				summary.OK++
				summary.Unchanged++
			}
		default:
			if authkeysSeedVerificationFailed(result) {
				summary.Failed++
			} else {
				summary.OK++
				summary.Changed++
			}
		}
	}
	return summary
}

func authkeysSeedVerificationFailed(result authkeysSeedResult) bool {
	return result.VerificationStatus == "failed" || result.VerificationStatus == "missing"
}

func authkeysSeedVerifyLabel(result authkeysSeedResult) string {
	switch result.VerificationStatus {
	case "verified":
		return fmt.Sprintf("verified(%d/%d)", result.Verified, result.Verified+result.Missing)
	case "missing":
		return fmt.Sprintf("missing(%d)", result.Missing)
	case "failed":
		if result.VerificationMessage != "" {
			return "failed"
		}
		return "failed"
	case "skipped":
		return "skipped"
	default:
		return result.VerificationStatus
	}
}

func firstNonEmptyLine(values ...string) string {
	for _, value := range values {
		for _, line := range strings.Split(value, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				return line
			}
		}
	}
	return ""
}

func runAuthkeysSeedInteractive(flags authkeysSeedFlags, stdout io.Writer, stderr io.Writer) int {
	if flags.Hops == nil {
		flags.Hops = map[string][]string{}
	}
	item, ok, err := ui.ChooseManagement(context.Background(), authkeysSeedSourceItems(), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Seed authorized keys",
		Mode:        "choose key source",
		Steps:       []string{"source", "keys", "hosts", "routes", "confirm"},
		CurrentStep: 0,
		Footer:      "enter select / type filter / arrows move / shift+arrows section / Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: seed picker failed: %v\n", err)
		return 1
	}
	if !ok || item.Token == "back" {
		fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
		return 0
	}
	switch item.Token {
	case "paste":
		line, ok, err := promptText(stderr, "Seed authorized keys", "key", "", validateNonEmpty("SSH public key"))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
			return 0
		}
		flags.Key = line
	case "file":
		path, ok, err := pickAuthkeysSeedFile(stderr)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
			return 0
		}
		flags.KeyFile = path
	case "directory":
		dir, ok, err := pickAuthkeysDirectory(stderr, "SSHERPA AUTHKEYS SEED SOURCE")
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
			return 0
		}
		flags.FromDir = dir
	}
	return runAuthkeysSeedInteractiveTargets(flags, stdout, stderr)
}

func authkeysSeedSourceItems() []ui.ManagementItem {
	return []ui.ManagementItem{
		{Kind: ui.ItemAuthkeys, Token: "paste", Title: "Paste one public key", Description: "enter a single authorized_keys line", Group: "Key Source", Badge: "key", Action: "Paste one public key to seed remotely"},
		{Kind: ui.ItemAuthkeys, Token: "file", Title: "Use public key file", Description: "read the first valid key from a .pub file", Group: "Key Source", Badge: "file", Action: "Load one public key from a local file path"},
		{Kind: ui.ItemAuthkeys, Token: "directory", Title: "Use directory of keys", Description: "read authorized_keys/ or *.pub files", Group: "Key Source", Badge: "dir", Action: "Load all valid public keys from a local directory"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to authorized_keys actions", Group: "Navigation", Badge: "back", Action: "Return without seeding keys"},
	}
}

func pickAuthkeysSeedFile(stderr io.Writer) (string, bool, error) {
	opts := transferBrowserOptions(stderr, filePickerOptions{}, "SSHERPA AUTHKEYS SEED FILE", "local-file", "LOCAL", ".", []string{"source", "keys", "hosts", "routes", "confirm"}, 1)
	opts.Footer = "enter open/use / type filter / arrows move / shift+arrows section / Q cancel"
	out, ok, err := ui.BrowseTransfer(context.Background(), localFileSource(), opts)
	if err != nil || !ok {
		return "", ok, err
	}
	return out.Token(), true, nil
}

func runAuthkeysSeedInteractiveTargets(flags authkeysSeedFlags, stdout io.Writer, stderr io.Writer) int {
	if !validateExplicitSSHKeygen(authkeysFlags{SSHKeygenPath: flags.SSHKeygenPath}, stderr) {
		return 1
	}
	if err := validateAuthkeysSeedKeySource(flags); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	keys, diagnostics, err := authkeysSeedKeys(flags)
	for _, diagnostic := range diagnostics {
		printAuthkeysDiagnostic(stderr, diagnostic)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if len(keys) == 0 {
		fmt.Fprintln(stderr, "ssherpa: no valid SSH public keys selected")
		return 1
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
		Title:       "Seed: pick remote hosts",
		Mode:        "choose remote seed targets",
		Steps:       []string{"source", "keys", "hosts", "routes", "confirm"},
		CurrentStep: 2,
		Footer:      "space toggle / enter continue / type filter / arrows move / shift+arrows section / Q back",
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: host picker failed: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
		return 0
	}
	flags.Targets = selected
	if flags.Hops == nil {
		flags.Hops = map[string][]string{}
	}
	routed, ok, code := configureAuthkeysSeedRoutes(flags, inventory, stderr)
	if !ok {
		if code != 0 {
			return code
		}
		fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
		return 0
	}
	flags = routed
	targets, err := resolveAuthkeysSeedTargets(flags, inventory)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	yes, answered, err := ui.Confirm(context.Background(), ui.ConfirmOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Confirm authorized key seed",
		Message:     authkeysSeedReviewMessage(keys, targets, flags.DryRun),
	})
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: seed confirmation failed: %v\n", err)
		return 1
	}
	if !answered || !yes {
		fmt.Fprintln(stdout, "[skipped] authkeys seed cancelled")
		return 0
	}
	flags.Yes = true
	out := executeAuthkeysSeed(flags, keys, targets, stdout)
	if err := showAuthkeysSeedReportScreen(out, stderr); err != nil {
		fmt.Fprintf(stderr, "ssherpa: seed report failed: %v\n", err)
		return 1
	}
	if out.Summary.Failed > 0 {
		return 2
	}
	return 0
}

func configureAuthkeysSeedRoutes(flags authkeysSeedFlags, inventory hostlist.Inventory, stderr io.Writer) (authkeysSeedFlags, bool, int) {
	for {
		items := authkeysSeedRouteItems(flags)
		item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			Title:       "Seed: configure routes",
			Mode:        "direct or ProxyJump per host",
			Steps:       []string{"source", "keys", "hosts", "routes", "confirm"},
			CurrentStep: 3,
			Summary:     authkeysSeedRouteSummary(flags),
			Footer:      "enter select / type filter / arrows move / shift+arrows section / Q back",
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
		updated, ok, code := configureAuthkeysSeedTargetRoute(flags, inventory, target, stderr)
		if !ok {
			if code != 0 {
				return flags, false, code
			}
			continue
		}
		flags = updated
	}
}

func authkeysSeedRouteItems(flags authkeysSeedFlags) []ui.ManagementItem {
	items := []ui.ManagementItem{{
		Kind:        ui.ItemCheck,
		Token:       "continue",
		Title:       "Continue to review",
		Description: authkeysSeedRouteSummary(flags),
		Group:       "Action",
		Badge:       "done",
		Action:      "Review the seed plan before running",
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
			Detail:      "writes login user's ~/.ssh/authorized_keys",
			Group:       "Targets",
			Badge:       "host",
			Action:      "Set direct or ProxyJump route for this host",
		})
	}
	return items
}

func configureAuthkeysSeedTargetRoute(flags authkeysSeedFlags, inventory hostlist.Inventory, target string, stderr io.Writer) (authkeysSeedFlags, bool, int) {
	item, ok, err := ui.ChooseManagement(context.Background(), authkeysSeedTargetRouteItems(target, flags.Hops[target]), ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Seed: route for " + target,
		Mode:        "choose route type",
		Steps:       []string{"source", "keys", "hosts", "routes", "confirm"},
		CurrentStep: 3,
		Footer:      "enter select / type filter / arrows move / Q back",
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

func authkeysSeedTargetRouteItems(target string, hops []string) []ui.ManagementItem {
	current := "current: direct"
	if len(hops) > 0 {
		current = "current: via " + strings.Join(hops, ",")
	}
	return []ui.ManagementItem{
		{Kind: ui.ItemAlias, Token: "direct", Title: "Direct", Description: current, Group: "Route", Badge: "direct", Action: "Connect directly to " + target},
		{Kind: ui.ItemJump, Token: "jump", Title: "Choose jump hops", Description: current, Group: "Route", Badge: "jump", Action: "Connect to " + target + " through ProxyJump hops"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to selected hosts", Group: "Navigation", Badge: "back", Action: "Return without changing this route"},
	}
}

func pickAuthkeysSeedHops(target string, aliases []hostlist.Alias, stderr io.Writer) ([]string, bool, int) {
	firstChoices := aliasesExcluding(aliases, target, nil)
	firstHop, ok, err := pickAlias(firstChoices, false, "", "", "Jump: pick first hop", stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
		return nil, false, 1
	}
	if !ok {
		return nil, false, 0
	}
	hops := []string{firstHop.Name}
	for {
		choices := aliasesExcluding(aliases, target, hops)
		if len(choices) == 0 {
			break
		}
		choice, ok, err := ui.ChooseJumpHop(context.Background(), choices, ui.JumpHopChooserOptions{
			Input:        os.Stdin,
			Output:       stderr,
			NoAltScreen:  envBool("SSHERPA_NO_ALT_SCREEN"),
			Destination:  target,
			Hops:         hops,
			RouteSummary: routeSummary(target, hops),
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: picker failed: %v\n", err)
			return nil, false, 1
		}
		if !ok {
			return nil, false, 0
		}
		if choice.Done {
			break
		}
		hops = append(hops, choice.Alias)
	}
	return hops, true, 0
}

func authkeysSeedRouteSummary(flags authkeysSeedFlags) string {
	routed := 0
	for _, target := range flags.Targets {
		if len(flags.Hops[target]) > 0 {
			routed++
		}
	}
	direct := len(flags.Targets) - routed
	return fmt.Sprintf("%d direct  %d routed", direct, routed)
}

func authkeysSeedReviewMessage(keys []authkeys.AuthorizedKey, targets []authkeysSeedTarget, dryRun bool) string {
	message := authkeysSeedConfirmMessage(keys, targets, dryRun)
	var routed []string
	for _, target := range targets {
		if len(target.Hops) > 0 {
			routed = append(routed, target.Alias+" via "+strings.Join(target.Hops, ","))
		}
	}
	if len(routed) > 0 {
		message += " Routed: " + strings.Join(routed, "; ") + "."
	}
	return message
}

func showAuthkeysSeedReportScreen(out authkeysSeedOutput, stderr io.Writer) error {
	return ui.ShowTextView(context.Background(), ui.TextViewOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Authorized key seed report",
		Steps:       []string{"source", "keys", "hosts", "routes", "confirm", "report"},
		CurrentStep: 5,
		Summary:     authkeysSeedReportSummary(out),
		Lines:       authkeysSeedReportLines(out),
		Footer:      "up/down scroll / pgup/pgdn page / q close report",
	})
}

func authkeysSeedReportSummary(out authkeysSeedOutput) string {
	return fmt.Sprintf("ok=%d  changed=%d  unchanged=%d  failed=%d", out.Summary.OK, out.Summary.Changed, out.Summary.Unchanged, out.Summary.Failed)
}

func authkeysSeedReportLines(out authkeysSeedOutput) []string {
	overall := "complete"
	if out.Summary.Failed > 0 {
		overall = "needs attention"
	}
	lines := []string{
		"Authorized key seed report",
		"",
		"Overall: " + overall,
		fmt.Sprintf("Dry run: %s", yesNo(out.DryRun)),
		fmt.Sprintf("Summary: ok=%d changed=%d unchanged=%d failed=%d", out.Summary.OK, out.Summary.Changed, out.Summary.Unchanged, out.Summary.Failed),
		fmt.Sprintf("Verification: %s", authkeysSeedReportVerificationSummary(out)),
		"",
		"Keys:",
	}
	for _, key := range out.Keys {
		comment := key.Comment
		if comment == "" {
			comment = "-"
		}
		lines = append(lines, fmt.Sprintf("  %s  %s  %s", key.Fingerprint, key.Type, comment))
	}
	lines = append(lines, "", "Hosts:")
	for _, result := range out.Results {
		route := "direct"
		if len(result.Route) > 0 {
			route = "via " + strings.Join(result.Route, ",")
		}
		hostStatus := result.Status
		if authkeysSeedVerificationFailed(result) {
			hostStatus += " / verification " + result.VerificationStatus
		}
		lines = append(lines,
			fmt.Sprintf("  %s", result.Target),
			fmt.Sprintf("    route: %s", route),
			fmt.Sprintf("    status: %s", hostStatus),
			fmt.Sprintf("    write: added=%d  already-present=%d", result.Added, result.AlreadyPresent),
		)
		if result.Message != "" {
			lines = append(lines, "    message: "+result.Message)
		}
		if result.VerificationStatus != "" {
			lines = append(lines, fmt.Sprintf("    verify: %s  verified=%d  missing=%d", result.VerificationStatus, result.Verified, result.Missing))
		}
		if result.VerificationMessage != "" {
			lines = append(lines, "    verify-message: "+result.VerificationMessage)
		}
	}
	lines = append(lines, "", "Verification reconnects to each successful target and reads the SSH login user's ~/.ssh/authorized_keys.")
	if out.Summary.Failed > 0 {
		lines = append(lines, "Review failed hosts above before relying on the seeded key.")
	}
	return lines
}

func authkeysSeedReportVerificationSummary(out authkeysSeedOutput) string {
	verified := 0
	missing := 0
	failed := 0
	skipped := 0
	for _, result := range out.Results {
		switch result.VerificationStatus {
		case "verified":
			verified++
		case "missing":
			missing++
		case "failed":
			failed++
		case "skipped":
			skipped++
		}
	}
	parts := []string{}
	if verified > 0 {
		parts = append(parts, fmt.Sprintf("verified=%d", verified))
	}
	if missing > 0 {
		parts = append(parts, fmt.Sprintf("missing=%d", missing))
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
