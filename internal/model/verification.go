package model

// VerificationRecord carries supply-chain signals for an installed skill.
// In v5 it's a thin omitempty carrier — present on a LockEntry only when at
// least one real signal has been recorded. Identity (SubtreeHash, Commit)
// lives at the top level of LockEntry; this struct holds only the optional
// per-pipeline-stage artifacts.
//
// A scan run populates Scan. A future signing pipeline populates Signature.
// Eval, Attestation, and SkillCard are reserved for the corresponding
// pipeline stages; their writers don't exist yet but the slots are stable
// so adding them won't require another schema bump.
type VerificationRecord struct {
	Scan        *ScanRef        `json:"scan,omitempty"`
	Eval        *EvalRef        `json:"eval,omitempty"`
	Signature   *SignatureBlock `json:"signature,omitempty"`
	Attestation *ArtifactRef    `json:"attestation,omitempty"`
	SkillCard   *ArtifactRef    `json:"skillCard,omitempty"`
}

// IsEmpty reports whether the record carries any signal. Callers can use
// this before assignment to decide whether to set entry.Verification or
// leave it nil (so it stays omitted from disk).
func (v *VerificationRecord) IsEmpty() bool {
	if v == nil {
		return true
	}
	return v.Scan == nil && v.Eval == nil && v.Signature == nil && v.Attestation == nil && v.SkillCard == nil
}

// ArtifactRef points at a JSON or YAML artifact alongside the skill,
// identified by its canonical sha256. Used for SKILLCARD.yaml,
// .quiver-attestation.json, and similar.
type ArtifactRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Schema string `json:"schema,omitempty"`
}

// ScanRef summarises a scanner pass. The full report lives on disk; only
// the digest and counts ride in the lockfile so `qvr lock verify` can
// detect drift without re-reading the report.
//
// Decision == "skipped" is a sentinel emitted when the gate was explicitly
// disabled (e.g. `--no-scan`). It carries no ReportSHA/Counts but does
// record Reason so a downstream attestation pipeline can tell "scanned
// and clean" from "scan was skipped". Without this sentinel, an absent
// or empty scan block is ambiguous (issue #78 / #71).
type ScanRef struct {
	ReportSHA      string         `json:"reportSHA,omitempty"`
	ScannerVersion string         `json:"scannerVersion,omitempty"`
	Counts         SeverityCounts `json:"counts"`
	Decision       string         `json:"decision"`
	Reason         string         `json:"reason,omitempty"`
	SarifPath      string         `json:"sarifPath,omitempty"`
}

// EvalRef summarises an eval-harness pass. Scores is open-ended — metric
// IDs are decided by the harness, not pinned by the lockfile schema.
type EvalRef struct {
	ReportSHA      string             `json:"reportSHA"`
	HarnessVersion string             `json:"harnessVersion"`
	SuiteSHA       string             `json:"suiteSHA"`
	Scores         map[string]float64 `json:"scores,omitempty"`
	Passed         bool               `json:"passed"`
}

// SignatureBlock captures everything needed to re-verify a signature
// offline against a trusted public key. ManifestDigest duplicates the
// outer VerificationRecord.SubtreeHash so a SignatureBlock is
// self-contained for replay.
type SignatureBlock struct {
	Path           string `json:"path"`
	EnvelopeSHA    string `json:"envelopeSHA"`
	Algorithm      string `json:"algorithm"`
	SignerID       string `json:"signerID,omitempty"`
	PublicKeySHA   string `json:"publicKeySHA,omitempty"`
	ManifestDigest string `json:"manifestDigest"`
}

// SeverityCounts is the per-severity tally of scanner findings. Phase 2's
// scanner writers populate this; Phase 1 only reserves the type.
type SeverityCounts struct {
	Critical int `json:"critical"`
	High     int `json:"high"`
	Medium   int `json:"medium"`
	Low      int `json:"low"`
	Info     int `json:"info"`
}
