package security

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToSARIF_StructureAndLevels(t *testing.T) {
	res := &ScanResult{
		Path:   "/some/path",
		Skill:  "demo",
		Checks: []string{"patterns"},
		Findings: []Finding{
			{Check: "patterns", RuleID: "P1", Category: CategoryPromptInjection, Severity: SeverityWarning, File: "SKILL.md", Line: 3, Message: "P1: hit"},
			{Check: "patterns", RuleID: "TM1b", Category: CategoryToolMisuse, Severity: SeverityCritical, File: "x.sh", Line: 9, Message: "TM1b: rm -rf /"},
			{Check: "patterns", RuleID: "EA3", Category: CategoryExcessiveAgency, Severity: SeverityInfo, File: "y.md", Line: 1, Message: "info note"},
		},
	}
	sarif := ToSARIF(res)
	assert.Equal(t, "2.1.0", sarif.Version)
	require.Len(t, sarif.Runs, 1)
	assert.Equal(t, "qvr", sarif.Runs[0].Tool.Driver.Name)
	require.Len(t, sarif.Runs[0].Results, 3)

	// Critical and Error both collapse to "error" in SARIF; Info → "note".
	assert.Equal(t, "warning", sarif.Runs[0].Results[0].Level)
	assert.Equal(t, "error", sarif.Runs[0].Results[1].Level)
	assert.Equal(t, "note", sarif.Runs[0].Results[2].Level)

	// JSON round-trips cleanly (sanity check).
	raw, err := json.Marshal(sarif)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"$schema"`)
	assert.Contains(t, string(raw), `"ruleId":"P1"`)
}

func TestToSARIF_RulesDedupedAcrossResults(t *testing.T) {
	res := &ScanResult{
		Findings: []Finding{
			{Check: "patterns", RuleID: "P1", Severity: SeverityWarning, File: "a.md", Line: 1},
			{Check: "patterns", RuleID: "P1", Severity: SeverityWarning, File: "b.md", Line: 1},
		},
	}
	sarif := ToSARIF(res)
	require.Len(t, sarif.Runs, 1)
	assert.Len(t, sarif.Runs[0].Tool.Driver.Rules, 1, "duplicate ruleIDs must collapse to one rule entry")
	assert.Len(t, sarif.Runs[0].Results, 2)
}

// TestSARIF_SeverityPreservedInProperties is the regression guard
// for issue #41. SARIF's `level` only has three values, so critical
// and error both flatten to "error" — properties.severity and
// properties.problem.severity carry the qvr ladder so a code-scanning
// UI can keep them visually distinct.
func TestSARIF_SeverityPreservedInProperties(t *testing.T) {
	res := &ScanResult{
		Findings: []Finding{
			{Check: "secrets", RuleID: "SEC_AWS_AKIA", Severity: SeverityCritical, File: "a", Line: 1, Message: "aws"},
			{Check: "patterns", RuleID: "TM1b", Severity: SeverityError, File: "b", Line: 2, Message: "rm"},
			{Check: "patterns", RuleID: "EA3", Severity: SeverityInfo, File: "c", Line: 3, Message: "info"},
		},
	}
	sarif := ToSARIF(res)
	require.Len(t, sarif.Runs[0].Results, 3)

	critProps := sarif.Runs[0].Results[0].Properties
	assert.Equal(t, "critical", critProps["severity"])
	critProblem, ok := critProps["problem"].(map[string]any)
	require.True(t, ok, "critical: problem must be a map")
	assert.Equal(t, "critical", critProblem["severity"])

	errProps := sarif.Runs[0].Results[1].Properties
	assert.Equal(t, "error", errProps["severity"])
	errProblem, ok := errProps["problem"].(map[string]any)
	require.True(t, ok, "error: problem must be a map")
	assert.Equal(t, "high", errProblem["severity"])

	infoProps := sarif.Runs[0].Results[2].Properties
	assert.Equal(t, "info", infoProps["severity"])
	infoProblem, ok := infoProps["problem"].(map[string]any)
	require.True(t, ok, "info: problem must be a map")
	assert.Equal(t, "low", infoProblem["severity"])
}

// TestSARIF_PerDetectorRuleIDs verifies the secrets/unicode/permissions
// checks now mint distinct ruleIDs so SARIF rules-list entries don't
// collapse multiple unrelated detections under one description
// (issue #41).
func TestSARIF_PerDetectorRuleIDs(t *testing.T) {
	res := &ScanResult{
		Findings: []Finding{
			{Check: "secrets", RuleID: "SEC_AWS_AKIA", Severity: SeverityCritical, File: "a", Line: 1, Message: "aws"},
			{Check: "secrets", RuleID: "SEC_GITHUB_PAT", Severity: SeverityCritical, File: "a", Line: 2, Message: "gh"},
			{Check: "unicode", RuleID: "UNI_ZERO_WIDTH", Severity: SeverityCritical, File: "a", Line: 3, Message: "zwsp"},
			{Check: "unicode", RuleID: "UNI_BIDI_OVERRIDE", Severity: SeverityCritical, File: "a", Line: 4, Message: "rtl"},
			{Check: "permissions", RuleID: "PERM_EXEC_BIT", Severity: SeverityWarning, File: "a", Line: 5, Message: "exec"},
			{Check: "permissions", RuleID: "PERM_CURL_PIPE_SHELL", Severity: SeverityError, File: "a", Line: 6, Message: "pipe"},
		},
	}
	sarif := ToSARIF(res)
	ids := map[string]bool{}
	for _, r := range sarif.Runs[0].Tool.Driver.Rules {
		ids[r.ID] = true
	}
	for _, want := range []string{"SEC_AWS_AKIA", "SEC_GITHUB_PAT", "UNI_ZERO_WIDTH", "UNI_BIDI_OVERRIDE", "PERM_EXEC_BIT", "PERM_CURL_PIPE_SHELL"} {
		assert.True(t, ids[want], "expected per-detector ruleId %s, got %v", want, ids)
	}
	assert.False(t, ids["secrets"], "generic 'secrets' ruleId must not leak into SARIF anymore")
	assert.False(t, ids["unicode"], "generic 'unicode' ruleId must not leak into SARIF anymore")
	assert.False(t, ids["permissions"], "generic 'permissions' ruleId must not leak into SARIF anymore")
}

func TestToSARIF_NilSafe(t *testing.T) {
	sarif := ToSARIF(nil)
	assert.Equal(t, "2.1.0", sarif.Version)
}
