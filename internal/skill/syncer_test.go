package skill_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
)

func TestStatus_Clean(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	s := newSyncer()
	status, err := s.Status(entry, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Dirty {
		t.Errorf("expected clean, got dirty")
	}
	if status.Ahead != 0 || status.Behind != 0 {
		t.Errorf("expected 0/0 ahead/behind, got %d/%d", status.Ahead, status.Behind)
	}
}

func TestStatus_Dirty(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	// Modify a file in the worktree (install is frozen; unlock first like edit).
	makeWorktreeEditable(t, skill.EntryWorktreePath(entry))
	skillFile := filepath.Join(skill.EntryWorktreePath(entry), entry.Path, "SKILL.md")
	data, _ := os.ReadFile(skillFile)
	_ = os.WriteFile(skillFile, append(data, []byte("\n# edit\n")...), 0o644)

	s := newSyncer()
	status, err := s.Status(entry, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Dirty {
		t.Errorf("expected dirty status")
	}
}

func TestStatus_BrokenWorktree(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	_ = os.RemoveAll(skill.EntryWorktreePath(entry))
	s := newSyncer()
	status, err := s.Status(entry, "")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.Broken {
		t.Errorf("expected broken=true, got %+v", status)
	}
}

func TestPull_FastForward(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	// Second worktree to push commits to origin — simulating another user.
	// Place it at a v5-derivable path so the synthetic entry's
	// EntryWorktreePath resolves to it.
	otherCommit := "ffffff7"
	other := registry.WorktreePath("acme", "code-review-other", registry.ShortSHA(otherCommit))
	w := git.NewGoGitWorktree()
	if err := w.Add(remote, other, "main"); err != nil {
		t.Fatalf("Add other worktree: %v", err)
	}
	// Modify and push directly from the second worktree to seed an upstream commit.
	otherFile := filepath.Join(other, "skills", "code-review", "SKILL.md")
	orig, _ := os.ReadFile(otherFile)
	_ = os.WriteFile(otherFile, append(orig, []byte("\n# from other\n")...), 0o644)

	pushed := commitAndPushWorktree(t, other, "main", "from other")

	// Now pull into the original worktree; expect fast-forward.
	s := newSyncer()
	got, err := s.Pull(context.Background(), entry)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	if got != pushed {
		t.Errorf("pull head = %s, want %s", got, pushed)
	}
	// Worktree file must reflect the upstream change.
	data, _ := os.ReadFile(filepath.Join(skill.EntryWorktreePath(entry), entry.Path, "SKILL.md"))
	if want := "from other"; !containsBytes(data, want) {
		t.Errorf("expected pulled content to contain %q, got: %s", want, string(data))
	}
}

func TestPull_RefusesOnDirty(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	_ = os.WriteFile(filepath.Join(skill.EntryWorktreePath(entry), "junk.txt"), []byte("x"), 0o644)

	s := newSyncer()
	_, err := s.Pull(context.Background(), entry)
	if !errors.Is(err, skill.ErrDivergence) {
		t.Errorf("expected ErrDivergence on dirty pull, got %v", err)
	}
}

func TestPull_Divergence(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	entry := installCodeReview(t, h, remote)

	// Other user pushes upstream. Worktree placed at a v5-derivable path
	// so the synthetic entry's EntryWorktreePath resolves to it.
	otherCommit := "ddddddd"
	other := registry.WorktreePath("acme", "cr-other", registry.ShortSHA(otherCommit))
	w := git.NewGoGitWorktree()
	if err := w.Add(remote, other, "main"); err != nil {
		t.Fatalf("add other: %v", err)
	}
	otherFile := filepath.Join(other, "skills", "code-review", "SKILL.md")
	_ = os.WriteFile(otherFile, []byte("from-other"), 0o644)
	commitAndPushWorktree(t, other, "main", "other")

	// A local-only commit in the original worktree (WITHOUT pulling first) gives
	// it history that origin lacks while origin has the commit above → diverged.
	makeWorktreeEditable(t, skill.EntryWorktreePath(entry))
	localFile := filepath.Join(skill.EntryWorktreePath(entry), entry.Path, "SKILL.md")
	_ = os.WriteFile(localFile, []byte("local-only"), 0o644)
	commitWorktreeLocal(t, skill.EntryWorktreePath(entry), "local")

	_, err := newSyncer().Pull(context.Background(), entry)
	if !errors.Is(err, skill.ErrDivergence) {
		t.Errorf("expected ErrDivergence, got %v", err)
	}
}

func TestPull_PinnedToTag(t *testing.T) {
	h := newHarness(t)
	remote := seedRemoteWithTags(t, map[string]string{"code-review": codeReviewSkill}, "v0.1.1")
	h.addRegistry(t, "acme", remote)

	// Install pinned to the tag rather than main.
	_, err := h.installer.Install(skill.InstallRequest{
		Skill:       "code-review@v0.1.1",
		Targets:     []string{"claude"},
		ProjectRoot: h.project,
	})
	if err != nil {
		t.Fatalf("install tag-pinned: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}

	_, err = newSyncer().Pull(context.Background(), entry)
	if !errors.Is(err, skill.ErrPinnedToTag) {
		t.Fatalf("expected ErrPinnedToTag, got %v", err)
	}
}
