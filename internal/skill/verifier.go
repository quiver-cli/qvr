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

// VerifySingleEntry recomputes the canonical subtree hash for entry's
// worktree and compares it against entry.SubtreeHash. Returns a structured
// result that callers (the CLI, integration tests, future daemons)
// classify into pass/fail per their own policy.
//
// In v5 SubtreeHash is the single load-bearing integrity field; verifier
// no longer compares git tree/commit SHAs separately (the working-copy
// hash captures any tamper that matters). Scan/signature/eval signals are
// surfaced via VerifyEntryResult by later phases.
func VerifySingleEntry(entry *model.LockEntry) VerifyEntryResult {
	res := VerifyEntryResult{Name: entry.Name}
	if entry.IsLink() {
		res.Status = VerifyStatusLink
		res.Message = "link install — no upstream to verify"
		return res
	}
	worktreePath := EntryWorktreePath(entry)
	if worktreePath == "" {
		res.Status = VerifyStatusMissing
		res.Message = "worktree path is empty"
		return res
	}
	if _, err := os.Stat(worktreePath); err != nil {
		res.Status = VerifyStatusMissing
		res.Message = fmt.Sprintf("worktree not found: %v", err)
		return res
	}
	// SubtreeHash must come from the *on-disk* worktree, not the git tree
	// at HEAD — otherwise tampering with the checked-out files after install
	// is invisible (the git tree object never changes when working-copy
	// bytes do). The disk hasher mirrors HashSubtree's algorithm so an
	// untampered checkout produces the same digest the installer recorded.
	diskHash, err := canonical.HashSubtreeFromDisk(filepath.Join(worktreePath, entry.Path))
	if err != nil {
		res.Status = VerifyStatusFailed
		res.Message = err.Error()
		return res
	}
	res.SubtreeHash = diskHash

	if entry.SubtreeHash == "" {
		res.Status = VerifyStatusUnverified
		res.Message = "no recorded subtree hash (run `qvr lock upgrade` to fill it)"
		return res
	}

	if entry.SubtreeHash != diskHash {
		res.Status = VerifyStatusDrift
		res.Drift = []VerifyDriftItem{{
			Field:    "subtreeHash",
			Expected: entry.SubtreeHash,
			Actual:   diskHash,
		}}
		return res
	}
	res.Status = VerifyStatusOK
	return res
}
