package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/astra-sh/qvr/internal/registry"
)

// AuxCachePruneResult summarises a sweep of the derived caches that back fast
// materialization — the content-addressed blob store and the global identity /
// provenance memos. These are all reconstructible (a re-install rebuilds them),
// so pruning them never loses data; it only reclaims space and tidies records
// that no installed skill references anymore.
type AuxCachePruneResult struct {
	IdentityRemoved   int   `json:"identityRemoved"`
	ProvenanceRemoved int   `json:"provenanceRemoved"`
	BlobsRemoved      int   `json:"blobsRemoved"`
	FreedBytes        int64 `json:"freedBytes"`
}

// Total reports the number of cache records removed across all three caches.
func (r AuxCachePruneResult) Total() int {
	return r.IdentityRemoved + r.ProvenanceRemoved + r.BlobsRemoved
}

// PruneAuxCaches removes derived-cache entries no longer referenced by any
// reachable install:
//
//   - identity / provenance memos whose (commit, path, …) key is not produced
//     by any reachable lock entry, and
//   - content-store blobs whose bytes are not present in any reachable worktree.
//
// Reachability is taken from registry.Reachable() — the same source `qvr cache
// prune` uses for worktrees. Deleting a blob is always safe even when it IS
// referenced: a materialized file is an independent inode (a reflink shares only
// data extents, which the filesystem keeps alive while the worktree references
// them, or a plain copy). We still prune only unreferenced blobs so the warm
// dedup/reflink source survives for skills still installed.
//
// With dryRun set, nothing is deleted; the result reports what WOULD be removed.
func PruneAuxCaches(dryRun bool) (AuxCachePruneResult, error) {
	var out AuxCachePruneResult
	reach, err := registry.Reachable()
	if err != nil {
		return out, err
	}

	// Live identity + provenance keys, derived from reachable lock entries.
	liveIdentity := make(map[string]struct{}, len(reach.Entries))
	liveProvenance := make(map[string]struct{}, len(reach.Entries))
	for _, e := range reach.Entries {
		liveIdentity[identityCacheKey(e.Commit, e.Path, e.RootCoexists)] = struct{}{}
		liveProvenance[provenanceCacheKey(e.Ref, e.Commit, e.Path)] = struct{}{}
	}
	idRemoved, idFreed := pruneKeyedCache(registry.IdentityCacheRoot(), liveIdentity, dryRun)
	provRemoved, provFreed := pruneKeyedCache(registry.ProvenanceCacheRoot(), liveProvenance, dryRun)
	out.IdentityRemoved = idRemoved
	out.ProvenanceRemoved = provRemoved

	// Live blob hashes = the content present in every reachable worktree.
	liveBlobs := reachableBlobHashes(reach.Worktrees)
	blobRemoved, blobFreed := pruneBlobStore(liveBlobs, dryRun)
	out.BlobsRemoved = blobRemoved

	out.FreedBytes = idFreed + provFreed + blobFreed
	return out, nil
}

// pruneKeyedCache walks a cache whose files are named "<key>.json" (two-char
// shard dirs) and removes any whose key is absent from keep. Returns the count
// removed and the bytes reclaimed. Best-effort: unreadable entries are skipped.
func pruneKeyedCache(root string, keep map[string]struct{}, dryRun bool) (removed int, freed int64) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if filepath.Ext(name) != ".json" {
			return nil
		}
		key := name[:len(name)-len(".json")]
		if _, live := keep[key]; live {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			freed += info.Size()
		}
		removed++
		if !dryRun {
			_ = os.Remove(path)
		}
		return nil
	})
	return removed, freed
}

// pruneBlobStore removes content-store blobs whose hash (the filename) is not in
// keep. An unreferenced blob is a true orphan — no reachable worktree shares its
// extents — so its full file size is genuinely reclaimable.
func pruneBlobStore(keep map[string]struct{}, dryRun bool) (removed int, freed int64) {
	_ = filepath.WalkDir(registry.BlobStoreRoot(), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		// Skip in-flight temp files (".blob-*") the store writer may be renaming.
		if len(name) == 0 || name[0] == '.' {
			return nil
		}
		if _, live := keep[name]; live {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			freed += info.Size()
		}
		removed++
		if !dryRun {
			_ = os.Remove(path)
		}
		return nil
	})
	return removed, freed
}

// reachableBlobHashes returns the set of sha256 hashes (hex) of every regular
// file in every reachable worktree — i.e. the blobs the content store must keep.
// Symlinks are not blobs (they're created directly, never stored) and are
// skipped. Skills are small, so hashing reachable content is cheap relative to a
// prune's other work. Unreadable files are skipped.
func reachableBlobHashes(worktrees map[string]struct{}) map[string]struct{} {
	live := map[string]struct{}{}
	for wt := range worktrees {
		_ = filepath.WalkDir(wt, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if d.Type()&fs.ModeSymlink != 0 {
				return nil
			}
			if sum, herr := hashFile(path); herr == nil {
				live[sum] = struct{}{}
			}
			return nil
		})
	}
	return live
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
