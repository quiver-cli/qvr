package cmd

import (
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/security"
)

// TestHashScanResult_deterministic guards issue #77 (reportSHA non-determinism):
// the same scan input must produce the same digest across runs so a no-op
// `qvr add <already-installed>` doesn't rewrite the lockfile. Previously the
// scanner stamped ScannedAt into the hashed bytes — every scan rolled a new
// reportSHA even when nothing changed.
func TestHashScanResult_deterministic(t *testing.T) {
	mk := func(stamp string) *security.ScanResult {
		return &security.ScanResult{
			Path:      "/some/path",
			Skill:     "foo",
			ScannedAt: stamp, // varies per run — must not perturb the hash
			Checks:    []string{"shell-injection", "secrets"},
			Findings: []security.Finding{
				{Check: "shell-injection", Severity: security.SeverityWarning, Message: "test"},
			},
			Summary: security.Summary{Warning: 1},
		}
	}
	a := hashScanResult(mk("2026-01-01T00:00:00+00:00"))
	b := hashScanResult(mk("2099-12-31T23:59:59+00:00"))
	if a == "" {
		t.Fatal("hashScanResult returned empty digest")
	}
	if a != b {
		t.Errorf("digest changed across runs:\n  a = %s\n  b = %s", a, b)
	}
}

// TestToScanRef_sentinelOnNoScan guards issue #78: `--no-scan` must produce
// a "skipped" sentinel block in the lockfile so downstream attestation
// pipelines can distinguish "scanned and clean" from "scan was skipped".
// Previously the gate emitted nil and the lockfile entry had no
// verification block at all — indistinguishable from a clean scan when
// scan_on_install was off.
func TestToScanRef_sentinelOnNoScan(t *testing.T) {
	gate := &scanGateResult{
		Skipped:        true,
		DisabledByFlag: true,
	}
	ref := toScanRef(gate)
	if ref == nil {
		t.Fatal("--no-scan should mint a sentinel ScanRef, got nil")
	}
	if ref.Decision != "skipped" {
		t.Errorf("Decision = %q, want %q", ref.Decision, "skipped")
	}
	if ref.Reason != "--no-scan" {
		t.Errorf("Reason = %q, want %q", ref.Reason, "--no-scan")
	}
}

// TestToScanRef_nilWhenConfigDisabled: a skipped gate due to
// scan_on_install=false in config is NOT attested per-entry — that's a
// config choice, not a deliberate per-call opt-out. Distinguishing this
// from --no-scan is the point of DisabledByFlag.
func TestToScanRef_nilWhenConfigDisabled(t *testing.T) {
	gate := &scanGateResult{
		Skipped:        true,
		DisabledByFlag: false,
	}
	if ref := toScanRef(gate); ref != nil {
		t.Errorf("config-disabled gate should not mint a sentinel, got %+v", ref)
	}
}

// TestApplyScanToEntry_clearsStaleBlockOnNoSignal guards the publish-side
// half of issue #71: when the gate produces no recordable signal (config
// disabled or no skill loaded), an existing scan block from a prior commit
// must be CLEARED rather than left attributed to the new commit. Otherwise
// a `--no-scan` publish silently carries the previous commit's scan
// attestation onto a different commit.
func TestApplyScanToEntry_clearsStaleBlockOnNoSignal(t *testing.T) {
	entry := &model.LockEntry{
		Name: "foo",
		Verification: &model.VerificationRecord{
			Scan: &model.ScanRef{
				ReportSHA:      "sha256:stale-from-previous-commit",
				ScannerVersion: "0.6.1",
				Decision:       "allowed",
			},
		},
	}
	// Gate produced no signal — config has scan_on_install off, or skill
	// failed to load. Stale block must be cleared.
	applyScanToEntry(entry, &scanGateResult{Skipped: true, DisabledByFlag: false})
	if entry.Verification != nil && entry.Verification.Scan != nil {
		t.Errorf("expected nil scan block, got %+v", entry.Verification.Scan)
	}
}

// TestApplyScanToEntry_writesSentinelOnNoScan covers the --no-scan publish
// path: the stale block from a prior commit must be replaced with the
// "skipped" sentinel so the lockfile no longer attributes the old commit's
// findings to the new commit (issue #71).
func TestApplyScanToEntry_writesSentinelOnNoScan(t *testing.T) {
	entry := &model.LockEntry{
		Name: "foo",
		Verification: &model.VerificationRecord{
			Scan: &model.ScanRef{
				ReportSHA: "sha256:stale-from-previous-commit",
				Decision:  "allowed",
			},
		},
	}
	applyScanToEntry(entry, &scanGateResult{Skipped: true, DisabledByFlag: true})
	if entry.Verification == nil || entry.Verification.Scan == nil {
		t.Fatal("--no-scan publish should keep a sentinel scan block")
	}
	if entry.Verification.Scan.Decision != "skipped" {
		t.Errorf("Decision = %q, want %q", entry.Verification.Scan.Decision, "skipped")
	}
	if entry.Verification.Scan.ReportSHA != "" {
		t.Errorf("sentinel must not carry the stale ReportSHA, got %q", entry.Verification.Scan.ReportSHA)
	}
}
