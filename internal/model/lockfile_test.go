package model_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/model"
)

func TestLockFile_WriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)

	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{
		Name:     "code-review",
		Registry: "acme",
		Path:     "skills/code-review",
		Branch:   "v2",
		Commit:   "abc123",
		Worktree: "/tmp/wt",
		Targets:  []string{"claude", "cursor"},
	})

	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}

	loaded, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	entry, err := loaded.Get("code-review")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry.Branch != "v2" || entry.Commit != "abc123" {
		t.Errorf("fields not preserved: %+v", entry)
	}
	if entry.InstalledAt.IsZero() || entry.UpdatedAt.IsZero() {
		t.Error("timestamps should be set by Put")
	}
	if entry.Source != "registry" {
		t.Errorf("expected default Source=registry, got %q", entry.Source)
	}
}

func TestLockFile_ReadMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	l, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("read missing should be OK: %v", err)
	}
	if len(l.Skills) != 0 {
		t.Error("expected empty lock file for missing path")
	}
	if l.Path() != path {
		t.Errorf("expected path %q, got %q", path, l.Path())
	}
}

func TestLockFile_ReadEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	l, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("empty file should read clean: %v", err)
	}
	if len(l.Skills) != 0 {
		t.Error("expected zero skills")
	}
}

func TestLockFile_ReadCorrupt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	if err := os.WriteFile(path, []byte("{not-json"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := model.ReadLockFile(path); err == nil {
		t.Error("expected corruption error")
	}
}

func TestLockFile_Remove(t *testing.T) {
	l := model.NewLockFile(filepath.Join(t.TempDir(), "lock.json"))
	l.Put(&model.LockEntry{Name: "foo"})
	if err := l.Remove("foo"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if err := l.Remove("foo"); !errors.Is(err, model.ErrLockSkillMissing) {
		t.Errorf("expected ErrLockSkillMissing, got %v", err)
	}
	if _, err := l.Get("foo"); !errors.Is(err, model.ErrLockSkillMissing) {
		t.Errorf("expected ErrLockSkillMissing after remove, got %v", err)
	}
}

func TestLockFile_AtomicWrite(t *testing.T) {
	// Verify no .tmp files remain after a successful write.
	dir := t.TempDir()
	l := model.NewLockFile(filepath.Join(dir, model.LockFileName))
	l.Put(&model.LockEntry{Name: "a"})
	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}

func TestLockFile_Entries_Sorted(t *testing.T) {
	l := model.NewLockFile(filepath.Join(t.TempDir(), "lock.json"))
	l.Put(&model.LockEntry{Name: "zeta"})
	l.Put(&model.LockEntry{Name: "alpha"})
	l.Put(&model.LockEntry{Name: "mu"})
	got := l.Entries()
	want := []string{"alpha", "mu", "zeta"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i, e := range got {
		if e.Name != want[i] {
			t.Errorf("entry %d: got %q want %q", i, e.Name, want[i])
		}
	}
}

func TestLockFile_PutPreservesInstalledAt(t *testing.T) {
	l := model.NewLockFile(filepath.Join(t.TempDir(), "lock.json"))
	installed := time.Now().Add(-24 * time.Hour).UTC()
	l.Put(&model.LockEntry{Name: "foo", InstalledAt: installed})
	entry, err := l.Get("foo")
	if err != nil {
		t.Fatalf("get foo: %v", err)
	}
	if entry == nil {
		t.Fatal("entry should not be nil after Put")
	}
	if !entry.InstalledAt.Equal(installed) {
		t.Errorf("InstalledAt overwritten: got %v want %v", entry.InstalledAt, installed)
	}
	if entry.UpdatedAt.Before(installed) {
		t.Error("UpdatedAt should advance on Put")
	}
}

func TestLockFile_DisabledRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{
		Name:     "shelved",
		Worktree: "/tmp/wt",
		Targets:  []string{"claude"},
		Disabled: true,
	})
	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	entry, err := loaded.Get("shelved")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !entry.Disabled {
		t.Errorf("Disabled flag lost on round-trip: %+v", entry)
	}
}

func TestLockFile_LegacyFilenameFallback(t *testing.T) {
	// A v0.4.5-era qvr.lock.json on disk should be transparently read when
	// the canonical qvr.lock is absent, then rewritten to the new path on
	// the next Write() (with the legacy file removed).
	dir := t.TempDir()
	canonical := filepath.Join(dir, model.LockFileName)
	legacy := filepath.Join(dir, model.LegacyLockFileName)

	seed := model.NewLockFile(legacy)
	seed.Put(&model.LockEntry{Name: "foo", Worktree: "/w", Targets: []string{"claude"}})
	if err := seed.Write(); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	// ReadLockFile against the canonical path finds the legacy file instead.
	loaded, err := model.ReadLockFile(canonical)
	if err != nil {
		t.Fatalf("read with fallback: %v", err)
	}
	if _, err := loaded.Get("foo"); err != nil {
		t.Errorf("entry lost on fallback read: %v", err)
	}
	if loaded.Path() != canonical {
		t.Errorf("Path() = %q, want canonical %q", loaded.Path(), canonical)
	}

	if err := loaded.Write(); err != nil {
		t.Fatalf("write after fallback: %v", err)
	}
	if _, err := os.Stat(canonical); err != nil {
		t.Errorf("canonical file should exist after write: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy file should be removed after migration, stat err = %v", err)
	}
}

func TestLockFile_LegacyFilenameFallbackPreferred(t *testing.T) {
	// When BOTH the canonical and legacy filenames exist, the canonical
	// wins. The legacy file is left alone (a future write will not touch
	// it, since legacyPath is only set when we fell back to it).
	dir := t.TempDir()
	canonical := filepath.Join(dir, model.LockFileName)
	legacy := filepath.Join(dir, model.LegacyLockFileName)

	current := model.NewLockFile(canonical)
	current.Put(&model.LockEntry{Name: "fresh", Worktree: "/w"})
	if err := current.Write(); err != nil {
		t.Fatalf("seed canonical: %v", err)
	}

	stale := model.NewLockFile(legacy)
	stale.Put(&model.LockEntry{Name: "stale", Worktree: "/old"})
	if err := stale.Write(); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}

	loaded, err := model.ReadLockFile(canonical)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := loaded.Get("fresh"); err != nil {
		t.Errorf("canonical entry missing: %v", err)
	}
	if _, err := loaded.Get("stale"); err == nil {
		t.Errorf("legacy entry should not be merged in when canonical exists")
	}
}

