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

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// TestInstaller_LayoutBRepoRoot reproduces bug #50: a layout-B repo (a single
// SKILL.md at the repo root, no `skills/<name>/` subdirectory) used to fail
// install with `name "X" must match directory name "X--X--main.staging"`
// because the validator compared the skill's frontmatter name against the
// basename of an internal staging path. The fix overrides the loader's
// directory-derived Name with the canonical name from the registry index
// before validation runs.
func TestInstaller_LayoutBRepoRoot(t *testing.T) {
	h := newHarness(t)
	remote := seedLayoutBRemote(t, "layoutb",
		"---\nname: layoutb\ndescription: a layout-B skill named like the repo\n---\n# layoutb\n")
	h.addRegistry(t, "layoutb", remote)

	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "layoutb",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		// Regression check: the pre-fix error mentioned a `.staging` directory
		// — surface that explicitly so a regression is unambiguous.
		if strings.Contains(err.Error(), ".staging") {
			t.Fatalf("layout-B install leaked internal staging path into error: %v", err)
		}
		t.Fatalf("layout-B install failed: %v", err)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("layoutb")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.Path != "." && entry.Path != "" {
		t.Errorf("layout-B entry.Path should be '.' or '', got %q", entry.Path)
	}

	// The symlink should resolve to a directory containing SKILL.md.
	linkPath := filepath.Join(h.project, ".claude/skills/layoutb/SKILL.md")
	if _, err := os.Stat(linkPath); err != nil {
		t.Fatalf("expected SKILL.md visible through symlink: %v", err)
	}
}

// seedLayoutBRemote creates a bare remote whose root holds a single SKILL.md
// (no skills/ subdir). Mirrors the layout-B fixture from the bug repro.
func seedLayoutBRemote(t *testing.T, skillName, skillMD string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), skillName+".git")
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
	if err := os.WriteFile(filepath.Join(seed, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := wt.Add("SKILL.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := sr.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitcfg.RefSpec{"refs/heads/master:refs/heads/main"},
	}); err != nil {
		// Fallback when the local default branch is `main` (newer git defaults).
		if err := sr.Push(&gogit.PushOptions{
			RemoteName: "origin",
			RefSpecs:   []gogitcfg.RefSpec{"refs/heads/main:refs/heads/main"},
		}); err != nil {
			t.Fatalf("push seed: %v", err)
		}
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

// satisfy import — the package uses context elsewhere in helpers_test.go; keep
// this file's imports tight.
var _ = context.Background
