package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
)

// TestSync_RestoresLockedCommitNotMovedRef is the uv reproducibility contract:
// once a skill is locked at a commit, `qvr sync` restores THAT commit even when
// the ref label (main) has advanced upstream. Only `qvr update` should move it.
//
// Without PinCommit in the reconciler, a fresh-checkout sync re-resolves the
// ref and silently installs today's tip — exactly the drift uv.lock prevents.
func TestSync_RestoresLockedCommitNotMovedRef(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@main",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	lockPath := filepath.Join(h.project, model.LockFileName)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	c1 := entry.Commit
	if c1 == "" {
		t.Fatal("install recorded no commit")
	}
	if entry.TreeOID == "" {
		t.Error("install recorded no treeOID")
	}

	// Move main forward upstream, then fetch the registry bare so the ref now
	// resolves past the locked commit.
	advanceRemoteMain(t, remote)
	if _, err := h.manager.Update(context.Background(), "acme"); err != nil {
		t.Fatalf("registry update: %v", err)
	}
	gc := git.NewGoGitClient()
	c2, err := gc.ResolveRef(registry.RegistryPath("acme"), "main")
	if err != nil {
		t.Fatalf("resolve main after advance: %v", err)
	}
	if c2 == c1 {
		t.Fatal("remote main did not advance — test would not detect drift")
	}

	// Simulate a fresh checkout: the worktree is gone, only qvr.lock remains.
	if err := os.RemoveAll(skill.EntryWorktreePath(entry)); err != nil {
		t.Fatalf("remove worktree: %v", err)
	}

	// Reconcile is what `qvr sync` runs.
	reconciler := skill.NewReconciler(h.installer)
	if _, err := reconciler.Reconcile(lock, h.project, h.home, skill.ReconcileOptions{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	lock2, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("re-read lock: %v", err)
	}
	e2, err := lock2.Get("code-review")
	if err != nil {
		t.Fatalf("lock get after sync: %v", err)
	}
	if e2.Commit != c1 {
		t.Errorf("sync moved lock off the pinned commit: got %s, want %s (advanced tip was %s)", e2.Commit, c1, c2)
	}
	head, err := gc.HeadCommit(skill.EntryWorktreePath(e2))
	if err != nil {
		t.Fatalf("worktree HEAD: %v", err)
	}
	if head != c1 {
		t.Errorf("restored worktree at %s, want pinned %s — uv reproducibility violated", head, c1)
	}
}

// TestCheckGitProvenance_UnsignedReportsNone confirms an ordinary unsigned
// install reports "none" (not "invalid") — unsigned must never look like
// tampering. Policy can still choose to reject unsigned refs.
func TestCheckGitProvenance_UnsignedReportsNone(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	prov := skill.CheckGitProvenance(skill.EntryWorktreePath(entry), entry.Ref, entry.Commit, entry.Path)
	if prov == nil {
		t.Fatal("expected a provenance result for an installed skill")
	}
	if prov.SignatureStatus != model.SignatureStatusNone {
		t.Errorf("unsigned skill reported %q, want %q", prov.SignatureStatus, model.SignatureStatusNone)
	}
}

// TestInstall_FreezesSharedWorktreeReadOnly verifies the immutability hinge:
// a shared install is frozen read-only at download, so an in-place overwrite
// (an agent or stray script writing through the symlink) is refused. Modifying
// a skill must go through `qvr edit`, which ejects a writable copy.
func TestInstall_FreezesSharedWorktreeReadOnly(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	skillFile := filepath.Join(skill.EntryWorktreePath(entry), entry.Path, "SKILL.md")
	info, err := os.Stat(skillFile)
	if err != nil {
		t.Fatalf("stat installed SKILL.md: %v", err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Errorf("installed SKILL.md mode = %o, want frozen (no write bits)", info.Mode().Perm())
	}
	// A direct in-place overwrite of a frozen install must be refused.
	// Root bypasses DAC permission checks, so the write would succeed there
	// regardless of the read-only bits — skip the refusal assertion under UID 0
	// (the mode-bit assertion above still pins the freeze).
	if os.Geteuid() == 0 {
		t.Log("running as root; skipping write-refusal assertion (DAC bypassed)")
	} else if err := os.WriteFile(skillFile, []byte("tamper"), 0o644); err == nil {
		t.Error("in-place write to a frozen install succeeded; want permission denied")
	}
}

// TestInstall_ExecutableFilePreservedAndVerifies is the regression for issue
// #135: a skill that ships an executable script (git mode 100755) must install,
// freeze read-only, and then verify clean — with no spurious subtreeHash drift.
//
// The bug: the recorded SubtreeHash is computed from the git tree (mode 100755),
// but the read-only freeze flattened every file to 0o444, stripping the exec
// bit. The verifier re-hashes the on-disk worktree, saw 100644, and reported
// permanent drift for any exec-shipping skill — breaking `qvr sync --check` on a
// pristine install. The fix preserves the exec bit on freeze (0o555), so disk
// and git agree. The earlier HashSubtreeAtCommit unit test missed this because
// it hashed a checkout that preserved modes, not the real 0o444 materialisation.
func TestInstall_ExecutableFilePreservedAndVerifies(t *testing.T) {
	h := newHarness(t)
	const execSkill = `---
name: exec-skill
description: Ships an executable script.
---

# Exec Skill
`
	remote := seedRemoteWithExecScript(t, "exec-skill", execSkill, "run.sh", "#!/bin/sh\necho hi\n")
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "exec-skill@main",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("exec-skill")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.SubtreeHash == "" {
		t.Fatal("install recorded no subtreeHash")
	}

	// The installed script must keep its exec bit (the #135 discriminator) while
	// staying write-protected: read + execute, no write — i.e. 0o555.
	scriptPath := filepath.Join(skill.EntryWorktreePath(entry), entry.Path, "run.sh")
	info, err := os.Stat(scriptPath)
	if err != nil {
		t.Fatalf("stat installed run.sh: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("installed run.sh mode = %o, want exec bit preserved", info.Mode().Perm())
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Errorf("installed run.sh mode = %o, want frozen (no write bits)", info.Mode().Perm())
	}

	// The heart of #135: a freshly installed exec-shipping skill verifies clean.
	res := skill.VerifySingleEntry(entry, h.project)
	if res.Status != skill.VerifyStatusOK {
		t.Errorf("verify status = %q (%s), drift=%+v; want OK — exec bit must not cause drift",
			res.Status, res.Message, res.Drift)
	}
}

// advanceRemoteMain clones the bare remote, adds a commit on main, and pushes —
// simulating an upstream that moved after a skill was locked.
func advanceRemoteMain(t *testing.T, remote string) {
	t.Helper()
	work := t.TempDir()
	repo, err := gogit.PlainClone(work, false, &gogit.CloneOptions{URL: remote})
	if err != nil {
		t.Fatalf("clone remote: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	extra := filepath.Join(work, "skills", "code-review", "UPSTREAM.md")
	if err := os.WriteFile(extra, []byte("upstream moved on\n"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", "code-review", "UPSTREAM.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("advance main", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Up", Email: "up@up", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := repo.Push(&gogit.PushOptions{}); err != nil {
		t.Fatalf("push: %v", err)
	}
}
