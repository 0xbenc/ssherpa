package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/term"

	"github.com/0xbenc/ssherpa/internal/authkeys"
	"github.com/0xbenc/ssherpa/internal/fsutil"
	"github.com/0xbenc/ssherpa/internal/sshkeys"
	"github.com/0xbenc/ssherpa/internal/ui"
)

const keyUsage = `Usage:
  ssherpa key import --from PATH [--name NAME] [--register] [--add-to-agent] [--agent-ttl D] [--delete-source] [--force] [--dry-run] [--yes] [--json] [--ssh-keygen PATH]
  ssherpa key generate [--name NAME] [--type ed25519|rsa|ecdsa] [--comment C] [--register] [--add-to-agent] [--agent-ttl D] [--force] [--dry-run] [--yes] [--json]

Set up your OWN SSH keypair in ~/.ssh with safe permissions (the directory
0700, private 0600, public 0644 — ssh refuses a loose-perm private key) and
print its SHA256 fingerprint.

  import    Copy an existing keypair from PATH (a backup/USB). The source is
            kept by default (see --delete-source). An encrypted key needs its
            passphrase: set SSHERPA_KEY_PASSPHRASE, pass --passphrase-fd N, or
            run from a terminal to be prompted.
  generate  Create a fresh keypair with ssh-keygen (default ed25519). It has no
            passphrase unless SSHERPA_KEY_PASSPHRASE / --passphrase-fd is set.

--register adds the key as the default identity (Host * IdentityFile, absolute
path) in ~/.ssh/config — idempotent and backed up. For a single host, set the
identity on that alias with "ssherpa edit" instead.

--add-to-agent loads the key into your running ssh-agent (ssh-add). With no
agent running it is skipped with a notice, not an error. --agent-ttl D (a Go
duration like 8h or 30m) caps the key's lifetime in the agent and implies
--add-to-agent.

--delete-source removes the original key file(s) pointed at by --from once the
copy in ~/.ssh is in place (the private key and its .pub sibling, if present) —
useful for clearing a key off a USB stick or download. The freshly-placed key
is never deleted (importing in place is a no-op). Without the flag, an
interactive run offers the same cleanup as a prompt.

Re-importing the same key is a no-op; a different key with the same name is
refused unless --force.
`

