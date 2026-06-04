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
	Scan        *ScanRef        `json:"scan,omitempty" toml:"scan,omitempty"`
	Eval        *EvalRef        `json:"eval,omitempty" toml:"eval,omitempty"`
	Provenance  *ProvenanceRef  `json:"provenance,omitempty" toml:"provenance,omitempty"`
	Signature   *SignatureBlock `json:"signature,omitempty" toml:"signature,omitempty"`
	Attestation *ArtifactRef    `json:"attestation,omitempty" toml:"attestation,omitempty"`
	SkillCard   *ArtifactRef    `json:"skillCard,omitempty" toml:"skillCard,omitempty"`
}

// IsEmpty reports whether the record carries any signal. Callers can use
// this before assignment to decide whether to set entry.Verification or
// leave it nil (so it stays omitted from disk).
func (v *VerificationRecord) IsEmpty() bool {
	if v == nil {
		return true
	}
	return v.Scan == nil && v.Eval == nil && v.Provenance == nil &&
		v.Signature == nil && v.Attestation == nil && v.SkillCard == nil
}

// ArtifactRef points at a JSON or YAML artifact alongside the skill,
// identified by its canonical sha256. Used for SKILLCARD.yaml,
// .quiver-attestation.json, and similar.
type ArtifactRef struct {
	Path   string `json:"path" toml:"path"`
	SHA256 string `json:"sha256" toml:"sha256"`
	Schema string `json:"schema,omitempty" toml:"schema,omitempty"`
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
	ReportSHA      string         `json:"reportSHA,omitempty" toml:"reportSHA,omitempty"`
	ScannerVersion string         `json:"scannerVersion,omitempty" toml:"scannerVersion,omitempty"`
	Counts         SeverityCounts `json:"counts" toml:"counts"`
	Decision       string         `json:"decision" toml:"decision"`
	Reason         string         `json:"reason,omitempty" toml:"reason,omitempty"`
	SarifPath      string         `json:"sarifPath,omitempty" toml:"sarifPath,omitempty"`
}

// EvalRef summarises an eval-harness pass. Scores is open-ended — metric
// IDs are decided by the harness, not pinned by the lockfile schema.
type EvalRef struct {
	ReportSHA      string             `json:"reportSHA" toml:"reportSHA"`
	HarnessVersion string             `json:"harnessVersion" toml:"harnessVersion"`
	SuiteSHA       string             `json:"suiteSHA" toml:"suiteSHA"`
	Scores         map[string]float64 `json:"scores,omitempty" toml:"scores,omitempty"`
	Passed         bool               `json:"passed" toml:"passed"`
}

// Git-native provenance signature statuses. v1 trust is "uv for agent
// skills": provenance is optional metadata derived from `git verify-tag` /
// `git verify-commit`, never a qvr-native signing format. SignatureStatusNone
// is the common case (most skill repos are unsigned) and only blocks when
// security.require_signed is enabled; SignatureStatusInvalid always blocks,
// because a present-but-broken signature signals tampering.
const (
	SignatureStatusVerified = "verified" // git verified a good signature
	SignatureStatusNone     = "none"     // no signature present on tag/commit
	SignatureStatusInvalid  = "invalid"  // signature present but failed verification
)

// ProvenanceRef records optional, git-native provenance for an installed
// skill: whether the resolved ref carried a verifiable Git signature, and
// who signed it. It is informational unless policy requires verified
// signatures. This is the v1 trust surface; the cryptographic
// Signature/Attestation slots stay reserved for a future signing track.
type ProvenanceRef struct {
	Provider        string `json:"provider" toml:"provider"`                 // "git"
	Tag             string `json:"tag,omitempty" toml:"tag,omitempty"`       // the ref verified, when a tag
	SignatureStatus string `json:"signatureStatus" toml:"signatureStatus"`   // verified | none | invalid
	Signer          string `json:"signer,omitempty" toml:"signer,omitempty"` // signer identity reported by git
}

// SignatureBlock captures everything needed to re-verify a signature
// offline against a trusted public key. ManifestDigest duplicates the
// outer VerificationRecord.SubtreeHash so a SignatureBlock is
// self-contained for replay.
type SignatureBlock struct {
	Path           string `json:"path" toml:"path"`
	EnvelopeSHA    string `json:"envelopeSHA" toml:"envelopeSHA"`
	Algorithm      string `json:"algorithm" toml:"algorithm"`
	SignerID       string `json:"signerID,omitempty" toml:"signerID,omitempty"`
	PublicKeySHA   string `json:"publicKeySHA,omitempty" toml:"publicKeySHA,omitempty"`
	ManifestDigest string `json:"manifestDigest" toml:"manifestDigest"`
}

// SeverityCounts is the per-severity tally of scanner findings. Phase 2's
// scanner writers populate this; Phase 1 only reserves the type.
type SeverityCounts struct {
	Critical int `json:"critical" toml:"critical"`
	High     int `json:"high" toml:"high"`
	Medium   int `json:"medium" toml:"medium"`
	Low      int `json:"low" toml:"low"`
	Info     int `json:"info" toml:"info"`
}
