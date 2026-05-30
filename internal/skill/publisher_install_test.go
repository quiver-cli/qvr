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
	plumbingPkg "github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// ejectedFixture sets up an end-to-end edit-mode state for publish tests:
// a fake shared worktree, an EjectToTarget run that copies it into a project
// dir, and the resulting lock entry. Returns the entry, the project root, and
// the canonical edit dir path. After this the entry is in Mode == "edit"
// with EditPath populated and a fresh git history at the canonical dir.
func ejectedFixture(t *testing.T, name string) (*model.LockEntry, string, string) {
	t.Helper()
	entry := seedSharedWorktreeForEject(t, name, "raks")
	projectRoot := t.TempDir()
	if _, err := skill.EjectToTarget(skill.EjectRequest{Entry: entry, ProjectRoot: projectRoot}); err != nil {
		t.Fatalf("eject: %v", err)
	}
	editDir := filepath.Join(projectRoot, entry.EditPath)
	return entry, projectRoot, editDir
}

// TestPublishInstalled_DryRun_ReportsRemote covers the basic "publish from
// an edit dir to its recorded Source" path in dry-run mode. We don't need
// a real remote because dry-run never pushes — we just want to confirm the
// dispatcher picked the right URL and branch.
func TestPublishInstalled_DryRun_ReportsRemote(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")

	p := skill.NewPublisher(git.NewGoGitClient())
	res, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		DryRun:      true,
		Tag:         "v0.1.0",
	})
	if err != nil {
		t.Fatalf("PublishInstalled: %v", err)
	}
	if !res.DryRun {
		t.Errorf("DryRun = false, want true")
	}
	if res.Remote != "git@example.com:raks.git" {
		t.Errorf("Remote = %q, want git@example.com:raks.git", res.Remote)
	}
	if res.Tag != "v0.1.0" {
		t.Errorf("Tag = %q, want v0.1.0", res.Tag)
	}
	// Lock entry must NOT have been mutated by a dry run.
	if entry.Mode != model.ModeEdit {
		t.Errorf("entry.Mode changed during dry-run: %q", entry.Mode)
	}
}

// TestPublishInstalled_DryRun_AgreesWithRealPublishOnBranch covers the
// user-reported divergence in dry-run branch reporting: pre-fix, the
// dry-run path resolved branch as `--branch → entry.Ref → "main"` while
// the real path (post-#95) resolved as `--branch → stage HEAD → remote
// symref → entry.Ref → "main"`. With entry.Ref = "v0.2.0" and the
// remote's HEAD symref = main, dry-run printed "would publish ...@v0.2.0"
// but the real publish landed on @main — silently misleading.
//
// Fix: dry-run now also consults remote symref (one cheap ls-remote
// --symref round-trip; still no clone). This test pins that dry-run and
// real-run return the same Branch for a fork-to-populated-remote whose
// HEAD differs from entry.Ref.
func TestPublishInstalled_DryRun_AgreesWithRealPublishOnBranch(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	// Stale ref label — what the lock would carry after a tagged install
	// that pinned to v0.2.0; the remote's HEAD points elsewhere.
	entry.Ref = "v0.2.0"

	// Stand up a populated bare with HEAD → refs/heads/main, then snapshot
	// the dry-run branch BEFORE running the real publish (which mutates
	// the entry, advances commits, etc.).
	forkURL := setupBareForkWithHEAD(t, "main")

	p := skill.NewPublisher(git.NewGoGitClient())
	dryRes, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("PublishInstalled dry-run: %v", err)
	}
	if dryRes.Branch == "v0.2.0" {
		t.Fatalf("dry-run reported Branch=%q (entry.Ref) — should consult remote symref (issue: dry-run vs real-run divergence)", dryRes.Branch)
	}
	if dryRes.Branch != "main" {
		t.Errorf("dry-run Branch = %q, want %q (from remote HEAD symref)", dryRes.Branch, "main")
	}

	realRes, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Message:     "real publish after dry-run",
	})
	if err != nil {
		t.Fatalf("PublishInstalled real: %v", err)
	}
	if dryRes.Branch != realRes.Branch {
		t.Errorf("dry-run vs real-run branch divergence: dry=%q real=%q — dry-run misleads the user about what real publish will do",
			dryRes.Branch, realRes.Branch)
	}
}

