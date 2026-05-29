package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

// TestInstall_DefaultPrefersLatestTag confirms that a bare `qvr install foo`
// resolves to the highest semver tag when tags are present on the registry.
func TestInstall_DefaultPrefersLatestTag(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "v1.0.0", "v1.1.0", "v2.0.0")
	h.addRegistry(t, "acme", remote)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if result.Version != "v2.0.0" {
		t.Errorf("version = %q, want v2.0.0", result.Version)
	}
	// Worktree path is SHA-keyed, so it won't contain the human ref label.
	// Just check that it points at a real directory holding the skill.
	if _, err := os.Stat(filepath.Join(result.Worktree, "skills", "code-review", "SKILL.md")); err != nil {
		t.Errorf("v2.0.0 worktree missing skill: %v", err)
	}
}

// TestInstall_NoTagsFallsBackToDefaultBranch confirms the fallback path when
// the registry only has branches.
func TestInstall_NoTagsFallsBackToDefaultBranch(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if result.Version != "main" {
		t.Errorf("version = %q, want main", result.Version)
	}
}

// TestInstall_NonSemverTagIgnored confirms non-semver tags don't affect the
// default resolution — a repo with only a tag like "stable" falls back to the
// default branch rather than picking the non-semver tag.
func TestInstall_NonSemverTagIgnored(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "stable")
	h.addRegistry(t, "acme", remote)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if result.Version != "main" {
		t.Errorf("version = %q, want main (non-semver tag should be ignored)", result.Version)
	}
}

// TestInstall_MultipleRefsCoexistOnDisk installs the same skill at two refs in
// sequence and verifies both worktree directories survive on disk. The lock
// file tracks the most recent install (current lock schema only pins one entry
// per skill), but worktrees are keyed by (registry, skill, ref) so the
// underlying isolation is already in place.
func TestInstall_MultipleRefsCoexistOnDisk(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "v1.0.0")
	h.addRegistry(t, "acme", remote)

	mainResult, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@main",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install main: %v", err)
	}

	// Second install at a different ref requires Force after the
	// "silently replaces the lock" footgun was closed (issue #12); this
	// test still exercises that worktrees coexist on disk.
	tagResult, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Force:       true,
	})
	if err != nil {
		t.Fatalf("install v1.0.0: %v", err)
	}

	if _, err := os.Stat(mainResult.Worktree); err != nil {
		t.Errorf("main worktree missing: %v", err)
	}
	if _, err := os.Stat(tagResult.Worktree); err != nil {
		t.Errorf("v1.0.0 worktree missing: %v", err)
	}
	// With SHA-keyed paths, two refs pointing at the *same* commit collapse
	// onto one worktree — that's the shared-cache invariant from #52.
	// Different commits (main HEAD vs v1.0.0 tag at HEAD~1 or wherever)
	// still split. seedRemoteWithTags places the tag at HEAD, so the two
	// install paths can legitimately share. The important property is that
	// both worktree paths exist and the main one wasn't deleted by the
	// second install (the #52 corruption symptom).
	if _, err := os.Stat(mainResult.Worktree); err != nil {
		t.Errorf("main worktree should survive a forced reinstall at a different ref: %v", err)
	}
}

