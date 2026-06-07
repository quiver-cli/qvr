package skill_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
)

// materializeAndCompare materializes (regName@main, subpath, rootCoexists) into a
// fresh temp dir with NO worktree, then asserts the on-disk subtree hash equals
// the bare-repo hash at the same commit. This is the load-bearing #204 invariant:
// a worktree-free install hashes byte-identically to a checkout, so the recorded
// SubtreeHash and `qvr lock verify`'s disk recomputation keep agreeing. Returns
// the materialized dest dir.
func materializeAndCompare(t *testing.T, regName, subpath string, rootCoexists bool) string {
	t.Helper()
	bare := registry.RegistryPath(regName)
	gc := git.NewGoGitClient()
	commit, err := gc.ResolveRef(bare, "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	dest := t.TempDir()
	m := &skill.Materializer{}
	got, err := m.MaterializeSubtree(bare, commit, subpath, rootCoexists, dest)
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got != commit {
		t.Errorf("materialized commit = %s, want %s", got, commit)
	}

	hashRoot := dest
	if clean := strings.Trim(subpath, "/"); clean != "" && clean != "." {
		hashRoot = filepath.Join(dest, filepath.FromSlash(clean))
	}
	diskHash, err := canonical.HashSubtreeFromDisk(hashRoot)
	if err != nil {
		t.Fatalf("disk hash: %v", err)
	}
	id, err := skill.ComputeEntryIdentityAtCommit(bare, commit, subpath, rootCoexists)
	if err != nil {
		t.Fatalf("commit hash: %v", err)
	}
	if diskHash != id.SubtreeHash {
		t.Errorf("disk hash %s != bare-commit hash %s — worktree-free install would drift on verify", diskHash, id.SubtreeHash)
	}
	return dest
}

func TestMaterialize_SubdirSkill(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	dest := materializeAndCompare(t, "acme", "skills/code-review", false)
	if _, err := os.Stat(filepath.Join(dest, "skills", "code-review", "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not materialized at repo-relative path: %v", err)
	}
	// No .git is ever written.
	if _, err := os.Stat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
		t.Errorf("materializer wrote a .git (want worktree-free): err=%v", err)
	}
}

func TestMaterialize_LoneRoot(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"prompt.txt":          "do the thing\n",
	})
	h.addRegistry(t, "solo", remote)

	dest := materializeAndCompare(t, "solo", "", false)
	// A lone root materializes the whole repo tree.
	for _, want := range []string{"SKILL.md", "references/guide.md", "prompt.txt"} {
		if _, err := os.Stat(filepath.Join(dest, filepath.FromSlash(want))); err != nil {
			t.Errorf("expected %s materialized: %v", want, err)
		}
	}
}

func TestMaterialize_CoexistRootScoped(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"a/SKILL.md":          "---\nname: a\ndescription: a sibling skill.\n---\n",
		"bin/app.sh":          "#!/bin/sh\necho hi\n",
	})
	h.addRegistry(t, "multi", remote)

	dest := materializeAndCompare(t, "multi", "", true)
	// Scoped to SKILL.md + recognized content dirs.
	if _, err := os.Stat(filepath.Join(dest, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not materialized: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "references", "guide.md")); err != nil {
		t.Errorf("references/ not materialized: %v", err)
	}
	// Siblings and unrelated app code are NOT part of the root skill.
	if _, err := os.Stat(filepath.Join(dest, "a")); !os.IsNotExist(err) {
		t.Errorf("sibling skill a/ leaked into root materialization: err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "bin")); !os.IsNotExist(err) {
		t.Errorf("unrelated bin/ leaked into root materialization: err=%v", err)
	}
}

func TestMaterialize_ExecBitPreserved(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithExecScript(t, "code-review", codeReviewSkill, "run.sh", "#!/bin/sh\necho hi\n")
	h.addRegistry(t, "acme", remote)

	dest := materializeAndCompare(t, "acme", "skills/code-review", false)
	fi, err := os.Stat(filepath.Join(dest, "skills", "code-review", "run.sh"))
	if err != nil {
		t.Fatalf("stat script: %v", err)
	}
	if fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("exec bit dropped on materialized script (#135): mode=%v", fi.Mode())
	}
}

// seedRemoteWithSymlink seeds a bare remote with skills/<name>/SKILL.md and a
// relative symlink skills/<name>/link.md -> SKILL.md, so the materializer's
// symlink handling (git mode 0120000, blob = link target) can be exercised.
func seedRemoteWithSymlink(t *testing.T, name string) string {
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
	if _, err := sr.CreateRemote(&gogitcfg.RemoteConfig{Name: "origin", URLs: []string{remote}}); err != nil {
		t.Fatalf("create remote: %v", err)
	}
	wt, err := sr.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	dir := filepath.Join(seed, "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(codeReviewSkill), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.Symlink("SKILL.md", filepath.Join(dir, "link.md")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
		t.Fatalf("add SKILL.md: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "link.md")); err != nil {
		t.Fatalf("add link: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	head, err := sr.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if err := sr.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), head.Hash(),
	)); err != nil {
		t.Fatalf("set main: %v", err)
	}
	if err := sr.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitcfg.RefSpec{"refs/heads/main:refs/heads/main"},
	}); err != nil {
		t.Fatalf("push: %v", err)
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

func TestMaterialize_SymlinkPreserved(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithSymlink(t, "code-review")
	h.addRegistry(t, "acme", remote)

	dest := materializeAndCompare(t, "acme", "skills/code-review", false)
	link := filepath.Join(dest, "skills", "code-review", "link.md")
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat link: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("materialized link is not a symlink: mode=%v", fi.Mode())
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink: %v", err)
	}
	if target != "SKILL.md" {
		t.Errorf("symlink target = %q, want SKILL.md", target)
	}
}

// TestMaterialize_AbsentSubtreeReported confirms a missing subtree at the commit
// surfaces as ErrSubtreeAbsent (which the installer maps to ErrSkillAbsentAtRef).
func TestMaterialize_AbsentSubtreeReported(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	bare := registry.RegistryPath("acme")
	gc := git.NewGoGitClient()
	commit, err := gc.ResolveRef(bare, "main")
	if err != nil {
		t.Fatalf("resolve main: %v", err)
	}
	m := &skill.Materializer{}
	if _, err := m.MaterializeSubtree(bare, commit, "skills/does-not-exist", false, t.TempDir()); !errors.Is(err, skill.ErrSubtreeAbsent) {
		t.Errorf("expected ErrSubtreeAbsent, got %v", err)
	}
}

// TestInstall_ReusesLegacyWorktreeDir is the back-compat guard: a pre-#204
// install left a real `.git` worktree at the SHA-keyed path. A fresh Install
// must reuse it (not fail opening it as worktree-free) and still record the
// correct, agreeing SubtreeHash computed from the bare repo.
func TestInstall_ReusesLegacyWorktreeDir(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	// Pre-create the legacy `.git` worktree at the SHA-keyed final path.
	legacy := installCodeReviewLegacyWorktree(t, h, remote, "main")
	if !skill.HasGitDir(skill.EntryWorktreePath(legacy)) {
		t.Fatalf("precondition: legacy worktree should carry .git")
	}

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install over legacy worktree: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.SubtreeHash == "" {
		t.Errorf("install over legacy worktree recorded no SubtreeHash")
	}
	if res := skill.VerifySingleEntry(entry, h.project); res.Status != skill.VerifyStatusOK {
		t.Errorf("verify after legacy reuse = %q (%s)", res.Status, res.Message)
	}
}
