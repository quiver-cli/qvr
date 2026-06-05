package git_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"

	"github.com/quiver-cli/qvr/internal/git"
)

func TestWorktree_Add(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{
		"code-review":   "# code-review\n",
		"deploy-helper": "# deploy-helper\n",
	})
	bare := bareCloneFor(t, remote)

	wtPath := filepath.Join(t.TempDir(), "wt")
	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wtPath, ".git")); err != nil {
		t.Errorf(".git not present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "skills", "code-review", "SKILL.md")); err != nil {
		t.Errorf("expected skill file checked out: %v", err)
	}

	// Origin URL must be rewritten to the real upstream, not the bare path.
	repo, err := gogit.PlainOpen(wtPath)
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	rem, err := repo.Remote("origin")
	if err != nil {
		t.Fatalf("remote: %v", err)
	}
	urls := rem.Config().URLs
	if len(urls) == 0 {
		t.Fatalf("origin has no URLs configured")
	}
	if urls[0] != remote {
		t.Errorf("origin URL = %q, want %q", urls[0], remote)
	}

	// HEAD should be on local branch "main".
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if !head.Name().IsBranch() || head.Name().Short() != "main" {
		t.Errorf("expected HEAD on branch main, got %s", head.Name())
	}
}

func TestWorktree_Add_AlreadyExists(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{"code-review": "# x\n"})
	bare := bareCloneFor(t, remote)

	wtPath := filepath.Join(t.TempDir(), "wt")
	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("first Add: %v", err)
	}
	if err := w.Add(bare, wtPath, "main"); !errors.Is(err, git.ErrWorktreeExists) {
		t.Errorf("expected ErrWorktreeExists, got %v", err)
	}
}

// TestWorktree_Add_HardlinksObjectsForDedup verifies content dedup: Add clones
// with `git clone --local`, which hardlinks the bare's object files into the
// worktree instead of copying them — so a worktree object shares an inode with
// the bare (os.SameFile), and identical content across worktrees costs no extra
// disk. Skips (rather than fails) when no hardlink is found, since dedup is a
// best-effort optimization that needs same-filesystem hardlink support.
func TestWorktree_Add_HardlinksObjectsForDedup(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{"code-review": "# code-review\n"})
	bare := bareCloneFor(t, remote)

	wtPath := filepath.Join(t.TempDir(), "wt")
	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	bareObjects := filepath.Join(bare, "objects")
	wtObjects := filepath.Join(wtPath, ".git", "objects")
	shared := false
	_ = filepath.Walk(bareObjects, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(bareObjects, p)
		if rerr != nil {
			return nil
		}
		wtInfo, werr := os.Stat(filepath.Join(wtObjects, rel))
		if werr == nil && os.SameFile(info, wtInfo) {
			shared = true
			return filepath.SkipAll
		}
		return nil
	})
	if !shared {
		t.Skip("no hardlinked object found — filesystem may not support same-FS hardlinks; dedup is best-effort")
	}
}

func TestWorktree_Add_MissingBare(t *testing.T) {
	w := git.NewGoGitWorktree()
	err := w.Add(filepath.Join(t.TempDir(), "nope.git"), filepath.Join(t.TempDir(), "wt"), "main")
	if !errors.Is(err, git.ErrBareNotFound) {
		t.Errorf("expected ErrBareNotFound, got %v", err)
	}
}

func TestWorktree_Checkout_Tag(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{"code-review": "# x\n"})

	// Add a tag on remote.
	remoteRepo, err := gogit.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	head, err := remoteRepo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if _, err := remoteRepo.CreateTag("v1.0", head.Hash(), nil); err != nil {
		t.Fatalf("tag: %v", err)
	}

	bare := bareCloneFor(t, remote)
	wtPath := filepath.Join(t.TempDir(), "wt")
	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "v1.0"); err != nil {
		t.Fatalf("Add at tag: %v", err)
	}

	// Detached HEAD is expected for tag checkouts.
	repo, err := gogit.PlainOpen(wtPath)
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	head2, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head2.Name().IsBranch() {
		t.Errorf("expected detached HEAD at tag, got branch %s", head2.Name())
	}
}

func TestWorktree_Remove(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{"code-review": "# x\n"})
	bare := bareCloneFor(t, remote)
	wtPath := filepath.Join(t.TempDir(), "wt")

	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Remove(wtPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Errorf("worktree still exists after Remove: %v", err)
	}
}

