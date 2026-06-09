package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/skill"
)

// TestPublishInstalled_UserCommitInEjectDir_NoHealFlagNeeded is the
// end-to-end guard for issue #99: after the user runs `git commit` inside
// their eject dir, the lockfile's commit field still points at the eject
// base. The publish path's silent-heal must kick in (no --allow-lockfile-heal
// needed) because the new HEAD is a legitimate descendant of the recorded
// commit. Combined with EntryCommitIsAncestorOfHead's shell-out switch to
// `git merge-base --is-ancestor`, this exercises the full real-world flow
// the user complained about in #99.
func TestPublishInstalled_UserCommitInEjectDir_NoHealFlagNeeded(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")

	// Seed entry.Commit to the eject-base SHA, mirroring what `qvr edit` writes.
	editRepo, err := gogit.PlainOpen(editDir)
	if err != nil {
		t.Fatalf("open edit repo: %v", err)
	}
	editHead, err := editRepo.Head()
	if err != nil {
		t.Fatalf("edit head: %v", err)
	}
	entry.Commit = editHead.Hash().String()

	// Stand up a fork to publish to.
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	// User makes a real commit in the eject dir — this is the #99 setup.
	if err := os.WriteFile(filepath.Join(editDir, "extra.md"), []byte("real user edit\n"), 0o644); err != nil {
		t.Fatalf("write extra: %v", err)
	}
	wt, err := editRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("stage user edit: %v", err)
	}
	if _, err := wt.Commit("user real edit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "u", Email: "u@e", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit user edit: %v", err)
	}

	// Sanity: lockfile commit now lags HEAD, but HEAD descends from it.
	newHead, err := editRepo.Head()
	if err != nil {
		t.Fatalf("new head: %v", err)
	}
	if newHead.Hash().String() == entry.Commit {
		t.Fatal("setup failed — HEAD didn't advance past entry.Commit")
	}
	ok, err := skill.EntryCommitIsAncestorOfHead(entry, projectRoot)
	if err != nil {
		t.Fatalf("EntryCommitIsAncestorOfHead: %v", err)
	}
	if !ok {
		t.Fatal("ancestor=false — silent heal path won't fire, #99 still broken")
	}

	// Publish without --allow-lockfile-heal. With the shell-out ancestor
	// check, this should silently heal and succeed.
	p := skill.NewPublisher(git.NewGoGitClient())
	res, perr := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Message:     "user edit publish",
	})
	if perr != nil {
		t.Fatalf("PublishInstalled (silent heal): %v — issue #99 regression", perr)
	}
	if res.NothingToPublish {
		t.Errorf("NothingToPublish=true; want a real publish to land")
	}
	// Post-publish, entry.Commit must track the eject dir's HEAD (the
	// snapshot we just published).
	if entry.Commit != newHead.Hash().String() {
		t.Errorf("entry.Commit = %s, want eject HEAD %s", entry.Commit, newHead.Hash().String())
	}
}

// TestPublishInstalled_SecondReleaseOnMigratedFork_RootLayoutPreserved is the
// regression guard for issue #155: the iterate-and-release loop on a migrated
// fork (`qvr edit` → edit → `qvr publish --tag vN`) died on the SECOND release
// with "commit: reference not found".
//
// Root cause: the first publish used --fork → layout "root", so the fork
// stores the skill at the repo root and the lock entry's Path is ".". The
// second publish omits --fork, so layout defaulted to "nested"; the nested
// destination then resolved to stageDir + "." == stageDir, and the nested
// cleanup `os.RemoveAll(contentDest)` deleted the stage clone's .git/, so the
// follow-up commit failed against a bricked repo. The fix defaults a
// root-layout entry (Path "." / "") back to "root".
func TestPublishInstalled_SecondReleaseOnMigratedFork_RootLayoutPreserved(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")

	editRepo, err := gogit.PlainOpen(editDir)
	if err != nil {
		t.Fatalf("open edit repo: %v", err)
	}
	if head, herr := editRepo.Head(); herr == nil {
		entry.Commit = head.Hash().String()
	}

	// A real (non-empty after first push) bare fork to release against.
	forkURL := filepath.Join(t.TempDir(), "fork.git")
	if _, err := gogit.PlainInit(forkURL, true); err != nil {
		t.Fatalf("init fork bare: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())

	// First release: --fork --migrate cuts v0.1.0 at root layout and flips
	// the entry to track the fork (Source → fork, Registry cleared).
	if _, perr := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     forkURL,
		Migrate:     true,
		Tag:         "v0.1.0",
		Message:     "v0.1.0",
	}); perr != nil {
		t.Fatalf("first publish (--fork --migrate): %v", perr)
	}

	// Mirror the consume-mode reinstall that auto-uneject performs: a
	// root-layout fork install records Path ".". (PublishInstalled itself
	// doesn't un-eject — that's the cmd layer — so set it explicitly.)
	entry.Path = "."

	// Make a real edit so the second release has content to commit (the
	// stage goes dirty, exercising the commit path that pre-fix hit the
	// nuked .git).
	commitEditFile(t, editRepo, editDir, "NEW.md", "second release\n", "second release edit")

	// Second release: NO --fork, NO --layout. Pre-fix this exited with
	// "commit: reference not found"; post-fix it must default to root and
	// cut v0.2.0 cleanly.
	res, perr := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		Tag:         "v0.2.0",
		Message:     "v0.2.0",
	})
	if perr != nil {
		t.Fatalf("second publish on migrated fork: %v — issue #155 regression", perr)
	}
	if res.Layout != "root" {
		t.Errorf("second publish layout = %q, want \"root\" (a Path \".\" entry must stay root-layout)", res.Layout)
	}

	// The v0.2.0 tag must exist on the fork at root layout (SKILL.md + NEW.md
	// at the repo root, not under skills/<name>/).
	assertTagTreeHasRootFiles(t, forkURL, "v0.2.0", "SKILL.md", "NEW.md")
}

