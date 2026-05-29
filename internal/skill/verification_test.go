package skill_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

// seedRepoAt creates a non-bare git repo at dir with a skill at
// skills/<name>/SKILL.md. Returns nothing — dir already known to caller.
func seedRepoAt(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	skillRel := filepath.Join("skills", name)
	skillAbs := filepath.Join(dir, skillRel)
	if err := os.MkdirAll(skillAbs, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillAbs, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add(filepath.Join(skillRel, "SKILL.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// commitOnTop adds a new commit with body to existing skill at skills/<name>/SKILL.md.
func commitOnTop(t *testing.T, dir, name, body string) {
	t.Helper()
	skillAbs := filepath.Join(dir, "skills", name, "SKILL.md")
	if err := os.WriteFile(skillAbs, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("update", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestComputeSubtreeHash_returnsCanonicalHash exercises the small helper that
// installer.go uses to seal an install.
func TestComputeSubtreeHash_returnsCanonicalHash(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := registry.WorktreePath("r", "foo", "abc1234")
	seedRepoAt(t, wt, "foo", "---\nname: foo\n---\nbody\n")

	got, err := skill.ComputeSubtreeHash(wt, "skills/foo")
	if err != nil {
		t.Fatalf("ComputeSubtreeHash: %v", err)
	}
	if got == "" {
		t.Error("expected non-empty SubtreeHash")
	}
}

// RefreshSubtreeHash rewrites entry.SubtreeHash to match the new on-disk
// state after a Pull / Switch / Upgrade. v5: no Verification side effects.
func TestRefreshSubtreeHash_updatesEntryInPlace(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := registry.WorktreePath("r", "foo", "abc1234")
	seedRepoAt(t, wt, "foo", "---\nname: foo\n---\nv1\n")

	entry := &model.LockEntry{
		Name:     "foo",
		Registry: "r",
		Source:   "git@example.test:r.git",
		Path:     "skills/foo",
		Ref:      "v1.0.0",
		Commit:   "abc1234",
	}
	original, err := skill.ComputeSubtreeHash(wt, entry.Path)
	if err != nil {
		t.Fatalf("seed hash: %v", err)
	}
	entry.SubtreeHash = original

	commitOnTop(t, wt, "foo", "---\nname: foo\n---\nv2\n")

	if err := skill.RefreshSubtreeHash(entry); err != nil {
		t.Fatalf("RefreshSubtreeHash: %v", err)
	}
	if entry.SubtreeHash == original {
		t.Errorf("hash did not refresh after content change (still %q)", entry.SubtreeHash)
	}
}

// Link installs are skipped by RefreshSubtreeHash — they have no upstream
// subtree to re-derive.
func TestRefreshSubtreeHash_skipsLink(t *testing.T) {
	entry := &model.LockEntry{
		Name:        "linked",
		Source:      "/some/local/path",
		Ref:         "local",
		SubtreeHash: "sha256:original",
	}
	if err := skill.RefreshSubtreeHash(entry); err != nil {
		t.Fatalf("RefreshSubtreeHash on link: %v", err)
	}
	if entry.SubtreeHash != "sha256:original" {
		t.Errorf("link entry hash mutated: got %q", entry.SubtreeHash)
	}
}
