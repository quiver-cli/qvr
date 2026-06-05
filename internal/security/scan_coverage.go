package security

import (
	"context"
	"fmt"

	"github.com/quiver-cli/qvr/internal/model"
)

// CoverageCheckName is the [Check.Name] of the scan-coverage check. It
// reports files the deterministic detectors could not inspect — the
// >maxScanBytes truncation in [WalkSkill] and binary files — so a
// "scan clean" verdict cannot silently cover unread content.
//
// Filed as issue #34: a one-byte pad over the 1 MiB cap turned every
// detector blind without any signal in the JSON / text report. The
// check emits informational findings so consumers (CI, dashboards)
// can still see the gap.
const CoverageCheckName = "coverage"

type coverageCheck struct{}

// NewCoverageCheck returns the scan-coverage check. It walks the file
// list (already produced by [WalkSkill]) and emits one info finding
// per file whose contents the other checks did not scan:
//
//   - Truncated == true: file exceeded the per-file size cap, so its
//     contents were never read.
//   - IsBinary == true: content read but skipped to avoid false
//     positives from random-byte sequences in compiled artifacts.
//
// The check intentionally does not raise the cap or the binary
// detection — that's a policy decision for the user. Its job is to
// guarantee the report names every blind spot.
func NewCoverageCheck() Check { return coverageCheck{} }

func (coverageCheck) Name() string { return CoverageCheckName }

func (coverageCheck) Run(_ context.Context, _ *model.Skill, files []FileEntry) []Finding {
	var findings []Finding
	for _, f := range files {
		switch {
		case f.Truncated:
			// Issue #44: previously SeverityInfo, which left the
			// default `--fail-on=error` blind to oversize gaps. Now
			// Warning so `--fail-on warning` gates on it for CI users
			// who want strict coverage. Default fail-on=error still
			// passes — the cap is configurable.
			findings = append(findings, Finding{
				Check:       CoverageCheckName,
				RuleID:      "COV_OVERSIZE",
				Severity:    SeverityWarning,
				File:        f.Path,
				Message:     fmt.Sprintf("file %s skipped: size %d B exceeds the %d B per-file scan cap; patterns/unicode/permissions did not read it (a streaming credential scan still ran)", f.Path, f.Size, CurrentMaxScanBytes()),
				Remediation: "raise the cap with --max-file-bytes <N> (0 to disable), set QVR_MAX_FILE_BYTES, split the file, or audit it independently",
			})
		case f.IsBinary:
			findings = append(findings, Finding{
				Check:       CoverageCheckName,
				RuleID:      "COV_BINARY",
				Severity:    SeverityInfo,
				File:        f.Path,
				Message:     fmt.Sprintf("file %s skipped: binary content (size %d B); text-pattern detectors did not run", f.Path, f.Size),
				Remediation: "if the file is intentional, document the reason; otherwise remove or replace with a text artifact",
			})
		}
	}
	return findings
}
