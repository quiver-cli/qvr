package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/canonical"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
)

// seedVerifierWorktree creates a git worktree on disk at
// registry.WorktreePath(reg, name, sha) so VerifySingleEntry can find it
// via EntryWorktreePath. Returns the absolute worktree path.
//
// QUIVER_HOME must already be set via t.Setenv before calling.
func seedVerifierWorktree(t *testing.T, reg, name, sha, body string) string {
	t.Helper()
	wtPath := registry.WorktreePath(reg, name, sha)
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	repo, err := gogit.PlainInit(wtPath, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	skillDir := filepath.Join(wtPath, "skills/foo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := wt.Add("skills/foo/SKILL.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return wtPath
}

// makeV5Entry builds a v5 LockEntry that resolves to wtPath via
// EntryWorktreePath, with SubtreeHash pre-sealed from the disk state.
//
// InstallCommit is kept at the seed `sha` so EntryWorktreePath finds the
// directory; Commit is pinned to the worktree's actual git HEAD so
// VerifySingleEntry's commit cross-check (issue #73) is satisfied. The
// two-field split mirrors how real installs work — InstallCommit pins
// the on-disk directory name, Commit tracks the current ref.
func makeV5Entry(t *testing.T, reg, name, sha, wtPath string) *model.LockEntry {
	t.Helper()
	id, err := canonical.HashSubtree(wtPath, "skills/foo")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	repo, err := gogit.PlainOpen(wtPath)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	return &model.LockEntry{
		Name:          name,
		Registry:      reg,
		Source:        "git@example.test:" + reg + ".git",
		Path:          "skills/foo",
		Ref:           "main",
		Commit:        head.Hash().String(),
		InstallCommit: sha,
		SubtreeHash:   id.SubtreeHash,
	}
}

func TestVerifySingleEntry_ok(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := seedVerifierWorktree(t, "ar", "foo", "abc1234", "---\nname: foo\n---\nbody\n")
	entry := makeV5Entry(t, "ar", "foo", "abc1234", wt)

	got := skill.VerifySingleEntry(entry, "")
	if got.Status != skill.VerifyStatusOK {
		t.Errorf("Status = %q, want %q (drift=%+v message=%q)", got.Status, skill.VerifyStatusOK, got.Drift, got.Message)
	}
	if len(got.Drift) != 0 {
		t.Errorf("Drift should be empty, got %+v", got.Drift)
	}
}

func TestVerifySingleEntry_driftOnDiskTamper(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := seedVerifierWorktree(t, "ar", "foo", "abc1234", "---\nname: foo\n---\noriginal\n")
	entry := makeV5Entry(t, "ar", "foo", "abc1234", wt)

	// Mutate on disk without committing — the working copy diverges from
	// the SubtreeHash recorded at install time.
	skillFile := filepath.Join(wt, "skills/foo/SKILL.md")
	f, err := os.OpenFile(skillFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString("\nINJECT_FROM_ATTACKER\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	got := skill.VerifySingleEntry(entry, "")
	if got.Status != skill.VerifyStatusDrift {
		t.Errorf("Status = %q, want %q (drift list = %+v)", got.Status, skill.VerifyStatusDrift, got.Drift)
	}
	if len(got.Drift) == 0 || got.Drift[0].Field != "subtreeHash" {
		t.Errorf("expected subtreeHash drift after on-disk append, got %+v", got.Drift)
	}
}

func TestVerifySingleEntry_unverifiedWhenNoHash(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := seedVerifierWorktree(t, "ar", "foo", "abc1234", "---\nname: foo\n---\nbody\n")
	// Entry exists, worktree exists, but SubtreeHash was never recorded
	// (e.g. an install that hit a hashing failure). Commit is pinned to the
	// real worktree HEAD so the commit cross-check (issue #73) passes and
	// "unverified" remains the dominant signal.
	repo, err := gogit.PlainOpen(wt)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	entry := &model.LockEntry{
		Name:          "foo",
		Registry:      "ar",
		Source:        "git@example.test:ar.git",
		Path:          "skills/foo",
		Ref:           "main",
		Commit:        head.Hash().String(),
		InstallCommit: "abc1234",
	}
	got := skill.VerifySingleEntry(entry, "")
	if got.Status != skill.VerifyStatusUnverified {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusUnverified)
	}
	// Regression for #61: the hint must point at a real command. `qvr
	// lock --repair` is a flag (verify --repair), not a subcommand; the
	// gap-filling subcommand is `qvr lock upgrade`.
	if !strings.Contains(got.Message, "qvr lock upgrade") {
		t.Errorf("hint should reference `qvr lock upgrade`, got: %q", got.Message)
	}
}

func TestVerifySingleEntry_missingWorktree(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	// No worktree seeded — EntryWorktreePath resolves to a non-existent path.
	entry := &model.LockEntry{
		Name:     "foo",
		Registry: "ar",
		Source:   "git@example.test:ar.git",
		Path:     "skills/foo",
		Ref:      "main",
		Commit:   "deadbeef",
	}
	got := skill.VerifySingleEntry(entry, "")
	if got.Status != skill.VerifyStatusMissing {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusMissing)
	}
}

// TestVerifySingleEntry_driftOnTamperedCommit is the regression guard for
// issue #73: a hand-edited `commit` field (e.g. rewritten to
// `deadbeef...`) must be flagged by `qvr lock verify` as drift. Prior to
// the fix, verify only checked SubtreeHash and a tampered commit field
// passed every audit qvr offers.
func TestVerifySingleEntry_driftOnTamperedCommit(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := seedVerifierWorktree(t, "ar", "foo", "abc1234", "---\nname: foo\n---\nbody\n")
	entry := makeV5Entry(t, "ar", "foo", "abc1234", wt)
	// On-disk content unchanged — only the lockfile's commit field has
	// been tampered with. SubtreeHash still matches the worktree.
	entry.Commit = "deadbeef00000000000000000000000000000000"

	got := skill.VerifySingleEntry(entry, "")
	if got.Status != skill.VerifyStatusDrift {
		t.Errorf("Status = %q, want %q (drift = %+v)", got.Status, skill.VerifyStatusDrift, got.Drift)
	}
	var sawCommitDrift bool
	for _, d := range got.Drift {
		if d.Field == "commit" {
			sawCommitDrift = true
			if d.Expected != entry.Commit {
				t.Errorf("commit drift Expected = %q, want %q", d.Expected, entry.Commit)
			}
		}
	}
	if !sawCommitDrift {
		t.Errorf("expected commit-field drift, got %+v", got.Drift)
	}
}

func TestVerifySingleEntry_linkSkipped(t *testing.T) {
	entry := &model.LockEntry{
		Name:   "foo",
		Source: "/some/local/path",
		Ref:    "local",
	}
	got := skill.VerifySingleEntry(entry, "")
	if got.Status != skill.VerifyStatusLink {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusLink)
	}
}

// Regression for #30: --repair rewrites SubtreeHash to match the on-disk
// worktree even when there are uncommitted edits. Pre-v5 the predecessor
// helper hashed git-tree-at-HEAD, which silently no-op'd this case.
func TestRepairSubtreeHashFromDisk_rewritesToDiskHash(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := seedVerifierWorktree(t, "ar", "foo", "abc1234", "---\nname: foo\n---\nbody\n")
	entry := makeV5Entry(t, "ar", "foo", "abc1234", wt)
	originalHash := entry.SubtreeHash
	if originalHash == "" {
		t.Fatal("seed did not record an initial hash")
	}

	// Tamper on-disk only — git tree at HEAD unchanged.
	skillFile := filepath.Join(wt, "skills/foo/SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: foo\n---\nrewritten by user\n"), 0o644); err != nil {
		t.Fatalf("rewrite on disk: %v", err)
	}

	if got := skill.VerifySingleEntry(entry, ""); got.Status != skill.VerifyStatusDrift {
		t.Fatalf("expected drift before repair, got %q", got.Status)
	}

	res := skill.RepairSubtreeHashFromDisk(entry, "")
	if res.Failed {
		t.Fatalf("repair failed: %s", res.Error)
	}
	if res.OldSubtreeHash != originalHash {
		t.Errorf("OldSubtreeHash = %q, want %q", res.OldSubtreeHash, originalHash)
	}
	if res.NewSubtreeHash == "" {
		t.Error("NewSubtreeHash empty after successful repair")
	}
	if res.NewSubtreeHash == originalHash {
		t.Error("repair produced the same hash — would be a no-op")
	}
	if entry.SubtreeHash != res.NewSubtreeHash {
		t.Errorf("entry.SubtreeHash not updated in place: got %q, want %q",
			entry.SubtreeHash, res.NewSubtreeHash)
	}

	if got := skill.VerifySingleEntry(entry, ""); got.Status != skill.VerifyStatusOK {
		t.Errorf("post-repair verify Status = %q, want %q (drift=%+v)",
			got.Status, skill.VerifyStatusOK, got.Drift)
	}
}

