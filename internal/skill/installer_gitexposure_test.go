package skill_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// readLinkedDirNames lists the entries an agent sees when it opens the skill's
// agent-dir symlink (following it). Returns the entry names.
func readLinkedDirNames(t *testing.T, linkPath string) []string {
	t.Helper()
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat %s: %v", linkPath, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink", linkPath)
	}
	ents, err := os.ReadDir(linkPath) // follows the symlink
	if err != nil {
		t.Fatalf("readdir through symlink %s: %v", linkPath, err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// assertRootSkillHidesGit installs root-app from remote and asserts the agent
// view exposes the skill content but NOT .git, while the underlying worktree
// keeps its real .git and the entry still verifies. This is the #154 fix:
// a path:"." install used to symlink the worktree root (with its live .git/)
// straight into .claude/skills/<name>.
func assertRootSkillHidesGit(t *testing.T, h *installerTestHarness) {
	t.Helper()
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "root-app", Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	linkPath := filepath.Join(h.project, ".claude", "skills", "root-app")

	names := readLinkedDirNames(t, linkPath)
	var hasSKILL, hasGit bool
	for _, n := range names {
		if n == "SKILL.md" {
			hasSKILL = true
		}
		if n == ".git" {
			hasGit = true
		}
	}
	if hasGit {
		t.Errorf(".git exposed through agent symlink (#154 regression); entries: %v", names)
	}
	if !hasSKILL {
		t.Errorf("SKILL.md not visible through agent symlink; entries: %v", names)
	}

	// SKILL.md must still be readable through the link.
	if _, err := os.ReadFile(filepath.Join(linkPath, "SKILL.md")); err != nil {
		t.Errorf("read SKILL.md through link: %v", err)
	}
	// .git must NOT be reachable through the link.
	if _, err := os.Stat(filepath.Join(linkPath, ".git")); !os.IsNotExist(err) {
		t.Errorf(".git reachable through agent link: err=%v (want not-exist)", err)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("root-app")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	// The real worktree must keep its .git so git ops (status/sync/verify) work.
	worktree := skill.EntryWorktreePath(entry)
	if _, err := os.Stat(filepath.Join(worktree, ".git")); err != nil {
		t.Errorf("worktree lost its .git (git ops would break): %v", err)
	}
	// And integrity must be unaffected — the view lives under .git, excluded
	// from the subtree hash.
	if res := skill.VerifySingleEntry(entry, h.project); res.Status != skill.VerifyStatusOK {
		t.Errorf("verify after install = %q (%s) — agent view must not perturb the hash", res.Status, res.Message)
	}
}

func TestAgentView_LoneRootHidesGit(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"prompt.txt":          "do the thing\n",
	})
	h.addRegistry(t, "solo", remote)
	assertRootSkillHidesGit(t, h)
}

func TestAgentView_CoexistRootHidesGit(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"a/SKILL.md":          "---\nname: a\ndescription: a sibling skill.\n---\n",
		"bin/app.sh":          "#!/bin/sh\necho hi\n",
	})
	h.addRegistry(t, "multi", remote)
	assertRootSkillHidesGit(t, h)
}

// TestAgentView_SubdirSkillUnaffected confirms a normal nested skill still
// links straight at its clean subtree (no view, no .git there) — the fix is
// scoped to root-layout installs.
// TestAgentView_ReconcileRelinksToView proves the heal path: if the agent
// symlink is lost, `qvr sync` (Reconcile) recreates it pointing at the
// sanitized view — not the worktree root — so a repaired install doesn't
// re-expose .git.
func TestAgentView_ReconcileRelinksToView(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"bin/app.sh":          "#!/bin/sh\necho hi\n",
		"a/SKILL.md":          "---\nname: a\ndescription: sibling.\n---\n",
	})
	h.addRegistry(t, "multi", remote)
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "root-app", Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	linkPath := filepath.Join(h.project, ".claude", "skills", "root-app")
	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}

	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	rec := skill.NewReconciler(h.installer)
	if _, err := rec.Reconcile(lock, h.project, h.home, skill.ReconcileOptions{}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	names := readLinkedDirNames(t, linkPath)
	for _, n := range names {
		if n == ".git" {
			t.Errorf("reconcile re-linked to worktree root exposing .git: %v", names)
		}
	}
	if _, err := os.ReadFile(filepath.Join(linkPath, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md unreadable after reconcile: %v", err)
	}
}

// TestAgentView_SubdirSkillUnaffected confirms a normal nested skill still
// links straight at its clean subtree (no view, no .git there) — the fix is
// scoped to root-layout installs.
func TestAgentView_SubdirSkillUnaffected(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{
		"foo": "---\nname: foo\ndescription: a nested skill.\n---\n# foo\n",
	})
	h.addRegistry(t, "nested", remote)
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "foo", Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, _ := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	entry, _ := lock.Get("foo")
	// Symlink target is the worktree subtree, not a .git/qvr-view.
	target := skill.AgentLinkTarget(entry, h.project)
	if strings.Contains(target, ".git") {
		t.Errorf("subdir skill link target unexpectedly routed through .git: %s", target)
	}
	names := readLinkedDirNames(t, filepath.Join(h.project, ".claude", "skills", "foo"))
	for _, n := range names {
		if n == ".git" {
			t.Errorf("subdir skill exposed .git: %v", names)
		}
	}
}
