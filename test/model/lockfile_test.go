package modeltests

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
