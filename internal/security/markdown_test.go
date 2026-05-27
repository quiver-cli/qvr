package security

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToMarkdown_GroupsByCategoryAndIncludesSummary(t *testing.T) {
	res := &ScanResult{
		Path:    "/p",
		Skill:   "demo",
		Checks:  []string{"patterns", "secrets"},
		Summary: Summary{Critical: 1, Warning: 1},
		Findings: []Finding{
			{Check: "patterns", RuleID: "P1", Category: CategoryPromptInjection, Severity: SeverityWarning, File: "a.md", Line: 1, Message: "P1: hit", Remediation: "fix"},
			{Check: "signatures", RuleID: "YR1_bash_reverse_shell", Category: CategoryYARAMatch, Severity: SeverityCritical, File: "b.sh", Line: 3, Message: "YR1: reverse shell"},
		},
	}
	out := ToMarkdown(res)
	assert.Contains(t, out, "# Quiver scan: demo")
	assert.Contains(t, out, "Summary: 1 critical")
	assert.Contains(t, out, "## Signature match (YARA-lite)")
	assert.Contains(t, out, "## Prompt injection")
	// YARA category is rendered first per categoryDisplayOrder.
	yaraIdx := strings.Index(out, "## Signature match (YARA-lite)")
	promptIdx := strings.Index(out, "## Prompt injection")
	assert.Less(t, yaraIdx, promptIdx, "high-risk categories should render first")
}

func TestToMarkdown_NilSafe(t *testing.T) {
	out := ToMarkdown(nil)
	assert.Contains(t, out, "Quiver scan")
}

func TestToMarkdown_NoFindingsCleanReport(t *testing.T) {
	out := ToMarkdown(&ScanResult{Path: "/p", Skill: "demo", Checks: []string{"patterns"}})
	assert.Contains(t, out, "_No findings._")
}

func TestEscapeCell_PipeAndNewline(t *testing.T) {
	got := escapeCell("a | b\nc")
	assert.Equal(t, `a \| b c`, got)
}