// Repairing an unverified entry (no prior SubtreeHash) seals the current
// disk state for the first time.
func TestRepairSubtreeHashFromDisk_sealsUnverified(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	wt := seedVerifierWorktree(t, "ar", "foo", "abc1234", "---\nname: foo\n---\nbody\n")
	repo, err := gogit.PlainOpen(wt)
	if err != nil {
		t.Fatalf("open repo: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	entry := &model.LockEntry{
		Name:          "foo",
		Registry:      "ar",
		Source:        "git@example.test:ar.git",
		Path:          "skills/foo",
		Ref:           "main",
		Commit:        head.Hash().String(),
		InstallCommit: "abc1234",
	}
	if got := skill.VerifySingleEntry(entry, ""); got.Status != skill.VerifyStatusUnverified {
		t.Fatalf("expected unverified pre-repair, got %q", got.Status)
	}

	res := skill.RepairSubtreeHashFromDisk(entry, "")
	if res.Failed {
		t.Fatalf("repair failed: %s", res.Error)
	}
	if res.OldSubtreeHash != "" {
		t.Errorf("OldSubtreeHash should be empty for first-time seal, got %q", res.OldSubtreeHash)
	}
	if res.NewSubtreeHash == "" {
		t.Error("NewSubtreeHash empty after sealing an unverified entry")
	}
	if entry.SubtreeHash != res.NewSubtreeHash {
		t.Errorf("entry not sealed: SubtreeHash=%q want %q", entry.SubtreeHash, res.NewSubtreeHash)
	}
}

func TestRepairSubtreeHashFromDisk_rejectsLink(t *testing.T) {
	entry := &model.LockEntry{
		Name:   "foo",
		Source: "/some/path",
		Ref:    "local",
	}
	res := skill.RepairSubtreeHashFromDisk(entry, "")
	if !res.Failed {
		t.Error("expected Failed=true for link-source entry")
	}
}
