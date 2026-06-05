package model_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quiver-cli/qvr/internal/model"
)

func TestLockFile_WriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)

	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{
		Name:        "code-review",
		Registry:    "acme",
		Source:      "git@github.com:acme/skills.git",
		Path:        "skills/code-review",
		Ref:         "v2",
		Commit:      "abc123",
		SubtreeHash: "sha256:deadbeef",
		Targets:     []string{"claude", "cursor"},
	})

	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		t.Fatalf("lockfile should be TOML, got JSON-like content: %s", raw)
	}
	if !strings.Contains(string(raw), "[skills.code-review]") {
		t.Fatalf("lockfile missing TOML skill table: %s", raw)
	}

	loaded, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	entry, err := loaded.Get("code-review")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry.Ref != "v2" || entry.Commit != "abc123" {
		t.Errorf("fields not preserved: %+v", entry)
	}
	if entry.SubtreeHash != "sha256:deadbeef" {
		t.Errorf("SubtreeHash not preserved: %q", entry.SubtreeHash)
	}
	if entry.Source != "git@github.com:acme/skills.git" {
		t.Errorf("Source not preserved: %q", entry.Source)
	}
	if entry.Name != "code-review" {
		t.Errorf("Name should be re-populated from map key: got %q", entry.Name)
	}
	if entry.InstalledAt.IsZero() {
		t.Error("InstalledAt should be set by Put")
	}
}

// TestLockFile_RootCoexistsRoundTrips ensures the scope flag survives a
// write/read cycle so a reproducible restore can honor it.
func TestLockFile_RootCoexistsRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), model.LockFileName)
	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{
		Name: "root-app", Registry: "multi", Source: "https://x/y.git",
		Path: ".", RootCoexists: true, Ref: "main", Commit: "abc123",
		SubtreeHash: "sha256:deadbeef", Targets: []string{"claude"},
	})
	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	entry, err := loaded.Get("root-app")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !entry.RootCoexists {
		t.Error("RootCoexists did not survive the write/read cycle")
	}
}

func TestSkillScopePaths(t *testing.T) {
	if got := model.SkillScopePaths("browse", false); len(got) != 1 || got[0] != "browse" {
		t.Errorf("non-root = %v, want [browse]", got)
	}
	if got := model.SkillScopePaths(".", false); got != nil {
		t.Errorf("lone root = %v, want nil", got)
	}
	got := model.SkillScopePaths(".", true)
	want := map[string]bool{"SKILL.md": true, "references": true, "scripts": true, "assets": true}
	if len(got) != len(want) {
		t.Fatalf("root-coexists = %v, want the 4 content patterns", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("unexpected scope path %q", p)
		}
	}
}

// TestLockFile_NameNotSerialized confirms the in-memory Name field
// (json:"-") never leaks onto disk. The map key on disk is authoritative.
func TestLockFile_NameNotSerialized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{Name: "foo", Source: "git@x:y.git", Ref: "main"})
	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if strings.Contains(string(data), "name =") {
		t.Errorf("v5 lock leaked `name` field on disk: %s", string(data))
	}
}

// TestLockFile_NoWorktreeOnDisk confirms the portability bug that v4 had
// (worktree as $HOME-rooted absolute path) cannot recur in v5.
func TestLockFile_NoWorktreeOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{Name: "foo", Source: "git@x:y.git", Ref: "main"})
	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if strings.Contains(string(data), "worktree") {
		t.Errorf("v5 lock leaked `worktree` field on disk: %s", string(data))
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

// TestLockFile_ConcurrentWritesAreAtomic hammers the lock from multiple
// goroutines so the rename-over-temp scheme is exercised under contention.
// Pre-Phase-6 hypothetical: a writer's tmp file could survive on the
// filesystem if another writer raced past it. After this test, the
// invariant is named: regardless of who wins, the final lock is well-formed
// TOML and no `.lock-*.tmp` siblings linger.
func TestLockFile_ConcurrentWritesAreAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)

	const writers = 16
	const writes = 8
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := range writers {
		go func(id int) {
			defer wg.Done()
			for j := range writes {
				l := model.NewLockFile(path)
				l.Put(&model.LockEntry{Name: fmt.Sprintf("w%d-%d", id, j)})
				if err := l.Write(); err != nil {
					t.Errorf("writer %d: %v", id, err)
					return
				}
			}
		}(i)
	}
	wg.Wait()

	// Final lock must parse cleanly under the current schema.
	final, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("read final lock: %v", err)
	}
	if final.Version != model.LockFileVersion {
		t.Errorf("version = %d, want %d", final.Version, model.LockFileVersion)
	}
	// And no stray temp siblings — `.lock-*.tmp` is the canonical scratch
	// name, and any survivor here means a writer crashed mid-rename.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".lock-") {
			t.Errorf("leftover temp file after concurrent writes: %s", e.Name())
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
}

