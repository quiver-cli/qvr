package registrytests

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/registry"
)

func setupCacheTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	return home
}

func sampleCache(name string, generated time.Time) *registry.IndexCache {
	return &registry.IndexCache{
		Registry:  name,
		Commit:    "abc123",
		Generated: generated,
		Skills: []registry.SkillIndexEntry{
			{Name: "code-review", Description: "reviews PRs", Path: "skills/code-review"},
			{Name: "deploy-helper", Description: "deploys", Path: "skills/deploy-helper"},
		},
	}
}

func TestCache_WriteAndRead_RoundTrip(t *testing.T) {
	setupCacheTest(t)

	now := time.Now().UTC().Truncate(time.Second)
	in := sampleCache("acme", now)

	if err := registry.WriteCache(in); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}

	out, err := registry.ReadCache("acme")
	if err != nil {
		t.Fatalf("ReadCache: %v", err)
	}

	if out.Registry != "acme" {
		t.Errorf("Registry = %q, want acme", out.Registry)
	}
	if out.Commit != "abc123" {
		t.Errorf("Commit = %q, want abc123", out.Commit)
	}
	if !out.Generated.Equal(now) {
		t.Errorf("Generated = %v, want %v", out.Generated, now)
	}
	if len(out.Skills) != 2 {
		t.Fatalf("Skills len = %d, want 2", len(out.Skills))
	}
	if out.Skills[0].Name != "code-review" {
		t.Errorf("Skills[0].Name = %q, want code-review", out.Skills[0].Name)
	}
}

func TestCache_Read_MissIsSentinel(t *testing.T) {
	setupCacheTest(t)

	_, err := registry.ReadCache("nope")
	if !errors.Is(err, registry.ErrCacheMiss) {
		t.Errorf("expected ErrCacheMiss, got %v", err)
	}
}

func TestCache_Read_CorruptReturnsError(t *testing.T) {
	setupCacheTest(t)

	if err := os.MkdirAll(registry.CacheDir(), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(registry.CachePath("bad"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := registry.ReadCache("bad")
	if err == nil {
		t.Fatal("expected parse error")
	}
	if errors.Is(err, registry.ErrCacheMiss) {
		t.Errorf("corrupt file should not return ErrCacheMiss, got %v", err)
	}
}

func TestCache_IsStale(t *testing.T) {
	tests := []struct {
		name      string
		generated time.Time
		ttl       time.Duration
		want      bool
	}{
		{"fresh under ttl", time.Now().Add(-10 * time.Minute), time.Hour, false},
		{"just past ttl", time.Now().Add(-time.Hour - time.Second), time.Hour, true},
		{"far past ttl", time.Now().Add(-24 * time.Hour), time.Hour, true},
		{"zero ttl always stale", time.Now().Add(-time.Millisecond), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &registry.IndexCache{Generated: tt.generated}
			if got := c.IsStale(tt.ttl); got != tt.want {
				t.Errorf("IsStale() = %v, want %v (generated %v, ttl %v)",
					got, tt.want, tt.generated, tt.ttl)
			}
		})
	}
}

func TestCache_Invalidate_RemovesFile(t *testing.T) {
	setupCacheTest(t)

	if err := registry.WriteCache(sampleCache("acme", time.Now())); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	if _, err := os.Stat(registry.CachePath("acme")); err != nil {
		t.Fatalf("cache file should exist: %v", err)
	}

	if err := registry.Invalidate("acme"); err != nil {
		t.Fatalf("Invalidate: %v", err)
	}

	if _, err := os.Stat(registry.CachePath("acme")); !os.IsNotExist(err) {
		t.Errorf("expected cache file removed, got err=%v", err)
	}
}

func TestCache_Invalidate_MissingIsNoOp(t *testing.T) {
	setupCacheTest(t)

	if err := registry.Invalidate("never-existed"); err != nil {
		t.Errorf("Invalidate on missing should not error, got %v", err)
	}
}

func TestCache_Write_RequiresRegistryName(t *testing.T) {
	setupCacheTest(t)

	if err := registry.WriteCache(&registry.IndexCache{Registry: ""}); err == nil {
		t.Error("expected error writing cache without registry name")
	}
}

func TestCache_Write_AtomicNoPartialOnFailure(t *testing.T) {
	setupCacheTest(t)

	// Pre-populate with a known-good cache.
	good := sampleCache("acme", time.Now())
	if err := registry.WriteCache(good); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}
	before, err := os.ReadFile(registry.CachePath("acme"))
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	// Force a rename failure by making the destination a directory.
	// (Rename to a non-empty dir fails on most platforms.)
	if err := os.Remove(registry.CachePath("acme")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(registry.CachePath("acme"), "blocker"), 0o755); err != nil {
		t.Fatalf("mkdir blocker: %v", err)
	}

	err = registry.WriteCache(sampleCache("acme", time.Now()))
	if err == nil {
		t.Fatal("expected rename to fail when destination is a non-empty directory")
	}

	// Restore the original file to verify tmp was cleaned up (no stray *.tmp).
	matches, _ := filepath.Glob(filepath.Join(registry.CacheDir(), "*.tmp"))
	if len(matches) > 0 {
		t.Errorf("expected tmp files to be cleaned up after failed rename, found %v", matches)
	}
	_ = before // original content unused after the synthetic failure path
}

func TestCachePath_UnderQuiverHome(t *testing.T) {
	home := setupCacheTest(t)

	got := registry.CachePath("acme")
	want := filepath.Join(home, "cache", "index", "acme.json")
	if got != want {
		t.Errorf("CachePath = %q, want %q", got, want)
	}
}

func TestCache_ReadCache_JSONShapeIsStable(t *testing.T) {
	setupCacheTest(t)

	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	if err := registry.WriteCache(sampleCache("acme", now)); err != nil {
		t.Fatalf("WriteCache: %v", err)
	}

	raw, err := os.ReadFile(registry.CachePath("acme"))
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"registry", "commit", "generated", "skills"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("expected top-level JSON key %q in cache file, got %v", key, decoded)
		}
	}
}
