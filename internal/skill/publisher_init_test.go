package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// scaffoldEditDirNoGit writes a minimal mode:edit skill dir WITHOUT a .git/ —
// exactly the state `qvr init` left behind before #150 was fixed — and returns
// the lock entry + project root.
func scaffoldEditDirNoGit(t *testing.T, name string) (*model.LockEntry, string) {
	t.Helper()
	projectRoot := t.TempDir()
	editRel := filepath.Join(".claude", "skills", name)
	editAbs := filepath.Join(projectRoot, editRel)
	if err := os.MkdirAll(editAbs, 0o755); err != nil {
		t.Fatalf("mkdir edit dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editAbs, "SKILL.md"),
		[]byte("---\nname: "+name+"\ndescription: greenfield skill.\n---\n# "+name+"\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := gogit.PlainOpen(editAbs); err == nil {
		t.Fatal("precondition: edit dir should NOT be a git repo")
	}
	return &model.LockEntry{
		Name:     name,
		Source:   "", // greenfield — fork provides the remote
		Ref:      "main",
		Mode:     model.ModeEdit,
		EditPath: editRel,
		Targets:  []string{"claude"},
	}, projectRoot
}

// TestPublishInstalled_ForkOnNonRepoEditDir is the direct #150 repro: the
// two-line "ship your first skill" flow `qvr init` advertises. A freshly
// init'd (un-git) edit dir must publish --fork without the opaque
// "status: open: repository does not exist" — publish initializes the repo in
// place. This is the exact command `qvr init` prints.
func TestPublishInstalled_ForkOnNonRepoEditDir(t *testing.T) {
	entry, projectRoot := scaffoldEditDirNoGit(t, "demo")

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
	})
	if err != nil {
		t.Fatalf("publish --fork on a non-repo edit dir failed (#150 regression): %v", err)
	}
	if !res.Migrated {
		t.Errorf("Migrated = false, want true")
	}
	if entry.Source != forkURL {
		t.Errorf("entry.Source = %q, want fork URL %q", entry.Source, forkURL)
	}
	// publish must have initialized the edit dir as a repo in place.
	editAbs := filepath.Join(projectRoot, entry.EditPath)
	if _, err := gogit.PlainOpen(editAbs); err != nil {
		t.Errorf("edit dir is still not a git repo after publish: %v", err)
	}
}
