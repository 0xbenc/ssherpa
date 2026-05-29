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

const ProxiesDir = "proxies"

type StoredProxy struct {
	Name           string     `json:"name"`
	SSHAlias       string     `json:"ssh_alias"`
	Bind           string     `json:"bind,omitempty"`
	Port           int        `json:"port"`
	Description    string     `json:"description,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastLaunchedAt *time.Time `json:"last_launched_at,omitempty"`
	StateVersion   int        `json:"state_version"`
}

func WriteProxy(stateDir string, spec StoredProxy) error {
	if err := ValidateProxyName(spec.Name); err != nil {
		return err
	}
	spec.StateVersion = StateVersion
	now := time.Now().UTC()
	spec.UpdatedAt = now
	existing, err := ReadProxy(stateDir, spec.Name)
	if err == nil {
		spec.CreatedAt = existing.CreatedAt
	} else if !spec.CreatedAt.IsZero() {
		spec.CreatedAt = spec.CreatedAt.UTC()
	} else {
		spec.CreatedAt = now
	}
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal proxy spec: %w", err)
	}
	data = append(data, '\n')
	_, err = fsutil.AtomicWriteFile(ProxyPath(stateDir, spec.Name), data, fsutil.WriteOptions{Mode: 0o600})
	if err != nil {
		return fmt.Errorf("write proxy spec %s: %w", spec.Name, err)
	}
	return nil
}

func ReadProxy(stateDir string, name string) (StoredProxy, error) {
	if err := ValidateProxyName(name); err != nil {
		return StoredProxy{}, err
	}
	data, err := os.ReadFile(ProxyPath(stateDir, name))
	if err != nil {
		return StoredProxy{}, err
	}
	var spec StoredProxy
	if err := json.Unmarshal(data, &spec); err != nil {
		return StoredProxy{}, fmt.Errorf("parse proxy spec %s: %w", name, err)
	}
	return spec, nil
}

func ListProxies(stateDir string) ([]StoredProxy, error) {
	dir := filepath.Join(filepath.Clean(stateDir), ProxiesDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []StoredProxy{}, nil
		}
		return nil, fmt.Errorf("read proxies directory: %w", err)
	}
	out := make([]StoredProxy, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		spec, err := ReadProxy(stateDir, strings.TrimSuffix(entry.Name(), ".json"))
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

func DeleteProxy(stateDir string, name string) error {
	if err := ValidateProxyName(name); err != nil {
		return err
	}
	err := os.Remove(ProxyPath(stateDir, name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove proxy spec %s: %w", name, err)
	}
	return nil
}

func ProxyPath(stateDir string, name string) string {
	return filepath.Join(filepath.Clean(stateDir), ProxiesDir, name+".json")
}

func ValidateProxyName(name string) error {
	return validateCatalogName("proxy", name)
}

func validateCatalogName(kind string, name string) error {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return fmt.Errorf("%s name is required", kind)
	}
	if name != trimmed {
		return fmt.Errorf("invalid %s name %q (no leading/trailing whitespace)", kind, name)
	}
	if strings.ContainsAny(name, " \t\r\n\x00/\\") {
		return fmt.Errorf("invalid %s name %q (no whitespace, slashes, or NUL)", kind, name)
	}
	if name != filepath.Base(name) {
		return fmt.Errorf("invalid %s name %q", kind, name)
	}
	if strings.HasPrefix(name, ".") {
		return fmt.Errorf("invalid %s name %q (cannot start with dot)", kind, name)
	}
	return nil
}
