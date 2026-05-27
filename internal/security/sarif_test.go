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

func TestToSARIF_NilSafe(t *testing.T) {
	sarif := ToSARIF(nil)
	assert.Equal(t, "2.1.0", sarif.Version)
}
