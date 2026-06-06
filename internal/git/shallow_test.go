package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quiver-cli/qvr/internal/git"
)

// buildVersionedRepo creates a source repo (via system git) with tagged
// versions whose tagged commits sit BEHIND the branch tip, plus a second
// branch. Lets the clone-mode tests assert exactly which refs each mode pulls.
func buildVersionedRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(content string) {
		t.Helper()
		p := filepath.Join(dir, "skills", "demo")
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(p, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	run("init", "-q", "-b", "main")
	write("---\nname: demo\ndescription: v1\n---\nv1\n")
	run("add", "-A")
	run("commit", "-qm", "c1")
	run("tag", "v1.0.0")
	write("---\nname: demo\ndescription: v2\n---\nv2\n")
	run("add", "-A")
	run("commit", "-qm", "c2")
	run("tag", "v2.0.0")
	run("branch", "dev")
	write("---\nname: demo\ndescription: v3\n---\nv3\n")
	run("add", "-A")
	run("commit", "-qm", "c3")
	return dir
}

func refNames(t *testing.T, bare string) (branches, tags map[string]bool) {
	t.Helper()
	g := git.NewGoGitClient()
	bs, err := g.ListBranches(bare)
	if err != nil {
		t.Fatalf("ListBranches: %v", err)
	}
	ts, err := g.ListTags(bare)
	if err != nil {
		t.Fatalf("ListTags: %v", err)
	}
	branches, tags = map[string]bool{}, map[string]bool{}
	for _, b := range bs {
		branches[b.Name] = true
	}
	for _, x := range ts {
		tags[x.Name] = true
	}
	return branches, tags
}

// TestBareClone_DefaultBranchOnly is the core cold-start contract: the default
// (AllRefs=false) clone fetches ONLY the remote's default branch — no tags, no
// other branches — yet still indexes every skill (skills live on the default
// branch) and can serve a worktree of the latest. Versions are absent on
// purpose: that's what triggers the "--full" diagnostic.
func TestBareClone_DefaultBranchOnly(t *testing.T) {
	requireSystemGit(t)
	src := buildVersionedRepo(t)
	url := "file://" + src // file:// so git honours --depth
	dest := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), url, dest, git.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("default BareClone: %v", err)
	}

	branches, tags := refNames(t, dest)
	if !branches["main"] {
		t.Errorf("default branch 'main' missing (got branches %v)", branches)
	}
	if branches["dev"] {
		t.Errorf("non-default branch 'dev' should NOT be cloned (got %v)", branches)
	}
	if len(tags) != 0 {
		t.Errorf("no tags should be cloned in default mode, got %v", tags)
	}
	if _, err := os.Stat(filepath.Join(dest, "shallow")); err != nil {
		t.Errorf("default clone should be shallow: %v", err)
	}
	if git.IsFullClone(dest) {
		t.Errorf("default clone must not report as a full clone")
	}
	// The indexer reads SKILL.md at HEAD — must be present.
	if b, err := client.ReadBlob(dest, "HEAD", "skills/demo/SKILL.md"); err != nil || len(b) == 0 {
		t.Errorf("ReadBlob HEAD: b=%d err=%v", len(b), err)
	}
	// A worktree of the latest works; a tag cannot be resolved (→ diagnostic).
	wt := git.NewGoGitWorktree()
	wtPath := filepath.Join(t.TempDir(), "wt-main")
	if err := wt.Add(dest, wtPath, "main"); err != nil {
		t.Fatalf("worktree add main: %v", err)
	}
	if _, err := client.ResolveRef(dest, "v1.0.0"); err == nil {
		t.Errorf("expected v1.0.0 to be unresolvable in default-branch clone")
	}
	// Updates must be wired up (git leaves the refspec empty for bare
	// single-branch clones; BareClone fixes that).
	if err := client.Fetch(context.Background(), dest); err != nil {
		t.Errorf("Fetch on default-branch clone: %v", err)
	}
}

