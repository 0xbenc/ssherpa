package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"

	"github.com/0xbenc/ssherpa/internal/authkeys"
	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/sshkeys"
)

const keyUsage = `Usage:
  ssherpa key import --from PATH [--name NAME] [--register] [--force] [--dry-run] [--yes] [--json] [--ssh-keygen PATH]
  ssherpa key generate [--name NAME] [--type ed25519|rsa|ecdsa] [--comment C] [--register] [--force] [--dry-run] [--yes] [--json]

Set up your OWN SSH keypair in ~/.ssh with safe permissions (the directory
0700, private 0600, public 0644 — ssh refuses a loose-perm private key) and
print its SHA256 fingerprint.

  import    Copy an existing keypair from PATH (a backup/USB). The source is
            left untouched. An encrypted key needs its passphrase: set
            SSHERPA_KEY_PASSPHRASE, pass --passphrase-fd N, or run from a
            terminal to be prompted.
  generate  Create a fresh keypair with ssh-keygen (default ed25519). It has no
            passphrase unless SSHERPA_KEY_PASSPHRASE / --passphrase-fd is set.

--register adds the key as the default identity (Host * IdentityFile, absolute
path) in ~/.ssh/config — idempotent and backed up. For a single host, set the
identity on that alias with "ssherpa edit" instead.

Re-importing the same key is a no-op; a different key with the same name is
refused unless --force.
`

func runKey(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, keyUsage)
		return 0
	}
	if len(args) == 0 {
		fmt.Fprint(stdout, keyUsage)
		return 1
	}
	switch args[0] {
	case "import":
		return runKeyImport(args[1:], stdout, stderr)
	case "generate", "gen":
		return runKeyGenerate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ssherpa: unknown key command %q\n", args[0])
		return 1
	}
}

type keyImportFlags struct {
	From          string
	Name          string
	Force         bool
	Register      bool
	DryRun        bool
	Yes           bool
	JSON          bool
	SSHKeygenPath string
	PassphraseFD  int // -1 = unset
}

