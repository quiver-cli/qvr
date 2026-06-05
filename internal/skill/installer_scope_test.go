package skill_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// seedFilesRemote creates a bare remote on `main` from an arbitrary repo-relative
// file map. Used to model multi-skill repos (root SKILL.md + siblings + app code)
// without the layout assumptions of the skills/-only seeders.
func seedFilesRemote(t *testing.T, files map[string]string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "multi.git")
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
	for rel, content := range files {
		full := filepath.Join(seed, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
		if _, err := wt.Add(rel); err != nil {
			t.Fatalf("add %s: %v", rel, err)
		}
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

// TestInstaller_RootCoexistsScopedAndPersisted verifies the #153 follow-up: a
// root SKILL.md that coexists with sibling skills is installed scoped to its own
// content (SKILL.md + content dirs, never app code), and that scope decision is
// persisted on the lock entry.
func TestInstaller_RootCoexistsScopedAndPersisted(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":                "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md":     "# guide\n",
		"a/SKILL.md":              "---\nname: a\ndescription: a sibling skill.\n---\n",
		"bin/app.sh":              "#!/bin/sh\necho hi\n",
		"test/fixtures/creds.env": "SECRET=AKIAIOSFODNN7EXAMPLE\n",
	})
	h.addRegistry(t, "multi", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "root-app", Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("root-app")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.Path != "." {
		t.Errorf("path = %q, want '.'", entry.Path)
	}
	if !entry.RootCoexists {
		t.Error("RootCoexists should be persisted true on the lock entry")
	}

	base := filepath.Join(h.project, ".claude", "skills", "root-app")
	if _, err := os.Stat(filepath.Join(base, "SKILL.md")); err != nil {
		t.Errorf("installed SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "references", "guide.md")); err != nil {
		t.Errorf("installed references/ missing: %v", err)
	}
	for _, gone := range []string{"bin", "test", "a"} {
		if _, err := os.Stat(filepath.Join(base, gone)); !os.IsNotExist(err) {
			t.Errorf("installed root skill leaked %q (over-scan/over-install): %v", gone, err)
		}
	}
}

// TestInstaller_PinRestoreHonorsLockedScope isolates the reproducibility fix: the
// registry's current HEAD reports a lone root skill (RootCoexists=false), but the
// lock — written when siblings still existed — records RootCoexists=true. A
// PinCommit restore must honor the LOCKED scope, not re-derive from HEAD, so the
// restored worktree stays narrowed and does not re-expand to the whole repo.
func TestInstaller_PinRestoreHonorsLockedScope(t *testing.T) {
	h := newHarness(t)
	// HEAD has no siblings → fresh index → RootCoexists=false → would otherwise
	// install the whole repo (including bin/).
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"bin/app.sh":          "#!/bin/sh\necho hi\n",
	})
	h.addRegistry(t, "multi", remote)

	// Natural install: lone root → unscoped, bin/ present, lock RootCoexists=false.
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "root-app", Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry := lock.Skills["root-app"]
	if entry == nil {
		t.Fatal("root-app missing from lock")
	}
	if entry.RootCoexists {
		t.Fatal("precondition: lone root should index as RootCoexists=false")
	}
	if _, err := os.Stat(filepath.Join(skill.EntryWorktreePath(entry), "bin")); err != nil {
		t.Fatalf("precondition: lone root install should include bin/, missing: %v", err)
	}

	// Simulate a lock written when siblings existed: flip the recorded scope and
	// drop the worktree so the pin restore re-materializes from scratch.
	pin := entry.Commit
	entry.RootCoexists = true
	if err := lock.Write(); err != nil {
		t.Fatalf("rewrite lock: %v", err)
	}
	if err := os.RemoveAll(skill.EntryWorktreePath(entry)); err != nil {
		t.Fatalf("drop worktree: %v", err)
	}

	// Reproducible restore at the pinned commit.
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "root-app", Targets: []string{"claude"}, ProjectRoot: h.project, PinCommit: pin,
	}); err != nil {
		t.Fatalf("pin restore: %v", err)
	}

	restored := skill.EntryWorktreePath(entry)
	if _, err := os.Stat(filepath.Join(restored, "SKILL.md")); err != nil {
		t.Errorf("restored SKILL.md missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(restored, "bin")); !os.IsNotExist(err) {
		t.Errorf("pin restore re-expanded to whole repo (bin/ present) — locked scope not honored: %v", err)
	}
}