func runKey(args []string, stdout io.Writer, stderr io.Writer) int {
	if hasHelpFlag(args) {
		fmt.Fprint(stdout, keyUsage)
		return 0
	}
	if len(args) == 0 {
		// The bare command opens the interactive setup, but only on a real
		// terminal; otherwise fall back to usage so scripts get a clear message
		// instead of a TUI error.
		if !term.IsTerminal(os.Stdin.Fd()) {
			fmt.Fprint(stdout, keyUsage)
			return 1
		}
		return runKeyInteractive(stdout, stderr)
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
	AddToAgent    bool
	AgentTTL      string
	SSHAddPath    string
	DeleteSource  bool
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
		case arg == "--add-to-agent":
			flags.AddToAgent = true
		case arg == "--agent-ttl":
			value, ok := nextArg(args, &i, stderr, "--agent-ttl")
			if !ok {
				return flags, false
			}
			flags.AgentTTL = value
		case strings.HasPrefix(arg, "--agent-ttl="):
			flags.AgentTTL = strings.TrimPrefix(arg, "--agent-ttl=")
		case arg == "--ssh-add":
			value, ok := nextArg(args, &i, stderr, "--ssh-add")
			if !ok {
				return flags, false
			}
			flags.SSHAddPath = value
		case strings.HasPrefix(arg, "--ssh-add="):
			flags.SSHAddPath = strings.TrimPrefix(arg, "--ssh-add=")
		case arg == "--delete-source":
			flags.DeleteSource = true
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
	Fingerprint   string `json:"fingerprint"`
	Type          string `json:"type"`
	Comment       string `json:"comment,omitempty"`
	Name          string `json:"name"`
	PrivatePath   string `json:"private_path"`
	PublicPath    string `json:"public_path"`
	DryRun        bool   `json:"dry_run"`
	Imported      bool   `json:"imported"`
	Skipped       bool   `json:"skipped,omitempty"`
	Registered    bool   `json:"registered,omitempty"`
	AddedToAgent  bool   `json:"added_to_agent,omitempty"`
	AgentSkipped  bool   `json:"agent_skipped,omitempty"`
	SourceDeleted bool   `json:"source_deleted,omitempty"`
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
	if strings.TrimSpace(flags.AgentTTL) != "" {
		flags.AddToAgent = true
	}
	if flags.AddToAgent && !validateAgentOptions(flags.SSHAddPath, flags.AgentTTL, stderr) {
		return 1
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
	var keyPassphrase string
	info, err := kg.DeriveBytes(ctx, data, "")
	if err == sshkeys.ErrEncrypted {
		pass, ok := readKeyPassphrase(flags, stderr)
		if !ok {
			return 1
		}
		keyPassphrase = pass
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
		if flags.AddToAgent {
			fmt.Fprintf(stderr, "  agent       %s\n", agentPreviewDetail(flags.AgentTTL))
		}
		if flags.DeleteSource {
			fmt.Fprintf(stderr, "  cleanup     delete source %s after import\n", src)
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

	if flags.AddToAgent {
		added, skipped, agErr := addKeyToAgent(res.PrivatePath, keyPassphrase, flags.AgentTTL, flags.SSHAddPath, stderr)
		if agErr != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", agErr)
			return 1
		}
		out.AddedToAgent = added
		out.AgentSkipped = skipped
	}

	if flags.JSON {
		deleted, delErr := resolveSourceDeletion(src, res, flags, stderr)
		if delErr != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", delErr)
		}
		out.SourceDeleted = deleted
		writeJSON(stdout, out)
		return 0
	}
	if res.Skipped {
		fmt.Fprintf(stderr, "Already present (perms repaired): %s\n  %s\n", info.Fingerprint, res.PrivatePath)
	} else {
		fmt.Fprintf(stderr, "Imported %s (%s)\n  %s  (0600)\n  %s  (0644)\n", info.Fingerprint, info.Type, res.PrivatePath, res.PublicPath)
	}
	// Now that the keypair lives in ~/.ssh, offer to clear the source copy. The
	// import has already succeeded, so a cleanup hiccup is a warning, not a
	// failure of the command.
	if _, delErr := resolveSourceDeletion(src, res, flags, stderr); delErr != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", delErr)
	}
	return 0
}

// importSourceCandidates returns the source files eligible for post-import
// cleanup: the private key pointed at by --from and, when it exists, its .pub
// sibling. Any path that is the very file we just wrote into ~/.ssh is excluded
// (compared by inode via os.SameFile), so importing a key in place never
// deletes the freshly-placed copy.
func importSourceCandidates(src string, res sshkeys.PlaceResult) []string {
	var out []string
	for _, c := range []struct{ path, dest string }{
		{src, res.PrivatePath},
		{src + ".pub", res.PublicPath},
	} {
		srcInfo, err := os.Stat(c.path)
		if err != nil {
			continue // missing (e.g. no .pub sibling was copied alongside)
		}
		if destInfo, derr := os.Stat(c.dest); derr == nil && os.SameFile(srcInfo, destInfo) {
			continue // this source IS the placed key — never delete it
		}
		out = append(out, c.path)
	}
	return out
}

// deleteSourcePrompt builds the confirmation message for removing the source
// key file(s) after they have been copied into ~/.ssh.
func deleteSourcePrompt(paths []string) string {
	if len(paths) == 1 {
		return fmt.Sprintf("Copied into ~/.ssh. Delete the original %s?", paths[0])
	}
	return fmt.Sprintf("Copied into ~/.ssh. Delete the originals (%s)?", strings.Join(paths, ", "))
}

// deleteImportSources removes each source file, reporting what it cleared. It
// continues past a failed removal — so a stuck .pub never leaves an
// already-deleted private key reported as "not deleted" — and returns how many
// files it removed along with any joined errors.
func deleteImportSources(paths []string, stderr io.Writer) (int, error) {
	var errs []error
	deleted := 0
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			errs = append(errs, fmt.Errorf("delete source %s: %w", p, err))
			continue
		}
		deleted++
		fmt.Fprintf(stderr, "Deleted source %s\n", p)
	}
	return deleted, errors.Join(errs...)
}