func parseKeyImportFlags(args []string, stderr io.Writer) (keyImportFlags, bool) {
	flags := keyImportFlags{PassphraseFD: -1}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--from":
			value, ok := nextArg(args, &i, stderr, "--from")
			if !ok {
				return flags, false
			}
			flags.From = value
		case strings.HasPrefix(arg, "--from="):
			flags.From = strings.TrimPrefix(arg, "--from=")
		case arg == "--name":
			value, ok := nextArg(args, &i, stderr, "--name")
			if !ok {
				return flags, false
			}
			flags.Name = value
		case strings.HasPrefix(arg, "--name="):
			flags.Name = strings.TrimPrefix(arg, "--name=")
		case arg == "--ssh-keygen":
			value, ok := nextArg(args, &i, stderr, "--ssh-keygen")
			if !ok {
				return flags, false
			}
			flags.SSHKeygenPath = value
		case strings.HasPrefix(arg, "--ssh-keygen="):
			flags.SSHKeygenPath = strings.TrimPrefix(arg, "--ssh-keygen=")
		case arg == "--passphrase-fd":
			value, ok := nextArg(args, &i, stderr, "--passphrase-fd")
			if !ok {
				return flags, false
			}
			fd, err := strconv.Atoi(value)
			if err != nil || fd < 0 {
				fmt.Fprintf(stderr, "ssherpa: invalid --passphrase-fd %q\n", value)
				return flags, false
			}
			flags.PassphraseFD = fd
		case arg == "--force":
			flags.Force = true
		case arg == "--register":
			flags.Register = true
		case arg == "--dry-run":
			flags.DryRun = true
		case arg == "--yes":
			flags.Yes = true
		case arg == "--json":
			flags.JSON = true
		default:
			fmt.Fprintf(stderr, "ssherpa: unknown key import flag %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

type keyImportOutput struct {
	Fingerprint string `json:"fingerprint"`
	Type        string `json:"type"`
	Comment     string `json:"comment,omitempty"`
	Name        string `json:"name"`
	PrivatePath string `json:"private_path"`
	PublicPath  string `json:"public_path"`
	DryRun      bool   `json:"dry_run"`
	Imported    bool   `json:"imported"`
	Skipped     bool   `json:"skipped,omitempty"`
	Registered  bool   `json:"registered,omitempty"`
}

func runKeyImport(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseKeyImportFlags(args, stderr)
	if !ok {
		return 1
	}
	if strings.TrimSpace(flags.From) == "" {
		fmt.Fprintln(stderr, "ssherpa: key import requires --from PATH")
		return 1
	}
	if flags.SSHKeygenPath != "" {
		if !validateExplicitSSHKeygen(authkeysFlags{SSHKeygenPath: flags.SSHKeygenPath}, stderr) {
			return 1
		}
	}

	src, err := expandUserPath(flags.From)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	data, err := os.ReadFile(src)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read %s: %v\n", src, err)
		return 1
	}
	// Friendly guard: a public key pointed at by mistake.
	if _, _, perr := authkeys.ParseFirstKey(data, src, authkeys.Validator{SkipSSHKeygen: true}); perr == nil {
		fmt.Fprintf(stderr, "ssherpa: %s looks like a PUBLIC key; point --from at the PRIVATE key\n", src)
		return 1
	}

	kg := sshkeys.Keygen{Path: flags.SSHKeygenPath}
	ctx := context.Background()
	info, err := kg.DeriveBytes(ctx, data, "")
	if err == sshkeys.ErrEncrypted {
		pass, ok := readKeyPassphrase(flags, stderr)
		if !ok {
			return 1
		}
		info, err = kg.DeriveBytes(ctx, data, pass)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	name := strings.TrimSpace(flags.Name)
	if name == "" {
		name = defaultKeyName(src)
	}
	sshDir, err := defaultSSHDir()
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	privDest := filepath.Join(sshDir, name)
	out := keyImportOutput{
		Fingerprint: info.Fingerprint,
		Type:        info.Type,
		Comment:     info.Comment,
		Name:        name,
		PrivatePath: privDest,
		PublicPath:  privDest + ".pub",
		DryRun:      flags.DryRun,
	}

	if !flags.JSON {
		printKeyImportPreview(stderr, info, privDest, flags.DryRun)
		if flags.Register {
			fmt.Fprintf(stderr, "  register    default identity in ~/.ssh/config\n")
		}
	}
	if flags.DryRun {
		if flags.JSON {
			writeJSON(stdout, out)
		}
		return 0
	}
	if !flags.Yes {
		yes, err := confirmActionChoice(stderr, "Import SSH key", fmt.Sprintf("%s into %s", info.Fingerprint, privDest))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if !yes {
			fmt.Fprintln(stderr, "Cancelled.")
			return 0
		}
	}

	res, err := sshkeys.Place(sshDir, name, data, info.PublicLine, flags.Force, fsutilWriteFunc)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	out.Imported = !res.Skipped
	out.Skipped = res.Skipped
	out.PrivatePath = res.PrivatePath
	out.PublicPath = res.PublicPath

	if flags.Register {
		registered, regErr := registerKeyIdentity(res.PrivatePath, stderr)
		if regErr != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", regErr)
			return 1
		}
		out.Registered = registered
	}

	if flags.JSON {
		writeJSON(stdout, out)
		return 0
	}
	if res.Skipped {
		fmt.Fprintf(stderr, "Already present (perms repaired): %s\n  %s\n", info.Fingerprint, res.PrivatePath)
	} else {
		fmt.Fprintf(stderr, "Imported %s (%s)\n  %s  (0600)\n  %s  (0644)\n", info.Fingerprint, info.Type, res.PrivatePath, res.PublicPath)
	}
	return 0
}

// registerKeyIdentity registers an absolute key path as the global default
// identity in ~/.ssh/config.
func registerKeyIdentity(keyPath string, stderr io.Writer) (bool, error) {
	configPath, err := defaultSSHConfigPath()
	if err != nil {
		return false, err
	}
	return registerGlobalIdentity(configPath, keyPath, stderr)
}

type keyGenerateFlags struct {
	Name          string
	Type          string
	Comment       string
	Bits          int
	Force         bool
	Register      bool
	DryRun        bool
	Yes           bool
	JSON          bool
	SSHKeygenPath string
	PassphraseFD  int
}

func parseKeyGenerateFlags(args []string, stderr io.Writer) (keyGenerateFlags, bool) {
	flags := keyGenerateFlags{PassphraseFD: -1}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--name":
			value, ok := nextArg(args, &i, stderr, "--name")
			if !ok {
				return flags, false
			}
			flags.Name = value
		case strings.HasPrefix(arg, "--name="):
			flags.Name = strings.TrimPrefix(arg, "--name=")
		case arg == "--type":
			value, ok := nextArg(args, &i, stderr, "--type")
			if !ok {
				return flags, false
			}
			flags.Type = value
		case strings.HasPrefix(arg, "--type="):
			flags.Type = strings.TrimPrefix(arg, "--type=")
		case arg == "--comment":
			value, ok := nextArg(args, &i, stderr, "--comment")
			if !ok {
				return flags, false
			}
			flags.Comment = value
		case strings.HasPrefix(arg, "--comment="):
			flags.Comment = strings.TrimPrefix(arg, "--comment=")
		case arg == "--bits":
			value, ok := nextArg(args, &i, stderr, "--bits")
			if !ok {
				return flags, false
			}
			n, err := strconv.Atoi(value)
			if err != nil || n <= 0 {
				fmt.Fprintf(stderr, "ssherpa: invalid --bits %q\n", value)
				return flags, false
			}
			flags.Bits = n
		case arg == "--ssh-keygen":
			value, ok := nextArg(args, &i, stderr, "--ssh-keygen")
			if !ok {
				return flags, false
			}
			flags.SSHKeygenPath = value
		case strings.HasPrefix(arg, "--ssh-keygen="):
			flags.SSHKeygenPath = strings.TrimPrefix(arg, "--ssh-keygen=")
		case arg == "--passphrase-fd":
			value, ok := nextArg(args, &i, stderr, "--passphrase-fd")
			if !ok {
				return flags, false
			}
			fd, err := strconv.Atoi(value)
			if err != nil || fd < 0 {
				fmt.Fprintf(stderr, "ssherpa: invalid --passphrase-fd %q\n", value)
				return flags, false
			}
			flags.PassphraseFD = fd
		case arg == "--force":
			flags.Force = true
		case arg == "--register":
			flags.Register = true
		case arg == "--dry-run":
			flags.DryRun = true
		case arg == "--yes":
			flags.Yes = true
		case arg == "--json":
			flags.JSON = true
		default:
			fmt.Fprintf(stderr, "ssherpa: unknown key generate flag %q\n", arg)
			return flags, false
		}
	}
	return flags, true
}

