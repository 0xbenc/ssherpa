package fsutil

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	DefaultFileMode = 0o600
	DefaultDirMode  = 0o700
)

type WriteOptions struct {
	DryRun       bool
	Backup       bool
	BackupPrefix string
	Mode         os.FileMode
	Now          func() time.Time
}

type WriteResult struct {
	Path       string
	Changed    bool
	DryRun     bool
	BackupPath string
	Diff       string
}

func AtomicWriteFile(path string, data []byte, opts WriteOptions) (WriteResult, error) {
	result := WriteResult{
		Path:   filepath.Clean(path),
		DryRun: opts.DryRun,
	}

	old, stat, exists, err := readExisting(result.Path)
	if err != nil {
		return result, err
	}

	if bytes.Equal(old, data) {
		return result, nil
	}
	result.Changed = true
	result.Diff = UnifiedDiff(old, data, result.Path, result.Path)

	if opts.DryRun {
		return result, nil
	}

	dir := filepath.Dir(result.Path)
	if err := os.MkdirAll(dir, DefaultDirMode); err != nil {
		return result, fmt.Errorf("create parent directory %s: %w", dir, err)
	}

	mode := os.FileMode(DefaultFileMode)
	if exists {
		mode = stat.Mode().Perm()
	}
	if opts.Mode != 0 {
		mode = opts.Mode.Perm()
	}
	if exists && opts.Backup {
		backup, err := createBackup(result.Path, old, mode, opts)
		if err != nil {
			return result, err
		}
		result.BackupPath = backup
	}

	if err := writeTempRename(result.Path, data, mode); err != nil {
		return result, err
	}
	return result, nil
}

func readExisting(path string) ([]byte, os.FileInfo, bool, error) {
	stat, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, false, nil
		}
		return nil, nil, false, fmt.Errorf("stat %s: %w", path, err)
	}
	if stat.IsDir() {
		return nil, nil, false, fmt.Errorf("%s is a directory", path)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	return data, stat, true, nil
}

func createBackup(path string, data []byte, mode os.FileMode, opts WriteOptions) (string, error) {
	name, err := backupPath(path, opts)
	if err != nil {
		return "", err
	}

	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", fmt.Errorf("create backup %s: %w", name, err)
	}
	defer file.Close()

	if _, err := file.Write(data); err != nil {
		return "", fmt.Errorf("write backup %s: %w", name, err)
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("sync backup %s: %w", name, err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close backup %s: %w", name, err)
	}
	return name, nil
}

func backupPath(path string, opts WriteOptions) (string, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	prefix := opts.BackupPrefix
	if prefix == "" {
		prefix = "ssherpa-backup"
	}
	stamp := now().UTC().Format("20060102T150405Z")
	base := fmt.Sprintf("%s.%s.%s", path, prefix, stamp)

	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s.%d", base, i)
		}
		_, err := os.Stat(candidate)
		if errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		}
		if err != nil {
			return "", fmt.Errorf("stat backup candidate %s: %w", candidate, err)
		}
	}
	return "", fmt.Errorf("could not choose unused backup name for %s", path)
}

func writeTempRename(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".ssherpa-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpName)
		}
	}()

	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temp file %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp file %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp file %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file %s: %w", tmpName, err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file into %s: %w", path, err)
	}
	removeTmp = false

	if err := syncDir(dir); err != nil {
		return fmt.Errorf("sync parent directory %s: %w", dir, err)
	}
	return nil
}

func syncDir(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()

	if err := file.Sync(); err != nil && !isUnsupportedSync(err) {
		return err
	}
	return nil
}

func isUnsupportedSync(err error) bool {
	return errors.Is(err, syscall.EINVAL) ||
		errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EPERM) ||
		strings.Contains(strings.ToLower(err.Error()), "invalid argument")
}

func UnifiedDiff(oldData []byte, newData []byte, oldName string, newName string) string {
	oldLines := splitDiffLines(oldData)
	newLines := splitDiffLines(newData)

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}

	oldSuffix := len(oldLines)
	newSuffix := len(newLines)
	for oldSuffix > prefix && newSuffix > prefix && oldLines[oldSuffix-1] == newLines[newSuffix-1] {
		oldSuffix--
		newSuffix--
	}

	contextStart := max(0, prefix-3)
	contextOldEnd := min(len(oldLines), oldSuffix+3)
	contextNewEnd := min(len(newLines), newSuffix+3)

	var b strings.Builder
	fmt.Fprintf(&b, "--- %s\n", oldName)
	fmt.Fprintf(&b, "+++ %s\n", newName)
	fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n",
		contextStart+1,
		max(0, contextOldEnd-contextStart),
		contextStart+1,
		max(0, contextNewEnd-contextStart),
	)

	for _, line := range oldLines[contextStart:prefix] {
		fmt.Fprintf(&b, " %s\n", line)
	}
	for _, line := range oldLines[prefix:oldSuffix] {
		fmt.Fprintf(&b, "-%s\n", line)
	}
	for _, line := range newLines[prefix:newSuffix] {
		fmt.Fprintf(&b, "+%s\n", line)
	}

	oldTailStart := oldSuffix
	newTailStart := newSuffix
	for oldTailStart < contextOldEnd && newTailStart < contextNewEnd {
		fmt.Fprintf(&b, " %s\n", oldLines[oldTailStart])
		oldTailStart++
		newTailStart++
	}
	return b.String()
}

func splitDiffLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	text = strings.TrimSuffix(text, "\n")
	if text == "" {
		return []string{""}
	}
	return strings.Split(text, "\n")
}