// setupBareForkWithHEAD seeds a bare repo at a tempdir with one commit
// on branch <name> and HEAD → refs/heads/<name>. Used as a populated
// fork destination for branch-resolution tests where we need a non-empty
// remote with a known symref.
func setupBareForkWithHEAD(t *testing.T, branch string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), "fork.git")
	bareRepo, err := gogit.PlainInit(bare, true)
	if err != nil {
		t.Fatalf("init bare: %v", err)
	}

	seed := t.TempDir()
	seedRepo, err := gogit.PlainInit(seed, false)
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if _, err := seedRepo.CreateRemote(&gogitcfg.RemoteConfig{
		Name: "origin",
		URLs: []string{bare},
	}); err != nil {
		t.Fatalf("set origin: %v", err)
	}
	seedWt, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("seed worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("# fork seed\n"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	if _, err := seedWt.Add("README.md"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	if _, err := seedWt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t", When: time.Now()},
	}); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	// Rename local branch to <branch> before pushing so the pushed ref
	// matches what we want as HEAD on the bare.
	head, err := seedRepo.Head()
	if err != nil {
		t.Fatalf("seed head: %v", err)
	}
	if err := seedRepo.Storer.SetReference(plumbingPkg.NewHashReference(
		plumbingPkg.NewBranchReferenceName(branch), head.Hash(),
	)); err != nil {
		t.Fatalf("set %s ref: %v", branch, err)
	}
	if err := seedRepo.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitcfg.RefSpec{gogitcfg.RefSpec("refs/heads/" + branch + ":refs/heads/" + branch)},
	}); err != nil {
		t.Fatalf("push seed → bare: %v", err)
	}
	if err := bareRepo.Storer.SetReference(plumbingPkg.NewSymbolicReference(
		plumbingPkg.HEAD, plumbingPkg.NewBranchReferenceName(branch),
	)); err != nil {
		t.Fatalf("set bare HEAD symref: %v", err)
	}
	return bare
}

