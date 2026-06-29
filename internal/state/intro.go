package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0xbenc/ssherpa/internal/fsutil"
)

// IntroSchemaVersion is the schema version stamped into intro.json. It
// lets a future reader detect a file written by a newer ssherpa.
const IntroSchemaVersion = 1

// IntroState is the small singleton persisted at <dir>/intro.json. It
// records the last intro (startup animation) version the user has seen
// so the animation plays only once per release. It lives in the state
// directory (not config) because it is derived runtime bookkeeping, not
// user-authored configuration.
type IntroState struct {
	SchemaVersion        int    `json:"schema_version"`
	LastSeenIntroVersion string `json:"last_seen_intro_version"`
}

// IntroPath returns the on-disk location of the intro singleton.
func IntroPath(dir string) string {
	return filepath.Join(filepath.Clean(dir), "intro.json")
}

// ReadIntro loads the intro singleton. A missing file is not an error:
// it returns the zero value (no version seen yet) so a first run plays
// the intro. A present-but-corrupt file returns an error.
func ReadIntro(dir string) (IntroState, error) {
	data, err := os.ReadFile(IntroPath(dir))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return IntroState{}, nil
		}
		return IntroState{}, fmt.Errorf("read intro state: %w", err)
	}
	var intro IntroState
	if err := json.Unmarshal(data, &intro); err != nil {
		return IntroState{}, fmt.Errorf("parse intro state: %w", err)
	}
	return intro, nil
}

// WriteIntro persists the intro singleton atomically with 0o600
// permissions, stamping the current schema version when unset.
func WriteIntro(dir string, intro IntroState) error {
	if intro.SchemaVersion <= 0 {
		intro.SchemaVersion = IntroSchemaVersion
	}
	data, err := json.MarshalIndent(intro, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal intro state: %w", err)
	}
	data = append(data, '\n')
	_, err = fsutil.AtomicWriteFile(IntroPath(dir), data, fsutil.WriteOptions{Mode: 0o600})
	if err != nil {
		return fmt.Errorf("write intro state: %w", err)
	}
	return nil
}
