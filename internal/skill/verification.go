package skill

import (
	"fmt"
	"path/filepath"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

// ComputeSubtreeHash returns the canonical content hash of a skill subtree
// rooted at worktreePath/subpath. This is the load-bearing integrity value
// stored on LockEntry.SubtreeHash — drift detection compares this to a
// fresh recomputation.
func ComputeSubtreeHash(worktreePath, subpath string) (string, error) {
	id, err := canonical.HashSubtree(worktreePath, subpath)
	if err != nil {
		return "", fmt.Errorf("canonical hash: %w", err)
	}
	return id.SubtreeHash, nil
}

// EntryWorktreePath returns the on-disk worktree path for a lock entry by
// re-deriving it from its registry / name / install-commit via
// registry.WorktreePath. Link installs return their Source (the absolute
// local path).
//
// The path is keyed by entry.InstallCommit (shortened to 7 hex) — pinned
// at install time so Pull / Switch advancing entry.Commit doesn't move the
// directory out from under the existing symlinks. Entries written before
// this field existed fall back to entry.Commit so legacy v5 installs keep
// resolving.
func EntryWorktreePath(entry *model.LockEntry) string {
	if entry == nil {
		return ""
	}
	if entry.IsLink() {
		return entry.Source
	}
	key := entry.InstallCommit
	if key == "" {
		key = entry.Commit
	}
	if key == "" {
		return ""
	}
	return registry.WorktreePath(entry.Registry, entry.Name, registry.ShortSHA(key))
}

// RefreshSubtreeHash recomputes entry.SubtreeHash from the on-disk worktree.
// Called after Pull / Switch / Upgrade so the lock stays aligned with the
// git state. Link installs are skipped — they have no upstream subtree to
// re-hash from this code path.
func RefreshSubtreeHash(entry *model.LockEntry) error {
	if entry == nil || entry.IsLink() {
		return nil
	}
	worktreePath := EntryWorktreePath(entry)
	hash, err := ComputeSubtreeHash(worktreePath, entry.Path)
	if err != nil {
		return err
	}
	entry.SubtreeHash = hash
	return nil
}

// RepairResult captures what RepairSubtreeHashFromDisk changed about an
// entry. Empty OldSubtreeHash means the entry had no recorded hash before
// repair. NewSubtreeHash is empty only on failure.
//
// OldCommit / NewCommit are populated when --repair healed entry.Commit
// to the worktree HEAD (issue #73). Empty when no commit-field drift was
// detected. This is the in-band fix for a tampered or stale commit field;
// callers can render a before/after diff from the two values.
type RepairResult struct {
	OldSubtreeHash string
	NewSubtreeHash string
	OldCommit      string
	NewCommit      string
	Failed         bool
	Error          string
}

// RepairSubtreeHashFromDisk rewrites entry.SubtreeHash using the on-disk
// worktree (working copy, including uncommitted edits) as the source of
// truth. This is the in-band recovery path for the `qvr edit` workflow
// where the user knowingly intends their disk state to be what's recorded.
//
// Also re-seals entry.Commit to the worktree HEAD when the recorded value
// has drifted (issue #73 — without this, a tampered `commit` field could
// only be fixed by manual lockfile editing).
//
// Unlike RefreshSubtreeHash, which uses HashSubtree (git tree at HEAD) and
// is therefore blind to uncommitted edits, this uses HashSubtreeFromDisk.
//
// projectRoot is consulted only for mode:edit entries with a relative
// EditPath; callers without one in scope may pass "".
func RepairSubtreeHashFromDisk(entry *model.LockEntry, projectRoot string) RepairResult {
	res := RepairResult{}
	if entry == nil || entry.IsLink() {
		res.Failed = true
		res.Error = "link install — no subtree to repair"
		return res
	}
	res.OldSubtreeHash = entry.SubtreeHash

	subtreeDir := ResolveSkillRepoPath(entry, projectRoot)
	if entry.IsEdit() {
		// mode:edit entries hash the edit dir directly — entry.Path doesn't
		// apply because the edit dir IS the skill, not a subpath of one.
	} else {
		subtreeDir = filepath.Join(EntryWorktreePath(entry), entry.Path)
	}
	diskHash, err := canonical.HashSubtreeFromDisk(subtreeDir)
	if err != nil {
		res.Failed = true
		res.Error = err.Error()
		return res
	}
	entry.SubtreeHash = diskHash
	res.NewSubtreeHash = diskHash

	// Re-seal entry.Commit to the worktree HEAD when it has drifted. Failure
	// to read HEAD is non-fatal — a degraded repo shouldn't block repair of
	// the subtree hash, which is still load-bearing.
	if head, hErr := ResolveEntryHeadCommit(entry, projectRoot); hErr == nil && head != "" && head != entry.Commit {
		res.OldCommit = entry.Commit
		res.NewCommit = head
		entry.Commit = head
	}
	return res
}
