package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeLock builds a minimal v3 lock file for tests. Hand-rolled JSON
// (rather than going through model.LockFile.Write) so the test stays
// independent of model-level serialization changes.
func writeLock(t *testing.T, path string, entries map[string]*model.LockEntry) {
	t.Helper()
	body := map[string]any{
		"version": model.LockFileVersion,
		"skills":  entries,
	}
	raw, err := json.MarshalIndent(body, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, raw, 0o644))
}

func writeTestSkillMD(t *testing.T, dir, name string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	body := "---\nname: " + name + "\ndescription: fixture skill for scan resolver tests\n---\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644))
}

// TestScanGlobalNestedRegistryResolves is the regression guard for
// issue #42 case 2. The mattpocock layout stores the per-skill SKILL.md
// under skills/<category>/<name>/. Before the fix, --global resolved
// to the worktree root and failed with "SKILL.md not found".
func TestScanGlobalNestedRegistryResolves(t *testing.T) {
	defer resetScanFlags()
	defer func() { scanGlobal = false }()

	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	reg, name, commit := "github.com--mattpocock--skills", "diagnose", "abc1234"
	worktree := registry.WorktreePath(reg, name, registry.ShortSHA(commit))
	subpath := "skills/engineering/diagnose"
	writeTestSkillMD(t, filepath.Join(worktree, subpath), "diagnose")
	writeLock(t, filepath.Join(home, model.LockFileName), map[string]*model.LockEntry{
		"diagnose": {
			Name:        name,
			Registry:    reg,
			Source:      "https://github.com/mattpocock/skills.git",
			Path:        subpath,
			Ref:         "main",
			Commit:      commit,
			InstalledAt: time.Now(),
		},
	})

	scanGlobal = true
	_, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()
	if err := runScan(scanCmd, []string{"diagnose"}); err != nil {
		t.Fatalf("expected clean resolution for nested global skill, got %v", err)
	}
}

// TestScanGlobalMissingNameProducesDomainError is the regression guard
// for issue #42 case 1: --global with a name absent from the global
// lock used to fall through to a CWD-relative stat and emit a
// misleading filesystem error.
func TestScanGlobalMissingNameProducesDomainError(t *testing.T) {
	defer resetScanFlags()
	defer func() { scanGlobal = false }()

	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	writeLock(t, filepath.Join(home, model.LockFileName), map[string]*model.LockEntry{})

	scanGlobal = true
	_, _, restore := withScanPrinter(t, output.FormatText)
	defer restore()

	err := runScan(scanCmd, []string{"absent-skill"})
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "no installed skill")
	assert.Contains(t, msg, "global")
	assert.NotContains(t, msg, "stat ", "domain error must not leak the relative-path stat failure")
	// Path-relative-to-cwd hint from old code looked like "stat path: stat /..." — must be gone.
	assert.False(t, strings.Contains(msg, "no such file or directory"),
		"--global must not fall through to filesystem resolution, got %q", msg)
}
