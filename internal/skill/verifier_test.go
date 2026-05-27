package skill_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// makeVerifierTestRepo seeds a non-bare git repo at t.TempDir() containing
// a skill at skills/foo/SKILL.md and returns the repo path.
func makeVerifierTestRepo(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	skillDir := filepath.Join(dir, "skills/foo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("skills/foo/SKILL.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

func TestVerifySingleEntry_ok(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\nbody\n")
	id, err := canonical.HashSubtree(repo, "skills/foo")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
		Verification: &model.VerificationRecord{
			SubtreeHash: id.SubtreeHash,
			TreeSHA:     id.TreeSHA,
			CommitSHA:   id.CommitSHA,
			Status:      model.StatusUnverified,
		},
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusOK {
		t.Errorf("Status = %q, want %q (drift=%+v message=%q)", got.Status, skill.VerifyStatusOK, got.Drift, got.Message)
	}
	if len(got.Drift) != 0 {
		t.Errorf("Drift should be empty, got %+v", got.Drift)
	}
}

func TestVerifySingleEntry_driftOnTamper(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\noriginal\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	entry.Verification = skill.PopulateVerification(entry, model.ProvenanceRef{})

	// Tamper: commit a new version on top, simulating an upstream change
	// that the lockfile hasn't been refreshed against.
	if err := os.WriteFile(
		filepath.Join(repo, "skills/foo/SKILL.md"),
		[]byte("---\nname: foo\n---\nMUTATED\n"),
		0o644,
	); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, _ := gogit.PlainOpen(repo)
	wt, _ := r.Worktree()
	if _, err := wt.Add("skills/foo/SKILL.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("tamper", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(1, 0).UTC()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusDrift {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusDrift)
	}
	foundSubtreeDrift := false
	for _, d := range got.Drift {
		if d.Field == "subtreeHash" {
			foundSubtreeDrift = true
		}
	}
	if !foundSubtreeDrift {
		t.Errorf("expected subtreeHash in drift list, got %+v", got.Drift)
	}
}

func TestVerifySingleEntry_unverifiedWhenNoBlock(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\nbody\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
		// No Verification — represents a v2-loaded entry.
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusUnverified {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusUnverified)
	}
}

func TestVerifySingleEntry_missingWorktree(t *testing.T) {
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: filepath.Join(t.TempDir(), "does-not-exist"),
		Path:     "skills/foo",
		Source:   "registry",
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusMissing {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusMissing)
	}
}

