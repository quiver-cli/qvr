package skill

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/raks097/quiver/internal/canonical"
	"github.com/raks097/quiver/internal/model"
)

// PopulateVerification computes a fresh VerificationRecord for entry. The
// record carries the canonical subtree hash, git tree/commit identifiers,
// caller-supplied provenance, and a best-effort signature skeleton if a
// qvr.sig is found on disk.
//
// Phase 1 never validates signatures — `Status` reflects only structural
// state: "unverified" (no signature present), "untrusted" (signature found
// but no verifier wired yet), or "failed" (hashing itself errored). Phase
// 5 will replace the placeholder warnings with real cryptographic checks.
//
// Returns nil for link-source entries; symlinked local skills carry no
// supply-chain provenance because there's no upstream to record.
func PopulateVerification(entry *model.LockEntry, prov model.ProvenanceRef) *model.VerificationRecord {
	if entry == nil || entry.Source == "link" {
		return nil
	}
	now := time.Now().UTC()
	if prov.FetchedAt.IsZero() {
		prov.FetchedAt = now
	}

	rec := &model.VerificationRecord{
		Provenance: prov,
		Status:     model.StatusUnverified,
		Warnings:   []string{"no signature present"},
		VerifiedAt: now,
	}

	id, err := canonical.HashSubtree(entry.Worktree, entry.Path)
	if err != nil {
		rec.Status = model.StatusFailed
		rec.Warnings = []string{fmt.Sprintf("canonical hash failed: %v", err)}
		return rec
	}
	rec.SubtreeHash = id.SubtreeHash
	rec.TreeSHA = id.TreeSHA
	rec.CommitSHA = id.CommitSHA

	// Best-effort signature pickup. The on-disk envelope is parsed for
	// metadata only — Phase 5 owns actual signature validation. We mark
	// the entry "untrusted" so downstream tooling can distinguish "no sig"
	// from "sig present but unverified".
	sigPath := filepath.Join(entry.Worktree, entry.Path, "qvr.sig")
	if data, err := os.ReadFile(sigPath); err == nil {
		var env canonical.QvrSignature
		if jerr := json.Unmarshal(data, &env); jerr == nil {
			block := &model.SignatureBlock{
				Path:           "qvr.sig",
				Algorithm:      env.Algorithm,
				ManifestDigest: id.SubtreeHash,
			}
			if digest, derr := env.EnvelopeDigest(); derr == nil {
				block.EnvelopeSHA = digest
			}
			rec.Signature = block
			rec.Status = model.StatusUntrusted
			rec.Warnings = []string{"signature found but verification not implemented"}
		} else {
			rec.Warnings = []string{fmt.Sprintf("qvr.sig present but unparseable: %v", jerr)}
		}
	}
	return rec
}

// RefreshVerification recomputes entry.Verification in place using the
// existing Provenance (if any). Used after operations that change the
// worktree's git state — Pull, Switch, Upgrade — so the lockfile's
// recorded SubtreeHash/CommitSHA/TreeSHA stay aligned with reality.
//
// If the entry has no prior Verification block, an empty ProvenanceRef
// is used. Link-source entries are left untouched (no worktree to hash).
func RefreshVerification(entry *model.LockEntry) {
	if entry == nil || entry.Source == "link" {
		return
	}
	var prov model.ProvenanceRef
	if entry.Verification != nil {
		prov = entry.Verification.Provenance
	}
	// Reset FetchedAt so the refresh is honest about when the worktree
	// state was last reconciled. The rest of Provenance (registry name,
	// URL, subpath) remains pinned to the original source.
	prov.FetchedAt = time.Time{}
	entry.Verification = PopulateVerification(entry, prov)
}

// RepairResult captures what RepairVerificationFromDisk changed about an
// entry's Verification block. Empty OldSubtreeHash means the entry had no
// recorded hash before repair (an unverified entry being sealed for the
// first time). NewSubtreeHash is empty only on failure.
type RepairResult struct {
	OldSubtreeHash string
	NewSubtreeHash string
	Failed         bool
	Error          string
}

// RepairVerificationFromDisk rewrites entry.Verification using the on-disk
// worktree as the source of truth for SubtreeHash. This is the in-band
// recovery path for intentional local edits (the `qvr edit` workflow) where
// the user knows the disk state is what they want recorded.
//
// Unlike RefreshVerification, which uses HashSubtree (git tree at HEAD) and
// is therefore blind to working-copy edits that haven't been committed, this
// uses HashSubtreeFromDisk for SubtreeHash. TreeSHA and CommitSHA still come
// from git HEAD — they describe the upstream commit pointer, which is the
// same regardless of working-copy state.
//
// Returns RepairResult so callers can report the before/after hashes to the
// user. Link-source and missing-worktree entries return Failed=true.
func RepairVerificationFromDisk(entry *model.LockEntry) RepairResult {
	res := RepairResult{}
	if entry == nil || entry.Source == "link" {
		res.Failed = true
		res.Error = "link install — no provenance to repair"
		return res
	}
	if entry.Verification != nil {
		res.OldSubtreeHash = entry.Verification.SubtreeHash
	}

	var prov model.ProvenanceRef
	if entry.Verification != nil {
		prov = entry.Verification.Provenance
	}
	prov.FetchedAt = time.Time{}

	rec, err := buildVerificationFromDisk(entry, prov)
	if err != nil {
		res.Failed = true
		res.Error = err.Error()
		return res
	}
	entry.Verification = rec
	res.NewSubtreeHash = rec.SubtreeHash
	return res
}

// buildVerificationFromDisk mirrors PopulateVerification but sources the
// SubtreeHash from disk via HashSubtreeFromDisk. TreeSHA/CommitSHA still
// come from the git tree at HEAD — they're orthogonal to working-copy
// edits and missing them shouldn't block a repair.
func buildVerificationFromDisk(entry *model.LockEntry, prov model.ProvenanceRef) (*model.VerificationRecord, error) {
	now := time.Now().UTC()
	if prov.FetchedAt.IsZero() {
		prov.FetchedAt = now
	}
	rec := &model.VerificationRecord{
		Provenance: prov,
		Status:     model.StatusUnverified,
		Warnings:   []string{"no signature present"},
		VerifiedAt: now,
	}

	diskHash, err := canonical.HashSubtreeFromDisk(filepath.Join(entry.Worktree, entry.Path))
	if err != nil {
		return nil, fmt.Errorf("hash disk subtree: %w", err)
	}
	rec.SubtreeHash = diskHash

	// Soft-fail on the git side — a worktree with a missing .git directory
	// is still a valid repair target as long as the disk hash is computable.
	if id, gerr := canonical.HashSubtree(entry.Worktree, entry.Path); gerr == nil {
		rec.TreeSHA = id.TreeSHA
		rec.CommitSHA = id.CommitSHA
	}

	sigPath := filepath.Join(entry.Worktree, entry.Path, "qvr.sig")
	if data, err := os.ReadFile(sigPath); err == nil {
		var env canonical.QvrSignature
		if jerr := json.Unmarshal(data, &env); jerr == nil {
			block := &model.SignatureBlock{
				Path:           "qvr.sig",
				Algorithm:      env.Algorithm,
				ManifestDigest: diskHash,
			}
			if digest, derr := env.EnvelopeDigest(); derr == nil {
				block.EnvelopeSHA = digest
			}
			rec.Signature = block
			rec.Status = model.StatusUntrusted
			rec.Warnings = []string{"signature found but verification not implemented"}
		} else {
			rec.Warnings = []string{fmt.Sprintf("qvr.sig present but unparseable: %v", jerr)}
		}
	}
	return rec, nil
}
