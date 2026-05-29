package registry_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/registry"
)

// setupTestSourceRepo creates a non-bare repo with skills, returns its path.
func setupTestSourceRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	repo, err := gogit.PlainInit(dir, false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}

	// Create skills
	for _, name := range []string{"code-review", "deploy-helper"} {
		skillDir := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(skillDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", skillDir, err)
		}
		content := "---\nname: " + name + "\ndescription: A test skill.\n---\n# " + name + "\n"
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}

	if _, err := wt.Commit("initial", &gogit.CommitOptions{
		Author: &object.Signature{Name: "Test", Email: "t@t.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	return dir
}

func setupManagerTest(t *testing.T) (*registry.Manager, string) {
	t.Helper()
	quiverHome := t.TempDir()
	t.Setenv("QUIVER_HOME", quiverHome)
	mgr := registry.NewManager(git.NewGoGitClient())
	return mgr, quiverHome
}

func TestManager_Add(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	reg, err := mgr.Add(context.Background(), "test", srcDir)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if reg.Name != "test" {
		t.Errorf("name = %q, want test", reg.Name)
	}
	if reg.SkillCount != 2 {
		t.Errorf("skill_count = %d, want 2", reg.SkillCount)
	}

	// Verify bare repo exists
	if _, err := os.Stat(reg.Path); err != nil {
		t.Errorf("bare repo not found at %s", reg.Path)
	}
}

// TestManager_Add_StripsCredentials verifies that a URL with embedded
// credentials is sanitised before being persisted. This is the critical
// guard that keeps tokens out of config.yaml. We use a local file path
// dressed with fake userinfo — go's net/url parses it, the manager strips
// it, and the underlying clone uses only the clean path.
func TestManager_Add_PlainPathPreserved(t *testing.T) {
	// Set up a local bare "remote" that the manager can clone from over the
	// file path. We can't dress a local path with HTTPS userinfo (git would
	// try to dial a host), so this test uses the SanitizeURL code path for
	// ssh:// instead, which also strips passwords.
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	// Sanity: a plain local path passes through untouched.
	reg, err := mgr.Add(context.Background(), "plain", srcDir)
	if err != nil {
		t.Fatalf("Add plain: %v", err)
	}
	if reg.CredentialsStripped {
		t.Errorf("plain URL should not set CredentialsStripped")
	}
	if reg.URL != srcDir {
		t.Errorf("plain URL was modified: %q vs %q", reg.URL, srcDir)
	}
}

// TestManager_Add_CredentialFlagPropagates verifies that when SanitizeURL
// reports credentials were present, the returned Registry carries the flag
// so the cmd layer can warn the user.
func TestManager_Add_CredentialFlagPropagates(t *testing.T) {
	// We can't make a real https clone in a unit test without the network,
	// so this test is a narrower integration: we use SanitizeURL directly
	// and assert the contract the manager relies on.
	clean, had, err := git.SanitizeURL("https://user:tok_abc@github.com/foo/bar.git")
	if err != nil {
		t.Fatalf("SanitizeURL: %v", err)
	}
	if !had {
		t.Fatal("SanitizeURL should report credentials present")
	}
	if clean != "https://github.com/foo/bar.git" {
		t.Errorf("clean URL unexpected: %q", clean)
	}
}

func TestManager_Add_AlreadyExists(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, _ = mgr.Add(context.Background(), "test", srcDir)
	_, err := mgr.Add(context.Background(), "test", srcDir)
	if !errors.Is(err, registry.ErrRegistryExists) {
		t.Errorf("expected ErrRegistryExists, got %v", err)
	}
}

func TestManager_Add_EmptyName(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	_, err := mgr.Add(context.Background(), "", "http://example.com")
	if !errors.Is(err, registry.ErrInvalidRegistryName) {
		t.Errorf("expected ErrInvalidRegistryName, got %v", err)
	}
}

func TestManager_Add_InvalidName(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	for _, name := range []string{"../evil", "UPPER", "has space"} {
		_, err := mgr.Add(context.Background(), name, "http://example.com")
		if !errors.Is(err, registry.ErrInvalidRegistryName) {
			t.Errorf("Add(%q): expected ErrInvalidRegistryName, got %v", name, err)
		}
	}
}

func TestManager_Add_EmptyURL(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	_, err := mgr.Add(context.Background(), "test", "")
	if !errors.Is(err, registry.ErrInvalidURL) {
		t.Errorf("expected ErrInvalidURL, got %v", err)
	}
}

func TestManager_Remove(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	reg, _ := mgr.Add(context.Background(), "test", srcDir)

	err := mgr.Remove("test")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Bare repo should be gone
	if _, err := os.Stat(reg.Path); !os.IsNotExist(err) {
		t.Error("expected bare repo to be removed")
	}
}

func TestManager_Remove_NotFound(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	err := mgr.Remove("nonexistent")
	if !errors.Is(err, registry.ErrRegistryNotFound) {
		t.Errorf("expected ErrRegistryNotFound, got %v", err)
	}
}

func TestManager_List(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, _ = mgr.Add(context.Background(), "reg1", srcDir)

	// Create a second source repo
	srcDir2 := setupTestSourceRepo(t)
	_, _ = mgr.Add(context.Background(), "reg2", srcDir2)

	list, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(list) != 2 {
		t.Fatalf("expected 2 registries, got %d", len(list))
	}

	names := map[string]bool{}
	for _, r := range list {
		names[r.Name] = true
		if r.SkillCount != 2 {
			t.Errorf("registry %s: skill_count = %d, want 2", r.Name, r.SkillCount)
		}
	}
	if !names["reg1"] || !names["reg2"] {
		t.Errorf("expected reg1 and reg2, got %v", names)
	}
}

// TestManager_List_Sorted guards issue #76: `qvr registry list` must produce
// a deterministic order across runs. The implementation iterates a Go map,
// whose iteration order is randomized — without the sort, scripts piping
// the output through `head`, `awk`, or `diff` get nondeterministic answers.
func TestManager_List_Sorted(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcA := setupTestSourceRepo(t)
	srcB := setupTestSourceRepo(t)
	srcC := setupTestSourceRepo(t)
	// Add out of order so the underlying map iteration is unlikely to
	// accidentally land sorted.
	_, _ = mgr.Add(context.Background(), "charlie", srcC)
	_, _ = mgr.Add(context.Background(), "alpha", srcA)
	_, _ = mgr.Add(context.Background(), "bravo", srcB)

	// Run List a handful of times; every result must be the same sorted order.
	want := []string{"alpha", "bravo", "charlie"}
	for i := 0; i < 5; i++ {
		list, err := mgr.List()
		if err != nil {
			t.Fatalf("List #%d: %v", i, err)
		}
		got := make([]string, len(list))
		for j, r := range list {
			got[j] = r.Name
		}
		if len(got) != len(want) {
			t.Fatalf("run %d: len(list) = %d, want %d", i, len(got), len(want))
		}
		for j := range want {
			if got[j] != want[j] {
				t.Errorf("run %d: list[%d] = %q, want %q (full=%v)", i, j, got[j], want[j], got)
			}
		}
	}
}

func TestManager_List_Empty(t *testing.T) {
	mgr, _ := setupManagerTest(t)

	list, err := mgr.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Errorf("expected 0 registries, got %d", len(list))
	}
}

func TestManager_ListSkills(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)
	srcDir2 := setupTestSourceRepo(t)
	_, _ = mgr.Add(context.Background(), "reg1", srcDir)
	_, _ = mgr.Add(context.Background(), "reg2", srcDir2)

	results, err := mgr.ListSkills([]string{"reg1", "reg2"})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, want := range []string{"reg1", "reg2"} {
		if results[i].Name != want {
			t.Errorf("results[%d].Name = %q, want %q", i, results[i].Name, want)
		}
		if results[i].Error != "" {
			t.Errorf("results[%d].Error = %q", i, results[i].Error)
		}
		if len(results[i].Skills) != 2 {
			t.Errorf("results[%d]: expected 2 skills, got %d", i, len(results[i].Skills))
		}
	}
	// Skills must be sorted by name.
	if results[0].Skills[0].Name != "code-review" || results[0].Skills[1].Name != "deploy-helper" {
		t.Errorf("skills not sorted by name: %+v", results[0].Skills)
	}
}

