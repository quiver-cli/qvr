package canonical_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/canonical"
)

// buildRepo creates a non-bare git repo at t.TempDir(), writes the given
// files, commits, and returns the repo path. Files is a map of relative
// path -> content; intermediate directories are created on demand.
func buildRepo(t *testing.T, files map[string]string) string {
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
	for relPath, content := range files {
		full := filepath.Join(dir, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
		if _, err := wt.Add(relPath); err != nil {
			t.Fatalf("add %s: %v", relPath, err)
		}
	}
	_, err = wt.Commit("init", &gogit.CommitOptions{
		Author: &object.Signature{Name: "t", Email: "t@t", When: time.Unix(0, 0).UTC()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return dir
}

func TestHashSubtree_singleFile(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody\n",
	})
	id, err := canonical.HashSubtree(repo, "skills/foo")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if id.SubtreeHash == "" {
		t.Fatal("empty SubtreeHash")
	}
	if len(id.CommitSHA) != 40 {
		t.Errorf("CommitSHA len = %d, want 40", len(id.CommitSHA))
	}
	if len(id.TreeSHA) != 40 {
		t.Errorf("TreeSHA len = %d, want 40", len(id.TreeSHA))
	}
	if id.SubtreeHash[:7] != "sha256:" {
		t.Errorf("SubtreeHash missing sha256 prefix: %s", id.SubtreeHash)
	}
}

// TestHashSubtreeAtCommit_matchesHEAD pins the contract `qvr lock` relies on:
// hashing a commit explicitly (from objects, no checkout) yields the exact same
// SubtreeIdentity as HashSubtree resolving the same commit via HEAD.
func TestHashSubtreeAtCommit_matchesHEAD(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md":       "---\nname: foo\n---\nbody\n",
		"skills/foo/scripts/run.sh": "#!/bin/sh\necho hi\n",
	})
	viaHead, err := canonical.HashSubtree(repo, "skills/foo")
	if err != nil {
		t.Fatalf("HashSubtree: %v", err)
	}
	viaCommit, err := canonical.HashSubtreeAtCommit(repo, viaHead.CommitSHA, "skills/foo")
	if err != nil {
		t.Fatalf("HashSubtreeAtCommit: %v", err)
	}
	if viaHead.SubtreeHash != viaCommit.SubtreeHash {
		t.Errorf("SubtreeHash mismatch:\n  head=%s\n  commit=%s", viaHead.SubtreeHash, viaCommit.SubtreeHash)
	}
	if viaHead.TreeSHA != viaCommit.TreeSHA {
		t.Errorf("TreeSHA mismatch: head=%s commit=%s", viaHead.TreeSHA, viaCommit.TreeSHA)
	}
	if viaCommit.CommitSHA != viaHead.CommitSHA {
		t.Errorf("CommitSHA mismatch: head=%s commit=%s", viaHead.CommitSHA, viaCommit.CommitSHA)
	}
}

func TestHashSubtreeAtCommit_unknownCommitErrors(t *testing.T) {
	repo := buildRepo(t, map[string]string{"skills/foo/SKILL.md": "---\nname: foo\n---\nx\n"})
	if _, err := canonical.HashSubtreeAtCommit(repo, "0000000000000000000000000000000000000000", "skills/foo"); err == nil {
		t.Fatal("expected error for unknown commit, got nil")
	}
}

func TestHashSubtree_deterministic(t *testing.T) {
	files := map[string]string{
		"skills/foo/SKILL.md":          "---\nname: foo\n---\nbody\n",
		"skills/foo/scripts/run.sh":    "#!/bin/sh\necho hi\n",
		"skills/foo/references/api.md": "# API\n",
	}
	repo := buildRepo(t, files)
	first, err := canonical.HashSubtree(repo, "skills/foo")
	if err != nil {
		t.Fatalf("hash 1: %v", err)
	}
	second, err := canonical.HashSubtree(repo, "skills/foo")
	if err != nil {
		t.Fatalf("hash 2: %v", err)
	}
	if first.SubtreeHash != second.SubtreeHash {
		t.Errorf("non-deterministic hash:\n  %s\n  %s", first.SubtreeHash, second.SubtreeHash)
	}
}

