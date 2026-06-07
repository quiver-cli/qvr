package skill

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// commitSkillAs writes skills/<name>/SKILL.md authored by a specific identity
// and returns the commit SHA. Builds a multi-skill, multi-author repo so the
// branch tip and a skill's own last-touching commit differ (#171/#173).
func commitSkillAs(t *testing.T, dir, name, body, authorName, authorEmail string, when time.Time) string {
	t.Helper()
	skillAbs := filepath.Join(dir, "skills", name, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(skillAbs), 0o755); err != nil {
		t.Fatalf("mkdir skill: %v", err)
	}
	if err := os.WriteFile(skillAbs, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r, err := gogit.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
		t.Fatalf("add: %v", err)
	}
	h, err := wt.Commit("commit "+name, &gogit.CommitOptions{
		Author: &object.Signature{Name: authorName, Email: authorEmail, When: when},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return h.String()
}

// TestMemoizedProvenanceMatchesPublic guards #209: the cold-install path resolves
// SkillCommit once and threads it into checkGitProvenanceAt / commitAuthorAt.
// Those internal variants MUST return byte-identical results to the public
// CheckGitProvenance / CommitAuthor (which recompute SkillCommit each call) for
// every path shape — otherwise the memoization would silently change the
// recorded provenance, weakening the #171/#173 invariants.
func TestMemoizedProvenanceMatchesPublic(t *testing.T) {
	dir := t.TempDir()
	if _, err := gogit.PlainInit(dir, false); err != nil {
		t.Fatalf("init: %v", err)
	}
	commitSkillAs(t, dir, "alpha", "---\nname: alpha\n---\nv1\n",
		"Alice", "alice@x", time.Unix(1, 0).UTC())
	tip := commitSkillAs(t, dir, "other", "---\nname: other\n---\nv1\n",
		"Bob", "bob@x", time.Unix(2, 0).UTC())

	for _, path := range []string{"skills/alpha", "skills/other", ".", "skills/missing"} {
		sc := SkillCommit(dir, tip, path)

		gotAuthor := commitAuthorAt(dir, sc)
		wantAuthor := CommitAuthor(dir, tip, path)
		if gotAuthor != wantAuthor {
			t.Errorf("path %q: commitAuthorAt = %q, public CommitAuthor = %q", path, gotAuthor, wantAuthor)
		}

		gotProv := checkGitProvenanceAt(dir, "main", sc)
		wantProv := CheckGitProvenance(dir, "main", tip, path)
		if (gotProv == nil) != (wantProv == nil) {
			t.Fatalf("path %q: provenance nil mismatch: memoized=%v public=%v", path, gotProv, wantProv)
		}
		if gotProv != nil && *gotProv != *wantProv {
			t.Errorf("path %q: provenance mismatch: memoized=%+v public=%+v", path, *gotProv, *wantProv)
		}
	}
}