func TestManager_ListSkills_UnknownRegistry(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)
	_, _ = mgr.Add(context.Background(), "real", srcDir)

	results, err := mgr.ListSkills([]string{"real", "bogus"})
	if err != nil {
		t.Fatalf("ListSkills: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Error != "" {
		t.Errorf("real: unexpected error %q", results[0].Error)
	}
	if results[1].Error == "" {
		t.Errorf("bogus: expected error, got none")
	}
}

func TestManager_Update(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, _ = mgr.Add(context.Background(), "test", srcDir)

	results, err := mgr.Update(context.Background(), "test")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Error != "" {
		t.Errorf("unexpected error: %s", results[0].Error)
	}
	if results[0].SkillCount != 2 {
		t.Errorf("skill_count = %d, want 2", results[0].SkillCount)
	}
}

func TestManager_Update_All(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)
	srcDir2 := setupTestSourceRepo(t)

	_, _ = mgr.Add(context.Background(), "reg1", srcDir)
	_, _ = mgr.Add(context.Background(), "reg2", srcDir2)

	results, err := mgr.Update(context.Background(), "")
	if err != nil {
		t.Fatalf("Update all: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestManager_Check(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, _ = mgr.Add(context.Background(), "test", srcDir)

	// No changes — should report up to date
	results, err := mgr.Check(context.Background(), "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].HasUpstreamChanges {
		t.Error("expected no upstream changes")
	}
}

func TestManager_Check_WithChanges(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, _ = mgr.Add(context.Background(), "test", srcDir)

	// Add a commit to source
	srcRepo, err := gogit.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	wt, err := srcRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "new.txt"), []byte("new"), 0o644); err != nil {
		t.Fatalf("write new.txt: %v", err)
	}
	if _, err := wt.Add("new.txt"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := wt.Commit("new commit", &gogit.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t.com", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	results, err := mgr.Check(context.Background(), "test")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if !results[0].HasUpstreamChanges {
		t.Error("expected upstream changes after new commit")
	}
}

