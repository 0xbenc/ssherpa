// Package portable defines the on-disk format for moving ssherpa aliases
// and saved presets (forwards/proxies) between machines as a single JSON
// bundle. It is the serialization layer only: it depends on internal/state
// and internal/sshconfig for the data shapes it carries, but never on
// internal/cli or internal/ui, so both the interactive and non-interactive
// export/import paths can share it.
package portable

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/0xbenc/ssherpa/internal/sshconfig"
	"github.com/0xbenc/ssherpa/internal/state"
)

// BundleSchemaVersion is the current bundle format version. A bundle whose
// schema_version exceeds this is refused on import (a newer ssherpa wrote it),
// mirroring the state-version posture in internal/state.
const BundleSchemaVersion = 1

// ErrFutureBundleVersion is returned by Unmarshal when a bundle's
// schema_version is newer than this build understands.
var ErrFutureBundleVersion = errors.New("bundle schema version is newer than this ssherpa supports")

// Bundle is the root document. Each category is optional; an exporter may
// include any mix of aliases, forwards, and proxies.
type Bundle struct {
	SchemaVersion int                   `json:"schema_version"`
	ExportedAt    string                `json:"exported_at,omitempty"`
	Aliases       []AliasEntry          `json:"aliases,omitempty"`
	Forwards      []state.StoredForward `json:"forwards,omitempty"`
	Proxies       []state.StoredProxy   `json:"proxies,omitempty"`
}

// AliasEntry is the portable, managed-fields-only view of an SSH alias. It
// deliberately mirrors sshconfig.AliasSpec rather than referencing it so the
// JSON shape is stable and forward-compatible (additive omitempty fields).
type AliasEntry struct {
	Alias          string `json:"alias"`
	HostName       string `json:"hostname,omitempty"`
	User           string `json:"user,omitempty"`
	Port           string `json:"port,omitempty"`
	IdentityFile   string `json:"identity_file,omitempty"`
	IdentitiesOnly bool   `json:"identities_only,omitempty"`
	ProxyJump      string `json:"proxy_jump,omitempty"`
	ForcePassword  bool   `json:"force_password,omitempty"`
}

// AliasEntryFromSpec converts a parsed AliasSpec into a portable entry.
func AliasEntryFromSpec(spec sshconfig.AliasSpec) AliasEntry {
	return AliasEntry{
		Alias:          spec.Alias,
		HostName:       spec.HostName,
		User:           spec.User,
		Port:           spec.Port,
		IdentityFile:   spec.IdentityFile,
		IdentitiesOnly: spec.IdentitiesOnly,
		ProxyJump:      spec.ProxyJump,
		ForcePassword:  spec.ForcePassword,
	}
}

// ToSpec converts a portable entry back into an AliasSpec for the mutation
// (write) path. Validation is the caller's responsibility via
// sshconfig.ValidateAliasSpec.
func (e AliasEntry) ToSpec() sshconfig.AliasSpec {
	return sshconfig.AliasSpec{
		Alias:          e.Alias,
		HostName:       e.HostName,
		User:           e.User,
		Port:           e.Port,
		IdentityFile:   e.IdentityFile,
		IdentitiesOnly: e.IdentitiesOnly,
		ProxyJump:      e.ProxyJump,
		ForcePassword:  e.ForcePassword,
	}
}

// IsEmpty reports whether the bundle carries no items.
func (b Bundle) IsEmpty() bool {
	return len(b.Aliases) == 0 && len(b.Forwards) == 0 && len(b.Proxies) == 0
}

// Marshal serializes the bundle, stamping the current schema version. When
// exportedAt is non-empty it is recorded verbatim (callers pass an RFC3339
// timestamp; this package takes no clock so workflows stay deterministic).
func Marshal(b Bundle, exportedAt string) ([]byte, error) {
	b.SchemaVersion = BundleSchemaVersion
	if exportedAt != "" {
		b.ExportedAt = exportedAt
	}
	data, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal bundle: %w", err)
	}
	return append(data, '\n'), nil
}

// Unmarshal parses a bundle and rejects malformed or future-version input.
func Unmarshal(data []byte) (Bundle, error) {
	var b Bundle
	if err := json.Unmarshal(data, &b); err != nil {
		return Bundle{}, fmt.Errorf("parse bundle: %w", err)
	}
	if b.SchemaVersion < 1 {
		return Bundle{}, fmt.Errorf("bundle is missing a valid schema_version")
	}
	if b.SchemaVersion > BundleSchemaVersion {
		return Bundle{}, fmt.Errorf("bundle schema_version %d (this ssherpa supports up to %d): %w",
			b.SchemaVersion, BundleSchemaVersion, ErrFutureBundleVersion)
	}
	return b, nil
}
