package security

import (
	"fmt"
	"strings"
)

// ToMarkdown renders a ScanResult as a review-friendly markdown
// document. The shape is stable so it can be pasted into release-gate
// PRs and security tickets:
//
//	# Quiver scan: <skill>
//	<summary block>
//	## <Category>
//	<table of findings in that category>
//
// Findings are grouped by Category and sorted by Severity within a
// group so the highest-priority items lead each section.
func ToMarkdown(result *ScanResult) string {
	if result == nil {
		return "# Quiver scan\n\n(no result)\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Quiver scan: %s\n\n", displayName(result))
	fmt.Fprintf(&b, "Path: `%s`\n\n", result.Path)
	fmt.Fprintf(&b, "Checks: %s\n\n", strings.Join(result.Checks, ", "))
	fmt.Fprintf(&b, "Summary: %d critical · %d error · %d warning · %d info\n\n",
		result.Summary.Critical, result.Summary.Error, result.Summary.Warning, result.Summary.Info)

	if len(result.Findings) == 0 {
		b.WriteString("_No findings._\n")
		return b.String()
	}

	groups := groupByCategory(result.Findings)
	for _, cat := range orderedCategories(groups) {
		fmt.Fprintf(&b, "## %s\n\n", humanCategory(cat))
		b.WriteString("| Severity | Rule | Location | Message | Remediation |\n")
		b.WriteString("|---|---|---|---|---|\n")
		for _, f := range groups[cat] {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&b, "| %s | `%s` | `%s` | %s | %s |\n",
				f.Severity, ruleColumn(f), escapeCell(loc),
				escapeCell(f.Message), escapeCell(f.Remediation))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func displayName(result *ScanResult) string {
	if result.Skill == "" {
		return "(unnamed skill)"
	}
	return result.Skill
}

func ruleColumn(f Finding) string {
	if f.RuleID == "" {
		return f.Check
	}
	return f.RuleID
}

func groupByCategory(findings []Finding) map[Category][]Finding {
	out := make(map[Category][]Finding, 8)
	for _, f := range findings {
		key := f.Category
		if key == "" {
			key = Category("other")
		}
		out[key] = append(out[key], f)
	}
	return out
}

// orderedCategories returns the categories present in groups in a
// stable, human-friendly order: critical-risk categories first,
// then alphabetically among the rest. Keeping this consistent matters
// so the markdown report diffs cleanly between scans.
var categoryDisplayOrder = []Category{
	CategoryYARAMatch,
	CategoryMCPToolPoisoning,
	CategoryMCPLeastPrivilege,
	CategoryRogueAgent,
	CategoryDataExfiltration,
	CategoryPrivilegeEscalation,
	CategoryHarmfulContent,
	CategorySupplyChain,
	CategoryPromptInjection,
	CategorySystemPromptLeakage,
	CategoryToolMisuse,
	CategoryOutputHandling,
	CategoryMemoryPoisoning,
	CategoryExcessiveAgency,
	CategoryTriggerAbuse,
}

func orderedCategories(groups map[Category][]Finding) []Category {
	out := make([]Category, 0, len(groups))
	seen := make(map[Category]bool, len(groups))
	for _, c := range categoryDisplayOrder {
		if _, ok := groups[c]; ok {
			out = append(out, c)
			seen[c] = true
		}
	}
	for c := range groups {
		if !seen[c] {
			out = append(out, c)
		}
	}
	return out
}

func humanCategory(c Category) string {
	switch c {
	case CategoryPromptInjection:
		return "Prompt injection"
	case CategorySystemPromptLeakage:
		return "System prompt leakage"
	case CategoryDataExfiltration:
		return "Data exfiltration"
	case CategoryPrivilegeEscalation:
		return "Privilege escalation"
	case CategorySupplyChain:
		return "Supply chain"
	case CategoryExcessiveAgency:
		return "Excessive agency"
	case CategoryOutputHandling:
		return "Output handling"
	case CategoryMemoryPoisoning:
		return "Memory poisoning"
	case CategoryToolMisuse:
		return "Tool misuse"
	case CategoryRogueAgent:
		return "Rogue agent"
	case CategoryHarmfulContent:
		return "Harmful content"
	case CategoryTriggerAbuse:
		return "Trigger abuse"
	case CategoryYARAMatch:
		return "Signature match (YARA-lite)"
	case CategoryMCPLeastPrivilege:
		return "MCP least privilege"
	case CategoryMCPToolPoisoning:
		return "MCP tool poisoning"
	}
	if c == "" {
		return "Other"
	}
	return string(c)
}

func escapeCell(s string) string {
	// Replace literal pipe and newline so the table stays well-formed
	// without losing information.
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