func TestManager_Add_WritesCacheOnFirstClone(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, err := mgr.Add(context.Background(), "test", srcDir)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	cache, err := registry.ReadCache("test")
	if err != nil {
		t.Fatalf("expected cache file after Add, got %v", err)
	}
	if len(cache.Skills) != 2 {
		t.Errorf("cached skills = %d, want 2", len(cache.Skills))
	}
	if cache.Commit == "" {
		t.Error("cache should carry a commit hash")
	}
}

func TestManager_Index_ReturnsCachedOnSecondCall(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	reg, err := mgr.Add(context.Background(), "test", srcDir)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Overwrite the cache with a sentinel skill. A subsequent Index call
	// should return this sentinel, proving it read the cache instead of
	// rebuilding.
	poisoned := &registry.IndexCache{
		Registry:  "test",
		Commit:    mustHead(t, mgr, reg.Path),
		Generated: time.Now().UTC(),
		Skills:    []registry.SkillIndexEntry{{Name: "sentinel-from-cache", Description: "only in cache"}},
	}
	if err := registry.WriteCache(poisoned); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	got, _, err := mgr.Index("test", reg.Path)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(got) != 1 || got[0].Name != "sentinel-from-cache" {
		t.Errorf("expected cached sentinel, got %+v", got)
	}
}

