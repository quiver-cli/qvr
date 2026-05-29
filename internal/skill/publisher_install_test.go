package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	plumbingPkg "github.com/go-git/go-git/v5/plumbing"

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

// TestPublishInstalled_Fork_StampsForkedFrom verifies that --fork writes
// `forked-from: <upstream>@<sha>` into SKILL.md before the commit. We use
// a real bare-repo remote so the push step actually runs end-to-end —
// regression check that the publish loop doesn't choke when a brand-new
// origin gets set just before the push.
func TestPublishInstalled_Fork_StampsForkedFrom(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")
	originalSource := entry.SourceUpstream // captured at eject time

	// Stand up a real bare repo as the fork destination so the push lands.
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Migrate:     false,
		Tag:         "v0.1.0",
		Message:     "first fork",
		AutoCommit:  true, // forked-from stamping dirties SKILL.md; explicit opt-in (issue #83)
	})
	if err != nil {
		t.Fatalf("PublishInstalled: %v", err)
	}

	// SKILL.md should now have forked-from stamped pointing at the original.
	skillBytes, err := os.ReadFile(filepath.Join(editDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(skillBytes), "forked-from:") {
		t.Errorf("SKILL.md missing forked-from stamp: %q", string(skillBytes))
	}
	if !strings.Contains(string(skillBytes), originalSource) {
		t.Errorf("forked-from missing original upstream %q: %q", originalSource, string(skillBytes))
	}

	// Without --migrate, the lock entry's Source must NOT have flipped.
	if entry.Source != originalSource {
		t.Errorf("entry.Source = %q, want unchanged %q (Migrate=false)", entry.Source, originalSource)
	}
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
// publish must succeed. qvr's own forked-from stamp dirties the working tree,
// but that's qvr's bookkeeping — it should not trip the guard meant for the
// user's WIP. The stamp lands in the publish commit; the eject dir is clean
// before the call and clean after it.
func TestPublishInstalled_Fork_CleanWD_NoAutoCommit(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Message:     "first fork",
		// AutoCommit deliberately false — WD is clean; qvr's stamp is its problem.
	})
	if err != nil {
		t.Fatalf("PublishInstalled with clean WD and no --auto-commit: %v", err)
	}

	// Sanity: the stamp landed in the committed SKILL.md.
	skillBytes, err := os.ReadFile(filepath.Join(editDir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if !strings.Contains(string(skillBytes), "forked-from:") {
		t.Errorf("SKILL.md missing forked-from stamp after publish: %q", string(skillBytes))
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