type keyGenerateOutput struct {
	Fingerprint string `json:"fingerprint,omitempty"`
	Type        string `json:"type"`
	Comment     string `json:"comment,omitempty"`
	Name        string `json:"name"`
	PrivatePath string `json:"private_path"`
	PublicPath  string `json:"public_path"`
	DryRun      bool   `json:"dry_run"`
	Generated   bool   `json:"generated"`
	Registered  bool   `json:"registered,omitempty"`
}

func runKeyGenerate(args []string, stdout io.Writer, stderr io.Writer) int {
	flags, ok := parseKeyGenerateFlags(args, stderr)
	if !ok {
		return 1
	}
	keyType := strings.TrimSpace(flags.Type)
	if keyType == "" {
		keyType = "ed25519"
	}
	if !sshkeys.SupportedGenerateType(keyType) {
		fmt.Fprintf(stderr, "ssherpa: unsupported key type %q (use ed25519, rsa, or ecdsa)\n", keyType)
		return 1
	}
	if flags.SSHKeygenPath != "" && !validateExplicitSSHKeygen(authkeysFlags{SSHKeygenPath: flags.SSHKeygenPath}, stderr) {
		return 1
	}

	name := strings.TrimSpace(flags.Name)
	if name == "" {
		name = "id_" + keyType
	}
	sshDir, err := defaultSSHDir()
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	privDest := filepath.Join(sshDir, name)
	out := keyGenerateOutput{
		Type:        keyType,
		Comment:     flags.Comment,
		Name:        name,
		PrivatePath: privDest,
		PublicPath:  privDest + ".pub",
		DryRun:      flags.DryRun,
	}

	if !flags.JSON {
		verb := "Generate"
		if flags.DryRun {
			verb = "Would generate"
		}
		fmt.Fprintf(stderr, "%s SSH key:\n", verb)
		fmt.Fprintf(stderr, "  type        %s\n", keyType)
		if flags.Comment != "" {
			fmt.Fprintf(stderr, "  comment     %s\n", flags.Comment)
		}
		fmt.Fprintf(stderr, "  private     %s  (0600)\n", privDest)
		fmt.Fprintf(stderr, "  public      %s  (0644)\n", privDest+".pub")
		fmt.Fprintf(stderr, "  ~/.ssh dir  0700\n")
		if flags.Register {
			fmt.Fprintf(stderr, "  register    default identity in ~/.ssh/config\n")
		}
	}
	if flags.DryRun {
		if flags.JSON {
			writeJSON(stdout, out)
		}
		return 0
	}
	if !flags.Yes {
		yes, err := confirmActionChoice(stderr, "Generate SSH key", fmt.Sprintf("%s into %s", keyType, privDest))
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
		if !yes {
			fmt.Fprintln(stderr, "Cancelled.")
			return 0
		}
	}

	passphrase := optionalNewPassphrase(flags)
	info, res, err := sshkeys.Keygen{Path: flags.SSHKeygenPath}.Generate(context.Background(), sshDir, name, sshkeys.GenerateOptions{
		Type:       keyType,
		Comment:    flags.Comment,
		Passphrase: passphrase,
		Bits:       flags.Bits,
	}, flags.Force)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	out.Fingerprint = info.Fingerprint
	out.PrivatePath = res.PrivatePath
	out.PublicPath = res.PublicPath
	out.Generated = true

	if flags.Register {
		registered, regErr := registerKeyIdentity(res.PrivatePath, stderr)
		if regErr != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", regErr)
			return 1
		}
		out.Registered = registered
	}

	if flags.JSON {
		writeJSON(stdout, out)
		return 0
	}
	fmt.Fprintf(stderr, "Generated %s (%s)\n  %s  (0600)\n  %s  (0644)\n", info.Fingerprint, info.Type, res.PrivatePath, res.PublicPath)
	return 0
}

