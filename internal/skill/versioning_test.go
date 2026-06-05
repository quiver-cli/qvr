package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
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
		Force:     true, // overwriting an existing same-name skill (issue #72)
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	// Greenfield publish lands the skill nested under skills/<name>/, so the
	// version tag is namespaced per skill (#152).
	if result.Tag != "code-review/v2.0.0" {
		t.Errorf("result.Tag = %q, want code-review/v2.0.0 (per-skill namespaced)", result.Tag)
	}

	tags, err := git.NewGoGitClient().ListTags(registry.RegistryPath("acme"))
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	var found bool
	for _, tag := range tags {
		if tag.Name == "code-review/v2.0.0" {
			found = true
			if tag.Hash == "" {
				t.Errorf("tag code-review/v2.0.0 has empty hash")
			}
			break
		}
	}
	if !found {
		t.Errorf("tag code-review/v2.0.0 not in registry after publish; got %+v", tags)
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

// TestInstall_SkipsTagWhoseCommitMissesSkillPath is the regression for
// issue #100: a fork registry frequently carries an older tag (e.g., v0.2.0)
// that points at a commit BEFORE the skill was added. The cached skill
// `path` reflects the current HEAD's tree, but the version resolver was
// preferring the latest semver tag — so `qvr add <skill>` checked out the
// older tag, sparse-checkout found nothing under the cached path, and the
// install failed with "load staged skill: stat skill dir: no such file or
// directory" while the staging worktree dir got created and immediately
// cleaned up.
//
// The fix walks semver tags newest-first and skips any whose commit doesn't
// contain `<path>/SKILL.md`. The test seeds a bare remote whose HEAD adds
// `skills/code-review/SKILL.md` AFTER the v0.2.0 tag was already planted at
// an earlier commit. Pre-fix: install errors out with the staging-dir stat
// failure. Post-fix: install picks the default branch (main) and succeeds.
func TestInstall_SkipsTagWhoseCommitMissesSkillPath(t *testing.T) {
	h := newHarness(t)

	// Seed a bare remote with: C0 = empty (just a README) tagged v0.2.0;
	// C1 = adds skills/code-review/SKILL.md on main. Mirrors a fork that
	// got tagged before its first skill was published.
	remote := seedRemoteWithTaggedAndPostTagSkill(t, "v0.2.0", "code-review", codeReviewSkill)
	h.addRegistry(t, "fork", remote)

	result, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	// Resolver should have skipped v0.2.0 (commit lacks skills/code-review)
	// and fallen through to the default branch.
	if result.Version != "main" {
		t.Errorf("version = %q, want main (v0.2.0's commit doesn't contain the skill)", result.Version)
	}
	// And the worktree must actually hold the SKILL.md the resolver promised.
	if _, statErr := os.Stat(filepath.Join(result.Worktree, "skills", "code-review", "SKILL.md")); statErr != nil {
		t.Errorf("worktree missing skill after install: %v", statErr)
	}
}

// seedRemoteWithTaggedAndPostTagSkill builds a bare remote with two commits:
//
//	C0 — empty fork (only a README), tagged with tagName.
//	C1 — adds skills/<skillName>/SKILL.md on main.
//
// Returns the remote (bare repo) path. The tag stays pinned at C0 even
// though main advanced — exactly the shape that triggered #100 for users
// who tagged a fork before publishing into it.
func seedRemoteWithTaggedAndPostTagSkill(t *testing.T, tagName, skillName, skillContent string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := gogit.PlainInit(remote, true); err != nil {
		t.Fatalf("init remote: %v", err)
	}

	seed := t.TempDir()
	sr, err := gogit.PlainInit(seed, false)
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if _, err := sr.CreateRemote(&gogitcfg.RemoteConfig{
		Name: "origin",
		URLs: []string{remote},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}
	wt, err := sr.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	// C0: README only. The tag will live here.
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# fork\n"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add readme: %v", err)
	}
	c0, err := wt.Commit("init fork", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit C0: %v", err)
	}
	if err := sr.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewTagReferenceName(tagName), c0,
	)); err != nil {
		t.Fatalf("set tag at C0: %v", err)
	}

	// C1: add the skill. Main advances; tag stays at C0.
	skillDir := filepath.Join(seed, "skills", skillName)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", skillName, "SKILL.md")); err != nil {
		t.Fatalf("add skill: %v", err)
	}
	c1, err := wt.Commit("add "+skillName, &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit C1: %v", err)
	}
	if err := sr.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), c1,
	)); err != nil {
		t.Fatalf("set main at C1: %v", err)
	}

	if err := sr.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs: []gogitcfg.RefSpec{
			"refs/heads/main:refs/heads/main",
			gogitcfg.RefSpec("refs/tags/" + tagName + ":refs/tags/" + tagName),
		},
	}); err != nil {
		t.Fatalf("push seed: %v", err)
	}

	rr, err := gogit.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	if err := rr.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main"),
	)); err != nil {
		t.Fatalf("set remote HEAD: %v", err)
	}
	return remote
}
