package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raks097/quiver/internal/model"
)

// Issue #cosmetic in the v0.4.0 review: AGENTS.md was rendering `_(reg@ref)_`
// on a fresh line when a skill's description came from a YAML folded/block
// scalar (embedded newlines or trailing whitespace). collapseWhitespace is the
// one-line fix; pin it so future refactors can't regress the AGENTS.md layout.
func TestCollapseWhitespace(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"hello world", "hello world"},
		{"hello\nworld", "hello world"},
		{"hello\n  world\ttab", "hello world tab"},
		{"  leading and trailing  \n", "leading and trailing"},
		{"multi\n\nblank\n\nlines", "multi blank lines"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := collapseWhitespace(tc.in); got != tc.want {
			t.Errorf("collapseWhitespace(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// AGENTS.md should stay in sync after switch/upgrade/pull, but only when the
// user has already opted in by running `qvr sync` at least once. Absence of
// the file means "don't clobber the project with auto-generated artefacts."
func TestRefreshAgentsMDIfPresent_NoopWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	entries := []*model.LockEntry{{Name: "demo", Registry: "acme", Branch: "main"}}
	if err := refreshAgentsMDIfPresent(dir, entries); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("expected AGENTS.md to stay absent, got err=%v", err)
	}
}

func TestRefreshAgentsMDIfPresent_RewritesWhenPresent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("stale content"), 0o644); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	entries := []*model.LockEntry{{Name: "demo", Registry: "acme", Branch: "v1.0.0"}}
	if err := refreshAgentsMDIfPresent(dir, entries); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(data), "stale content") {
		t.Errorf("expected stale content to be replaced, got: %s", data)
	}
	if !strings.Contains(string(data), "demo") || !strings.Contains(string(data), "v1.0.0") {
		t.Errorf("expected demo@v1.0.0 in regenerated AGENTS.md, got: %s", data)
	}
}

// refreshAgentsMDFromLock is the convenience wrapper install/link/remove use
// when the lock was mutated by a helper (installer.*) and a live entries
// slice isn't in hand. Verify it reads the lock and rewrites an existing
// AGENTS.md, and leaves a missing AGENTS.md alone.
func TestRefreshAgentsMDFromLock(t *testing.T) {
	dir := t.TempDir()
	lock := model.NewLockFile(filepath.Join(dir, model.LockFileName))
	lock.Put(&model.LockEntry{Name: "demo", Registry: "acme", Branch: "v1.0.0"})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	// No AGENTS.md yet — wrapper must not create one.
	refreshAgentsMDFromLock(dir)
	if _, err := os.Stat(filepath.Join(dir, "AGENTS.md")); !os.IsNotExist(err) {
		t.Errorf("expected AGENTS.md to stay absent, got err=%v", err)
	}

	// With a seeded AGENTS.md, wrapper rewrites from lock contents.
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("seed AGENTS.md: %v", err)
	}
	refreshAgentsMDFromLock(dir)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "demo") || !strings.Contains(string(data), "v1.0.0") {
		t.Errorf("expected demo@v1.0.0 in regenerated AGENTS.md, got: %s", data)
	}
}