// TestBareClone_Full verifies --full (AllRefs) pulls every branch and tag
// so any version is installable, and reports as a full clone.
func TestBareClone_Full(t *testing.T) {
	requireSystemGit(t)
	src := buildVersionedRepo(t)
	dest := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), "file://"+src, dest, git.CloneOptions{AllRefs: true}); err != nil {
		t.Fatalf("full BareClone: %v", err)
	}
	branches, tags := refNames(t, dest)
	for _, want := range []string{"main", "dev"} {
		if !branches[want] {
			t.Errorf("full clone missing branch %q (got %v)", want, branches)
		}
	}
	for _, want := range []string{"v1.0.0", "v2.0.0"} {
		if !tags[want] {
			t.Errorf("full clone missing tag %q (got %v)", want, tags)
		}
	}
	if !git.IsFullClone(dest) {
		t.Errorf("full clone should report as a full clone")
	}
	// A pinned older version resolves and checks out.
	if _, err := client.ResolveRef(dest, "v1.0.0"); err != nil {
		t.Errorf("v1.0.0 should resolve in a full clone: %v", err)
	}
}

// allRefs lists every ref in a bare repo as `name -> objectname` via
// for-each-ref, so a test can assert on namespaces (e.g. refs/pull/*) that
// ListBranches/ListTags don't surface.
func allRefs(t *testing.T, bare string) []string {
	t.Helper()
	cmd := exec.Command("git", "-C", bare, "for-each-ref", "--format=%(refname)")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("for-each-ref: %v\n%s", err, out)
	}
	var refs []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			refs = append(refs, line)
		}
	}
	return refs
}

// TestBareClone_FullExcludesPullRefs is the regression guard for the --full
// slowdown: a full clone must take branches and tags but NOT the remote's
// refs/pull/* PR refs (GitHub maps one or two per PR ever opened — thousands on
// a busy repo — and `--mirror` used to drag them all in). The source repo gets
// a synthetic refs/pull/1/head so we can assert the clone leaves it behind.
func TestBareClone_FullExcludesPullRefs(t *testing.T) {
	requireSystemGit(t)
	src := buildVersionedRepo(t)

	// Plant a PR-shaped ref upstream pointing at a real commit.
	plant := exec.Command("git", "-C", src, "update-ref", "refs/pull/1/head", "refs/heads/dev")
	if out, err := plant.CombinedOutput(); err != nil {
		t.Fatalf("plant pull ref: %v\n%s", err, out)
	}

	dest := filepath.Join(t.TempDir(), "bare.git")
	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), "file://"+src, dest, git.CloneOptions{AllRefs: true}); err != nil {
		t.Fatalf("full BareClone: %v", err)
	}

	for _, ref := range allRefs(t, dest) {
		if strings.HasPrefix(ref, "refs/pull/") {
			t.Errorf("full clone pulled a PR ref it should have skipped: %s", ref)
		}
	}
	// Sanity: branches and tags still made it.
	branches, tags := refNames(t, dest)
	if !branches["dev"] || !tags["v1.0.0"] {
		t.Errorf("full clone dropped expected refs (branches=%v tags=%v)", branches, tags)
	}

	// And the exclusion must survive an update: a newly-planted PR ref upstream
	// must not appear after Fetch (the configured refspec has no refs/pull/*).
	plant2 := exec.Command("git", "-C", src, "update-ref", "refs/pull/2/head", "refs/heads/main")
	if out, err := plant2.CombinedOutput(); err != nil {
		t.Fatalf("plant second pull ref: %v\n%s", err, out)
	}
	if err := client.Fetch(context.Background(), dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	for _, ref := range allRefs(t, dest) {
		if strings.HasPrefix(ref, "refs/pull/") {
			t.Errorf("update pulled a PR ref it should have skipped: %s", ref)
		}
	}
}