// TestPublishInstalled_Fork_StampsForkedFrom verifies that --fork writes
// `forked-from: <upstream>@<sha>` into the SKILL.md that lands on the fork
// remote. Post-#98 the stamp lives in the stage clone, never in the user's
// eject dir — so we verify the *published* artifact carries it, and
// separately assert the eject dir stays clean.
func TestPublishInstalled_Fork_StampsForkedFrom(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")
	originalSource := entry.SourceUpstream // captured at eject time

	// Stand up a real bare repo as the fork destination so the push lands.
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	// Snapshot the eject dir's SKILL.md BEFORE publish so we can assert
	// publish didn't touch it (#98 regression guard).
	editSKILLBefore, err := os.ReadFile(filepath.Join(editDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read eject SKILL.md (before): %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err = p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Migrate:     false,
		Tag:         "v0.1.0",
		Message:     "first fork",
	})
	if err != nil {
		t.Fatalf("PublishInstalled: %v", err)
	}

	// #98: eject dir's SKILL.md must NOT have been mutated by qvr's stamp.
	editSKILLAfter, err := os.ReadFile(filepath.Join(editDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read eject SKILL.md (after): %v", err)
	}
	if string(editSKILLBefore) != string(editSKILLAfter) {
		t.Errorf("publish mutated eject SKILL.md (issue #98): before=%q after=%q", editSKILLBefore, editSKILLAfter)
	}

	// The fork's SKILL.md must carry the stamp — that's where the published
	// artifact actually lives.
	forkSKILL := readSKILLFromBareRepo(t, forkURL, "v0.1.0", "SKILL.md")
	if !strings.Contains(forkSKILL, "forked-from:") {
		t.Errorf("fork SKILL.md missing forked-from stamp: %q", forkSKILL)
	}
	if !strings.Contains(forkSKILL, originalSource) {
		t.Errorf("forked-from missing original upstream %q: %q", originalSource, forkSKILL)
	}

	// Without --migrate, the lock entry's Source must NOT have flipped.
	if entry.Source != originalSource {
		t.Errorf("entry.Source = %q, want unchanged %q (Migrate=false)", entry.Source, originalSource)
	}
}

// readSKILLFromBareRepo reads a file from a bare repo at the given tag-or-branch.
// Used to assert what actually landed on the fork after publish, without
// shelling out to `git show`.
func readSKILLFromBareRepo(t *testing.T, bareRepoPath, ref, file string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(bareRepoPath)
	if err != nil {
		t.Fatalf("open bare %s: %v", bareRepoPath, err)
	}
	// Try as a tag first (handles annotated tags), then as a branch.
	var hash plumbingPkg.Hash
	if tagRef, err := repo.Reference(plumbingPkg.NewTagReferenceName(ref), true); err == nil {
		hash = tagRef.Hash()
		if tagObj, err := repo.TagObject(hash); err == nil {
			if commit, err := tagObj.Commit(); err == nil {
				hash = commit.Hash
			}
		}
	} else if branchRef, err := repo.Reference(plumbingPkg.NewBranchReferenceName(ref), true); err == nil {
		hash = branchRef.Hash()
	} else {
		t.Fatalf("ref %q not found in %s", ref, bareRepoPath)
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		t.Fatalf("commit %s: %v", hash, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	f, err := tree.File(file)
	if err != nil {
		t.Fatalf("file %s in commit %s: %v", file, hash, err)
	}
	body, err := f.Contents()
	if err != nil {
		t.Fatalf("contents: %v", err)
	}
	return body
}

// TestPublishInstalled_ForkMigrate_RewritesSource covers the same --fork
// flow with Migrate=true: after the successful push, entry.Source flips to
// the fork URL and entry.SourceUpstream preserves the original.
func TestPublishInstalled_ForkMigrate_RewritesSource(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	originalSource := entry.SourceUpstream

	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	res, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Migrate:     true,
		Tag:         "v0.1.0",
		AutoCommit:  true, // forked-from stamping dirties SKILL.md; explicit opt-in (issue #83)
	})
	if err != nil {
		t.Fatalf("PublishInstalled: %v", err)
	}
	if !res.Migrated {
		t.Errorf("Migrated = false, want true")
	}
	if entry.Source != forkURL {
		t.Errorf("entry.Source = %q, want fork URL %q", entry.Source, forkURL)
	}
	if entry.SourceUpstream != originalSource {
		t.Errorf("entry.SourceUpstream = %q, want preserved original %q", entry.SourceUpstream, originalSource)
	}
}

// TestPublishInstalled_RefusesDirtyWithoutAutoCommit is the regression guard
// for issue #83: PublishInstalled used to silently `git add . && git commit`
// any uncommitted edits in the eject dir, surprising users whose WIP debug
// notes / secrets ended up on the remote. Default now refuses; --auto-commit
// opts back into the old behavior.
func TestPublishInstalled_RefusesDirtyWithoutAutoCommit(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}
	// Make the edit dir dirty: add an uncommitted change that publish would
	// otherwise silently auto-commit.
	if err := os.WriteFile(filepath.Join(editDir, "WIP-debug.md"),
		[]byte("# WIP debug notes — do not ship\n"), 0o644); err != nil {
		t.Fatalf("write WIP: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Message:     "test",
		// AutoCommit defaults to false — should refuse.
	})
	if err == nil {
		t.Fatalf("expected refusal on dirty WD without --auto-commit, got nil")
	}
	if !strings.Contains(err.Error(), "auto-commit") {
		t.Errorf("error should mention --auto-commit, got %v", err)
	}
}

// TestPublishInstalled_MigrateClearsRegistry verifies issue #85: after
// `--fork --migrate`, the entry's Registry field is cleared (the v5 lock
// is self-contained by Source URL alone, so a stale Registry pointer is
// worse than no pointer).
func TestPublishInstalled_MigrateClearsRegistry(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	entry.Registry = "original-registry"
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	if _, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Migrate:     true,
		AutoCommit:  true,
		Tag:         "v0.1.0",
	}); err != nil {
		t.Fatalf("PublishInstalled: %v", err)
	}
	if entry.Registry != "" {
		t.Errorf("entry.Registry = %q, want empty after --migrate (issue #85)", entry.Registry)
	}
}

