//go:build unix

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDirSize_DiscountsHardlinkedSharedFiles pins the secondary half of issue
// #158: a worktree is a `git clone --local` of the bare registry, so its
// object files are hardlinked from the bare. Deleting the worktree doesn't
// free those shared blocks (the bare still references them), so dirSize — the
// "what would prune reclaim" walk — must not count them. A naive Size() sum
// reported "freeing 644 MB" when ~1 MB was actually reclaimable.
func TestDirSize_DiscountsHardlinkedSharedFiles(t *testing.T) {
	bare := t.TempDir()
	worktree := t.TempDir()

	// A "shared object": lives in the bare, hardlinked into the worktree.
	shared := filepath.Join(bare, "object")
	if err := os.WriteFile(shared, make([]byte, 4096), 0o644); err != nil {
		t.Fatalf("write shared: %v", err)
	}
	if err := os.Link(shared, filepath.Join(worktree, "shared-object")); err != nil {
		t.Skipf("hardlinks unsupported on this filesystem: %v", err)
	}
	// A file unique to the worktree (the checkout) — this IS reclaimable.
	if err := os.WriteFile(filepath.Join(worktree, "SKILL.md"), make([]byte, 512), 0o644); err != nil {
		t.Fatalf("write unique: %v", err)
	}

	got, err := dirSize(worktree)
	if err != nil {
		t.Fatalf("dirSize: %v", err)
	}
	// Only the unique 512-byte file counts; the 4096-byte hardlinked object is
	// shared with the (retained) bare and contributes 0.
	if got != 512 {
		t.Errorf("dirSize = %d, want 512 (hardlinked shared object must not count)", got)
	}

	// fullDirSize (used for the bare in `cache clean --registries`) counts
	// everything, since removing the bare genuinely frees the shared blocks.
	full, err := fullDirSize(worktree)
	if err != nil {
		t.Fatalf("fullDirSize: %v", err)
	}
	if full != 4096+512 {
		t.Errorf("fullDirSize = %d, want %d (must count shared blocks too)", full, 4096+512)
	}
}
