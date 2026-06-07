//go:build unix

package cmd

import (
	"os"
	"syscall"
)

// reclaimableFileSize returns the on-disk bytes that deleting this file would
// actually free. A worktree is a `git clone --local` of the bare registry, so
// its object files are HARDLINKED from the bare repo (see
// internal/git/worktree.go: localHardlinkClone). Those blocks are shared on
// disk and stay live as long as the bare repo — or a sibling worktree — still
// references them, so removing one worktree frees nothing for them. Counting
// them, as a naive Size() walk does, inflates `qvr cache list` totals and
// `qvr cache prune`'s "freed" figure by a large multiple — issue #158 saw
// prune report "freeing 644 MB" when ~1 MB was actually reclaimed.
//
// A file with link count > 1 has another reference (the bare clone, or another
// worktree), so it contributes 0; a file unique to this tree (nlink == 1 —
// the checkout itself, and everything under a copy-clone fallback) contributes
// its full size. The result is the honest "what you'd get back" number.
//
// Note on reflinks (#205): a worktree-free install's blobs are copy-on-write
// CLONES of the content store, which have nlink == 1 (independent inodes that
// merely share data extents). They are therefore counted at full size here —
// an OVER-estimate of reclaimable bytes, which is the safe direction (prune
// never claims to free MORE than it does). Exact shared-extent accounting would
// need st_blocks-based measurement; that's a separate refinement, not required
// for honest worst-case reporting.
func reclaimableFileSize(info os.FileInfo) int64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st.Nlink > 1 {
		return 0
	}
	return info.Size()
}