func TestLockFile_RejectsUnsupportedVersion(t *testing.T) {
	// version < MinSupported / missing / >LockFileVersion all error.
	cases := []struct {
		name string
		body string
	}{
		{"missing version", `{"skills": {}}`},
		{"version=0", `{"version": 0, "skills": {}}`},
		{"version=1 pre-history", `{"version": 1, "skills": {}}`},
		{"version=999 future", `{"version": 999, "skills": {}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, model.LockFileName)
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatalf("seed: %v", err)
			}
			_, err := model.ReadLockFile(path)
			if err == nil {
				t.Fatal("expected error for unsupported version, got nil")
			}
			if !errors.Is(err, model.ErrLockVersionUnsupported) {
				t.Errorf("expected ErrLockVersionUnsupported, got %v", err)
			}
		})
	}
}

func TestLockFile_AcceptsSupportedVersions(t *testing.T) {
	// v2 (current min) and v3 (current max) both load cleanly.
	dir := t.TempDir()
	for _, version := range []int{model.MinSupportedLockFileVersion, model.LockFileVersion} {
		path := filepath.Join(dir, model.LockFileName)
		body := []byte(`{"version": ` + itoa(version) + `, "skills": {}}`)
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatalf("seed v%d: %v", version, err)
		}
		if _, err := model.ReadLockFile(path); err != nil {
			t.Errorf("v%d should load: %v", version, err)
		}
		_ = os.Remove(path)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestDefaultLockPath(t *testing.T) {
	local := model.DefaultLockPath("/proj", "/quiver", false)
	if local != filepath.Join("/proj", model.LockFileName) {
		t.Errorf("local path: %s", local)
	}
	global := model.DefaultLockPath("/proj", "/quiver", true)
	if global != filepath.Join("/quiver", model.LockFileName) {
		t.Errorf("global path: %s", global)
	}
}
