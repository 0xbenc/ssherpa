// Package sshkeys provides the security-sensitive primitives for importing and
// trusting your OWN SSH keypair locally: validating a private key (and deriving
// its public half) with ssh-keygen, repairing ~/.ssh permissions, and placing a
// keypair without clobbering an existing one. Unlike GPG there is no "ownertrust"
// object in SSH — "trusting your key" means possessing the private key with the
// correct permissions (and, optionally, registering it as an IdentityFile or
// loading it into the agent, which higher layers handle).
//
// OpenSSH facts this package relies on (verified against OpenSSH 8.9):
//   - ssh (and ssh-keygen -y / ssh-add) refuse a group/world-readable private
//     key — the mode gate fires before the key is even parsed, so we chmod 0600
//     before validating.
//   - `ssh-keygen -y -f <priv> -P ”` derives the public half of an UNENCRYPTED
//     key (exit 0); an encrypted key needs the passphrase, which ssh-keygen will
//     never read from stdin. We feed it via an env-backed SSH_ASKPASS helper so
//     the passphrase never appears on argv (world-visible via /proc/<pid>/cmdline).
//   - The .pub file is convenience only; ssh derives the public half from the
//     private key at auth time.
package sshkeys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/authkeys"
)

// ErrEncrypted is returned by Derive/DeriveBytes when the private key needs a
// passphrase and none (or an empty one) was supplied, so a caller can prompt
// and retry.
var ErrEncrypted = errors.New("private key is encrypted; a passphrase is required")

// ErrNoAgent is returned by Agent.Add when no ssh-agent is reachable
// (SSH_AUTH_SOCK is unset), so a caller can skip with a notice rather than fail.
var ErrNoAgent = errors.New("no ssh-agent is running (SSH_AUTH_SOCK is not set)")

const (
	dirMode  = 0o700
	privMode = 0o600
	pubMode  = 0o644
)

// KeyInfo describes a validated keypair (from its private key).
type KeyInfo struct {
	PublicLine  string `json:"public_line"`
	Type        string `json:"type"`
	Comment     string `json:"comment,omitempty"`
	Fingerprint string `json:"fingerprint"`
}

// Keygen runs ssh-keygen. An empty Path resolves "ssh-keygen" via PATH.
type Keygen struct {
	Path string
}

func (kg Keygen) bin() string {
	if strings.TrimSpace(kg.Path) != "" {
		return kg.Path
	}
	return "ssh-keygen"
}

// DeriveBytes validates private-key bytes and returns the public half +
// fingerprint, without touching the caller's source file: it writes the bytes
// to a private (0600) temp file and derives from there. With an empty
// passphrase it probes an unencrypted key and returns ErrEncrypted if the key
// needs one.
func (kg Keygen) DeriveBytes(ctx context.Context, priv []byte, passphrase string) (KeyInfo, error) {
	dir, err := os.MkdirTemp("", "ssherpa-key-")
	if err != nil {
		return KeyInfo{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(dir)
	tmp := filepath.Join(dir, "key")
	if err := os.WriteFile(tmp, priv, privMode); err != nil {
		return KeyInfo{}, fmt.Errorf("write temp key: %w", err)
	}
	return kg.Derive(ctx, tmp, passphrase)
}

// Derive validates the private key at path and returns its public half +
// fingerprint. It first repairs the file mode to 0600 (ssh-keygen refuses a
// loose-perm key before parsing it). With an empty passphrase it probes an
// unencrypted key and returns ErrEncrypted if the key is encrypted.
func (kg Keygen) Derive(ctx context.Context, path, passphrase string) (KeyInfo, error) {
	// chmod best-effort: if we don't own the file, ssh-keygen will surface the
	// permission error itself.
	_ = os.Chmod(path, privMode)

	args := []string{"-y", "-f", path}
	var env []string
	if passphrase == "" {
		args = append(args, "-P", "")
		env = os.Environ()
	} else {
		helper, cleanup, err := askpassHelper(passphrase)
		if err != nil {
			return KeyInfo{}, err
		}
		defer cleanup()
		env = append(os.Environ(),
			"SSH_ASKPASS="+helper,
			"SSH_ASKPASS_REQUIRE=force",
			askpassEnvKey+"="+passphrase,
		)
	}

	cmd := exec.CommandContext(ctx, kg.bin(), args...)
	cmd.Env = env
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if passphrase == "" && mentionsPassphrase(msg) {
			return KeyInfo{}, ErrEncrypted
		}
		return KeyInfo{}, fmt.Errorf("ssh-keygen could not read the private key: %s", firstLine(msg))
	}

	line := strings.TrimSpace(stdout.String())
	key, err := authkeys.ParsePublicKeyLine(line)
	if err != nil {
		return KeyInfo{}, fmt.Errorf("derived public key did not parse: %w", err)
	}
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		return KeyInfo{}, err
	}
	return KeyInfo{PublicLine: line, Type: key.Type, Comment: key.Comment, Fingerprint: fp}, nil
}

const askpassEnvKey = "SSHERPA_ASKPASS_VALUE"