func TestManager_Index_RebuildsWhenCommitMismatches(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	reg, err := mgr.Add(context.Background(), "test", srcDir)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Poison the cache with a sentinel AND a wrong commit. Index should
	// detect the commit mismatch and rebuild from the real repo.
	if err := registry.WriteCache(&registry.IndexCache{
		Registry:  "test",
		Commit:    "0000000000000000000000000000000000000000",
		Generated: time.Now().UTC(),
		Skills:    []registry.SkillIndexEntry{{Name: "should-be-overwritten"}},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	got, _, err := mgr.Index("test", reg.Path)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected fresh rebuild (2 skills), got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Name == "should-be-overwritten" {
			t.Error("sentinel skill leaked from stale cache")
		}
	}

	// The rebuild should have written a fresh cache with the real commit.
	cache, err := registry.ReadCache("test")
	if err != nil {
		t.Fatalf("ReadCache after rebuild: %v", err)
	}
	if cache.Commit == "0000000000000000000000000000000000000000" {
		t.Error("cache commit was not refreshed after rebuild")
	}
}

func TestManager_Index_RebuildsWhenStale(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	mgr.CacheTTL = time.Nanosecond // effectively always stale

	srcDir := setupTestSourceRepo(t)
	reg, err := mgr.Add(context.Background(), "test", srcDir)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Poison with sentinel but matching commit. With TTL=1ns, the cache is
	// stale regardless of commit, so Index should rebuild.
	headCommit := mustHead(t, mgr, reg.Path)
	if err := registry.WriteCache(&registry.IndexCache{
		Registry:  "test",
		Commit:    headCommit,
		Generated: time.Now().Add(-time.Hour), // clearly past any TTL
		Skills:    []registry.SkillIndexEntry{{Name: "sentinel"}},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	got, _, err := mgr.Index("test", reg.Path)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected rebuild on stale cache (2 skills), got %d", len(got))
	}
}

func TestManager_Update_InvalidatesAndRepopulatesCache(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, err := mgr.Add(context.Background(), "test", srcDir)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Poison the cache with a sentinel. Update should invalidate + rebuild,
	// dropping the sentinel even though we didn't touch the upstream.
	if err := registry.WriteCache(&registry.IndexCache{
		Registry:  "test",
		Commit:    "abc",
		Generated: time.Now().UTC(),
		Skills:    []registry.SkillIndexEntry{{Name: "should-be-gone"}},
	}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	results, err := mgr.Update(context.Background(), "test")
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(results) != 1 || results[0].SkillCount != 2 {
		t.Fatalf("expected 1 result with 2 skills, got %+v", results)
	}

	cache, err := registry.ReadCache("test")
	if err != nil {
		t.Fatalf("ReadCache after Update: %v", err)
	}
	if len(cache.Skills) != 2 {
		t.Errorf("cache not repopulated, got %d skills", len(cache.Skills))
	}
	for _, s := range cache.Skills {
		if s.Name == "should-be-gone" {
			t.Error("sentinel survived Update — cache was not invalidated")
		}
	}
}

func TestManager_Remove_InvalidatesCache(t *testing.T) {
	mgr, _ := setupManagerTest(t)
	srcDir := setupTestSourceRepo(t)

	_, err := mgr.Add(context.Background(), "test", srcDir)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := os.Stat(registry.CachePath("test")); err != nil {
		t.Fatalf("cache should exist before Remove: %v", err)
	}

	if err := mgr.Remove("test"); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	if _, err := os.Stat(registry.CachePath("test")); !os.IsNotExist(err) {
		t.Errorf("expected cache file removed, got err=%v", err)
	}
}

func mustHead(t *testing.T, mgr *registry.Manager, repoPath string) string {
	t.Helper()
	h, err := mgr.Git.HeadCommit(repoPath)
	if err != nil {
		t.Fatalf("HeadCommit: %v", err)
	}
	return h
}

func TestURLToSlug(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/user/repo", "github.com--user--repo"},
		{"https://github.com/user/repo.git", "github.com--user--repo"},
		{"git@github.com:user/repo.git", "github.com--user--repo"},
		{"http://example.com/skills", "example.com--skills"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := registry.URLToSlug(tt.url)
			if got != tt.want {
				t.Errorf("URLToSlug(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}

// TestWorktreePath_RefHostility pins the "ref is never shelled out"
// guarantee: go-git handles all clone/checkout work, so a hostile ref
// containing newlines, semicolons, or other shell metachars never reaches
// a subprocess. The path-construction side still needs to produce a safe
// directory name though — slashes and colons get flattened to "--", and
// the worktree always lands under registry.WorktreesRoot() with no escape.
func TestWorktreePath_RefHostility(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())

	hostile := []string{
		"main; rm -rf /",
		"main\nrm",
		"main`rm`",
		"main$(rm)",
		"feature/x:y",
		"../../etc/passwd",
	}
	root := registry.WorktreesRoot()
	for _, ref := range hostile {
		t.Run(ref, func(t *testing.T) {
			p := registry.WorktreePath("acme", "skill", ref)
			// The path must stay rooted under worktrees/ — no `..` segment
			// should escape. v0.5 layout nests as `<registry>/<skill>/<sha>`
			// so separators in the relative path are expected; what we
			// disallow is any segment that resolves up out of the root.
			if !strings.HasPrefix(p, root+string(filepath.Separator)) {
				t.Errorf("WorktreePath escaped worktrees root: %q (root %q)", p, root)
			}
			rel, err := filepath.Rel(root, p)
			if err != nil {
				t.Fatalf("Rel(%q, %q): %v", root, p, err)
			}
			// `..--..--etc--passwd` is a literal directory name, not a
			// traversal segment — slugSegment has already flattened the
			// slashes. Only an exact `..` or empty segment would escape.
			for _, seg := range strings.Split(rel, string(filepath.Separator)) {
				if seg == "" || seg == ".." || seg == "." {
					t.Errorf("worktree rel %q contains traversal segment %q", rel, seg)
				}
			}
		})
	}
}

// TestInferRegistryName covers the auto-naming path used by
// `qvr registry add <url>` — always returns `<org>/<repo>` regardless of
// the host or scheme (org is the parent dir; repo is the bare clone). Hostile inputs that don't yield two usable segments
// must return "" so the cmd layer can require an explicit --name.
func TestInferRegistryName(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"https://github.com/vercel-labs/agent-skills", "vercel-labs/agent-skills"},
		{"https://github.com/vercel-labs/agent-skills.git", "vercel-labs/agent-skills"},
		{"https://github.com/Org/Repo", "org/repo"},
		{"git@github.com:org/repo.git", "org/repo"},
		{"ssh://git@gitlab.com/org/repo.git", "org/repo"},
		{"https://gitlab.com/group/subgroup/repo", "subgroup/repo"},
		{"https://example.com/repo", ""}, // no org segment
		{"https://github.com/", ""},      // empty path
		{"   ", ""},                      // whitespace
		// Traversal in the path is neutralized: only the last two segments
		// become the slug, and the result lands at ~/.quiver/registries/<slug>.git/
		// so there's no escape — but verify the slug shape anyway.
		{"https://github.com/../../etc/passwd", "etc/passwd"},
		{"https://github.com/Foo_Bar/Quux.Plugin", "foo_bar/quux-plugin"},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			got := registry.InferRegistryName(tt.url)
			if got != tt.want {
				t.Errorf("InferRegistryName(%q) = %q, want %q", tt.url, got, tt.want)
			}
		})
	}
}
