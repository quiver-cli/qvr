package registry_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/registry"
)

// seedTinyRegistry builds a one-skill bare repo and registers it under name.
// Returns the bare clone's repo path so tests can pass it to Manager.Index
// directly.
func seedTinyRegistry(t *testing.T, name string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote")
	seedDir := filepath.Join(remote, "skills", "demo")
	if err := os.MkdirAll(seedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: demo\ndescription: demo\n---\n# demo\n"
	if err := os.WriteFile(filepath.Join(seedDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, cmd := range [][]string{
		{"git", "init", "-q", "-b", "main"},
		{"git", "add", "-A"},
		{"git", "-c", "user.email=t@t.t", "-c", "user.name=t", "commit", "-q", "-m", "init"},
	} {
		runGit(t, remote, cmd[1:]...)
	}

	bare := registry.RegistryPath(name)
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatalf("mkdir bare parent: %v", err)
	}
	gc := git.NewGoGitClient()
	if err := gc.BareClone(context.Background(), remote, bare); err != nil {
		t.Fatalf("bare clone: %v", err)
	}
	return bare
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// TestIndexWithOptions_RefreshBypassesCache pins #46's acceptance: --refresh
// must rebuild from the bare clone even when the cache is fresh, and the
// rebuild must still write the new cache back to disk.
func TestIndexWithOptions_RefreshBypassesCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	bare := seedTinyRegistry(t, "demo-reg")
	mgr := registry.NewManager(git.NewGoGitClient())
	mgr.CacheTTL = time.Hour

	// First read populates the cache.
	if _, _, err := mgr.Index("demo-reg", bare); err != nil {
		t.Fatalf("seed Index: %v", err)
	}
	cachePath := registry.CachePath("demo-reg")
	info1, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("cache should exist after first Index: %v", err)
	}

	// Sleep just enough for mtime resolution, then refresh.
	time.Sleep(20 * time.Millisecond)
	if _, _, err := mgr.IndexWithOptions("demo-reg", bare, registry.IndexOptions{Refresh: true}); err != nil {
		t.Fatalf("Index --refresh: %v", err)
	}
	info2, err := os.Stat(cachePath)
	if err != nil {
		t.Fatalf("cache should still exist after refresh: %v", err)
	}
	if !info2.ModTime().After(info1.ModTime()) {
		t.Errorf("cache mtime should advance on refresh: was %v now %v", info1.ModTime(), info2.ModTime())
	}
}

// TestIndex_ZeroTTLAlwaysRebuilds pins the acceptance: index_ttl=0 means
// every read rebuilds from the bare clone. We verify by setting TTL=0,
// reading twice, and checking the cache mtime advanced on the second read.
func TestIndex_ZeroTTLAlwaysRebuilds(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	bare := seedTinyRegistry(t, "demo-reg")
	mgr := registry.NewManager(git.NewGoGitClient())
	mgr.CacheTTL = 0

	if _, _, err := mgr.Index("demo-reg", bare); err != nil {
		t.Fatalf("seed Index: %v", err)
	}
	info1, err := os.Stat(registry.CachePath("demo-reg"))
	if err != nil {
		t.Fatalf("cache after first Index: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, _, err := mgr.Index("demo-reg", bare); err != nil {
		t.Fatalf("second Index: %v", err)
	}
	info2, err := os.Stat(registry.CachePath("demo-reg"))
	if err != nil {
		t.Fatalf("cache after second Index: %v", err)
	}
	if !info2.ModTime().After(info1.ModTime()) {
		t.Errorf("cache mtime should advance on every read when TTL=0: was %v now %v", info1.ModTime(), info2.ModTime())
	}
}

// TestIndex_DefaultTTLReusesCache makes sure the historical 1h default still
// reuses the cache (so non-#46 users see no behaviour change).
func TestIndex_DefaultTTLReusesCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)

	bare := seedTinyRegistry(t, "demo-reg")
	mgr := registry.NewManager(git.NewGoGitClient())
	mgr.CacheTTL = time.Hour

	if _, _, err := mgr.Index("demo-reg", bare); err != nil {
		t.Fatalf("seed Index: %v", err)
	}
	info1, err := os.Stat(registry.CachePath("demo-reg"))
	if err != nil {
		t.Fatalf("cache after first Index: %v", err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, _, err := mgr.Index("demo-reg", bare); err != nil {
		t.Fatalf("second Index: %v", err)
	}
	info2, err := os.Stat(registry.CachePath("demo-reg"))
	if err != nil {
		t.Fatalf("cache after second Index: %v", err)
	}
	if !info2.ModTime().Equal(info1.ModTime()) {
		t.Errorf("cache mtime should NOT advance when within TTL: was %v now %v", info1.ModTime(), info2.ModTime())
	}
}