// TestEdit_BranchesFromOriginWhenRemoteExists pins the bug #15 fix. When
// qvr/<user>/<skill> already exists on origin (from a previous `qvr push`
// session), `qvr edit` must check it out at the remote tip instead of
// silently creating a local branch from local HEAD — the pre-fix behaviour
// made `qvr push` correctly non-fast-forward, but the user's natural
// `git push --force` response quietly destroyed upstream commits.
func TestEdit_BranchesFromOriginWhenRemoteExists(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	// Pre-seed origin with qvr/alice/code-review ahead of main by one commit
	// in a different direction. Clone, branch, edit, push.
	seed := filepath.Join(t.TempDir(), "seed")
	sclone, err := gogit.PlainClone(seed, false, &gogit.CloneOptions{URL: remote})
	if err != nil {
		t.Fatalf("clone for seed: %v", err)
	}
	scloneWT, err := sclone.Worktree()
	if err != nil {
		t.Fatalf("seed wt: %v", err)
	}
	seedBranch := plumbing.NewBranchReferenceName("qvr/alice/code-review")
	head, err := sclone.Head()
	if err != nil {
		t.Fatalf("seed head: %v", err)
	}
	if err := sclone.Storer.SetReference(plumbing.NewHashReference(seedBranch, head.Hash())); err != nil {
		t.Fatalf("create seed branch: %v", err)
	}
	if err := scloneWT.Checkout(&gogit.CheckoutOptions{Branch: seedBranch}); err != nil {
		t.Fatalf("checkout seed branch: %v", err)
	}
	divergentFile := filepath.Join(seed, "skills", "code-review", "UPSTREAM.md")
	if err := os.WriteFile(divergentFile, []byte("from a past push\n"), 0o644); err != nil {
		t.Fatalf("write divergent: %v", err)
	}
	if _, err := scloneWT.Add(filepath.Join("skills", "code-review", "UPSTREAM.md")); err != nil {
		t.Fatalf("add divergent: %v", err)
	}
	upstreamCommit, err := scloneWT.Commit("upstream edit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Alice", Email: "a@a", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit divergent: %v", err)
	}
	if err := sclone.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitcfg.RefSpec{"refs/heads/qvr/alice/code-review:refs/heads/qvr/alice/code-review"},
	}); err != nil {
		t.Fatalf("push divergent: %v", err)
	}

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@main",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	mainCommit := entry.Commit

	syncer := newSyncer()
	updated, warning, err := syncer.CreateEditBranch(context.Background(), entry, "qvr/alice/code-review")
	if err != nil {
		t.Fatalf("create edit branch: %v", err)
	}
	if warning == "" {
		t.Error("expected warning that branch already exists on origin")
	}
	if updated.Commit != upstreamCommit.String() {
		t.Errorf("commit = %s, want upstream %s (main was %s)",
			updated.Commit, upstreamCommit.String(), mainCommit)
	}
}

// TestEdit_CreatesLocalBranchFromHEAD installs at a tag and then branches off
// via Syncer.CreateEditBranch, asserting the worktree HEAD, lock entry, and
// symlink all move to the new branch atomically.
func TestEdit_CreatesLocalBranchFromHEAD(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "v1.0.0")
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	tagCommit := entry.Commit
	installPath := skill.EntryWorktreePath(entry)

	syncer := newSyncer()
	updated, _, err := syncer.CreateEditBranch(context.Background(), entry, "qvr/alice/code-review")
	if err != nil {
		t.Fatalf("create edit branch: %v", err)
	}
	if err := skill.ApplySwitch(updated, h.project, false); err != nil {
		t.Fatalf("apply switch: %v", err)
	}

	if updated.Ref != "qvr/alice/code-review" {
		t.Errorf("branch = %q, want qvr/alice/code-review", updated.Ref)
	}
	if updated.Commit != tagCommit {
		t.Errorf("commit changed: was %s, now %s", tagCommit, updated.Commit)
	}

	// Worktree path is SHA-keyed and the edit didn't change the commit, so
	// the on-disk path is unchanged from install — only the in-worktree HEAD
	// label and the lock entry's Ref move.
	if skill.EntryWorktreePath(updated) != installPath {
		t.Errorf("worktree path drifted: was %q, now %q", installPath, skill.EntryWorktreePath(updated))
	}
	if _, err := os.Stat(skill.EntryWorktreePath(updated)); err != nil {
		t.Errorf("new worktree path does not exist: %v", err)
	}

	symlink := filepath.Join(h.project, ".claude/skills/code-review")
	target, err := os.Readlink(symlink)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if !strings.HasPrefix(target, skill.EntryWorktreePath(updated)) {
		t.Errorf("symlink = %q, expected to point inside %q", target, skill.EntryWorktreePath(updated))
	}

	// Confirm the worktree's git HEAD actually moved to the new branch.
	repo, err := gogit.PlainOpen(skill.EntryWorktreePath(updated))
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Name().Short() != "qvr/alice/code-review" {
		t.Errorf("worktree HEAD = %q, want qvr/alice/code-review", head.Name().Short())
	}
}

