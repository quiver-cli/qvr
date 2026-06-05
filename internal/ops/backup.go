package ops

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/quiver-cli/qvr/internal/config"
)

// Backup helpers shared by every HookInstaller. The on-disk layout is:
//
//	$QUIVER_HOME/backups/<agent>/<yyyy-mm-dd-hh-mm-ss>/<config>.bak
//
// Install copies the agent's live config into a fresh timestamped dir
// before mutating it; Uninstall restores from the newest such dir. Keeping
// these here (rather than per-adapter) means one copy/timestamp/glob-newest
// implementation, identical semantics across agents.

// backupTimestampLayout is sortable lexicographically, so the newest
// backup dir is always the max string — LatestBackupDir relies on this.
const backupTimestampLayout = "2006-01-02-15-04-05"

// BackupRoot returns $QUIVER_HOME/backups.
func BackupRoot() string {
	return filepath.Join(config.Dir(), "backups")
}

// NewBackupDir creates and returns a fresh timestamped backup directory
// for agent: $QUIVER_HOME/backups/<agent>/<timestamp>/. The timestamp has
// second resolution; callers that might back up twice in the same second
// should reuse the returned dir rather than calling twice.
func NewBackupDir(agent string) (string, error) {
	dir := filepath.Join(BackupRoot(), agent, time.Now().UTC().Format(backupTimestampLayout))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	return dir, nil
}

// LatestBackupDir returns the newest timestamped backup dir for agent, or
// ("", nil) if none exist. Newest = lexicographically-largest name, which
// is also chronologically newest given backupTimestampLayout.
func LatestBackupDir(agent string) (string, error) {
	base := filepath.Join(BackupRoot(), agent)
	entries, err := os.ReadDir(base)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read backup dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", nil
	}
	sort.Strings(names)
	return filepath.Join(base, names[len(names)-1]), nil
}

// CopyFileInto copies srcPath into destDir, preserving the base name and
// appending a .bak suffix, and returns the written path. Used at install
// time to snapshot the agent's config before mutation.
func CopyFileInto(srcPath, destDir string) (string, error) {
	dest := filepath.Join(destDir, filepath.Base(srcPath)+".bak")
	if err := CopyFile(srcPath, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// CopyFile copies src to dst with 0600 perms, creating parent dirs as
// needed. It is a plain byte copy — fine for the small JSON/JS config
// files Quiver backs up.
func CopyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir for %s: %w", dst, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}
