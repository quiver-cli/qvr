package skill

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/model"
)

// Per-entry verification status codes used by VerifySingleEntry. These
// are surfaced unchanged in `qvr lock verify` JSON output, so they're
// part of the public CLI contract.
const (
	VerifyStatusOK         = "ok"
	VerifyStatusDrift      = "drift"
	VerifyStatusUnverified = "unverified"
	VerifyStatusMissing    = "missing"
	VerifyStatusLink       = "link"
	VerifyStatusFailed     = "failed"
	// VerifyStatusRepaired is emitted only by the --repair pass when an
	// entry was rewritten from disk state. Distinct from "ok" so frozen
	// CI can still gate on "this run sealed drift" vs "this run was clean".
	VerifyStatusRepaired = "repaired"
)

// VerifyEntryResult is one row of `qvr lock verify` output.
type VerifyEntryResult struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	SubtreeHash string `json:"subtreeHash,omitempty"`
	// OldSubtreeHash is the hash that was on disk before --repair rewrote
	// the entry. Populated only when Status == "repaired" so JSON
	// consumers can render a before/after diff.
	OldSubtreeHash string            `json:"oldSubtreeHash,omitempty"`
	Drift          []VerifyDriftItem `json:"drift,omitempty"`
	Message        string            `json:"message,omitempty"`
}

// VerifyDriftItem names one field that diverged between recorded and
// computed state for an entry.
type VerifyDriftItem struct {
	Field    string `json:"field"`
	Expected string `json:"expected"`
	Actual   string `json:"actual"`
}

// VerifySingleEntry recomputes the canonical subtree hash for entry's
// worktree and compares it against entry.SubtreeHash. Returns a structured
// result that callers (the CLI, integration tests, future daemons)
// classify into pass/fail per their own policy.
//
// In v5 SubtreeHash is the load-bearing integrity field; verifier also
// cross-checks entry.Commit against the worktree HEAD so a tampered
// commit SHA (rewritten to `deadbeef...` or similar) is caught here —
// previously verify only checked SubtreeHash and a tampered commit field
// passed every audit (issue #73). For mode:edit entries the edit-dir
// repo is authoritative; for shared entries the bare-clone worktree HEAD
// is. Scan/signature/eval signals are surfaced by later phases.
//
// projectRoot is consulted only for mode:edit entries with a relative
// EditPath; callers that don't have a project root in scope may pass "".
func VerifySingleEntry(entry *model.LockEntry, projectRoot string) VerifyEntryResult {
	res := VerifyEntryResult{Name: entry.Name}
	if entry.IsLink() {
		res.Status = VerifyStatusLink
		res.Message = "link install — no upstream to verify"
		return res
	}

	subtreeDir, ok := resolveVerifyPaths(entry, projectRoot, &res)
	if !ok {
		return res
	}

	// SubtreeHash must come from the *on-disk* worktree, not the git tree
	// at HEAD — otherwise tampering with the checked-out files after install
	// is invisible (the git tree object never changes when working-copy
	// bytes do).
	diskHash, err := canonical.HashSubtreeFromDisk(subtreeDir)
	if err != nil {
		res.Status = VerifyStatusFailed
		res.Message = err.Error()
		return res
	}
	res.SubtreeHash = diskHash

	drift := detectVerifyDrift(entry, projectRoot, diskHash)

	if entry.SubtreeHash == "" && len(drift) == 0 {
		res.Status = VerifyStatusUnverified
		res.Message = "no recorded subtree hash (run `qvr lock upgrade` to fill it)"
		return res
	}
	if len(drift) > 0 {
		res.Status = VerifyStatusDrift
		res.Drift = drift
		return res
	}
	res.Status = VerifyStatusOK
	return res
}

// resolveVerifyPaths resolves the on-disk subtree dir to hash. mode:edit entries
// live at <projectRoot>/<EditPath> with a real .git/; shared entries live in the
// bare-clone worktree. On a missing path it records the failure status/message
// on res and returns ok=false so the caller returns early.
func resolveVerifyPaths(entry *model.LockEntry, projectRoot string, res *VerifyEntryResult) (subtreeDir string, ok bool) {
	var repoDir string
	if entry.IsEdit() {
		repoDir = ResolveSkillRepoPath(entry, projectRoot)
		subtreeDir = repoDir
	} else {
		worktreePath := EntryWorktreePath(entry)
		if worktreePath == "" {
			res.Status = VerifyStatusMissing
			res.Message = "worktree path is empty"
			return "", false
		}
		repoDir = worktreePath
		subtreeDir = filepath.Join(worktreePath, entry.Path)
	}
	if repoDir == "" || subtreeDir == "" {
		res.Status = VerifyStatusMissing
		res.Message = "no on-disk path for entry"
		return "", false
	}
	if _, err := os.Stat(repoDir); err != nil {
		res.Status = VerifyStatusMissing
		res.Message = fmt.Sprintf("repo not found: %v", err)
		return "", false
	}
	return subtreeDir, true
}

// detectVerifyDrift compares the recorded SubtreeHash and Commit against the
// on-disk diskHash and repo HEAD, returning any drift items.
//
// Commit cross-check (issue #73): compare entry.Commit against repo HEAD.
// Skipped when entry.Commit is empty (older v5 entries pre-date the field).
// HEAD-read errors are tolerated for shared entries on degraded repos — failing
// to open a git dir is not the same as detecting tamper. Distinguish two cases:
// (a) HEAD descends from entry.Commit (the user committed legitimately on top of
// the lockfile-recorded base — issue #99, no drift) versus (b) entry.Commit
// isn't reachable from HEAD at all (#74 tamper case, real drift).
func detectVerifyDrift(entry *model.LockEntry, projectRoot, diskHash string) []VerifyDriftItem {
	var drift []VerifyDriftItem
	if entry.SubtreeHash != "" && entry.SubtreeHash != diskHash {
		drift = append(drift, VerifyDriftItem{
			Field:    "subtreeHash",
			Expected: entry.SubtreeHash,
			Actual:   diskHash,
		})
	}
	if entry.Commit != "" {
		if head, herr := ResolveEntryHeadCommit(entry, projectRoot); herr == nil && head != "" && head != entry.Commit {
			if ancestor, _ := EntryCommitIsAncestorOfHead(entry, projectRoot); !ancestor {
				drift = append(drift, VerifyDriftItem{
					Field:    "commit",
					Expected: entry.Commit,
					Actual:   head,
				})
			}
		}
	}
	return drift
}