// optionalNewPassphrase reads a NEW passphrase to protect a generated key from
// SSHERPA_KEY_PASSPHRASE or --passphrase-fd; empty means no passphrase.
func optionalNewPassphrase(flags keyGenerateFlags) string {
	if env := os.Getenv("SSHERPA_KEY_PASSPHRASE"); env != "" {
		return env
	}
	if flags.PassphraseFD >= 0 {
		if f := os.NewFile(uintptr(flags.PassphraseFD), "passphrase-fd"); f != nil {
			if data, err := io.ReadAll(f); err == nil {
				return strings.TrimRight(string(data), "\r\n")
			}
		}
	}
	return ""
}

func printKeyImportPreview(stderr io.Writer, info sshkeys.KeyInfo, privDest string, dryRun bool) {
	verb := "Import"
	if dryRun {
		verb = "Would import"
	}
	fmt.Fprintf(stderr, "%s SSH key:\n", verb)
	fmt.Fprintf(stderr, "  type        %s\n", info.Type)
	fmt.Fprintf(stderr, "  fingerprint %s\n", info.Fingerprint)
	if info.Comment != "" {
		fmt.Fprintf(stderr, "  comment     %s\n", info.Comment)
	}
	fmt.Fprintf(stderr, "  private     %s  (0600)\n", privDest)
	fmt.Fprintf(stderr, "  public      %s  (0644)\n", privDest+".pub")
	fmt.Fprintf(stderr, "  ~/.ssh dir  0700\n")
}

