package skill

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/model"
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

// VerifySingleEntry recomputes the canonical subtree hash for entry and
// compares it against the recorded VerificationRecord. Returns a
// structured result that callers (the CLI, integration tests, future
// daemons) classify into pass/fail per their own policy.
//
// Phase 1 detects drift at the subtree/tree/commit level; signature and
// scan verification are wired in later phases by extending the result
// with additional Drift entries.
func VerifySingleEntry(entry *model.LockEntry) VerifyEntryResult {
	res := VerifyEntryResult{Name: entry.Name}
	if entry.Source == "link" {
		res.Status = VerifyStatusLink
		res.Message = "link install — no upstream to verify"
		return res
	}
	if entry.Worktree == "" {
		res.Status = VerifyStatusMissing
		res.Message = "worktree path is empty"
		return res
	}
	if _, err := os.Stat(entry.Worktree); err != nil {
		res.Status = VerifyStatusMissing
		res.Message = fmt.Sprintf("worktree not found: %v", err)
		return res
	}
	// SubtreeHash must come from the *on-disk* worktree, not the git tree
	// at HEAD — otherwise tampering with the checked-out files after install
	// is invisible (the git tree object never changes when working-copy
	// bytes do). The disk hasher mirrors HashSubtree's algorithm so an
	// untampered checkout produces the same digest the installer recorded.
	diskHash, err := canonical.HashSubtreeFromDisk(filepath.Join(entry.Worktree, entry.Path))
	if err != nil {
		res.Status = VerifyStatusFailed
		res.Message = err.Error()
		return res
	}
	res.SubtreeHash = diskHash

	// TreeSHA / CommitSHA still come from the git tree at HEAD — they
	// describe the upstream commit pointer, which is orthogonal to whether
	// the working copy has been mutated. Soft-fail (warning, not failure)
	// when the git side can't be read: a missing .git directory in the
	// worktree shouldn't mask a working-copy tamper detected above.
	gitID, gitErr := canonical.HashSubtree(entry.Worktree, entry.Path)
	if entry.Verification == nil || entry.Verification.SubtreeHash == "" {
		res.Status = VerifyStatusUnverified
		res.Message = "no recorded subtree hash (legacy entry — run `qvr lock upgrade`)"
		return res
	}

	rec := entry.Verification
	var drift []VerifyDriftItem
	if rec.SubtreeHash != diskHash {
		drift = append(drift, VerifyDriftItem{Field: "subtreeHash", Expected: rec.SubtreeHash, Actual: diskHash})
	}
	if gitErr == nil {
		if rec.TreeSHA != "" && rec.TreeSHA != gitID.TreeSHA {
			drift = append(drift, VerifyDriftItem{Field: "treeSHA", Expected: rec.TreeSHA, Actual: gitID.TreeSHA})
		}
		if rec.CommitSHA != "" && rec.CommitSHA != gitID.CommitSHA {
			drift = append(drift, VerifyDriftItem{Field: "commitSHA", Expected: rec.CommitSHA, Actual: gitID.CommitSHA})
		}
	}
	if len(drift) == 0 {
		res.Status = VerifyStatusOK
		return res
	}
	res.Status = VerifyStatusDrift
	res.Drift = drift
	return res
}