// TestPublishInstalled_Fork_CleanWD_NoAutoCommit covers issue #98: when the
// user's eject dir is clean and they pass --fork without --auto-commit, the
// publish must succeed. qvr's own forked-from stamp lands in the stage
// clone — never in the user's eject dir — so the guard meant for user WIP
// has nothing to complain about. The stamp ships in the publish commit on
// the fork; the eject dir's SKILL.md stays byte-identical.
func TestPublishInstalled_Fork_CleanWD_NoAutoCommit(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	editSKILLBefore, err := os.ReadFile(filepath.Join(editDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read eject SKILL.md (before): %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	res, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Message:     "first fork",
		// AutoCommit deliberately false — WD is clean and stays that way.
	})
	if err != nil {
		t.Fatalf("PublishInstalled with clean WD and no --auto-commit: %v", err)
	}

	editSKILLAfter, err := os.ReadFile(filepath.Join(editDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read eject SKILL.md (after): %v", err)
	}
	if string(editSKILLBefore) != string(editSKILLAfter) {
		t.Errorf("publish mutated eject SKILL.md (issue #98): before=%q after=%q", editSKILLBefore, editSKILLAfter)
	}

	// The stamp lives on the fork. Read the just-pushed branch and confirm.
	forkSKILL := readSKILLFromBareRepo(t, forkURL, res.Branch, "SKILL.md")
	if !strings.Contains(forkSKILL, "forked-from:") {
		t.Errorf("fork SKILL.md missing forked-from stamp after publish: %q", forkSKILL)
	}
}

// TestPublishInstalled_Fork_EmptyRemote_PushesNotNoop covers issue #97:
// publishing to a fork whose branch is empty must not short-circuit as
// "Nothing to publish". The decision has to be based on the remote's actual
// state, not the local WD's cleanliness — otherwise an empty fork is
// silently ignored.
func TestPublishInstalled_Fork_EmptyRemote_PushesNotNoop(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	// Bare repo with no refs — simulates a brand-new fork.
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	res, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Message:     "first publish to empty fork",
	})
	if err != nil {
		t.Fatalf("PublishInstalled to empty fork: %v", err)
	}
	if res.NothingToPublish {
		t.Errorf("NothingToPublish=true for an empty fork (issue #97); want false")
	}
	// Verify the fork now actually has refs/heads/<branch> populated.
	forkRepo, err := gogit.PlainOpen(forkURL)
	if err != nil {
		t.Fatalf("open fork: %v", err)
	}
	refs, err := forkRepo.References()
	if err != nil {
		t.Fatalf("refs: %v", err)
	}
	var foundBranch bool
	_ = refs.ForEach(func(r *plumbingPkg.Reference) error {
		if r.Name().IsBranch() {
			foundBranch = true
		}
		return nil
	})
	if !foundBranch {
		t.Errorf("fork has no branch refs after publish — push didn't actually land")
	}
}

// TestPublishInstalled_ExplicitBranch_RemapsRefspec covers issue #95 part 2:
// passing --branch main when the local eject dir only has a "master" branch
// must succeed. The push refspec uses HEAD (not refs/heads/<name>) for root
// layout, so the local branch name doesn't have to match the target.
func TestPublishInstalled_ExplicitBranch_RemapsRefspec(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	res, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Branch:      "main", // local is "master"; this used to fail with "src refspec ... does not match any"
		Message:     "explicit main",
	})
	if err != nil {
		t.Fatalf("PublishInstalled --branch main on master-local: %v", err)
	}
	if res.Branch != "main" {
		t.Errorf("result.Branch = %q, want main", res.Branch)
	}
	// Confirm refs/heads/main landed on the fork.
	forkRepo, err := gogit.PlainOpen(forkURL)
	if err != nil {
		t.Fatalf("open fork: %v", err)
	}
	if _, err := forkRepo.Reference(plumbingPkg.NewBranchReferenceName("main"), true); err != nil {
		t.Errorf("fork missing refs/heads/main after publish: %v", err)
	}
}

// TestPublishInstalled_PushFailure_PropagatesError covers issue #94: a
// failed git push must return a non-nil error so the cmd-level RunE exits
// non-zero. Regression guard against the 0.7.0 path that printed the push
// error to stderr but still exited 0 (CI scripts ran the next step).
func TestPublishInstalled_PushFailure_PropagatesError(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	// Force the publisher to attempt a push to a remote that cannot exist:
	// /this/path/does/not/exist isn't writeable and isn't a bare repo.
	bogusFork := filepath.Join(t.TempDir(), "definitely-not-a-repo")

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     bogusFork,
		Message:     "doomed publish",
	})
	if err == nil {
		t.Fatal("PublishInstalled returned nil error on push to bogus URL — issue #94 regression")
	}
}

// TestPublishInstalled_RefusesNonEdit covers the guard rail: you can't
// publish a shared-mode install directly. Users have to `qvr edit` first
// so the consolidation rule is intentional, not accidental.
func TestPublishInstalled_RefusesNonEdit(t *testing.T) {
	entry := seedSharedWorktreeForEject(t, "demo", "raks") // shared, never ejected
	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: t.TempDir(),
		DryRun:      true,
	})
	if err == nil {
		t.Fatalf("expected error publishing a non-edit-mode entry, got nil")
	}
}