// resolveSourceDeletion decides whether to remove the source files after a
// successful `key import` and does so. The decision:
//   - nothing eligible (e.g. imported in place) → no-op
//   - --delete-source → delete, no prompt (honored even in scripts)
//   - otherwise prompt, but only in an interactive run (a real terminal that
//     was not given --yes/--json); scripts opt in via the flag instead
//
// It returns whether anything was deleted.
func resolveSourceDeletion(src string, res sshkeys.PlaceResult, flags keyImportFlags, stderr io.Writer) (bool, error) {
	candidates := importSourceCandidates(src, res)
	if len(candidates) == 0 {
		return false, nil
	}
	doDelete := flags.DeleteSource
	if !doDelete {
		interactive := !flags.Yes && !flags.JSON && term.IsTerminal(os.Stdin.Fd())
		if !interactive {
			return false, nil
		}
		yes, err := confirmDeleteChoice(stderr, "Delete source", deleteSourcePrompt(candidates))
		if err != nil {
			return false, err
		}
		doDelete = yes
	}
	if !doDelete {
		return false, nil
	}
	deleted, err := deleteImportSources(candidates, stderr)
	return deleted > 0, err
}

// promptSourceDeletion offers the same source cleanup as resolveSourceDeletion
// for the interactive import flow, which is always driven from a terminal and
// so always asks.
func promptSourceDeletion(src string, res sshkeys.PlaceResult, stderr io.Writer) (bool, error) {
	candidates := importSourceCandidates(src, res)
	if len(candidates) == 0 {
		return false, nil
	}
	yes, err := confirmDeleteChoice(stderr, "Delete source", deleteSourcePrompt(candidates))
	if err != nil {
		return false, err
	}
	if !yes {
		return false, nil
	}
	deleted, err := deleteImportSources(candidates, stderr)
	return deleted > 0, err
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

// parseAgentTTL turns the --agent-ttl flag (a Go duration, e.g. "8h") into a
// lifetime for ssh-add. Empty means no expiry; a negative duration is rejected.
func parseAgentTTL(raw string) (time.Duration, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --agent-ttl %q: %v", raw, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid --agent-ttl %q: must not be negative", raw)
	}
	return d, nil
}

// validateAgentOptions fails fast on a bad --agent-ttl or an unusable ssh-add
// binary, before any key material is touched.
func validateAgentOptions(sshAddPath, ttl string, stderr io.Writer) bool {
	if _, err := parseAgentTTL(ttl); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return false
	}
	return validateSSHAdd(sshAddPath, stderr)
}

func agentPreviewDetail(ttl string) string {
	if t := strings.TrimSpace(ttl); t != "" {
		return "load into ssh-agent (ttl " + t + ")"
	}
	return "load into ssh-agent"
}

// addKeyToAgent loads the placed key into the running ssh-agent. A missing agent
// is a soft skip (notice + skipped=true), not an error: the import/generation
// itself already succeeded.
func addKeyToAgent(privPath, passphrase, ttlRaw, sshAddPath string, stderr io.Writer) (added bool, skipped bool, err error) {
	ttl, err := parseAgentTTL(ttlRaw)
	if err != nil {
		return false, false, err
	}
	agent := sshkeys.Agent{Path: resolveSSHAddPath(sshAddPath)}
	if addErr := agent.Add(context.Background(), privPath, passphrase, ttl); addErr != nil {
		if errors.Is(addErr, sshkeys.ErrNoAgent) {
			fmt.Fprintln(stderr, "No ssh-agent running (SSH_AUTH_SOCK unset); skipped --add-to-agent.")
			return false, true, nil
		}
		return false, false, addErr
	}
	if ttl > 0 {
		fmt.Fprintf(stderr, "Added to ssh-agent (expires in %s).\n", ttl)
	} else {
		fmt.Fprintln(stderr, "Added to ssh-agent.")
	}
	return true, false, nil
}