// assertTagTreeHasRootFiles opens the bare repo at forkURL and asserts the named
// annotated tag's tree contains each wantFile at the repo root (root layout).
func assertTagTreeHasRootFiles(t *testing.T, forkURL, tag string, wantFiles ...string) {
	t.Helper()
	fork, err := gogit.PlainOpen(forkURL)
	if err != nil {
		t.Fatalf("open fork: %v", err)
	}
	tagRef, err := fork.Tag(tag)
	if err != nil {
		t.Fatalf("%s not on fork: %v", tag, err)
	}
	tagObj, err := fork.TagObject(tagRef.Hash())
	if err != nil {
		t.Fatalf("resolve tag object: %v", err)
	}
	tree, err := tagObj.Tree()
	if err != nil {
		t.Fatalf("tag tree: %v", err)
	}
	for _, f := range wantFiles {
		if _, err := tree.File(f); err != nil {
			t.Errorf("%s not at fork root in %s (root layout broken): %v", f, tag, err)
		}
	}
}

// commitEditFile writes name=content into editDir, stages all changes in
// editRepo, and commits them with msg.
func commitEditFile(t *testing.T, editRepo *gogit.Repository, editDir, name, content, msg string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(editDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	wt, err := editRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("stage edit: %v", err)
	}
	if _, err := wt.Commit(msg, &gogit.CommitOptions{
		Author: &object.Signature{Name: "u", Email: "u@e", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit edit: %v", err)
	}
}

// TestPublishInstalled_TamperedCommit_RefusesAtCmdLayer is the
// end-to-end guard for issue #74 at the publisher level. Because the
// cmd/publish.go integrity check sits outside the publisher, we exercise
// the helper here and assert that a hand-edited SHA is correctly
// classified as "not an ancestor" — which is what triggers the cmd-layer
// refusal. The cmd-layer test in cmd/ covers the refusal message itself.
func TestPublishInstalled_TamperedCommit_RefusesAtCmdLayer(t *testing.T) {
	entry, projectRoot, _ := ejectedFixture(t, "demo")
	entry.Commit = "deadbeef00000000000000000000000000000000"

	ok, err := skill.EntryCommitIsAncestorOfHead(entry, projectRoot)
	if err != nil {
		t.Fatalf("EntryCommitIsAncestorOfHead: %v", err)
	}
	if ok {
		t.Errorf("ancestor=true for tampered SHA — cmd-layer refusal won't fire, #74 still broken")
	}
}

// TestPublishInstalled_EjectDirUnchangedAfterPushFailure is the regression
// guard for issue #86: a failed push must NOT leave a phantom commit in
// the eject dir's history. With the new design (publish stages through a
// temp clone) the eject dir is never touched, so HEAD before and after a
// failed publish must be byte-identical.
func TestPublishInstalled_EjectDirUnchangedAfterPushFailure(t *testing.T) {
	entry, projectRoot, editDir := ejectedFixture(t, "demo")

	editRepo, err := gogit.PlainOpen(editDir)
	if err != nil {
		t.Fatalf("open edit repo: %v", err)
	}
	headBefore, err := editRepo.Head()
	if err != nil {
		t.Fatalf("head before: %v", err)
	}

	// Drive the publish into a guaranteed push failure — bogus fork URL.
	p := skill.NewPublisher(git.NewGoGitClient())
	_, perr := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		ForkURL:     filepath.Join(t.TempDir(), "not-a-repo"),
		Message:     "doomed",
	})
	if perr == nil {
		t.Fatal("PublishInstalled returned nil on bogus fork URL — push didn't fail")
	}
	if !strings.Contains(perr.Error(), "push") && !strings.Contains(perr.Error(), "clone") && !strings.Contains(perr.Error(), "set stage origin") {
		t.Logf("note: error wording changed (%v) — still treated as a real failure", perr)
	}

	headAfter, err := editRepo.Head()
	if err != nil {
		t.Fatalf("head after: %v", err)
	}
	if headBefore.Hash() != headAfter.Hash() {
		t.Errorf("eject HEAD advanced after failed publish (#86 regression):\n  before: %s\n  after:  %s",
			headBefore.Hash(), headAfter.Hash())
	}
}