// TestLockFile_PutPreservesPriorInstalledAtOnUpdate guards issue #77: a no-op
// re-add (e.g. `qvr add <already-installed-skill>`) builds a fresh LockEntry
// with InstalledAt = zero and calls Put. Put must preserve the previously
// recorded InstalledAt rather than stamping time.Now(), otherwise the
// lockfile churns on every run and uv-parity idempotency is violated.
func TestLockFile_PutPreservesPriorInstalledAtOnUpdate(t *testing.T) {
	l := model.NewLockFile(filepath.Join(t.TempDir(), "lock.json"))
	prior := time.Now().Add(-7 * 24 * time.Hour).UTC()
	l.Put(&model.LockEntry{Name: "foo", InstalledAt: prior})

	// Simulate a no-op re-add: fresh entry with the same name, InstalledAt
	// zero. Without the fix, Put would stamp time.Now() here.
	l.Put(&model.LockEntry{Name: "foo"})
	entry, err := l.Get("foo")
	if err != nil {
		t.Fatalf("get foo: %v", err)
	}
	if !entry.InstalledAt.Equal(prior) {
		t.Errorf("Put churned InstalledAt on no-op update: got %v want %v",
			entry.InstalledAt, prior)
	}
}

func TestLockFile_DisabledRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{
		Name:     "shelved",
		Source:   "git@x:y.git",
		Ref:      "main",
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

func TestLockFile_ForkedFromRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	l := model.NewLockFile(path)
	l.Put(&model.LockEntry{
		Name:       "auth",
		Source:     "git@github.com:me/fork.git",
		Ref:        "main",
		Targets:    []string{"claude"},
		ForkedFrom: "git@github.com:up/auth.git@abc1234",
	})
	if err := l.Write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	loaded, err := model.ReadLockFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	entry, err := loaded.Get("auth")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if entry.ForkedFrom != "git@github.com:up/auth.git@abc1234" {
		t.Errorf("ForkedFrom round-trip lost: %q", entry.ForkedFrom)
	}

	// Empty ForkedFrom must omit the TOML key entirely (omitempty).
	l2 := model.NewLockFile(filepath.Join(dir, "empty.lock"))
	l2.Put(&model.LockEntry{
		Name:    "plain",
		Source:  "git@x:y.git",
		Ref:     "main",
		Targets: []string{"claude"},
	})
	if err := l2.Write(); err != nil {
		t.Fatalf("write empty: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "empty.lock"))
	if err != nil {
		t.Fatalf("read raw: %v", err)
	}
	if strings.Contains(string(raw), "forkedFrom") {
		t.Errorf("empty ForkedFrom should not serialise; got: %s", raw)
	}
}

func TestLockFile_RejectsUnsupportedVersion(t *testing.T) {
	// v4 is the floor — anything older is rejected outright (qvr is
	// pre-release with no users, so the only recourse is to delete the
	// lock and reinstall). Future versions (>LockFileVersion) and missing
	// version field all error.
	cases := []struct {
		name string
		body string
		want string // substring expected in the error message
	}{
		{"missing version", `[skills]`, "missing"},
		{"version=0", "version = 0\n[skills]\n", "missing"},
		{"version=2 legacy", "version = 2\n[skills]\n", "delete qvr.lock"},
		{"version=3 legacy", "version = 3\n[skills]\n", "delete qvr.lock"},
		{"version=4 legacy", "version = 4\n[skills]\n", "delete qvr.lock"},
		{"version=999 future", "version = 999\n[skills]\n", "upgrade qvr"},
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
			if !contains(err.Error(), tc.want) {
				t.Errorf("error %q missing substring %q", err.Error(), tc.want)
			}
		})
	}
}

// Regression: a non-integer `version` field (string, bool, array) should route through the
// friendly ErrLockVersionUnsupported template so the user sees actionable
// recovery advice — and the advice should now mention deleting the lock.
func TestLockFile_RejectsTypeMismatchVersion(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantInError string
	}{
		{"string version", "version = \"three\"\n[skills]\n", `"three"`},
		{"bool version", "version = true\n[skills]\n", "true"},
		{"array version", "version = [3]\n[skills]\n", "[3]"},
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
				t.Fatal("expected error for type-mismatched version, got nil")
			}
			if !errors.Is(err, model.ErrLockVersionUnsupported) {
				t.Errorf("expected ErrLockVersionUnsupported, got %v", err)
			}
			msg := err.Error()
			if contains(msg, "Go struct field") || contains(msg, "toml:") {
				t.Errorf("raw TOML error leaked into message: %q", msg)
			}
			if !contains(msg, tc.wantInError) {
				t.Errorf("error %q missing expected substring %q", msg, tc.wantInError)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestLockFile_AcceptsCurrentVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, model.LockFileName)
	body := []byte("version = " + itoa(model.LockFileVersion) + "\n[skills]\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := model.ReadLockFile(path); err != nil {
		t.Errorf("v%d should load: %v", model.LockFileVersion, err)
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

func TestLockFile_IsGlobal(t *testing.T) {
	home := "/q"
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"unset path", "", false},
		{"global location", filepath.Join(home, model.LockFileName), true},
		{"project location", filepath.Join("/proj", model.LockFileName), false},
		{"clean-equivalent global", filepath.Join(home, "./", model.LockFileName), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := model.NewLockFile(tc.path)
			if got := l.IsGlobal(home); got != tc.want {
				t.Errorf("IsGlobal(%q, home=%q) = %v, want %v", tc.path, home, got, tc.want)
			}
		})
	}
	t.Run("empty home is never global", func(t *testing.T) {
		l := model.NewLockFile(filepath.Join(home, model.LockFileName))
		if l.IsGlobal("") {
			t.Error("empty home should not match")
		}
	})
}
