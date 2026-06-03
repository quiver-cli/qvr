package skill

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/registry"
)

// CheckGitProvenance derives optional, git-native provenance for an install:
// whether the resolved ref carries a verifiable Git signature, and who signed
// it. It prefers a signed annotated tag (the requested ref) and falls back to
// the commit-level signature. Returns nil when nothing could be checked
// (e.g. git couldn't read the repo) so the caller records no misleading
// "none". A returned ProvenanceRef with SignatureStatus == invalid is the
// only outcome an install should treat as fatal.
//
// repoPath is the bare/working repo to verify against; ref is the requested
// version label; commit is the resolved SHA used for the commit-level
// fallback.
func CheckGitProvenance(repoPath, ref, commit string) *model.ProvenanceRef {
	ctx := context.Background()
	// Prefer the requested ref as a signed annotated tag.
	if status, signer, err := git.VerifyTagSignature(ctx, repoPath, ref); err == nil {
		if status == git.SigVerified || status == git.SigInvalid {
			return &model.ProvenanceRef{
				Provider:        "git",
				Tag:             ref,
				SignatureStatus: status,
				Signer:          signer,
			}
		}
	}
	// Fall back to the commit-level signature.
	target := commit
	if target == "" {
		target = ref
	}
	status, signer, err := git.VerifyCommitSignature(ctx, repoPath, target)
	if err != nil {
		return nil
	}
	return &model.ProvenanceRef{
		Provider:        "git",
		SignatureStatus: status,
		Signer:          signer,
	}
}

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

// ComputeEntryIdentity computes the canonical identity of a lock entry's
// content from the git tree at worktreePath's HEAD, honoring root-coexist
// scoping. A rootCoexists entry (path ".", sharing its repo with sibling
// skills) is hashed over SKILL.md + the recognized content dirs only — the
// same scope the sparse worktree carries, so this digest matches the verifier's
// disk hash. Every other entry hashes its path subtree (or the whole repo for a
// lone root, via the "." → "" normalization in canonical). Issues #151/#154.
func ComputeEntryIdentity(worktreePath, path string, rootCoexists bool) (*canonical.SubtreeIdentity, error) {
	var (
		id  *canonical.SubtreeIdentity
		err error
	)
	if rootCoexists {
		id, err = canonical.HashScoped(worktreePath, model.SkillScopePaths(path, true))
	} else {
		id, err = canonical.HashSubtree(worktreePath, path)
	}
	if err != nil {
		return nil, fmt.Errorf("canonical hash: %w", err)
	}
	return id, nil
}

// ComputeEntryIdentityAtCommit is ComputeEntryIdentity pinned to an explicit
// commit in a bare clone (no worktree) — used by `qvr lock` re-pin.
func ComputeEntryIdentityAtCommit(repoPath, commit, path string, rootCoexists bool) (*canonical.SubtreeIdentity, error) {
	var (
		id  *canonical.SubtreeIdentity
		err error
	)
	if rootCoexists {
		id, err = canonical.HashScopedAtCommit(repoPath, commit, model.SkillScopePaths(path, true))
	} else {
		id, err = canonical.HashSubtreeAtCommit(repoPath, commit, path)
	}
	if err != nil {
		return nil, fmt.Errorf("canonical hash: %w", err)
	}
	return id, nil
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
//
// Aliased entries (installed via `qvr add <skill> --as <alias>`) have
// entry.Name == alias but the worktree on disk is keyed by the canonical
// registry-side name — the install path builds finalPath with the
// canonical name (installer.go: registry.WorktreePath(reg, name, sha)),
// so we mirror that here by preferring entry.Canonical when set. Without
// this every read-side caller (info, status, diff, edit, doctor) goes
// looking at .../reg/<alias>/<sha> while the real dir lives at
// .../reg/<canonical>/<sha>. Issue #102.
func EntryWorktreePath(entry *model.LockEntry) string {
	// Delegates to registry.WorktreePathForEntry — the single source of truth
	// for the alias/install-commit derivation. Keeping the logic here too is
	// what let registry.Reachable drift and delete referenced alias worktrees
	// (issue #158); this wrapper stays only for the established call sites.
	return registry.WorktreePathForEntry(entry)
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
	id, err := ComputeEntryIdentity(worktreePath, entry.Path, entry.RootCoexists)
	if err != nil {
		return err
	}
	entry.SubtreeHash = id.SubtreeHash
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
