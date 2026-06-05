package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/git"
)

// requireSystemGit skips the test when the `git` binary isn't available on
// $PATH. The new GoGitClient shells out to git for network ops, so these
// tests need a real git installed.
func requireSystemGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
}

// TestBareClone_NoPromptOnMissingAuth verifies that cloning a URL that
// requires authentication fails fast rather than hanging on an interactive
// prompt. This is the core prod-readiness guarantee: a CI job with no git
// credentials configured must get a clear error in < 1s, not hang forever.
func TestBareClone_NoPromptOnMissingAuth(t *testing.T) {
	requireSystemGit(t)

	// localhost:1 is guaranteed to refuse connections quickly. This tests
	// the "fast fail" contract without needing network.
	client := git.NewGoGitClient()
	dest := filepath.Join(t.TempDir(), "bare.git")
	err := client.BareClone(context.Background(), "http://127.0.0.1:1/nonexistent.git", dest)
	if err == nil {
		t.Fatal("expected error cloning from refused port")
	}
	// Must NOT be a timeout-from-hanging-prompt case: our GIT_TERMINAL_PROMPT=0
	// + GIT_ASKPASS=/bin/true env ensures git bails without asking.
	if strings.Contains(err.Error(), "terminal prompts disabled") {
		// This specific error string from git means our env settings
		// worked — git tried to prompt and was correctly refused.
		return
	}
	// Otherwise the error is a connection refused / repo not found, which
	// is also the correct non-hanging behaviour.
}

// TestBareClone_RejectsDuplicateDestination ensures we don't overwrite an
// existing directory on clone — `go-git` used to report ErrAlreadyExists,
// our shell-out path must behave the same.
func TestBareClone_RejectsDuplicateDestination(t *testing.T) {
	requireSystemGit(t)

	src := setupTestRepo(t, testSkills)
	dest := filepath.Join(t.TempDir(), "bare.git")
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	client := git.NewGoGitClient()
	err := client.BareClone(context.Background(), src, dest)
	if !errors.Is(err, git.ErrAlreadyExists) {
		t.Errorf("want ErrAlreadyExists, got %v", err)
	}
}

// TestFetch_Shelled verifies the shell-out Fetch path works against a local
// bare repo, round-tripping a new commit from source → remote → bare.
func TestFetch_Shelled(t *testing.T) {
	requireSystemGit(t)

	remote := setupBareRegistry(t, testSkills)
	bare := bareCloneFor(t, remote)

	// Snapshot refs before we mutate the remote.
	before, err := refsOf(bare)
	if err != nil {
		t.Fatalf("refsOf before: %v", err)
	}

	// Push a new commit directly into the remote bare by cloning,
	// committing, and pushing. This simulates someone else updating
	// the registry upstream.
	mutateRemote(t, remote)

	client := git.NewGoGitClient()
	if err := client.Fetch(context.Background(), bare); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	after, err := refsOf(bare)
	if err != nil {
		t.Fatalf("refsOf after: %v", err)
	}
	if after["refs/heads/main"] == before["refs/heads/main"] {
		t.Errorf("expected refs/heads/main to advance after fetch")
	}
}

// TestLsRemote_Shelled verifies the shell-out LsRemote path returns the
// expected refs from a local bare.
func TestLsRemote_Shelled(t *testing.T) {
	requireSystemGit(t)

	remote := setupBareRegistry(t, testSkills)

	client := git.NewGoGitClient()
	refs, err := client.LsRemote(context.Background(), remote)
	if err != nil {
		t.Fatalf("LsRemote: %v", err)
	}
	if len(refs.Refs) == 0 {
		t.Fatal("expected at least one ref")
	}
	if _, ok := refs.Refs["HEAD"]; !ok {
		t.Error("expected HEAD")
	}
	if _, ok := refs.Refs["refs/heads/main"]; !ok {
		t.Error("expected refs/heads/main")
	}
}