func TestHashSubtree_excludesSignatureFile(t *testing.T) {
	// Two repos identical except one has a qvr.sig in the skill subtree.
	// Hashes must match — qvr.sig is excluded from the canonical digest.
	withoutSig := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody\n",
	})
	withSig := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody\n",
		"skills/foo/qvr.sig":  `{"version":"qvr-signature-v1"}`,
	})
	a, err := canonical.HashSubtree(withoutSig, "skills/foo")
	if err != nil {
		t.Fatalf("hash without: %v", err)
	}
	b, err := canonical.HashSubtree(withSig, "skills/foo")
	if err != nil {
		t.Fatalf("hash with: %v", err)
	}
	if a.SubtreeHash != b.SubtreeHash {
		t.Errorf("qvr.sig leaked into digest:\n  without: %s\n  with:    %s", a.SubtreeHash, b.SubtreeHash)
	}
}

func TestHashSubtree_excludesAttestationFile(t *testing.T) {
	withoutAtt := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody\n",
	})
	withAtt := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md":                 "---\nname: foo\n---\nbody\n",
		"skills/foo/.quiver-attestation.json": `{"_type":"in-toto"}`,
	})
	a, err := canonical.HashSubtree(withoutAtt, "skills/foo")
	if err != nil {
		t.Fatalf("hash without: %v", err)
	}
	b, err := canonical.HashSubtree(withAtt, "skills/foo")
	if err != nil {
		t.Fatalf("hash with: %v", err)
	}
	if a.SubtreeHash != b.SubtreeHash {
		t.Errorf(".quiver-attestation.json leaked into digest")
	}
}

func TestHashSubtree_contentChange(t *testing.T) {
	original := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody\n",
	})
	mutated := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody-modified\n",
	})
	a, _ := canonical.HashSubtree(original, "skills/foo")
	b, _ := canonical.HashSubtree(mutated, "skills/foo")
	if a.SubtreeHash == b.SubtreeHash {
		t.Errorf("hash did not change after content edit")
	}
}

func TestHashSubtree_renameChangesHash(t *testing.T) {
	// Paths are part of the canonical hash — renaming a file is a content
	// change from the subtree-identity perspective.
	original := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md":     "---\nname: foo\n---\nx\n",
		"skills/foo/scripts/a.sh": "#!/bin/sh\n",
	})
	renamed := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md":     "---\nname: foo\n---\nx\n",
		"skills/foo/scripts/b.sh": "#!/bin/sh\n",
	})
	a, _ := canonical.HashSubtree(original, "skills/foo")
	b, _ := canonical.HashSubtree(renamed, "skills/foo")
	if a.SubtreeHash == b.SubtreeHash {
		t.Errorf("hash unchanged after rename — paths should be part of digest")
	}
}

func TestHashSubtree_missingSubtree(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "x",
	})
	if _, err := canonical.HashSubtree(repo, "skills/does-not-exist"); err == nil {
		t.Fatal("expected error for missing subtree")
	}
}

func TestHashSubtree_emptySubtree(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		// only the excluded file present
		"skills/foo/qvr.sig": "{}",
	})
	if _, err := canonical.HashSubtree(repo, "skills/foo"); err == nil {
		t.Fatal("expected error when only excluded files are present")
	}
}

func TestIsExcluded(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"qvr.sig", true},
		{".quiver-attestation.json", true},
		{"SKILL.md", false},
		{"scripts/run.sh", false},
		{"qvr.sig.bak", false},
	}
	for _, c := range cases {
		got := canonical.IsExcluded(c.path)
		if got != c.want {
			t.Errorf("IsExcluded(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}
