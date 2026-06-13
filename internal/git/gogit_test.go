package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/git"
)

func TestBareClone(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	err := client.BareClone(context.Background(), srcDir, destDir, git.CloneOptions{AllRefs: true})
	if err != nil {
		t.Fatalf("BareClone: %v", err)
	}

	// Verify it's a bare repo (no worktree)
	_, err = os.Stat(filepath.Join(destDir, "HEAD"))
	if err != nil {
		t.Errorf("expected HEAD file in bare repo: %v", err)
	}
	_, err = os.Stat(filepath.Join(destDir, ".git"))
	if !os.IsNotExist(err) {
		t.Error("bare repo should not have .git subdirectory")
	}
}

func TestBareClone_AlreadyExists(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	destDir := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), srcDir, destDir, git.CloneOptions{AllRefs: true}); err != nil {
		t.Fatalf("first BareClone: %v", err)
	}
	err := client.BareClone(context.Background(), srcDir, destDir, git.CloneOptions{AllRefs: true})
	if !errors.Is(err, git.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestClone(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	destDir := filepath.Join(t.TempDir(), "clone")

	client := git.NewGoGitClient()
	err := client.Clone(context.Background(), srcDir, destDir)
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	// Verify it has a .git directory (non-bare)
	_, err = os.Stat(filepath.Join(destDir, ".git"))
	if err != nil {
		t.Errorf("expected .git directory: %v", err)
	}
}

func TestFetch(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	bareDir := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), srcDir, bareDir, git.CloneOptions{AllRefs: true}); err != nil {
		t.Fatalf("BareClone: %v", err)
	}

	// Add a new commit to the source repo
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	wt, err := srcRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "new-file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("new-file.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("add new file", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if err := client.Fetch(context.Background(), bareDir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
}

func TestFetch_AlreadyUpToDate(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	err := client.Fetch(context.Background(), bareDir)
	if err != nil {
		t.Fatalf("Fetch (already up to date) should not error: %v", err)
	}
}

// addCommit writes a file into the non-bare repo at srcDir and commits it,
// advancing the default branch — used to give a later Fetch something to pull.
func addCommit(t *testing.T, srcDir, file, body string) {
	t.Helper()
	repo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open source: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, file), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if _, err := wt.Add(file); err != nil {
		t.Fatalf("add %s: %v", file, err)
	}
	if _, err := wt.Commit("add "+file, &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestBareClone_ConfigViaGoGit_FetchWorks guards #209: BareClone now writes the
// remote.origin.fetch refspec in-process via go-git instead of spawning
// `git config`. This verifies the config it writes is readable by a real
// `git fetch` (the subprocess fetch path) for both full and single-branch
// clones, and that IsFullClone reflects the mode — catching any config-format
// incompatibility end-to-end.
func TestBareClone_ConfigViaGoGit_FetchWorks(t *testing.T) {
	client := git.NewGoGitClient()

	t.Run("full", func(t *testing.T) {
		srcDir := setupTestRepo(t, testSkills)
		bareDir := filepath.Join(t.TempDir(), "full.git")
		if err := client.BareClone(context.Background(), srcDir, bareDir, git.CloneOptions{AllRefs: true}); err != nil {
			t.Fatalf("BareClone full: %v", err)
		}
		if !git.IsFullClone(bareDir) {
			t.Error("IsFullClone(full) = false, want true")
		}
		before, _ := client.HeadCommit(bareDir)
		addCommit(t, srcDir, "f.txt", "hello")
		if err := client.Fetch(context.Background(), bareDir); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if after, _ := client.HeadCommit(bareDir); after == before {
			t.Errorf("HeadCommit did not advance after fetch (%q) — full refspec not honored", after)
		}
	})

	t.Run("single-branch", func(t *testing.T) {
		srcDir := setupTestRepo(t, testSkills)
		bareDir := filepath.Join(t.TempDir(), "single.git")
		if err := client.BareClone(context.Background(), srcDir, bareDir, git.CloneOptions{AllRefs: false}); err != nil {
			t.Fatalf("BareClone single: %v", err)
		}
		if git.IsFullClone(bareDir) {
			t.Error("IsFullClone(single) = true, want false")
		}
		before, _ := client.HeadCommit(bareDir)
		addCommit(t, srcDir, "s.txt", "world")
		if err := client.Fetch(context.Background(), bareDir); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if after, _ := client.HeadCommit(bareDir); after == before {
			t.Errorf("HeadCommit did not advance after fetch (%q) — single-branch refspec not honored", after)
		}
	})
}

func TestListBranches(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	// Add extra branches directly on the bare repo
	bareRepo, err := gogit.PlainOpen(bareDir)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	head, err := bareRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if err := bareRepo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("develop"),
		head.Hash(),
	)); err != nil {
		t.Fatalf("set develop: %v", err)
	}
	if err := bareRepo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("feature-x"),
		head.Hash(),
	)); err != nil {
		t.Fatalf("set feature-x: %v", err)
	}

	client := git.NewGoGitClient()
	branches, err := client.ListBranches(bareDir)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}

	names := make(map[string]bool)
	for _, b := range branches {
		names[b.Name] = true
	}

	for _, expected := range []string{"master", "develop", "feature-x"} {
		if !names[expected] {
			t.Errorf("expected branch %q not found in %v", expected, names)
		}
	}
}