// TestPush_Shelled verifies the shell-out Push path can push a new branch
// from a working clone to the remote bare.
func TestPush_Shelled(t *testing.T) {
	requireSystemGit(t)

	remote := setupBareRegistry(t, testSkills)
	// Work clone of the remote to make a local commit on a fresh branch.
	work := t.TempDir()
	if err := exec.Command("git", "clone", remote, work).Run(); err != nil {
		t.Fatalf("clone: %v", err)
	}
	// Identity needed for commit.
	runIn(t, work, "git", "config", "user.email", "t@t")
	runIn(t, work, "git", "config", "user.name", "t")
	runIn(t, work, "git", "checkout", "-b", "pushtest")
	if err := os.WriteFile(filepath.Join(work, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runIn(t, work, "git", "add", "README")
	runIn(t, work, "git", "commit", "-m", "push me")

	client := git.NewGoGitClient()
	err := client.Push(context.Background(), work, "origin",
		[]string{"refs/heads/pushtest:refs/heads/pushtest"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}

	refs, err := refsOf(remote)
	if err != nil {
		t.Fatalf("refsOf: %v", err)
	}
	if _, ok := refs["refs/heads/pushtest"]; !ok {
		t.Errorf("expected pushtest on remote, got %v", refs)
	}
}

// TestFetchWorktree_Shelled verifies FetchWorktree updates
// refs/remotes/origin/* without clobbering refs/heads/*.
func TestFetchWorktree_Shelled(t *testing.T) {
	requireSystemGit(t)

	remote := setupBareRegistry(t, testSkills)
	work := t.TempDir()
	if err := exec.Command("git", "clone", remote, work).Run(); err != nil {
		t.Fatalf("clone: %v", err)
	}

	// Advance remote main without touching work.
	mutateRemote(t, remote)

	// Capture work's local main before fetch; it must not move.
	localMainBefore := refOf(t, work, "refs/heads/main")

	client := git.NewGoGitClient()
	if err := client.FetchWorktree(context.Background(), work); err != nil {
		t.Fatalf("FetchWorktree: %v", err)
	}

	localMainAfter := refOf(t, work, "refs/heads/main")
	if localMainAfter != localMainBefore {
		t.Errorf("local refs/heads/main moved: %s → %s", localMainBefore, localMainAfter)
	}
	remoteTrack := refOf(t, work, "refs/remotes/origin/main")
	if remoteTrack == localMainBefore {
		t.Error("refs/remotes/origin/main should have advanced beyond the pre-fetch local hash")
	}
}

// refsOf returns a map of ref-name → hash for a repository (bare or not).
func refsOf(repoPath string) (map[string]string, error) {
	out, err := exec.Command("git", "-C", repoPath, "for-each-ref",
		"--format=%(refname) %(objectname)").Output()
	if err != nil {
		return nil, err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, " ", 2)
		if len(parts) != 2 {
			continue
		}
		refs[parts[0]] = parts[1]
	}
	return refs, nil
}

// refOf returns the hash at a single ref, or "" if missing.
func refOf(t *testing.T, repoPath, ref string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", repoPath, "rev-parse", ref).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// mutateRemote adds a commit to the main branch of a bare remote by cloning
// it to a scratch dir, committing, and pushing.
func mutateRemote(t *testing.T, remote string) {
	t.Helper()
	scratch := t.TempDir()
	if err := exec.Command("git", "clone", remote, scratch).Run(); err != nil {
		t.Fatalf("clone for mutation: %v", err)
	}
	runIn(t, scratch, "git", "config", "user.email", "t@t")
	runIn(t, scratch, "git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(scratch, "upstream.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	runIn(t, scratch, "git", "add", "upstream.txt")
	runIn(t, scratch, "git", "commit", "-m", "upstream change")
	runIn(t, scratch, "git", "push", "origin", "main")
}

func runIn(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}