type keyGenerateFlags struct {
	Name          string
	Type          string
	Comment       string
	Bits          int
	Force         bool
	Register      bool
	AddToAgent    bool
	AgentTTL      string
	SSHAddPath    string
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
		case arg == "--add-to-agent":
			flags.AddToAgent = true
		case arg == "--agent-ttl":
			value, ok := nextArg(args, &i, stderr, "--agent-ttl")
			if !ok {
				return flags, false
			}
			flags.AgentTTL = value
		case strings.HasPrefix(arg, "--agent-ttl="):
			flags.AgentTTL = strings.TrimPrefix(arg, "--agent-ttl=")
		case arg == "--ssh-add":
			value, ok := nextArg(args, &i, stderr, "--ssh-add")
			if !ok {
				return flags, false
			}
			flags.SSHAddPath = value
		case strings.HasPrefix(arg, "--ssh-add="):
			flags.SSHAddPath = strings.TrimPrefix(arg, "--ssh-add=")
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
	Fingerprint  string `json:"fingerprint,omitempty"`
	Type         string `json:"type"`
	Comment      string `json:"comment,omitempty"`
	Name         string `json:"name"`
	PrivatePath  string `json:"private_path"`
	PublicPath   string `json:"public_path"`
	DryRun       bool   `json:"dry_run"`
	Generated    bool   `json:"generated"`
	Registered   bool   `json:"registered,omitempty"`
	AddedToAgent bool   `json:"added_to_agent,omitempty"`
	AgentSkipped bool   `json:"agent_skipped,omitempty"`
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
	if strings.TrimSpace(flags.AgentTTL) != "" {
		flags.AddToAgent = true
	}
	if flags.AddToAgent && !validateAgentOptions(flags.SSHAddPath, flags.AgentTTL, stderr) {
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
		if flags.AddToAgent {
			fmt.Fprintf(stderr, "  agent       %s\n", agentPreviewDetail(flags.AgentTTL))
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

	if flags.AddToAgent {
		added, skipped, agErr := addKeyToAgent(res.PrivatePath, passphrase, flags.AgentTTL, flags.SSHAddPath, stderr)
		if agErr != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", agErr)
			return 1
		}
		out.AddedToAgent = added
		out.AgentSkipped = skipped
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

// runKeyInteractive drives the home-menu "set up your own SSH key" flow: a
// small management menu that branches into the import or generate workflow,
// reusing the same primitives as the verbs (DeriveBytes, Place, register,
// agent). It loops back to the menu after each action until the user backs out.
func runKeyInteractive(stdout io.Writer, stderr io.Writer) int {
	sshDir, err := defaultSSHDir()
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	for {
		item, ok, err := ui.ChooseManagement(context.Background(), keyMenuItems(), ui.ManagementChooserOptions{
			Input:       os.Stdin,
			Output:      stderr,
			NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
			Title:       "SSH key setup",
			Mode:        "set up your own SSH key",
			Steps:       []string{"action", "input", "confirm"},
			CurrentStep: 0,
			Summary:     sshDir,
			Footer:      "enter select / type filter / arrows move / esc back",
		})
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: key picker failed: %v\n", err)
			return 1
		}
		if !ok || item.Token == "back" {
			fmt.Fprintln(stdout, "[skipped] key setup cancelled")
			return 0
		}
		switch item.Token {
		case "import":
			if code := importKeyInteractive(sshDir, stdout, stderr); code != 0 {
				return code
			}
		case "generate":
			if code := generateKeyInteractive(sshDir, stdout, stderr); code != 0 {
				return code
			}
		}
	}
}

func keyMenuItems() []ui.ManagementItem {
	return []ui.ManagementItem{
		{Kind: ui.ItemImportKey, Token: "import", Title: "Import an existing key", Description: "copy a private+public key from a backup/USB into ~/.ssh", Group: "Set up", Badge: "import", Action: "Browse for a private key and place it with safe permissions"},
		{Kind: ui.ItemImportKey, Token: "generate", Title: "Generate a new key", Description: "create a fresh keypair with ssh-keygen", Group: "Set up", Badge: "gen", Action: "Create a new keypair in ~/.ssh"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to the previous menu", Group: "Navigation", Badge: "back", Action: "Return without changing ~/.ssh"},
	}
}

// importKeyInteractive walks the user through choosing a private key file,
// unlocking it if needed, naming it, and optionally registering it / loading it
// into the agent — then places it.
func importKeyInteractive(sshDir string, stdout io.Writer, stderr io.Writer) int {
	path, ok, err := pickLocalFileWith(stderr, filePickerOptions{}, ".", "SSHERPA IMPORT KEY", "import-key", "PRIVATE KEY", []string{"choose", "name", "confirm"}, 0)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] key import cancelled")
		return 0
	}

	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: read %s: %v\n", path, err)
		return 1
	}
	if _, _, perr := authkeys.ParseFirstKey(data, path, authkeys.Validator{SkipSSHKeygen: true}); perr == nil {
		fmt.Fprintf(stderr, "ssherpa: %s looks like a PUBLIC key; choose the PRIVATE key\n", path)
		return 1
	}

	ctx := context.Background()
	kg := sshkeys.Keygen{}
	var passphrase string
	info, err := kg.DeriveBytes(ctx, data, "")
	if err == sshkeys.ErrEncrypted {
		pass, ok, perr := promptSecret(stderr, "Encrypted private key", "passphrase", nil)
		if perr != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", perr)
			return 1
		}
		if !ok {
			fmt.Fprintln(stdout, "[skipped] key import cancelled")
			return 0
		}
		passphrase = pass
		info, err = kg.DeriveBytes(ctx, data, pass)
	}
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	name, ok, err := promptText(stderr, "Destination key name", "name", defaultKeyName(path), validateKeyNameInput)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] key import cancelled")
		return 0
	}
	name = strings.TrimSpace(name)

	register, addAgent, ok, code := promptKeyOptions(stdout, stderr)
	if !ok {
		return code
	}

	privDest := filepath.Join(sshDir, name)
	if err := showKeyReview(ctx, stderr, "Import", info, privDest, register, addAgent); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	force, ok, code := confirmOverwriteIfPresent(stdout, stderr, privDest)
	if !ok {
		return code
	}

	yes, err := confirmActionChoice(stderr, "Import SSH key", fmt.Sprintf("%s into %s", info.Fingerprint, privDest))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !yes {
		fmt.Fprintln(stdout, "[skipped] key import cancelled")
		return 0
	}

	res, err := sshkeys.Place(sshDir, name, data, info.PublicLine, force, fsutilWriteFunc)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if code := applyKeyExtras(res.PrivatePath, passphrase, register, addAgent, stderr); code != 0 {
		return code
	}
	if res.Skipped {
		fmt.Fprintf(stderr, "Already present (perms repaired): %s\n  %s\n", info.Fingerprint, res.PrivatePath)
	} else {
		fmt.Fprintf(stderr, "Imported %s (%s)\n  %s  (0600)\n  %s  (0644)\n", info.Fingerprint, info.Type, res.PrivatePath, res.PublicPath)
	}
	// The keypair now lives in ~/.ssh; offer to clear the source copy.
	if _, err := promptSourceDeletion(path, res, stderr); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
	}
	return 0
}