func TestRefVersions(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	head, err := srcRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if _, err := srcRepo.CreateTag("v1.0.0", head.Hash(), nil); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if _, err := gogit.PlainClone(bareDir, true, &gogit.CloneOptions{URL: srcDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	client := git.NewGoGitClient()
	vers, err := client.RefVersions(bareDir)
	if err != nil {
		t.Fatalf("RefVersions: %v", err)
	}
	if len(vers) < 2 {
		t.Fatalf("expected >=2 refs, got %d (%+v)", len(vers), vers)
	}

	sawTag, sawBranch := scanRefVersions(t, vers)
	if !sawTag {
		t.Errorf("expected the v1.0.0 tag in %+v", vers)
	}
	if !sawBranch {
		t.Errorf("expected at least one branch in %+v", vers)
	}

	assertRefVersionsSorted(t, vers)
}

// scanRefVersions asserts every ref version has a non-empty hash and non-zero
// commit time, and reports whether the v1.0.0 tag and at least one branch were
// seen.
func scanRefVersions(t *testing.T, vers []git.RefVersion) (sawTag, sawBranch bool) {
	t.Helper()
	for _, v := range vers {
		if v.Hash == "" {
			t.Errorf("ref %q has empty hash", v.Name)
		}
		if v.Time.IsZero() {
			t.Errorf("ref %q has zero commit time", v.Name)
		}
		if v.IsTag && v.Name == "v1.0.0" {
			sawTag = true
		}
		if !v.IsTag {
			sawBranch = true
		}
	}
	return sawTag, sawBranch
}

// assertRefVersionsSorted verifies vers is ordered newest-commit-first
// (non-increasing time).
func assertRefVersionsSorted(t *testing.T, vers []git.RefVersion) {
	t.Helper()
	for i := 1; i < len(vers); i++ {
		if vers[i-1].Time.Before(vers[i].Time) {
			t.Errorf("versions not sorted newest-first at %d: %v before %v",
				i, vers[i-1].Time, vers[i].Time)
		}
	}
}

func TestListTags(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	head, err := srcRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}

	// Create lightweight tags
	if _, err := srcRepo.CreateTag("v1.0.0", head.Hash(), nil); err != nil {
		t.Fatalf("create tag v1.0.0: %v", err)
	}
	if _, err := srcRepo.CreateTag("v1.1.0", head.Hash(), nil); err != nil {
		t.Fatalf("create tag v1.1.0: %v", err)
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if _, err := gogit.PlainClone(bareDir, true, &gogit.CloneOptions{URL: srcDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	client := git.NewGoGitClient()
	tags, err := client.ListTags(bareDir)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}

	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	names := make(map[string]bool)
	for _, tag := range tags {
		names[tag.Name] = true
		if !tag.IsTag {
			t.Errorf("expected IsTag=true for %q", tag.Name)
		}
	}
	if !names["v1.0.0"] || !names["v1.1.0"] {
		t.Errorf("expected tags v1.0.0 and v1.1.0, got %v", names)
	}
}

func TestHeadCommit(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	hash, err := client.HeadCommit(bareDir)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	if len(hash) != 40 {
		t.Errorf("expected 40-char hash, got %q", hash)
	}
}

func TestDefaultBranch(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	branch, err := client.DefaultBranch(bareDir)
	if err != nil {
		t.Fatalf("DefaultBranch: %v", err)
	}
	if branch != "master" {
		t.Errorf("expected master, got %q", branch)
	}
}

func TestReadBlob(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	data, err := client.ReadBlob(bareDir, "HEAD", "skills/code-review/SKILL.md")
	if err != nil {
		t.Fatalf("ReadBlob: %v", err)
	}

	content := string(data)
	if len(content) == 0 {
		t.Error("expected non-empty content")
	}
	if !strings.Contains(content, "code-review") {
		t.Errorf("expected content to contain 'code-review', got %q", content)
	}
}

func TestReadBlob_NotFound(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	_, err := client.ReadBlob(bareDir, "HEAD", "nonexistent/file.md")
	if !errors.Is(err, git.ErrBlobNotFound) {
		t.Errorf("expected ErrBlobNotFound, got %v", err)
	}
}

func TestListTree(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	entries, err := client.ListTree(bareDir, "HEAD", "skills")
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries under skills/, got %d", len(entries))
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
		if !e.IsDir {
			t.Errorf("expected directory, got file: %s", e.Name)
		}
	}
	if !names["code-review"] || !names["deploy-helper"] {
		t.Errorf("expected code-review and deploy-helper, got %v", names)
	}
}

func TestListBlobsRecursive(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	blobs, err := client.ListBlobsRecursive(bareDir, "HEAD", "")
	if err != nil {
		t.Fatalf("ListBlobsRecursive: %v", err)
	}

	got := make(map[string]bool)
	for _, b := range blobs {
		if b.IsDir {
			t.Errorf("ListBlobsRecursive must return blobs only, got dir %q", b.Path)
		}
		got[b.Path] = true
		// Name is the basename of the full path.
		if base := b.Path[strings.LastIndex(b.Path, "/")+1:]; b.Name != base {
			t.Errorf("entry %q has Name %q, want %q", b.Path, b.Name, base)
		}
	}
	// Skills live two levels deep — the walk must descend into them and return
	// full repo-relative paths.
	for _, want := range []string{"skills/code-review/SKILL.md", "skills/deploy-helper/SKILL.md"} {
		if !got[want] {
			t.Errorf("expected blob %q in %v", want, got)
		}
	}
}

func TestListBlobsRecursive_Subpath(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	blobs, err := client.ListBlobsRecursive(bareDir, "HEAD", "skills/code-review")
	if err != nil {
		t.Fatalf("ListBlobsRecursive: %v", err)
	}
	if len(blobs) != 1 || blobs[0].Path != "skills/code-review/SKILL.md" {
		t.Fatalf("scoped walk = %+v, want only skills/code-review/SKILL.md", blobs)
	}
}

func TestListTree_NotFound(t *testing.T) {
	bareDir := setupTestBareRepo(t, testSkills)

	client := git.NewGoGitClient()
	_, err := client.ListTree(bareDir, "HEAD", "nonexistent")
	if !errors.Is(err, git.ErrTreeNotFound) {
		t.Errorf("expected ErrTreeNotFound, got %v", err)
	}
}

func TestLsRemote(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)

	client := git.NewGoGitClient()
	refs, err := client.LsRemote(context.Background(), srcDir)
	if err != nil {
		t.Fatalf("LsRemote: %v", err)
	}

	if len(refs.Refs) == 0 {
		t.Error("expected at least one ref")
	}

	// Should have HEAD and refs/heads/master
	if _, ok := refs.Refs["HEAD"]; !ok {
		t.Error("expected HEAD ref")
	}
}

func TestResolveRef_Tag(t *testing.T) {
	srcDir := setupTestRepo(t, testSkills)
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	head, err := srcRepo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if _, err := srcRepo.CreateTag("v2.0.0", head.Hash(), nil); err != nil {
		t.Fatalf("create tag: %v", err)
	}

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if _, err := gogit.PlainClone(bareDir, true, &gogit.CloneOptions{URL: srcDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	client := git.NewGoGitClient()
	data, err := client.ReadBlob(bareDir, "v2.0.0", "skills/code-review/SKILL.md")
	if err != nil {
		t.Fatalf("ReadBlob via tag: %v", err)
	}
	if !strings.Contains(string(data), "code-review") {
		t.Error("expected content via tag ref")
	}
}

// TestRemoteDefaultBranch_ReturnsSymrefHeadBranch covers issue #95:
// publish needs the remote's authoritative default branch (what `git
// ls-remote --symref <url> HEAD` reports) rather than the stale
// entry.Ref fallback. setupBareRegistry already wires HEAD →
// refs/heads/main, so the helper should return "main".
func TestRemoteDefaultBranch_ReturnsSymrefHeadBranch(t *testing.T) {
	bare := setupBareRegistry(t, testSkills)

	client := git.NewGoGitClient()
	got, err := client.RemoteDefaultBranch(context.Background(), bare)
	if err != nil {
		t.Fatalf("RemoteDefaultBranch: %v", err)
	}
	if got != "main" {
		t.Errorf("RemoteDefaultBranch = %q, want %q (issue #95 — sanity check that --symref is parsed)", got, "main")
	}
}

// TestRemoteDefaultBranch_HonoursCustomDefault is the load-bearing
// regression for issue #95: when upstream has been renamed (e.g.
// trunk-based development), publish must follow what the remote
// currently considers its default — not the cached entry.Ref label
// from the original install.
func TestRemoteDefaultBranch_HonoursCustomDefault(t *testing.T) {
	bare := setupBareRegistry(t, testSkills)
	repo, err := gogit.PlainOpen(bare)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	// Mirror what `git branch -m main trunk && git symbolic-ref HEAD refs/heads/trunk`
	// does — copy the main hash onto a trunk ref, repoint HEAD to it, drop the
	// old main ref. setupBareRegistry created HEAD → refs/heads/main; this
	// renames it on-disk so the symref helper sees "trunk".
	mainRef, err := repo.Reference(plumbing.NewBranchReferenceName("main"), false)
	if err != nil {
		t.Fatalf("read main: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("trunk"), mainRef.Hash(),
	)); err != nil {
		t.Fatalf("set trunk: %v", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("trunk"),
	)); err != nil {
		t.Fatalf("repoint HEAD: %v", err)
	}
	if err := repo.Storer.RemoveReference(plumbing.NewBranchReferenceName("main")); err != nil {
		t.Fatalf("remove main: %v", err)
	}

	client := git.NewGoGitClient()
	got, err := client.RemoteDefaultBranch(context.Background(), bare)
	if err != nil {
		t.Fatalf("RemoteDefaultBranch: %v", err)
	}
	if got != "trunk" {
		t.Errorf("RemoteDefaultBranch = %q, want %q — publish would otherwise pick a stale entry.Ref (issue #95)", got, "trunk")
	}
}

// TestParseSymrefHead_HandlesEmptyAndNonBranchHEAD documents the two
// "no signal" paths the parser must return "" for: a remote with no
// symref header at all (empty repo), and a HEAD that points at a
// non-branch (detached or tag). Both leave the caller to fall through
// to entry.Ref / "main" rather than seeding a nonsense branch.
func TestRemoteDefaultBranch_EmptyRepo(t *testing.T) {
	bareDir := filepath.Join(t.TempDir(), "empty.git")
	if _, err := gogit.PlainInit(bareDir, true); err != nil {
		t.Fatalf("init empty bare: %v", err)
	}
	client := git.NewGoGitClient()
	got, err := client.RemoteDefaultBranch(context.Background(), bareDir)
	if err != nil {
		t.Fatalf("RemoteDefaultBranch on empty repo: %v — want (\"\", nil) so caller falls through", err)
	}
	if got != "" {
		t.Errorf("RemoteDefaultBranch on empty repo = %q, want empty", got)
	}
}

// mkGraphCommit writes a unique body and commits it with explicit parents and a
// fixed time, so a test can build an arbitrary DAG (including merges) without
// branch checkouts. The tree differs each call (unique body), so go-git never
// rejects an empty commit.
func mkGraphCommit(t *testing.T, wt *gogit.Worktree, dir, body, msg string, when time.Time, parents []plumbing.Hash) plumbing.Hash {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := wt.Add("f.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	sig := &object.Signature{Name: "Test", Email: "t@t", When: when}
	h, err := wt.Commit(msg, &gogit.CommitOptions{Author: sig, Committer: sig, Parents: parents})
	if err != nil {
		t.Fatalf("commit %q: %v", msg, err)
	}
	return h
}

// TestCommitGraph builds a diamond DAG (A → B, A → C, merge M of B+C) and
// asserts CommitGraph returns every node with correct parent edges, honors the
// limit bound, sorts newest-first, and tolerates an unresolvable tip.
func TestCommitGraph(t *testing.T) {
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	a := mkGraphCommit(t, wt, dir, "a", "A", base, nil)
	b := mkGraphCommit(t, wt, dir, "b", "B", base.Add(1*time.Hour), []plumbing.Hash{a})
	c := mkGraphCommit(t, wt, dir, "c", "C", base.Add(2*time.Hour), []plumbing.Hash{a})
	m := mkGraphCommit(t, wt, dir, "m", "M", base.Add(3*time.Hour), []plumbing.Hash{b, c})

	client := git.NewGoGitClient()
	nodes, err := client.CommitGraph(dir, []string{m.String()}, 0)
	if err != nil {
		t.Fatalf("CommitGraph: %v", err)
	}
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d (%+v)", len(nodes), nodes)
	}

	byHash := map[string]git.CommitNode{}
	for _, n := range nodes {
		byHash[n.Hash] = n
	}
	assertCommitParents(t, byHash, "A", a)
	assertCommitParents(t, byHash, "B", b, a)
	assertCommitParents(t, byHash, "C", c, a)
	assertCommitParents(t, byHash, "M", m, b, c)

	// Newest-first ordering: M is newest, A oldest.
	if nodes[0].Hash != m.String() || nodes[len(nodes)-1].Hash != a.String() {
		t.Errorf("not sorted newest-first: head=%s tail=%s", nodes[0].Hash, nodes[len(nodes)-1].Hash)
	}
}

// assertCommitParents checks one node's parent edges against the expected hashes.
func assertCommitParents(t *testing.T, byHash map[string]git.CommitNode, name string, h plumbing.Hash, want ...plumbing.Hash) {
	t.Helper()
	n, ok := byHash[h.String()]
	if !ok {
		t.Fatalf("%s (%s) missing from graph", name, h)
	}
	var wantStr []string
	for _, w := range want {
		wantStr = append(wantStr, w.String())
	}
	if strings.Join(n.Parents, ",") != strings.Join(wantStr, ",") {
		t.Errorf("%s parents = %v, want %v", name, n.Parents, wantStr)
	}
}

// TestCommitGraphBounds covers the walk's edge behaviors: the limit bound, an
// unresolvable tip, and a 7-char short-SHA tip (skill spans record commits
// abbreviated, so resolving them is what lets the lineage graph render).
func TestCommitGraphBounds(t *testing.T) {
	dir := t.TempDir()
	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	a := mkGraphCommit(t, wt, dir, "a", "A", base, nil)
	b := mkGraphCommit(t, wt, dir, "b", "B", base.Add(time.Hour), []plumbing.Hash{a})

	client := git.NewGoGitClient()

	bounded, err := client.CommitGraph(dir, []string{b.String()}, 1)
	if err != nil {
		t.Fatalf("CommitGraph bounded: %v", err)
	}
	if len(bounded) != 1 {
		t.Errorf("expected 1 bounded node, got %d", len(bounded))
	}

	empty, err := client.CommitGraph(dir, []string{plumbing.ZeroHash.String()}, 0)
	if err != nil {
		t.Fatalf("CommitGraph zero tip: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("expected empty graph for zero tip, got %d", len(empty))
	}

	shortGraph, err := client.CommitGraph(dir, []string{b.String()[:7]}, 0)
	if err != nil {
		t.Fatalf("CommitGraph short tip: %v", err)
	}
	if len(shortGraph) != 2 {
		t.Errorf("short-SHA tip should walk the full chain (2 nodes), got %d", len(shortGraph))
	}
}
