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
	"github.com/raks097/quiver/pkg/skillspec"
)

// codeReviewSkill and deployHelperSkill are the minimal valid SKILL.md bodies
// reused across installer, syncer, publisher, and integration tests.
const codeReviewSkill = `---
name: code-review
description: Performs thorough code review of staged changes.
---

# Code Review

Apply standard review patterns.
`

const deployHelperSkill = `---
name: deploy-helper
description: Helps with deployment workflows.
---

# Deploy Helper
`

// testEnv isolates QUIVER_HOME to a temp directory so registry + worktree
// paths don't pollute real state. Returns the temp dir for convenience.
func testEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	return home
}

// seedRemote creates a bare remote repo pre-seeded with the given skills on
// branch main, plus any extra branches pointing at the same HEAD. Returns
// the remote (bare repo) path.
func seedRemote(t *testing.T, skills map[string]string, branches ...string) string {
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
	for name, content := range skills {
		skillDir := filepath.Join(seed, "skills", name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	_, err = wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t", When: time.Now()},
	})
	if err != nil {
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
	refs := []gogitcfg.RefSpec{"refs/heads/main:refs/heads/main"}
	for _, b := range branches {
		if err := sr.Storer.SetReference(plumbing.NewHashReference(
			plumbing.NewBranchReferenceName(b), head.Hash(),
		)); err != nil {
			t.Fatalf("set branch %s: %v", b, err)
		}
		refs = append(refs, gogitcfg.RefSpec("refs/heads/"+b+":refs/heads/"+b))
	}
	if err := sr.Push(&gogit.PushOptions{RemoteName: "origin", RefSpecs: refs}); err != nil {
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

// seedRemoteWithExecScript creates a bare remote seeded on main with a single
// skill that ships an executable script (committed at git mode 100755)
// alongside its SKILL.md. Returns the bare repo path. Used to regression-test
// issue #135: a skill with an exec file must round-trip through install →
// freeze → verify without spurious subtreeHash drift.
func seedRemoteWithExecScript(t *testing.T, name, skillBody, scriptName, scriptBody string) string {
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

	skillDir := filepath.Join(seed, "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillBody), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	// 0o755 → go-git records the blob at git mode 100755, the exec-bit case
	// that #135 dropped on the read-only materialisation.
	if err := os.WriteFile(filepath.Join(skillDir, scriptName), []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
		t.Fatalf("add SKILL.md: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, scriptName)); err != nil {
		t.Fatalf("add script: %v", err)
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

// seedRemoteWithTags creates a bare remote seeded with the given skills on
// main, plus a tag at HEAD for every entry in tags. Returns the bare repo
// path. The tags are lightweight refs at the same commit — sufficient for
// testing install resolution and upgrade flows.
func seedRemoteWithTags(t *testing.T, skills map[string]string, tags ...string) string {
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
	for name, content := range skills {
		skillDir := filepath.Join(seed, "skills", name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	_, err = wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t", When: time.Now()},
	})
	if err != nil {
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
	refs := []gogitcfg.RefSpec{"refs/heads/main:refs/heads/main"}
	for _, tag := range tags {
		if err := sr.Storer.SetReference(plumbing.NewHashReference(
			plumbing.NewTagReferenceName(tag), head.Hash(),
		)); err != nil {
			t.Fatalf("set tag %s: %v", tag, err)
		}
		refs = append(refs, gogitcfg.RefSpec("refs/tags/"+tag+":refs/tags/"+tag))
	}
	if err := sr.Push(&gogit.PushOptions{RemoteName: "origin", RefSpecs: refs}); err != nil {
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

// installerTestHarness wires up a real registry manager, worktree manager,
// and git client against an isolated QUIVER_HOME.
type installerTestHarness struct {
	installer *skill.Installer
	manager   *registry.Manager
	home      string
	project   string
}

func newHarness(t *testing.T) *installerTestHarness {
	t.Helper()
	home := testEnv(t)
	project := t.TempDir()
	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	mgr := registry.NewManager(gc)
	inst := skill.NewInstaller(mgr, wt, gc)
	return &installerTestHarness{
		installer: inst,
		manager:   mgr,
		home:      home,
		project:   project,
	}
}

func (h *installerTestHarness) addRegistry(t *testing.T, name, url string) {
	t.Helper()
	if _, err := h.manager.Add(context.Background(), name, url); err != nil {
		t.Fatalf("registry add: %v", err)
	}
}

// installCodeReview installs code-review for a fresh harness and returns the
// lock entry so tests can poke at the worktree.
func installCodeReview(t *testing.T, h *installerTestHarness, remote string, branches ...string) *model.LockEntry {
	t.Helper()
	ref := "main"
	if len(branches) > 0 {
		ref = branches[0]
	}
	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@" + ref,
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
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
	_ = remote // keeps signature flexible for callers
	return entry
}

func newSyncer() *skill.Syncer {
	return skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())
}

// makeWorktreeEditable restores write permissions on a worktree subtree so a
// test can simulate a user modifying an installed skill. Installs are frozen
// read-only ("uv for agent skills": immutable at rest); in real use `qvr edit`
// performs this transition by ejecting a writable copy. These unit tests mutate
// the shared worktree directly, so they unlock it first — mirroring edit mode.
func makeWorktreeEditable(t *testing.T, root string) {
	t.Helper()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			_ = os.Chmod(p, 0o755)
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		_ = os.Chmod(p, 0o644)
		return nil
	})
	if err != nil {
		t.Fatalf("make worktree editable: %v", err)
	}
}

// makeSkill builds an in-memory Skill struct for validator tests.
func makeSkill(name, desc, dir string) *model.Skill {
	return &model.Skill{
		Skill: skillspec.Skill{
			Frontmatter: skillspec.Frontmatter{
				Name:        name,
				Description: desc,
			},
		},
		Dir:  "/test/" + dir,
		Name: dir,
	}
}

// makeSkillDir writes a minimal valid skill to disk under a fresh temp dir.
// Returns the skill directory path (parent of SKILL.md).
func makeSkillDir(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: test\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return skillDir
}

// writeLocalSkill writes a valid skill with the given name/description to a
// temp directory and also drops a supplemental file so copy-dir behaviour is
// exercised. Returns the skill directory.
func writeLocalSkill(t *testing.T, name, description string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = os.WriteFile(filepath.Join(dir, "NOTES.md"), []byte("notes"), 0o644)
	return dir
}

// assertHasError fails the test if result doesn't have a matching error.
func assertHasError(t *testing.T, result *skill.ValidationResult, field, substr string) {
	t.Helper()
	for _, e := range result.Errors {
		if e.Field == field && strings.Contains(strings.ToLower(e.Message), strings.ToLower(substr)) {
			return
		}
	}
	t.Errorf("expected error on field %q containing %q, got: %v", field, substr, result.Errors)
}

// containsBytes is a tiny alternative to strings.Contains so we keep imports
// minimal in some test files.
func containsBytes(haystack []byte, needle string) bool {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(haystack); i++ {
		match := true
		for j := range n {
			if haystack[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// initEmptyBareWithHEAD creates a fresh `git init --bare` repo under
// t.TempDir() and pins its HEAD symref to refs/heads/<branch>. Used by
// the eject/publish fixtures to give entries a reachable Source URL —
// before this helper the fixtures hard-coded `git@example.com:<reg>.git`
// and every dry-run publish hung for ~150s on an ls-remote SSH timeout
// to a host that doesn't run a git server, bringing the internal/skill
// package's test wall-time to ~5 minutes. With a local bare:
//   - ls-remote returns immediately (no refs → fall through).
//   - the local-bare HEAD reader (issue #113 fix) picks up `branch` so
//     dry-run reports the correct target without the network probe.
//
// gogit's PlainInit defaults to "master"; the explicit symref overwrite
// mirrors what a system with `init.defaultBranch = main` produces.
func initEmptyBareWithHEAD(t *testing.T, registryName, branch string) string {
	t.Helper()
	bare := filepath.Join(t.TempDir(), registryName+".git")
	repo, err := gogit.PlainInit(bare, true)
	if err != nil {
		t.Fatalf("init bare fixture %s: %v", registryName, err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName(branch),
	)); err != nil {
		t.Fatalf("pin HEAD → %s on bare fixture %s: %v", branch, registryName, err)
	}
	return bare
}

// commitAndPushWorktree stages everything in a worktree, commits it, and pushes
// the branch to origin, returning the new commit hash. It replicates exactly
// what the former Syncer.Push did (go-git commit + GitClient.Push) and exists
// so Pull tests can seed an upstream commit now that Syncer.Push has been
// removed as dead code (#160 cleanup — production publish uses git.Push directly).
func commitAndPushWorktree(t *testing.T, worktreePath, branch, message string) string {
	t.Helper()
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		t.Fatalf("open worktree %s: %v", worktreePath, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree handle: %v", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	hash, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	refspec := "refs/heads/" + branch + ":refs/heads/" + branch
	if err := git.NewGoGitClient().Push(context.Background(), worktreePath, "origin", []string{refspec}); err != nil {
		t.Fatalf("push: %v", err)
	}
	return hash.String()
}

// commitWorktreeLocal stages and commits everything in a worktree WITHOUT
// pushing, manufacturing a local-only commit. Used to diverge a worktree from
// origin in Pull tests.
func commitWorktreeLocal(t *testing.T, worktreePath, message string) {
	t.Helper()
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		t.Fatalf("open worktree %s: %v", worktreePath, err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree handle: %v", err)
	}
	if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if _, err := wt.Commit(message, &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "test@test", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}