func TestWorktree_Remove_NotFound(t *testing.T) {
	w := git.NewGoGitWorktree()
	err := w.Remove(filepath.Join(t.TempDir(), "ghost"))
	if !errors.Is(err, git.ErrWorktreeNotFound) {
		t.Errorf("expected ErrWorktreeNotFound, got %v", err)
	}
}

func TestWorktree_List(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{"code-review": "# x\n"})
	bare := bareCloneFor(t, remote)
	root := t.TempDir()

	w := git.NewGoGitWorktree()
	if err := w.Add(bare, filepath.Join(root, "a"), "main"); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if err := w.Add(bare, filepath.Join(root, "b"), "main"); err != nil {
		t.Fatalf("Add b: %v", err)
	}
	// Stray file shouldn't crash List.
	_ = os.WriteFile(filepath.Join(root, "stray.txt"), []byte("x"), 0o644)

	wts, err := w.List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(wts) != 2 {
		t.Fatalf("expected 2 worktrees, got %d", len(wts))
	}
	for _, wt := range wts {
		if wt.Branch != "main" {
			t.Errorf("expected branch main, got %q", wt.Branch)
		}
		if len(wt.Commit) != 40 {
			t.Errorf("expected 40-char commit, got %q", wt.Commit)
		}
	}
}

func TestWorktree_List_MissingRoot(t *testing.T) {
	w := git.NewGoGitWorktree()
	wts, err := w.List(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Errorf("expected nil error for missing root, got %v", err)
	}
	if len(wts) != 0 {
		t.Errorf("expected 0 worktrees, got %d", len(wts))
	}
}

func TestWorktree_SetSparseCheckout(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{
		"code-review":   "# x\n",
		"deploy-helper": "# y\n",
	})
	bare := bareCloneFor(t, remote)
	wtPath := filepath.Join(t.TempDir(), "wt")

	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Before trim, both skills exist.
	if _, err := os.Stat(filepath.Join(wtPath, "skills", "deploy-helper")); err != nil {
		t.Fatalf("deploy-helper missing pre-sparse: %v", err)
	}

	if err := w.SetSparseCheckout(wtPath, []string{"skills/code-review"}); err != nil {
		t.Fatalf("SetSparseCheckout: %v", err)
	}

	if _, err := os.Stat(filepath.Join(wtPath, "skills", "code-review", "SKILL.md")); err != nil {
		t.Errorf("kept skill missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "skills", "deploy-helper")); !os.IsNotExist(err) {
		t.Errorf("trimmed skill still present: %v", err)
	}
	// .git must remain untouched.
	if _, err := os.Stat(filepath.Join(wtPath, ".git")); err != nil {
		t.Errorf(".git was trimmed: %v", err)
	}
	// Sparse marker was written.
	if _, err := os.Stat(filepath.Join(wtPath, ".git", "info", "sparse-checkout")); err != nil {
		t.Errorf("sparse marker missing: %v", err)
	}
}

