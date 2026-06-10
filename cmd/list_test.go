package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
)

// TestRunList_EditModeSourceColumn is the #117 regression for the list
// surface. Pre-fix the SOURCE column rendered the literal word
// "registry" whenever entry.Source was empty — meaningless to a user
// who'd just run `qvr create` and expected the column to reflect the
// install kind. Now the empty-Source fallback consults the entry's
// mode: edit → "edit", everything else → "-".
func TestRunList_EditModeSourceColumn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	project := t.TempDir()
	t.Chdir(project)

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        "demo",
		Mode:        model.ModeEdit,
		EditPath:    ".claude/skills/demo",
		Source:      "", // greenfield init: no upstream
		Ref:         "main",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	// Materialise the eject dir so EffectiveTarget / loaders are happy.
	editAbs := filepath.Join(project, ".claude", "skills", "demo")
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: demo\ndescription: list source col\n---\n# demo\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	buf := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: buf, Err: &bytes.Buffer{}, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	t.Cleanup(func() {
		listGlobal = false
		listAll = false
	})
	listGlobal = false
	listAll = false

	if err := runList(listCmd, nil); err != nil {
		t.Fatalf("runList: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "registry") {
		// The literal word "registry" used to appear in the SOURCE column
		// for empty-Source entries. We want "edit" instead.
		t.Errorf("SOURCE column still uses literal 'registry' for edit-mode entry — issue #117:\n%s", got)
	}
	if !strings.Contains(got, "edit") {
		t.Errorf("SOURCE column missing 'edit' for edit-mode entry:\n%s", got)
	}
}

// TestRunList_EditModeSourceColumn_AfterEjectFromRegistry is the
// follow-up #117 regression: a skill installed via `qvr add` and then
// ejected with `qvr edit` still has its upstream URL recorded in
// entry.Source (preserved as provenance.upstream too). Pre-follow-up the
// SOURCE column keyed off the raw Source field, so ejected entries
// painted identical to shared ones (`https://github.com/foo/bar.git`).
// Mode now wins precedence over Source so the column reflects the
// install kind, not the leftover upstream URL.
func TestRunList_EditModeSourceColumn_AfterEjectFromRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	project := t.TempDir()
	t.Chdir(project)

	lockPath := filepath.Join(project, model.LockFileName)
	lock := model.NewLockFile(lockPath)
	lock.Put(&model.LockEntry{
		Name:        "code-review",
		Registry:    "raks",
		Mode:        model.ModeEdit,
		EditPath:    ".claude/skills/code-review",
		Source:      "https://github.com/astra-sh/qvr_playground.git",
		Provenance:  &model.ProvenanceRef{Upstream: "https://github.com/astra-sh/qvr_playground.git"},
		Ref:         "v0.2.0",
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	editAbs := filepath.Join(project, ".claude", "skills", "code-review")
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: code-review\ndescription: ejected\n---\n# code-review\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	buf := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: buf, Err: &bytes.Buffer{}, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	t.Cleanup(func() {
		listGlobal = false
		listAll = false
	})

	if err := runList(listCmd, nil); err != nil {
		t.Fatalf("runList: %v", err)
	}
	got := buf.String()
	if strings.Contains(got, "github.com/astra-sh/qvr_playground.git") {
		t.Errorf("SOURCE column still prints upstream URL for ejected entry — issue #117 follow-up:\n%s", got)
	}
	if !strings.Contains(got, "edit") {
		t.Errorf("SOURCE column missing 'edit' for ejected-from-registry entry:\n%s", got)
	}
}
