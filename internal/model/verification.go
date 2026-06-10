package model

// ScanRef summarises a scanner pass. The full report lives on disk; only
// the digest and counts ride in the lockfile so `qvr lock verify` can
// detect drift without re-reading the report. Serialized as an inline
// table directly on the lock entry (v6) — there is no intermediate
// verification wrapper.
//
// Decision == "skipped" is a sentinel emitted when the gate was explicitly
// disabled (e.g. `--no-scan`). It carries no ReportSHA/Counts but does
// record Reason so a downstream attestation pipeline can tell "scanned
// and clean" from "scan was skipped". Without this sentinel, an absent
// or empty scan block is ambiguous (issue #78 / #71).
type ScanRef struct {
	ReportSHA      string         `json:"reportSHA,omitempty" toml:"reportSHA,omitempty"`
	ScannerVersion string         `json:"scannerVersion,omitempty" toml:"scannerVersion,omitempty"`
	Counts         SeverityCounts `json:"counts" toml:"counts,inline"`
	Decision       string         `json:"decision" toml:"decision"`
	Reason         string         `json:"reason,omitempty" toml:"reason,omitempty"`
	SarifPath      string         `json:"sarifPath,omitempty" toml:"sarifPath,omitempty"`
}

// Git-native provenance signature statuses. v1 trust is "uv for agent
// skills": provenance is optional metadata derived from `git verify-tag` /
// `git verify-commit`, never a qvr-native signing format. SignatureStatusNone
// is the common case (most skill repos are unsigned) and only blocks when
// security.require_signed is enabled; SignatureStatusInvalid always blocks,
// because a present-but-broken signature signals tampering. An empty
// SignatureStatus means the signature was never checked (e.g. git couldn't
// read the repo) — distinct from "checked and unsigned".
const (
	SignatureStatusVerified = "verified" // git verified a good signature
	SignatureStatusNone     = "none"     // no signature present on tag/commit
	SignatureStatusInvalid  = "invalid"  // signature present but failed verification
)

// ProvenanceRef classifies everything about where an installed skill's
// content came from and who made it: the authoring identity of the commit
// that last touched the skill subtree, the git-native signature check on
// the resolved ref, and source-lineage markers written by `qvr edit` /
// `qvr publish --fork --migrate`. Serialized as an inline table directly
// on the lock entry (v6). All of it is informational unless policy gates
// on it (author pins, security.require_signed). Provenance is always
// git-derived — there is no provider discriminator.
type ProvenanceRef struct {
	// CommitAuthor is the author identity (`Name <email>`) on the commit
	// that last modified the skill subtree — not the branch tip. Trust
	// policy can pin allowed authors per registry.
	CommitAuthor string `json:"commitAuthor,omitempty" toml:"commitAuthor,omitempty"`

	// Tag is the ref whose signature was verified, when it was a tag.
	Tag string `json:"tag,omitempty" toml:"tag,omitempty"`

	// SignatureStatus is verified | none | invalid, or empty when the
	// signature was never checked.
	SignatureStatus string `json:"signatureStatus,omitempty" toml:"signatureStatus,omitempty"`

	// Signer is the signer identity reported by git on a verified signature.
	Signer string `json:"signer,omitempty" toml:"signer,omitempty"`

	// Upstream records the original upstream URL when an entry has moved
	// off its first source — set by `qvr edit` (mirrors Source at eject
	// time) and preserved through `qvr publish --fork --migrate` (when
	// Source flips to the fork URL). Empty for entries that never
	// diverged. Never used to drive pushes.
	Upstream string `json:"upstream,omitempty" toml:"upstream,omitempty"`

	// ForkedFrom records the upstream this skill was forked from when
	// published via `qvr publish --fork --migrate`, as
	// "<git-url>@<short-sha>". Set on the author's local lock at migrate
	// time; the published artifact's SKILL.md is never mutated, so
	// downstream consumers don't carry this unless they themselves
	// migrate. v0.9's trust layer will read this to verify fork policy.
	ForkedFrom string `json:"forkedFrom,omitempty" toml:"forkedFrom,omitempty"`
}

// IsEmpty reports whether the record carries any signal. Callers use this
// before assignment to decide whether to set entry.Provenance or leave it
// nil (so it stays omitted from disk).
func (p *ProvenanceRef) IsEmpty() bool {
	if p == nil {
		return true
	}
	return *p == ProvenanceRef{}
}

// SeverityCounts is the per-severity tally of scanner findings.
type SeverityCounts struct {
	Critical int `json:"critical" toml:"critical"`
	High     int `json:"high" toml:"high"`
	Medium   int `json:"medium" toml:"medium"`
	Low      int `json:"low" toml:"low"`
	Info     int `json:"info" toml:"info"`
}