// TestPush_AfterEdit_PushesNewBranch exercises the full edit → modify → push
// flow and verifies the new branch lands on the bare upstream.
func TestPush_AfterEdit_PushesNewBranch(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "v1.0.0")
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}

	syncer := newSyncer()
	updated, _, err := syncer.CreateEditBranch(context.Background(), entry, "qvr/alice/code-review")
	if err != nil {
		t.Fatalf("create edit branch: %v", err)
	}
	if err := skill.ApplySwitch(updated, h.project, false); err != nil {
		t.Fatalf("apply switch: %v", err)
	}

	// Modify via symlink.
	linkPath := filepath.Join(h.project, ".claude/skills/code-review", "SKILL.md")
	original, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("read via symlink: %v", err)
	}
	if err := os.WriteFile(linkPath, append(original, []byte("\n## Edit\n")...), 0o644); err != nil {
		t.Fatalf("write via symlink: %v", err)
	}

	hash, err := syncer.Push(context.Background(), updated, skill.PushOptions{
		Message: "edit",
		Author:  "Test",
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if len(hash) != 40 {
		t.Fatalf("push hash: %q", hash)
	}

	// The new branch should now exist on the bare remote.
	rr, err := gogit.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	if _, err := rr.Reference(plumbing.NewBranchReferenceName("qvr/alice/code-review"), false); err != nil {
		t.Errorf("new branch not present on remote: %v", err)
	}
}

// TestEdit_ReentryAfterPushAndSwitch pins bug #21 (regression of #15). After
// `qvr edit` → `qvr push` → `qvr switch <default>`, re-running `qvr edit`
// without --branch must succeed: the default `qvr/<user>/<skill>` branch
// exists both on origin and locally, and a naive CreateBranchFromRef would
// reject it with "branch already exists". The fix adopts the existing local
// branch instead.
func TestEdit_ReentryAfterPushAndSwitch(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@main",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}

	syncer := newSyncer()

	// Step 1: edit → creates local branch at HEAD, origin still clean.
	editBranch := "qvr/alice/code-review"
	updated, _, err := syncer.CreateEditBranch(context.Background(), entry, editBranch)
	if err != nil {
		t.Fatalf("first edit: %v", err)
	}
	if err := skill.ApplySwitch(updated, h.project, false); err != nil {
		t.Fatalf("apply switch (edit): %v", err)
	}
	lock.Put(updated)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock after edit: %v", err)
	}

	// Step 2: push → origin now has qvr/alice/code-review.
	linkPath := filepath.Join(h.project, ".claude/skills/code-review", "SKILL.md")
	body, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("read via symlink: %v", err)
	}
	if err := os.WriteFile(linkPath, append(body, []byte("\n## First edit\n")...), 0o644); err != nil {
		t.Fatalf("write via symlink: %v", err)
	}
	if _, err := syncer.Push(context.Background(), updated, skill.PushOptions{
		Message: "first edit",
		Author:  "Test",
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Refresh the lock entry — Push rewrites commit/UpdatedAt.
	lock, err = model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("reread lock after push: %v", err)
	}
	entry, err = lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get after push: %v", err)
	}

	// Step 3: switch back to main — simulates the user returning to the
	// default ref between edit sessions.
	switched, err := syncer.Switch(context.Background(), entry, "main")
	if err != nil {
		t.Fatalf("switch to main: %v", err)
	}
	if err := skill.ApplySwitch(switched, h.project, false); err != nil {
		t.Fatalf("apply switch (main): %v", err)
	}
	lock.Put(switched)
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock after switch: %v", err)
	}

	// Step 4: edit again without --branch. This is the regression — it
	// previously hard-errored with "branch already exists".
	reEntered, warning, err := syncer.CreateEditBranch(context.Background(), switched, editBranch)
	if err != nil {
		t.Fatalf("re-entry edit: %v", err)
	}
	if reEntered.Ref != editBranch {
		t.Errorf("branch = %q, want %q", reEntered.Ref, editBranch)
	}
	if warning == "" {
		t.Error("expected a non-empty warning noting the branch already exists")
	}

	if err := skill.ApplySwitch(reEntered, h.project, false); err != nil {
		t.Fatalf("apply switch (re-entry): %v", err)
	}

	// Confirm the worktree HEAD is back on the edit branch.
	repo, err := gogit.PlainOpen(skill.EntryWorktreePath(reEntered))
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Name().Short() != editBranch {
		t.Errorf("worktree HEAD = %q, want %q", head.Name().Short(), editBranch)
	}
}

