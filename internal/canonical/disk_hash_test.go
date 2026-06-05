package canonical_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/quiver-cli/qvr/internal/canonical"
)

// The disk hasher and the git-tree hasher must agree on a freshly-checked-out
// worktree — that's the load-bearing property: PopulateVerification records
// HashSubtree (git-based), Verify recomputes HashSubtreeFromDisk, and an
// untampered install should still report "ok".
func TestHashSubtreeFromDisk_matchesGitHashOnFreshCheckout(t *testing.T) {
	files := map[string]string{
		"skills/foo/SKILL.md":          "---\nname: foo\n---\nbody\n",
		"skills/foo/scripts/run.sh":    "#!/bin/sh\necho hi\n",
		"skills/foo/references/api.md": "# API\n",
	}
	repo := buildRepo(t, files)

	gitHash, err := canonical.HashSubtree(repo, "skills/foo")
	if err != nil {
		t.Fatalf("git hash: %v", err)
	}
	diskHash, err := canonical.HashSubtreeFromDisk(filepath.Join(repo, "skills/foo"))
	if err != nil {
		t.Fatalf("disk hash: %v", err)
	}
	if gitHash.SubtreeHash != diskHash {
		t.Errorf("disk and git hashes disagree on fresh checkout:\n  git:  %s\n  disk: %s", gitHash.SubtreeHash, diskHash)
	}
}

func TestHashSubtreeFromDisk_detectsAppendedBytes(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody\n",
	})
	subPath := filepath.Join(repo, "skills/foo")

	before, err := canonical.HashSubtreeFromDisk(subPath)
	if err != nil {
		t.Fatalf("before: %v", err)
	}

	// Simulate the #18 attack: append junk to the loaded file.
	f, err := os.OpenFile(filepath.Join(subPath, "SKILL.md"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString("\nINJECT_FROM_ATTACKER\n"); err != nil {
		t.Fatalf("append: %v", err)
	}
	_ = f.Close()

	after, err := canonical.HashSubtreeFromDisk(subPath)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if before == after {
		t.Errorf("disk hash unchanged after content append — drift would be invisible")
	}
}

func TestHashSubtreeFromDisk_detectsDeletedFile(t *testing.T) {
	// When the sole file is deleted, the walker has no hashable entries and
	// must return an error (which the verifier surfaces as "failed").
	repo := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "---\nname: foo\n---\nbody\n",
	})
	subPath := filepath.Join(repo, "skills/foo")
	if err := os.Remove(filepath.Join(subPath, "SKILL.md")); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := canonical.HashSubtreeFromDisk(subPath); err == nil {
		t.Error("expected error when no hashable files remain")
	}
}

func TestHashSubtreeFromDisk_detectsFileAdded(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "x\n",
	})
	subPath := filepath.Join(repo, "skills/foo")
	before, err := canonical.HashSubtreeFromDisk(subPath)
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subPath, "ADDED.md"), []byte("smuggled\n"), 0o644); err != nil {
		t.Fatalf("write added: %v", err)
	}
	after, err := canonical.HashSubtreeFromDisk(subPath)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if before == after {
		t.Errorf("disk hash unchanged after extra file added")
	}
}

func TestHashSubtreeFromDisk_excludesSignatureAndAttestation(t *testing.T) {
	// qvr.sig and .quiver-attestation.json must NOT leak into the digest,
	// matching HashSubtree's behaviour, so a signed install round-trips
	// through verify without drift.
	repo := buildRepo(t, map[string]string{
		"skills/foo/SKILL.md": "x\n",
	})
	subPath := filepath.Join(repo, "skills/foo")
	before, err := canonical.HashSubtreeFromDisk(subPath)
	if err != nil {
		t.Fatalf("before: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subPath, "qvr.sig"), []byte(`{"v":"1"}`), 0o644); err != nil {
		t.Fatalf("write sig: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subPath, ".quiver-attestation.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write att: %v", err)
	}
	after, err := canonical.HashSubtreeFromDisk(subPath)
	if err != nil {
		t.Fatalf("after: %v", err)
	}
	if before != after {
		t.Errorf("excluded wrapper artifacts leaked into digest:\n  before: %s\n  after:  %s", before, after)
	}
}