// generateKeyInteractive walks the user through creating a fresh keypair:
// choosing the type, name, comment, and an optional passphrase, then the same
// register/agent options as import.
func generateKeyInteractive(sshDir string, stdout io.Writer, stderr io.Writer) int {
	keyType, ok, err := chooseKeyType(stderr)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] key generation cancelled")
		return 0
	}

	name, ok, err := promptText(stderr, "New key name", "name", "id_"+keyType, validateKeyNameInput)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] key generation cancelled")
		return 0
	}
	name = strings.TrimSpace(name)

	comment, ok, err := promptText(stderr, "Comment (optional)", "comment", defaultKeyComment(), nil)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !ok {
		fmt.Fprintln(stdout, "[skipped] key generation cancelled")
		return 0
	}

	passphrase, ok := promptNewPassphrase(stderr)
	if !ok {
		fmt.Fprintln(stdout, "[skipped] key generation cancelled")
		return 0
	}

	register, addAgent, ok, code := promptKeyOptions(stdout, stderr)
	if !ok {
		return code
	}

	ctx := context.Background()
	privDest := filepath.Join(sshDir, name)
	if err := showKeyReview(ctx, stderr, "Generate", sshkeys.KeyInfo{Type: keyType, Comment: comment}, privDest, register, addAgent); err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}

	force, ok, code := confirmOverwriteIfPresent(stdout, stderr, privDest)
	if !ok {
		return code
	}

	yes, err := confirmActionChoice(stderr, "Generate SSH key", fmt.Sprintf("%s into %s", keyType, privDest))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if !yes {
		fmt.Fprintln(stdout, "[skipped] key generation cancelled")
		return 0
	}

	info, res, err := sshkeys.Keygen{}.Generate(ctx, sshDir, name, sshkeys.GenerateOptions{
		Type:       keyType,
		Comment:    comment,
		Passphrase: passphrase,
	}, force)
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return 1
	}
	if code := applyKeyExtras(res.PrivatePath, passphrase, register, addAgent, stderr); code != 0 {
		return code
	}
	fmt.Fprintf(stderr, "Generated %s (%s)\n  %s  (0600)\n  %s  (0644)\n", info.Fingerprint, info.Type, res.PrivatePath, res.PublicPath)
	return 0
}