// askpassHelper writes a tiny SSH_ASKPASS program that echoes the passphrase
// read from its environment (never embedded in the file body, never on argv).
// It lives 0700 in a private temp dir; the returned cleanup deletes it.
//
// The helper is SINGLE-SHOT: it emits the passphrase on its first invocation
// and then refuses (exit 1) on every subsequent one. This matters for ssh-add,
// which loops re-prompting on a wrong passphrase and only stops when the prompt
// is cancelled — a robot that returned the same wrong value forever would spin
// it at 100% CPU. ssh-keygen -y prompts just once, so the guard is harmless
// there.
func askpassHelper(_ string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "ssherpa-askpass-")
	if err != nil {
		return "", func() {}, fmt.Errorf("create askpass dir: %w", err)
	}
	path := filepath.Join(dir, "askpass.sh")
	used := filepath.Join(dir, ".used")
	script := "#!/bin/sh\n" +
		"if [ -e '" + used + "' ]; then exit 1; fi\n" +
		": > '" + used + "'\n" +
		"printf '%s\\n' \"$" + askpassEnvKey + "\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("write askpass helper: %w", err)
	}
	return path, func() { os.RemoveAll(dir) }, nil
}

func mentionsPassphrase(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "passphrase")
}

// Agent runs ssh-add against the user's running ssh-agent. An empty Path
// resolves "ssh-add" via PATH.
type Agent struct {
	Path string
}

func (a Agent) bin() string {
	if strings.TrimSpace(a.Path) != "" {
		return a.Path
	}
	return "ssh-add"
}

// Add loads the private key at path into the running ssh-agent. It returns
// ErrNoAgent when SSH_AUTH_SOCK is unset so the caller can skip with a notice.
// For an encrypted key it feeds the passphrase via the same env-backed
// SSH_ASKPASS helper used for Derive (so the passphrase never appears on argv),
// and forces askpass (SSH_ASKPASS_REQUIRE=force) so ssh-add never blocks on a
// terminal prompt. When ttl > 0 the key is given that lifetime in the agent
// (ssh-add -t seconds, rounded up to whole seconds).
func (a Agent) Add(ctx context.Context, path, passphrase string, ttl time.Duration) error {
	if strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")) == "" {
		return ErrNoAgent
	}

	var args []string
	if ttl > 0 {
		secs := int((ttl + time.Second - 1) / time.Second)
		if secs < 1 {
			secs = 1
		}
		args = append(args, "-t", strconv.Itoa(secs))
	}
	args = append(args, path)

	// Always route any passphrase prompt through the env-backed askpass helper
	// (empty value for an unencrypted key, which ssh-add never consults). This
	// keeps the call strictly non-interactive: an encrypted key with no/ wrong
	// passphrase fails fast instead of hanging on a tty prompt.
	helper, cleanup, err := askpassHelper(passphrase)
	if err != nil {
		return err
	}
	defer cleanup()
	env := append(os.Environ(),
		"SSH_ASKPASS="+helper,
		"SSH_ASKPASS_REQUIRE=force",
		askpassEnvKey+"="+passphrase,
	)

	cmd := exec.CommandContext(ctx, a.bin(), args...)
	cmd.Env = env
	cmd.Stdin = nil
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ssh-add failed: %s", firstLine(strings.TrimSpace(stderr.String())))
	}
	return nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "unrecognized key"
	}
	return s
}

// EnsureDir creates dir (e.g. ~/.ssh) at 0700, repairing the mode if it exists.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		return fmt.Errorf("chmod %s: %w", dir, err)
	}
	return nil
}

// RepairKeyPerms forces 0600 on the private key and 0644 on the public key (if
// present). It is a separate pass because an atomic writer that skips
// byte-identical content will not re-chmod an already-present file.
func RepairKeyPerms(privPath string) error {
	if err := os.Chmod(privPath, privMode); err != nil {
		return fmt.Errorf("chmod %s 0600: %w", privPath, err)
	}
	pub := privPath + ".pub"
	if _, err := os.Stat(pub); err == nil {
		if err := os.Chmod(pub, pubMode); err != nil {
			return fmt.Errorf("chmod %s 0644: %w", pub, err)
		}
	}
	return nil
}

// PlaceResult reports where a keypair landed.
type PlaceResult struct {
	PrivatePath string
	PublicPath  string
	Skipped     bool // an identical key was already present
}

