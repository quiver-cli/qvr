package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
)

// writeTreeLock writes a v5 project lock at project/qvr.lock with the given
// entries and returns once it's on disk. Entries are keyed by Name.
func writeTreeLock(t *testing.T, project string, entries ...*model.LockEntry) {
	t.Helper()
	lock := model.NewLockFile(filepath.Join(project, model.LockFileName))
	for _, e := range entries {
		lock.Put(e)
	}
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

func resetTreeFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		treeGlobal = false
		treeAll = false
	})
	treeGlobal = false
	treeAll = false
}

func captureTreeText(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: buf, Err: &bytes.Buffer{}, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })
	return buf
}

// TestRunTree_GroupsByRegistryWithTargets is the happy path: two registries,
// a multi-target skill, and the box-drawing connectors. The reachable-vs-
// orphan distinction doesn't matter here — tree reads the lock, not the cache.
func TestRunTree_GroupsByRegistryWithTargets(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	resetTreeFlags(t)

	writeTreeLock(t, project,
		&model.LockEntry{
			Name: "code-reviewer", Registry: "acme", Source: "git@x:acme.git",
			Ref: "v1.2.0", Commit: "a1b2c3d4567", Targets: []string{"cursor", "claude"},
			InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		&model.LockEntry{
			Name: "doc-writer", Registry: "beta", Source: "git@x:beta.git",
			Ref: "main", Commit: "9f8e7d6c000", Targets: []string{"claude"},
			InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	)

	buf := captureTreeText(t)
	if err := runTree(treeCmd, nil); err != nil {
		t.Fatalf("runTree: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		"acme", "beta",
		"code-reviewer@v1.2.0 (a1b2c3d)",
		"doc-writer@main (9f8e7d6)",
		"├── ", "└── ",
		"claude", "cursor",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("tree output missing %q:\n%s", want, got)
		}
	}
	// Targets are sorted, so claude precedes cursor under code-reviewer.
	if strings.Index(got, "claude") > strings.Index(got, "cursor") {
		t.Errorf("targets not sorted (claude should precede cursor):\n%s", got)
	}
}

// TestRunTree_MarkersAndLocalGroup covers edit/link/disabled markers and the
// synthetic "(local)" group for registry-less entries. A link entry has no
// commit, so the line shows an em dash.
func TestRunTree_MarkersAndLocalGroup(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	resetTreeFlags(t)

	writeTreeLock(t, project,
		&model.LockEntry{
			Name: "ejected", Registry: "acme", Mode: model.ModeEdit,
			EditPath: ".claude/skills/ejected", Ref: "main", Commit: "deadbeef111",
			Targets: []string{"claude"}, Disabled: true,
			InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		&model.LockEntry{
			Name: "my-dev", Mode: model.ModeLink, Ref: "local",
			Source: "/abs/path/my-dev", Targets: []string{"claude"},
			InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
	)

	buf := captureTreeText(t)
	if err := runTree(treeCmd, nil); err != nil {
		t.Fatalf("runTree: %v", err)
	}
	got := buf.String()

	if !strings.Contains(got, "(local)") {
		t.Errorf("expected synthetic (local) group for registry-less link entry:\n%s", got)
	}
	if !strings.Contains(got, "[edit, disabled]") {
		t.Errorf("expected [edit, disabled] markers on ejected entry:\n%s", got)
	}
	if !strings.Contains(got, "[link]") {
		t.Errorf("expected [link] marker on link entry:\n%s", got)
	}
	if !strings.Contains(got, "my-dev@local (—)") {
		t.Errorf("expected em-dash commit for link entry:\n%s", got)
	}
}

// TestRunTree_JSONGrouping checks the grouped JSON envelope: an array of
// registry groups, each with full commits and a targets array.
func TestRunTree_JSONGrouping(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	resetTreeFlags(t)

	writeTreeLock(t, project, &model.LockEntry{
		Name: "code-reviewer", Registry: "acme", Source: "git@x:acme.git",
		Ref: "v1.2.0", Commit: "a1b2c3d4567890", Targets: []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})

	printer = &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: output.FormatJSON}
	if err := runTree(treeCmd, nil); err != nil {
		t.Fatalf("runTree json: %v", err)
	}
	outBuf, ok := printer.Out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("printer.Out is not a *bytes.Buffer; got %T", printer.Out)
	}
	var groups []treeGroup
	if err := json.Unmarshal(outBuf.Bytes(), &groups); err != nil {
		t.Fatalf("unmarshal tree JSON: %v", err)
	}
	if len(groups) != 1 || groups[0].Registry != "acme" || len(groups[0].Skills) != 1 {
		t.Fatalf("unexpected groups: %+v", groups)
	}
	s := groups[0].Skills[0]
	if s.Commit != "a1b2c3d4567890" {
		t.Errorf("JSON should carry the full commit, got %q", s.Commit)
	}
	if len(s.Targets) != 1 || s.Targets[0] != "claude" {
		t.Errorf("unexpected targets: %v", s.Targets)
	}
}

// TestRunTree_AllAddsScopeHeaders pins the --all section headers. A
// project-scope entry and a global-scope entry must surface under "project:"
// and "global:" headers respectively.
func TestRunTree_AllAddsScopeHeaders(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	project := t.TempDir()
	t.Chdir(project)
	resetTreeFlags(t)
	treeAll = true

	// Project lock.
	writeTreeLock(t, project, &model.LockEntry{
		Name: "proj-skill", Registry: "acme", Source: "git@x:acme.git",
		Ref: "main", Commit: "aaa1111", Targets: []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	// Global lock at $QUIVER_HOME/qvr.lock.
	globalLock := model.NewLockFile(filepath.Join(home, model.LockFileName))
	globalLock.Put(&model.LockEntry{
		Name: "glob-skill", Registry: "beta", Source: "git@x:beta.git",
		Ref: "main", Commit: "bbb2222", Targets: []string{"cursor"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := globalLock.Write(); err != nil {
		t.Fatalf("write global lock: %v", err)
	}

	buf := captureTreeText(t)
	if err := runTree(treeCmd, nil); err != nil {
		t.Fatalf("runTree --all: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "project:") || !strings.Contains(got, "global:") {
		t.Errorf("--all missing scope headers:\n%s", got)
	}
	// project section comes first, and its skill precedes the global one.
	if strings.Index(got, "proj-skill") > strings.Index(got, "glob-skill") {
		t.Errorf("project scope should render before global:\n%s", got)
	}
}

// TestRunTree_EmptyText / JSON: no installed skills renders a friendly line in
// text and an empty array (not null) in JSON.
func TestRunTree_Empty(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	t.Chdir(t.TempDir())
	resetTreeFlags(t)

	buf := captureTreeText(t)
	if err := runTree(treeCmd, nil); err != nil {
		t.Fatalf("runTree empty text: %v", err)
	}
	if !strings.Contains(buf.String(), "No installed skills") {
		t.Errorf("expected empty-state message, got: %q", buf.String())
	}

	printer = &output.Printer{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}, Format: output.FormatJSON}
	if err := runTree(treeCmd, nil); err != nil {
		t.Fatalf("runTree empty json: %v", err)
	}
	emptyBuf, ok := printer.Out.(*bytes.Buffer)
	if !ok {
		t.Fatalf("printer.Out is not a *bytes.Buffer; got %T", printer.Out)
	}
	if got := strings.TrimSpace(emptyBuf.String()); got != "[]" {
		t.Errorf("empty JSON should be [], got %q", got)
	}
}