// promptKeyOptions asks whether to register the key as the default identity and
// (when an agent is reachable) whether to load it into the agent. The bool
// reports whether to proceed; on cancellation it returns false with an exit
// code for the caller to return.
func promptKeyOptions(stdout io.Writer, stderr io.Writer) (register bool, addAgent bool, ok bool, code int) {
	register, err := confirmActionChoice(stderr, "Default identity", "Add as the default identity (Host * IdentityFile) in ~/.ssh/config?")
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return false, false, false, 1
	}
	if sshAgentAvailable() {
		addAgent, err = confirmActionChoice(stderr, "ssh-agent", "Load the key into your running ssh-agent now?")
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return false, false, false, 1
		}
	}
	return register, addAgent, true, 0
}

// applyKeyExtras runs the register and agent steps shared by both interactive
// flows, surfacing any hard error as a non-zero code.
func applyKeyExtras(privPath, passphrase string, register, addAgent bool, stderr io.Writer) int {
	if register {
		if _, err := registerKeyIdentity(privPath, stderr); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
	}
	if addAgent {
		if _, _, err := addKeyToAgent(privPath, passphrase, "", "", stderr); err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return 1
		}
	}
	return 0
}

// confirmOverwriteIfPresent asks before reusing an existing key name. It returns
// force=true (proceed, overwriting differing bytes) when the user confirms; ok
// is false when they decline or the prompt errors.
func confirmOverwriteIfPresent(stdout io.Writer, stderr io.Writer, privDest string) (force bool, ok bool, code int) {
	if _, statErr := os.Stat(privDest); statErr != nil {
		return false, true, 0
	}
	yes, err := confirmActionChoice(stderr, "Overwrite", fmt.Sprintf("%s already exists. Overwrite it?", privDest))
	if err != nil {
		fmt.Fprintf(stderr, "ssherpa: %v\n", err)
		return false, false, 1
	}
	if !yes {
		fmt.Fprintln(stdout, "[skipped] key setup cancelled")
		return false, false, 0
	}
	return true, true, 0
}

func showKeyReview(ctx context.Context, stderr io.Writer, verb string, info sshkeys.KeyInfo, privDest string, register, addAgent bool) error {
	return ui.ShowTextView(ctx, ui.TextViewOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Review SSH key " + strings.ToLower(verb),
		Steps:       []string{"choose", "name", "confirm"},
		CurrentStep: 2,
		Summary:     privDest,
		Lines:       keyReviewLines(verb, info, privDest, register, addAgent),
		Footer:      "enter continue / esc back",
	})
}