// Regression for #18: a worktree whose checked-out bytes are modified
// post-install must report drift, even though the underlying git tree
// at HEAD is unchanged. Pre-fix the verifier compared the recorded hash
// against a re-derivation from the same static git tree — so any disk
// edit (or even rm) would silently report "ok".
func TestVerifySingleEntry_driftOnDiskTamperWithStaticGitTree(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\nbody\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	entry.Verification = skill.PopulateVerification(entry, model.ProvenanceRef{})
	if entry.Verification == nil || entry.Verification.SubtreeHash == "" {
		t.Fatalf("PopulateVerification did not record a hash: %+v", entry.Verification)
	}

	// Mutate on disk without committing — git tree at HEAD is unchanged.
	skillFile := filepath.Join(repo, "skills/foo/SKILL.md")
	f, err := os.OpenFile(skillFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString("\nINJECT_FROM_ATTACKER\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusDrift {
		t.Errorf("Status = %q, want %q (drift list = %+v)", got.Status, skill.VerifyStatusDrift, got.Drift)
	}
	foundSubtreeDrift := false
	for _, d := range got.Drift {
		if d.Field == "subtreeHash" {
			foundSubtreeDrift = true
		}
	}
	if !foundSubtreeDrift {
		t.Errorf("expected subtreeHash drift after on-disk append, got %+v", got.Drift)
	}
}

// Regression for #18: deleting the sole file in a skill's subtree must
// produce a non-"ok" status. Pre-fix this silently passed because the
// verifier hashed the git tree, not the disk.
func TestVerifySingleEntry_failedWhenAllContentDeleted(t *testing.T) {
	repo := makeVerifierTestRepo(t, "x\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	entry.Verification = skill.PopulateVerification(entry, model.ProvenanceRef{})

	if err := os.Remove(filepath.Join(repo, "skills/foo/SKILL.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}

	got := skill.VerifySingleEntry(entry)
	if got.Status == skill.VerifyStatusOK {
		t.Errorf("Status reported ok after sole file deletion — drift invisible")
	}
}

// Regression for #30: --repair must rewrite the recorded SubtreeHash to
// match the on-disk worktree. Pre-fix RepairVerificationFromDisk's
// predecessor used HashSubtree (git-tree-at-HEAD), so when the user edited
// disk files without committing, the "repair" wrote the unchanged git tree
// hash back — a no-op. Subsequent verify runs still reported drift.
func TestRepairVerificationFromDisk_rewritesToDiskHash(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\nbody\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	entry.Verification = skill.PopulateVerification(entry, model.ProvenanceRef{})
	originalHash := entry.Verification.SubtreeHash
	if originalHash == "" {
		t.Fatal("PopulateVerification did not record an initial hash")
	}

	// Tamper on-disk only — git tree at HEAD unchanged.
	skillFile := filepath.Join(repo, "skills/foo/SKILL.md")
	if err := os.WriteFile(skillFile, []byte("---\nname: foo\n---\nrewritten by user\n"), 0o644); err != nil {
		t.Fatalf("rewrite on disk: %v", err)
	}

	// Sanity: pre-repair, the entry shows drift.
	if got := skill.VerifySingleEntry(entry); got.Status != skill.VerifyStatusDrift {
		t.Fatalf("expected drift before repair, got %q", got.Status)
	}

	res := skill.RepairVerificationFromDisk(entry)
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
	if entry.Verification.SubtreeHash != res.NewSubtreeHash {
		t.Errorf("entry not updated in place: got %q, want %q",
			entry.Verification.SubtreeHash, res.NewSubtreeHash)
	}

	// Post-repair, a follow-up verify must succeed — the in-band recovery
	// contract is that a single --repair pass seals the drift.
	if got := skill.VerifySingleEntry(entry); got.Status != skill.VerifyStatusOK {
		t.Errorf("post-repair verify Status = %q, want %q (drift=%+v)",
			got.Status, skill.VerifyStatusOK, got.Drift)
	}
}

// Repairing an unverified entry (no prior Verification block) seals the
// current disk state for the first time — useful when migrating a v2 entry
// that lacked provenance and the user trusts the current worktree.
func TestRepairVerificationFromDisk_sealsUnverified(t *testing.T) {
	repo := makeVerifierTestRepo(t, "---\nname: foo\n---\nbody\n")
	entry := &model.LockEntry{
		Name:     "foo",
		Worktree: repo,
		Path:     "skills/foo",
		Source:   "registry",
	}
	if got := skill.VerifySingleEntry(entry); got.Status != skill.VerifyStatusUnverified {
		t.Fatalf("expected unverified pre-repair, got %q", got.Status)
	}

	res := skill.RepairVerificationFromDisk(entry)
	if res.Failed {
		t.Fatalf("repair failed: %s", res.Error)
	}
	if res.OldSubtreeHash != "" {
		t.Errorf("OldSubtreeHash should be empty for first-time seal, got %q", res.OldSubtreeHash)
	}
	if res.NewSubtreeHash == "" {
		t.Error("NewSubtreeHash empty after sealing an unverified entry")
	}
	if entry.Verification == nil || entry.Verification.SubtreeHash != res.NewSubtreeHash {
		t.Errorf("entry not sealed: %+v", entry.Verification)
	}
}

// Link installs have no upstream to hash. RepairVerificationFromDisk should
// reject them cleanly rather than producing a partial Verification block.
func TestRepairVerificationFromDisk_rejectsLink(t *testing.T) {
	entry := &model.LockEntry{
		Name:       "foo",
		Source:     "link",
		LinkTarget: "/some/path",
	}
	res := skill.RepairVerificationFromDisk(entry)
	if !res.Failed {
		t.Error("expected Failed=true for link-source entry")
	}
	if entry.Verification != nil {
		t.Errorf("link entry mutated: %+v", entry.Verification)
	}
}

func TestVerifySingleEntry_linkSkipped(t *testing.T) {
	entry := &model.LockEntry{
		Name:       "foo",
		LinkTarget: "/some/local/path",
		Source:     "link",
	}
	got := skill.VerifySingleEntry(entry)
	if got.Status != skill.VerifyStatusLink {
		t.Errorf("Status = %q, want %q", got.Status, skill.VerifyStatusLink)
	}
}
