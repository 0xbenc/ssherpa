package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0xbenc/ssherpa/internal/fsutil"
)

// ForwardsDir is the subdirectory under the state dir where saved
// forward specs live. One JSON file per saved forward, keyed by
// Name. Lives alongside `sessions/` (the runtime record dir) so the
// whole ssherpa state tree is `<stateDir>/{sessions,forwards}/…`.
const ForwardsDir = "forwards"

// StoredForward is a persisted "I run this tunnel often" entry — the
// ssherpa-owned half of the save-as-alias split. The corresponding
// `Host` block in ~/.ssh/config carries the route (HostName, User,
// ProxyJump); StoredForward carries the local/remote forward spec.
// Two layers because baking LocalForward into ~/.ssh/config collides
// with `ssherpa forward` adding its own -L flag — second bind fails
// with "Address already in use" (we hit this for real in Phase 1
// testing; see docs/forward-phase-2.md).
type StoredForward struct {
	Name           string     `json:"name"`
	SSHAlias       string     `json:"ssh_alias"`
	LocalBind      string     `json:"local_bind,omitempty"`
	LocalPort      int        `json:"local_port"`
	RemoteHost     string     `json:"remote_host"`
	RemotePort     int        `json:"remote_port"`
	Through        string     `json:"through,omitempty"`
	Description    string     `json:"description,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastLaunchedAt *time.Time `json:"last_launched_at,omitempty"`
	StateVersion   int        `json:"state_version"`
}

// WriteForward serializes the spec to <stateDir>/forwards/<name>.json
// atomically (temp file + rename via fsutil.AtomicWriteFile, mode
// 0600). Validates the name first so a typo can't write to a path
// outside the catalog. UpdatedAt is touched on every write; CreatedAt
// is only set when the file is new (preserved otherwise).
func WriteForward(stateDir string, spec StoredForward) error {
	if err := ValidateForwardName(spec.Name); err != nil {
		return err
	}
	spec.StateVersion = StateVersion
	now := time.Now().UTC()
	spec.UpdatedAt = now
	// Preserve CreatedAt if a previous version exists; otherwise stamp
	// now. This lets `forward save` update an existing entry without
	// losing its provenance.
	existing, err := ReadForward(stateDir, spec.Name)
	if err == nil {
		spec.CreatedAt = existing.CreatedAt
	} else {
		spec.CreatedAt = now
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal forward spec: %w", err)
	}
	data = append(data, '\n')
	_, err = fsutil.AtomicWriteFile(ForwardPath(stateDir, spec.Name), data, fsutil.WriteOptions{Mode: 0o600})
	if err != nil {
		return fmt.Errorf("write forward spec %s: %w", spec.Name, err)
	}
	return nil
}

// ReadForward returns the saved spec by name, or an error wrapping
// os.ErrNotExist if no such file exists.
func ReadForward(stateDir string, name string) (StoredForward, error) {
	if err := ValidateForwardName(name); err != nil {
		return StoredForward{}, err
	}
	path := ForwardPath(stateDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return StoredForward{}, err
	}
	var spec StoredForward
	if err := json.Unmarshal(data, &spec); err != nil {
		return StoredForward{}, fmt.Errorf("parse forward spec %s: %w", name, err)
	}
	return spec, nil
}

// ListForwards enumerates every saved forward, sorted by Name. A
// missing forwards/ directory is not an error — it just means no
// forwards have been saved yet.
func ListForwards(stateDir string) ([]StoredForward, error) {
	dir := filepath.Join(filepath.Clean(stateDir), ForwardsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []StoredForward{}, nil
		}
		return nil, fmt.Errorf("read forwards directory: %w", err)
	}
	out := make([]StoredForward, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		spec, err := ReadForward(stateDir, name)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// DeleteForward removes the saved spec by name. Returns nil if the
// file didn't exist (idempotent).
func DeleteForward(stateDir string, name string) error {
	if err := ValidateForwardName(name); err != nil {
		return err
	}
	err := os.Remove(ForwardPath(stateDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove forward spec %s: %w", name, err)
	}
	return nil
}

// ForwardPath returns the absolute path where a forward spec lives.
func ForwardPath(stateDir string, name string) string {
	return filepath.Join(filepath.Clean(stateDir), ForwardsDir, name+".json")
}

// ValidateForwardName accepts kebab/snake/dot-separated identifiers
// — anything that's a clean filesystem base name and an unambiguous
// CLI argument. Mirrors validateID's posture without sharing code so
// the two namespaces (session IDs and saved-forward names) can
// diverge later if either gets stricter.
func ValidateForwardName(name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return errors.New("forward name is required")
	}
	if name != trimmed {
		return fmt.Errorf("invalid forward name %q (no leading/trailing whitespace)", name)
	}
	if strings.ContainsAny(name, " \t\r\n\x00/\\") {
		return fmt.Errorf("invalid forward name %q (no whitespace, slashes, or NUL)", name)
	}
	if name != filepath.Base(name) {
		return fmt.Errorf("invalid forward name %q", name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid forward name %q (cannot start with dot)", name)
	}
	return nil
}