func keyReviewLines(verb string, info sshkeys.KeyInfo, privDest string, register, addAgent bool) []string {
	lines := []string{verb + " SSH key:", ""}
	if info.Type != "" {
		lines = append(lines, "  type         "+info.Type)
	}
	if info.Fingerprint != "" {
		lines = append(lines, "  fingerprint  "+info.Fingerprint)
	}
	if info.Comment != "" {
		lines = append(lines, "  comment      "+info.Comment)
	}
	lines = append(lines,
		"  private      "+privDest+"  (0600)",
		"  public       "+privDest+".pub  (0644)",
		"  ~/.ssh dir   0700",
	)
	if register {
		lines = append(lines, "  register     default identity in ~/.ssh/config")
	}
	if addAgent {
		lines = append(lines, "  agent        load into ssh-agent")
	}
	return lines
}

func chooseKeyType(stderr io.Writer) (string, bool, error) {
	items := []ui.ManagementItem{
		{Kind: ui.ItemImportKey, Token: "ed25519", Title: "ed25519", Description: "modern, fast — recommended", Group: "Key type", Badge: "ed"},
		{Kind: ui.ItemImportKey, Token: "rsa", Title: "rsa 4096", Description: "widest compatibility", Group: "Key type", Badge: "rsa"},
		{Kind: ui.ItemImportKey, Token: "ecdsa", Title: "ecdsa", Description: "NIST P-256 curve", Group: "Key type", Badge: "ec"},
		{Kind: ui.ItemKind("back"), Token: "back", Title: "Back", Description: "return to the key menu", Group: "Navigation", Badge: "back"},
	}
	item, ok, err := ui.ChooseManagement(context.Background(), items, ui.ManagementChooserOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       "Generate SSH key",
		Mode:        "choose a key type",
		Steps:       []string{"type", "name", "confirm"},
		CurrentStep: 0,
		Footer:      "enter select / arrows move / esc back",
	})
	if err != nil {
		return "", false, err
	}
	if !ok || item.Token == "back" {
		return "", false, nil
	}
	return item.Token, true, nil
}

// promptNewPassphrase reads a NEW passphrase for a generated key, confirming it
// by re-entry. An empty first entry means "no passphrase". ok is false when the
// user cancels or keeps mismatching.
func promptNewPassphrase(stderr io.Writer) (string, bool) {
	for attempt := 0; attempt < 3; attempt++ {
		first, ok, err := promptSecret(stderr, "New passphrase (blank for none)", "passphrase", nil)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return "", false
		}
		if !ok {
			return "", false
		}
		if first == "" {
			return "", true
		}
		second, ok, err := promptSecret(stderr, "Confirm passphrase", "again", nil)
		if err != nil {
			fmt.Fprintf(stderr, "ssherpa: %v\n", err)
			return "", false
		}
		if !ok {
			return "", false
		}
		if first == second {
			return first, true
		}
		fmt.Fprintln(stderr, "Passphrases did not match; try again.")
	}
	fmt.Fprintln(stderr, "Too many mismatches; cancelled.")
	return "", false
}

func promptSecret(stderr io.Writer, title, label string, validate func(string) error) (string, bool, error) {
	return ui.PromptSecret(context.Background(), ui.TextPromptOptions{
		Input:       os.Stdin,
		Output:      stderr,
		NoAltScreen: envBool("SSHERPA_NO_ALT_SCREEN"),
		Title:       title,
		Label:       label,
		Validate:    validate,
	})
}

func validateKeyNameInput(value string) error {
	v := strings.TrimSpace(value)
	if v == "" {
		return fmt.Errorf("a key name is required")
	}
	if strings.ContainsAny(v, "/\\") {
		return fmt.Errorf("the name must not contain a path separator")
	}
	return nil
}

func sshAgentAvailable() bool {
	return strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")) != ""
}

// defaultKeyComment mirrors ssh-keygen's default user@host comment.
func defaultKeyComment() string {
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	if user == "" {
		user = os.Getenv("LOGNAME")
	}
	if user != "" && host != "" {
		return user + "@" + host
	}
	return ""
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
