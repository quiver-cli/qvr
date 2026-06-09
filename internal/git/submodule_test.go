package git_test

import (
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/git"
)

// storeObject encodes obj into repo's object store and returns its hash.
func storeObject(t *testing.T, repo *gogit.Repository, enc interface {
	Encode(plumbing.EncodedObject) error
}) plumbing.Hash {
	t.Helper()
	o := repo.Storer.NewEncodedObject()
	if err := enc.Encode(o); err != nil {
		t.Fatalf("encode object: %v", err)
	}
	h, err := repo.Storer.SetEncodedObject(o)
	if err != nil {
		t.Fatalf("store object: %v", err)
	}
	return h
}

// setupRepoWithGitlink builds a bare repo whose HEAD tree contains
// skills/my-skill as a gitlink (mode 160000) plus one regular blob — the
// exact shape `git add` produces when a nested repo is committed (#241).
// Plumbing-built because go-git's worktree Add can't stage gitlinks.
func setupRepoWithGitlink(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, true)
	if err != nil {
		t.Fatalf("init bare: %v", err)
	}

	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	w, err := blob.Writer()
	if err != nil {
		t.Fatalf("blob writer: %v", err)
	}
	if _, err := w.Write([]byte("name: my-team-skills\n")); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	_ = w.Close()
	blobHash, err := repo.Storer.SetEncodedObject(blob)
	if err != nil {
		t.Fatalf("store blob: %v", err)
	}

	// The gitlink's target commit doesn't exist anywhere — exactly like a
	// pushed registry whose contributor never pushed the nested repo.
	gitlinkTarget := plumbing.NewHash("0123456789abcdef0123456789abcdef01234567")
	subTree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "my-skill", Mode: filemode.Submodule, Hash: gitlinkTarget},
	}}
	subHash := storeObject(t, repo, subTree)

	rootTree := &object.Tree{Entries: []object.TreeEntry{
		{Name: "registry.yaml", Mode: filemode.Regular, Hash: blobHash},
		{Name: "skills", Mode: filemode.Dir, Hash: subHash},
	}}
	rootHash := storeObject(t, repo, rootTree)

	sig := object.Signature{Name: "T", Email: "t@t", When: time.Now()}
	commit := &object.Commit{
		Message:   "seed with gitlink",
		TreeHash:  rootHash,
		Author:    sig,
		Committer: sig,
	}
	commitHash := storeObject(t, repo, commit)

	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), commitHash)); err != nil {
		t.Fatalf("set main: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	return dir
}

// TestListSubmodulePaths_FindsGitlink: the dedicated walk must surface
// mode-160000 entries that ListBlobsRecursive (tree.Files()) never yields.
func TestListSubmodulePaths_FindsGitlink(t *testing.T) {
	repoPath := setupRepoWithGitlink(t)
	gc := git.NewGoGitClient()

	paths, err := gc.ListSubmodulePaths(repoPath, "HEAD")
	if err != nil {
		t.Fatalf("ListSubmodulePaths: %v", err)
	}
	if len(paths) != 1 || paths[0] != "skills/my-skill" {
		t.Errorf("paths = %v, want [skills/my-skill]", paths)
	}

	// Contrast: the blob walk is blind to the gitlink.
	blobs, err := gc.ListBlobsRecursive(repoPath, "HEAD", "")
	if err != nil {
		t.Fatalf("ListBlobsRecursive: %v", err)
	}
	for _, b := range blobs {
		if b.Path == "skills/my-skill" {
			t.Errorf("blob walk unexpectedly surfaced the gitlink: %+v", b)
		}
	}
}

// TestListSubmodulePaths_NoGitlinks: a regular repo yields an empty list.
func TestListSubmodulePaths_NoGitlinks(t *testing.T) {
	repoPath := setupTestRepo(t, testSkills)
	gc := git.NewGoGitClient()

	paths, err := gc.ListSubmodulePaths(repoPath, "HEAD")
	if err != nil {
		t.Fatalf("ListSubmodulePaths: %v", err)
	}
	if len(paths) != 0 {
		t.Errorf("paths = %v, want none", paths)
	}
}