// fsutilWriteFunc adapts fsutil.AtomicWriteFile to sshkeys.WriteFunc.
func fsutilWriteFunc(path string, data []byte, mode os.FileMode, backup bool) (string, error) {
	res, err := fsutil.AtomicWriteFile(path, data, fsutil.WriteOptions{Mode: mode, Backup: backup})
	return res.BackupPath, err
}

func defaultSSHConfigPath() (string, error) {
	dir, err := defaultSSHDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config"), nil
}

// registerGlobalIdentity appends a `Host * / IdentityFile <abs>` stanza to the
// ssh config so the key is offered to every host (the "default identity" the
// new machine should use). It is idempotent — a no-op when that absolute path
// is already an IdentityFile anywhere in the config — and keeps a backup.
// IdentityFile must be an ABSOLUTE path: ssh expands ~ via getpwuid, not $HOME.
func registerGlobalIdentity(configPath, keyPath string, stderr io.Writer) (bool, error) {
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", configPath, err)
	}
	if identityLinePresent(data, keyPath) {
		fmt.Fprintf(stderr, "Identity already registered in %s\n", configPath)
		return false, nil
	}
	value := keyPath
	if strings.ContainsAny(value, " \t") {
		value = strconv.Quote(value)
	}
	var b []byte
	b = append(b, data...)
	if len(b) > 0 && b[len(b)-1] != '\n' {
		b = append(b, '\n')
	}
	b = append(b, []byte(fmt.Sprintf("\n# Added by ssherpa key — default identity\nHost *\n    IdentityFile %s\n", value))...)
	res, err := fsutil.AtomicWriteFile(configPath, b, fsutil.WriteOptions{Mode: 0o600, Backup: true})
	if err != nil {
		return false, fmt.Errorf("write %s: %w", configPath, err)
	}
	fmt.Fprintf(stderr, "Registered as default identity in %s\n", configPath)
	if res.BackupPath != "" {
		fmt.Fprintf(stderr, "  backup: %s\n", res.BackupPath)
	}
	return res.Changed, nil
}

func identityLinePresent(data []byte, keyPath string) bool {
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "IdentityFile") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
		if unquoted, err := strconv.Unquote(val); err == nil {
			val = unquoted
		}
		if val == keyPath {
			return true
		}
	}
	return false
}

func defaultSSHDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".ssh"), nil
}

func defaultKeyName(src string) string {
	name := filepath.Base(src)
	name = strings.TrimSuffix(name, ".pub")
	if name == "" || name == "." || name == "/" {
		return "id_imported"
	}
	return name
}

// readKeyPassphrase resolves the passphrase for an encrypted key from (in
// order) SSHERPA_KEY_PASSPHRASE, --passphrase-fd, or an interactive terminal
// prompt. It returns false with a message when none is available.
func readKeyPassphrase(flags keyImportFlags, stderr io.Writer) (string, bool) {
	if env := os.Getenv("SSHERPA_KEY_PASSPHRASE"); env != "" {
		return env, true
	}
	if flags.PassphraseFD >= 0 {
		f := os.NewFile(uintptr(flags.PassphraseFD), "passphrase-fd")
		if f == nil {
			fmt.Fprintf(stderr, "ssherpa: invalid --passphrase-fd %d\n", flags.PassphraseFD)
			return "", false
		}
		data, err := io.ReadAll(f)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: read passphrase fd: %v\n", err)
			return "", false
		}
		return strings.TrimRight(string(data), "\r\n"), true
	}
	if term.IsTerminal(os.Stdin.Fd()) {
		fmt.Fprint(stderr, "Passphrase for the private key: ")
		pass, err := term.ReadPassword(os.Stdin.Fd())
		fmt.Fprintln(stderr)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: read passphrase: %v\n", err)
			return "", false
		}
		return string(pass), true
	}
	fmt.Fprintln(stderr, "ssherpa: the private key is encrypted; set SSHERPA_KEY_PASSPHRASE, pass --passphrase-fd N, or run from a terminal")
	return "", false
}