// TestPublish_WithTag_CreatesAndPushesTag runs Publisher with a --tag and then
// verifies the tag appears on the bare remote after the publish fetch.
func TestPublish_WithTag_CreatesAndPushesTag(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	// Prepare an updated local skill to publish.
	local := writeLocalSkill(t, "code-review", "updated description")

	pub := skill.NewPublisher(git.NewGoGitClient())
	result, err := pub.Publish(context.Background(), skill.PublishRequest{
		LocalPath: local,
		Registry:  "acme",
		Branch:    "main",
		Tag:       "v2.0.0",
		Message:   "release v2",
		Author:    "Test",
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if result.Tag != "v2.0.0" {
		t.Errorf("result.Tag = %q, want v2.0.0", result.Tag)
	}

	tags, err := git.NewGoGitClient().ListTags(registry.RegistryPath("acme"))
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	var found bool
	for _, tag := range tags {
		if tag.Name == "v2.0.0" {
			found = true
			if tag.Hash == "" {
				t.Errorf("tag v2.0.0 has empty hash")
			}
			break
		}
	}
	if !found {
		t.Errorf("tag v2.0.0 not in registry after publish; got %+v", tags)
	}
}

// TestUpgrade_FollowsLatestTag installs at v1.0.0, adds a v2.0.0 tag to the
// remote, refreshes the registry, then runs the upgrade logic (Syncer.Switch +
// ApplySwitch) and verifies the worktree moved.
func TestUpgrade_FollowsLatestTag(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{
		"code-review": codeReviewSkill,
	}, "v1.0.0")
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v1.0.0",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	// Add a v2.0.0 tag to the bare remote, then refresh the registry cache.
	addTagToBareRemote(t, remote, "v2.0.0")
	if _, err := h.manager.Update(context.Background(), "acme"); err != nil {
		t.Fatalf("registry update: %v", err)
	}

	loc, err := h.manager.FindSkill("code-review")
	if err != nil {
		t.Fatalf("find skill: %v", err)
	}
	latest := skill.LatestSemverTag(loc.Entry.Versions.Tags)
	if latest != "v2.0.0" {
		t.Fatalf("LatestSemverTag = %q, want v2.0.0", latest)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	// Upgrade is now Install(Force=true) at the new ref — see cmd/upgrade.go.
	// That creates a fresh SHA-keyed worktree and leaves the previous one in
	// place for other projects pinned to it.
	_ = entry
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@" + latest,
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
		Force:       true,
	}); err != nil {
		t.Fatalf("upgrade install: %v", err)
	}
	lock2, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("re-read lock: %v", err)
	}
	updated, err := lock2.Get("code-review")
	if err != nil {
		t.Fatalf("lock get after upgrade: %v", err)
	}

	if updated.Ref != "v2.0.0" {
		t.Errorf("branch = %q, want v2.0.0", updated.Ref)
	}
	if _, err := os.Stat(skill.EntryWorktreePath(updated)); err != nil {
		t.Errorf("new worktree missing: %v", err)
	}
}

// TestUpgrade_NoTags_Errors covers the error path when LatestSemverTag returns
// empty — the command layer uses this signal to print "no semver tags found".
func TestUpgrade_NoTags_Errors(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"code-review": codeReviewSkill,
	})
	h.addRegistry(t, "acme", remote)

	loc, err := h.manager.FindSkill("code-review")
	if err != nil {
		t.Fatalf("find skill: %v", err)
	}
	if got := skill.LatestSemverTag(loc.Entry.Versions.Tags); got != "" {
		t.Errorf("LatestSemverTag = %q, want empty for no-tag registry", got)
	}
}

// addTagToBareRemote writes a lightweight tag ref into a bare repo pointing at
// the current main HEAD. Used to simulate a new release between an install and
// an upgrade.
func addTagToBareRemote(t *testing.T, barePath, tag string) {
	t.Helper()
	repo, err := gogit.PlainOpen(barePath)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	head, err := repo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewTagReferenceName(tag), head.Hash(),
	)); err != nil {
		t.Fatalf("set tag: %v", err)
	}
}