// TestWorktree_ScopeMultiSkillRepo is the #153 scan/install-scope regression:
// in a repo with a root SKILL.md, sibling <name>/SKILL.md skills, and unrelated
// app code (bin/, test/ fixtures), a non-root skill must scope to its own
// subtree and a root-with-siblings skill must scope to SKILL.md + content dirs —
// neither may drag in bin/ or the test/ fixture that carries a "secret".
func TestWorktree_ScopeMultiSkillRepo(t *testing.T) {
	bare := seedBareFromFiles(t, map[string]string{
		"SKILL.md":                "---\nname: root-app\ndescription: root.\n---\n",
		"references/guide.md":     "# guide\n",
		"a/SKILL.md":              "---\nname: a\ndescription: sibling.\n---\n",
		"b/SKILL.md":              "---\nname: b\ndescription: sibling.\n---\n",
		"bin/app.sh":              "#!/bin/sh\necho hi\n",
		"test/fixtures/creds.env": "SECRET=AKIAIOSFODNN7EXAMPLE\n",
	})

	w := git.NewGoGitWorktree()

	// --- non-root sibling skill: only its subtree survives ---
	sibWt := filepath.Join(t.TempDir(), "sib")
	if err := w.Add(bare, sibWt, "main"); err != nil {
		t.Fatalf("Add sibling: %v", err)
	}
	if err := w.SetSparseCheckout(sibWt, []string{"a"}); err != nil {
		t.Fatalf("SetSparseCheckout: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sibWt, "a", "SKILL.md")); err != nil {
		t.Errorf("sibling 'a' SKILL.md missing: %v", err)
	}
	for _, gone := range []string{"b", "bin", "test", "SKILL.md"} {
		if _, err := os.Stat(filepath.Join(sibWt, gone)); !os.IsNotExist(err) {
			t.Errorf("sibling scope leaked %q into scan dir (err=%v)", gone, err)
		}
	}

	// --- root-with-siblings skill: SKILL.md + content dirs, no app code ---
	rootWt := filepath.Join(t.TempDir(), "root")
	if err := w.Add(bare, rootWt, "main"); err != nil {
		t.Fatalf("Add root: %v", err)
	}
	if err := w.SetSparseCheckoutPatterns(rootWt, []string{"SKILL.md", "references", "scripts", "assets"}); err != nil {
		t.Fatalf("SetSparseCheckoutPatterns: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootWt, "SKILL.md")); err != nil {
		t.Errorf("root SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rootWt, "references", "guide.md")); err != nil {
		t.Errorf("root references/ missing: %v", err)
	}
	for _, gone := range []string{"a", "b", "bin", "test"} {
		if _, err := os.Stat(filepath.Join(rootWt, gone)); !os.IsNotExist(err) {
			t.Errorf("root scope leaked %q (the over-scan #153 bug) into scan dir (err=%v)", gone, err)
		}
	}
}

// TestWorktree_SetSparseCheckout_CleanGitStatus pins the bug #13 fix: after
// setting a sparse checkout, `git status` must be clean. The pre-fix code
// wrote .git/info/sparse-checkout and deleted files on disk without setting
// core.sparseCheckout or updating the index, so git reported every pruned
// file as "deleted" — which turned `qvr diff` into megabytes of noise and
// made `qvr push` from an install worktree delete every other skill.
func TestWorktree_SetSparseCheckout_CleanGitStatus(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{
		"code-review":   "# x\n",
		"deploy-helper": "# y\n",
	})
	bare := bareCloneFor(t, remote)
	wtPath := filepath.Join(t.TempDir(), "wt")

	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.SetSparseCheckout(wtPath, []string{"skills/code-review"}); err != nil {
		t.Fatalf("SetSparseCheckout: %v", err)
	}

	repo, err := gogit.PlainOpen(wtPath)
	if err != nil {
		t.Fatalf("open worktree: %v", err)
	}
	gwt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree handle: %v", err)
	}
	status, err := gwt.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.IsClean() {
		t.Errorf("git status should be clean after sparse setup; got:\n%s", status)
	}
}

func TestWorktree_SetSparseCheckout_RootEquivalent(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{"code-review": "# x\n"})
	bare := bareCloneFor(t, remote)
	wtPath := filepath.Join(t.TempDir(), "wt")

	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// "." selects everything — nothing should be removed.
	if err := w.SetSparseCheckout(wtPath, []string{"."}); err != nil {
		t.Fatalf("sparse .: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "skills", "code-review", "SKILL.md")); err != nil {
		t.Errorf("sparse=. should keep everything, got: %v", err)
	}
}

func TestWorktree_Checkout_ReappliesSparse(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{
		"code-review":   "# x\n",
		"deploy-helper": "# y\n",
	})

	// Add a branch "v2" to the remote with an additional skill.
	remoteRepo, err := gogit.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	mainHead, err := remoteRepo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	if err := remoteRepo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("v2"), mainHead.Hash(),
	)); err != nil {
		t.Fatalf("set v2: %v", err)
	}

	bare := bareCloneFor(t, remote)
	wtPath := filepath.Join(t.TempDir(), "wt")
	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.SetSparseCheckout(wtPath, []string{"skills/code-review"}); err != nil {
		t.Fatalf("sparse: %v", err)
	}
	if err := w.Checkout(wtPath, "v2"); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	// deploy-helper must still be trimmed after switch.
	if _, err := os.Stat(filepath.Join(wtPath, "skills", "deploy-helper")); !os.IsNotExist(err) {
		t.Errorf("expected sparse to re-apply after checkout, deploy-helper present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(wtPath, "skills", "code-review", "SKILL.md")); err != nil {
		t.Errorf("kept skill missing after switch: %v", err)
	}
}

func TestWorktree_Checkout_BadRef(t *testing.T) {
	remote := setupBareRegistry(t, map[string]string{"code-review": "# x\n"})
	bare := bareCloneFor(t, remote)
	wtPath := filepath.Join(t.TempDir(), "wt")
	w := git.NewGoGitWorktree()
	if err := w.Add(bare, wtPath, "main"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	err := w.Checkout(wtPath, "nope-branch")
	if err == nil || !strings.Contains(err.Error(), "reference not found") {
		t.Errorf("expected ref-not-found error, got %v", err)
	}
}