// Place writes the keypair into dir as name / name.pub with correct modes
// (0600 / 0644). It refuses to overwrite an existing private key whose bytes
// differ unless force is set (in which case a timestamped backup is kept by the
// caller's writer); it no-ops (Skipped) when the same key is already present,
// still repairing perms and a missing .pub.
func Place(dir, name string, priv []byte, pubLine string, force bool, write WriteFunc) (PlaceResult, error) {
	if err := validKeyName(name); err != nil {
		return PlaceResult{}, err
	}
	if err := EnsureDir(dir); err != nil {
		return PlaceResult{}, err
	}
	privDest := filepath.Join(dir, name)
	pubDest := privDest + ".pub"
	pubData := []byte(strings.TrimRight(pubLine, "\n") + "\n")

	if existing, err := os.ReadFile(privDest); err == nil {
		if bytes.Equal(existing, priv) {
			// Same key already here — ensure the .pub and perms are right.
			if _, statErr := os.Stat(pubDest); statErr != nil {
				if _, err := write(pubDest, pubData, pubMode, false); err != nil {
					return PlaceResult{}, err
				}
			}
			if err := RepairKeyPerms(privDest); err != nil {
				return PlaceResult{}, err
			}
			return PlaceResult{PrivatePath: privDest, PublicPath: pubDest, Skipped: true}, nil
		}
		if !force {
			return PlaceResult{}, fmt.Errorf("%s already exists with different contents; choose another name or overwrite explicitly", privDest)
		}
	}

	if _, err := write(privDest, priv, privMode, force); err != nil {
		return PlaceResult{}, err
	}
	if _, err := write(pubDest, pubData, pubMode, force); err != nil {
		return PlaceResult{}, err
	}
	if err := RepairKeyPerms(privDest); err != nil {
		return PlaceResult{}, err
	}
	return PlaceResult{PrivatePath: privDest, PublicPath: pubDest}, nil
}

// GenerateOptions tunes a fresh keypair.
type GenerateOptions struct {
	Type       string // ed25519 (default), rsa, ecdsa
	Comment    string
	Passphrase string // empty = no passphrase
	Bits       int    // rsa only; defaults to 4096
}

// SupportedGenerateType reports whether t is a key type Generate accepts.
func SupportedGenerateType(t string) bool {
	switch t {
	case "", "ed25519", "rsa", "ecdsa":
		return true
	}
	return false
}

// Generate creates a fresh keypair at dir/name (+ .pub) with ssh-keygen and
// returns its info. It refuses to overwrite an existing key unless force is set.
// A non-empty passphrase is passed via ssh-keygen's -N, which briefly appears in
// the ssh-keygen process arguments — acceptable since it comes from a file
// descriptor or env, not the shell.
func (kg Keygen) Generate(ctx context.Context, dir, name string, opts GenerateOptions, force bool) (KeyInfo, PlaceResult, error) {
	if err := validKeyName(name); err != nil {
		return KeyInfo{}, PlaceResult{}, err
	}
	typ := opts.Type
	if typ == "" {
		typ = "ed25519"
	}
	if !SupportedGenerateType(typ) {
		return KeyInfo{}, PlaceResult{}, fmt.Errorf("unsupported key type %q (use ed25519, rsa, or ecdsa)", typ)
	}
	if err := EnsureDir(dir); err != nil {
		return KeyInfo{}, PlaceResult{}, err
	}
	priv := filepath.Join(dir, name)
	pub := priv + ".pub"
	if _, err := os.Stat(priv); err == nil {
		if !force {
			return KeyInfo{}, PlaceResult{}, fmt.Errorf("%s already exists; choose another name or pass --force", priv)
		}
		// ssh-keygen won't overwrite non-interactively; clear the way.
		_ = os.Remove(priv)
		_ = os.Remove(pub)
	}

	args := []string{"-t", typ, "-f", priv, "-N", opts.Passphrase, "-C", opts.Comment}
	if typ == "rsa" {
		bits := opts.Bits
		if bits == 0 {
			bits = 4096
		}
		args = append(args, "-b", strconv.Itoa(bits))
	}
	cmd := exec.CommandContext(ctx, kg.bin(), args...)
	cmd.Stdin = nil
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return KeyInfo{}, PlaceResult{}, fmt.Errorf("ssh-keygen generate failed: %s", firstLine(strings.TrimSpace(stderr.String())))
	}
	if err := RepairKeyPerms(priv); err != nil {
		return KeyInfo{}, PlaceResult{}, err
	}

	pubBytes, err := os.ReadFile(pub)
	if err != nil {
		return KeyInfo{}, PlaceResult{}, fmt.Errorf("read generated public key: %w", err)
	}
	key, err := authkeys.ParsePublicKeyLine(strings.TrimSpace(string(pubBytes)))
	if err != nil {
		return KeyInfo{}, PlaceResult{}, fmt.Errorf("generated public key did not parse: %w", err)
	}
	fp, err := key.SHA256Fingerprint()
	if err != nil {
		return KeyInfo{}, PlaceResult{}, err
	}
	return KeyInfo{PublicLine: strings.TrimSpace(string(pubBytes)), Type: key.Type, Comment: key.Comment, Fingerprint: fp},
		PlaceResult{PrivatePath: priv, PublicPath: pub}, nil
}

func validKeyName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("key name is required")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("key name %q must not contain a path separator", name)
	}
	return nil
}

// WriteFunc abstracts the atomic, permission-aware, backup-keeping writer so the
// CLI can plug in fsutil.AtomicWriteFile while this package stays dependency-light
// and easy to test. backup is requested when overwriting.
type WriteFunc func(path string, data []byte, mode os.FileMode, backup bool) (string, error)