// TestDeepenToFull_InPlace is the core of the in-place `--full` deepen (#184):
// a latest-only (shallow, single-branch) clone becomes a full clone — all
// branches, all tags, unshallowed, but still no PR refs — without re-cloning.
// IsFullClone must flip false→true, and a previously-unresolvable tag must
// resolve afterward.
func TestDeepenToFull_InPlace(t *testing.T) {
	requireSystemGit(t)
	src := buildVersionedRepo(t)

	// A PR ref upstream that the deepen must continue to ignore.
	plant := exec.Command("git", "-C", src, "update-ref", "refs/pull/7/head", "refs/heads/dev")
	if out, err := plant.CombinedOutput(); err != nil {
		t.Fatalf("plant pull ref: %v\n%s", err, out)
	}

	dest := filepath.Join(t.TempDir(), "bare.git")
	client := git.NewGoGitClient()

	// Start latest-only: one branch, no tags, shallow — and a tag unresolvable.
	if err := client.BareClone(context.Background(), "file://"+src, dest, git.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("default BareClone: %v", err)
	}
	if git.IsFullClone(dest) {
		t.Fatalf("precondition: latest-only clone must not report full")
	}
	if _, err := client.ResolveRef(dest, "v1.0.0"); err == nil {
		t.Fatalf("precondition: v1.0.0 must be unresolvable before deepen")
	}

	// Deepen in place.
	if err := client.DeepenToFull(context.Background(), dest); err != nil {
		t.Fatalf("DeepenToFull: %v", err)
	}

	if !git.IsFullClone(dest) {
		t.Errorf("clone should report full after deepen")
	}
	branches, tags := refNames(t, dest)
	for _, want := range []string{"main", "dev"} {
		if !branches[want] {
			t.Errorf("deepened clone missing branch %q (got %v)", want, branches)
		}
	}
	for _, want := range []string{"v1.0.0", "v2.0.0"} {
		if !tags[want] {
			t.Errorf("deepened clone missing tag %q (got %v)", want, tags)
		}
	}
	if _, err := client.ResolveRef(dest, "v1.0.0"); err != nil {
		t.Errorf("v1.0.0 should resolve after deepen: %v", err)
	}
	for _, ref := range allRefs(t, dest) {
		if strings.HasPrefix(ref, "refs/pull/") {
			t.Errorf("deepen pulled a PR ref it should have skipped: %s", ref)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "shallow")); err == nil {
		t.Errorf("deepened clone should no longer be shallow")
	}
}

// TestDeepenToFull_AlreadyFull verifies deepening a clone that's already full is
// a safe no-op-ish update: it stays full and keeps its branches/tags (does not
// error on the missing shallow marker by passing --unshallow).
func TestDeepenToFull_AlreadyFull(t *testing.T) {
	requireSystemGit(t)
	src := buildVersionedRepo(t)
	dest := filepath.Join(t.TempDir(), "bare.git")
	client := git.NewGoGitClient()

	if err := client.BareClone(context.Background(), "file://"+src, dest, git.CloneOptions{AllRefs: true}); err != nil {
		t.Fatalf("full BareClone: %v", err)
	}
	if err := client.DeepenToFull(context.Background(), dest); err != nil {
		t.Fatalf("DeepenToFull on already-full clone: %v", err)
	}
	if !git.IsFullClone(dest) {
		t.Errorf("clone should still report full")
	}
	_, tags := refNames(t, dest)
	if !tags["v1.0.0"] || !tags["v2.0.0"] {
		t.Errorf("already-full deepen dropped tags (got %v)", tags)
	}
}

// TestFetch_DefaultBranchStaysSingleBranch verifies a default-mode registry
// update picks up new commits on the default branch but does NOT start pulling
// other branches, and stays shallow.
func TestFetch_DefaultBranchStaysSingleBranch(t *testing.T) {
	requireSystemGit(t)
	src := buildVersionedRepo(t)
	dest := filepath.Join(t.TempDir(), "bare.git")

	client := git.NewGoGitClient()
	if err := client.BareClone(context.Background(), "file://"+src, dest, git.CloneOptions{Depth: 1}); err != nil {
		t.Fatalf("default BareClone: %v", err)
	}

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = src
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// New commit on main + a brand-new branch upstream.
	if err := os.WriteFile(filepath.Join(src, "skills", "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: v4\n---\nv4\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "-A")
	run("commit", "-qm", "c4")
	run("branch", "another-branch")

	if err := client.Fetch(context.Background(), dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	branches, _ := refNames(t, dest)
	if branches["another-branch"] || branches["dev"] {
		t.Errorf("fetch pulled extra branches; should stay single-branch (got %v)", branches)
	}
	if _, err := os.Stat(filepath.Join(dest, "shallow")); err != nil {
		t.Errorf("registry should remain shallow after fetch: %v", err)
	}
}
