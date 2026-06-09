package security

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/astra-sh/qvr/internal/model"
)

// MCPToolPoisoningCheckName is the [Check.Name] of the MCP
// tool-poisoning check.
const MCPToolPoisoningCheckName = "mcp_tool_poisoning"

// Frontmatter fields are the public surface of the skill that an LLM
// caller may consume to decide whether to invoke it. Four known
// poisoning techniques (TP1-TP4) hide an instruction or confuse the LLM
// by smuggling content into these fields.
//
// Issue #38 extends the check to also scan SKILL.md / references body
// text for imperative tool-call hijacking instructions (TP5) and the
// adjacent covert-behavior hedges (TP6). These attacks are the
// textbook MCP threat model — a SKILL.md telling the agent to
// silently invoke a side-effect tool with attacker-controlled args —
// and the original implementation only scanned frontmatter, so the
// canonical attack returned zero findings.
type mcpToolPoisoningCheck struct {
	htmlComment   *regexp.Regexp
	mdComment     *regexp.Regexp
	zeroWidth     *regexp.Regexp
	dataURI       *regexp.Regexp
	base64Blob    *regexp.Regexp
	injectionVerb *regexp.Regexp
	toolHijack    *regexp.Regexp
	covertHedge   *regexp.Regexp
}

// NewMCPToolPoisoningCheck returns the MCP tool-poisoning check. It
// inspects SKILL.md frontmatter fields (description, license,
// compatibility, allowed-tools, metadata) for hidden instructions
// (TP1), unicode deception (TP2), and prompt-injection verbs (TP3).
//
// TP4 (description / behavior mismatch) requires an LLM and is left to
// the semantic check layer; this check stays purely structural.
func NewMCPToolPoisoningCheck() Check {
	return &mcpToolPoisoningCheck{
		htmlComment:   regexp.MustCompile(`<!--[\s\S]*?-->`),
		mdComment:     regexp.MustCompile(`\[//\]:\s*#\s*\([^)]*\)`),
		zeroWidth:     regexp.MustCompile(`[\x{200B}\x{200C}\x{200D}\x{2060}\x{FEFF}]`),
		dataURI:       regexp.MustCompile(`(?i)data:[\w/+\-]+;base64,[A-Za-z0-9+/=]{20,}`),
		base64Blob:    regexp.MustCompile(`[A-Za-z0-9+/]{60,}={0,2}`),
		injectionVerb: regexp.MustCompile(`(?i)(?:ignore\s+(?:all|previous)\s+instructions|system\s+prompt\s*:|you\s+must|override\s+(?:safety|rules)|<\|im_start\|>|<\|system\|>)`),

		// TP5 — imperative tool-call hijacking. Matches "call/invoke/use
		// (the) X tool" or a direct function-call shape where the name
		// looks like a side-effect tool (send_*, http_*, read_*,
		// exec_*, write_*, email_*, web_*, fetch_*, delete_*). The
		// regex is deliberately specific to side-effect verbs — bare
		// "call helper()" in prose should not fire.
		toolHijack: regexp.MustCompile(`(?i)(?:\b(?:call|invoke|use|run|execute|trigger)\s+(?:the\s+)?` +
			"[`'\"]?" +
			`(?:send_\w+|http_\w+|read_\w+|exec_\w+|write_\w+|email_\w+|web_\w+|fetch_\w+|delete_\w+|file_\w+|shell_\w+)\b` +
			`|\b(?:send_\w+|http_\w+|read_\w+|exec_\w+|write_\w+|email_\w+|web_\w+|fetch_\w+|delete_\w+|file_\w+|shell_\w+)\s*\([^)]*\))`),

		// TP6 — covert-behavior hedge adjacent to action language. The
		// hedge has to be near (within ~80 chars) a tool/action verb
		// for the rule to fire, so "secretly designed for X" prose
		// stays quiet.
		covertHedge: regexp.MustCompile(`(?i)(?:secretly|silently|covertly|without\s+(?:informing|telling|notifying)\s+(?:the\s+)?user|do\s+not\s+(?:mention|tell)\s+(?:the\s+)?user|hide\s+this\s+from\s+the\s+user)[^.\n]{0,80}\b(?:call|invoke|tool|run|execute|send|transmit|forward|email|post|upload|fetch|read|write|exfiltrate)\b`),
	}
}

func (*mcpToolPoisoningCheck) Name() string { return MCPToolPoisoningCheckName }

func (c *mcpToolPoisoningCheck) Run(_ context.Context, skill *model.Skill, files []FileEntry) []Finding {
	var findings []Finding

	if skill != nil {
		for _, field := range metadataFields(skill) {
			findings = append(findings, c.scanField(field)...)
		}
	}
	// Scan markdown / rst / txt bodies for imperative tool-call
	// hijacking (issue #38). Code files are out of scope here — the
	// patterns check + signatures handle code-shape exfil.
	for _, f := range files {
		if !isProseFile(f.Path) || f.Content == "" {
			continue
		}
		findings = append(findings, c.scanBody(f)...)
	}
	return findings
}

func isProseFile(p string) bool {
	switch {
	case strings.HasSuffix(p, ".md"), strings.HasSuffix(p, ".markdown"),
		strings.HasSuffix(p, ".mdx"), strings.HasSuffix(p, ".rst"),
		strings.HasSuffix(p, ".txt"):
		return true
	}
	return false
}

func (c *mcpToolPoisoningCheck) scanBody(f FileEntry) []Finding {
	var findings []Finding
	for lineIdx, line := range strings.Split(f.Content, "\n") {
		if loc := c.toolHijack.FindStringIndex(line); loc != nil {
			findings = append(findings, Finding{
				Check:       MCPToolPoisoningCheckName,
				RuleID:      "TP5",
				Category:    CategoryMCPToolPoisoning,
				Severity:    SeverityError,
				Confidence:  0.85,
				File:        f.Path,
				Line:        lineIdx + 1,
				Message:     fmt.Sprintf("TP5: imperative tool-call hijacking in %s — instructs the agent to invoke a side-effect tool", f.Path),
				Remediation: "remove imperative \"call/invoke/use the X tool\" instructions for side-effect tools (send_*, http_*, read_*, exec_*, write_*); document tool use as guidance, not orders",
			})
		}
		if c.covertHedge.MatchString(line) {
			findings = append(findings, Finding{
				Check:       MCPToolPoisoningCheckName,
				RuleID:      "TP6",
				Category:    CategoryMCPToolPoisoning,
				Severity:    SeverityError,
				Confidence:  0.9,
				File:        f.Path,
				Line:        lineIdx + 1,
				Message:     fmt.Sprintf("TP6: covert-behavior hedge adjacent to action verb in %s (\"secretly\", \"without informing the user\", \"do not mention\")", f.Path),
				Remediation: "any user-affecting tool use must be transparent; remove instructions that hide actions from the user",
			})
		}
	}
	return findings
}

// metadataField is one (name, value) pair from SKILL.md frontmatter.
type metadataField struct {
	name  string
	value string
}

// metadataFields enumerates the frontmatter fields that an LLM caller
// may consume. The list intentionally includes the parameterised
// metadata map so authors can't smuggle instructions through a custom
// key like `metadata.notes`.
func metadataFields(skill *model.Skill) []metadataField {
	if skill == nil {
		return nil
	}
	fm := skill.Frontmatter
	out := []metadataField{
		{"name", fm.Name},
		{"description", fm.Description},
		{"license", fm.License},
		{"compatibility", fm.Compatibility},
		{"allowed-tools", fm.AllowedTools},
	}
	for k, v := range fm.Metadata {
		out = append(out, metadataField{"metadata." + k, v})
	}
	return out
}

func (c *mcpToolPoisoningCheck) scanField(f metadataField) []Finding {
	if f.value == "" {
		return nil
	}
	var findings []Finding

	if loc := c.htmlComment.FindStringIndex(f.value); loc != nil {
		match := f.value[loc[0]:loc[1]]
		conf := 0.85
		if c.injectionVerb.MatchString(match) {
			conf = 0.95
		}
		findings = append(findings, Finding{
			Check:       MCPToolPoisoningCheckName,
			RuleID:      "TP1",
			Category:    CategoryMCPToolPoisoning,
			Severity:    SeverityError,
			Confidence:  conf,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("TP1: HTML comment in frontmatter field %q hides content from human review", f.name),
			Remediation: "remove HTML comments from frontmatter; metadata must contain plain visible text",
		})
	}

	if c.mdComment.MatchString(f.value) {
		findings = append(findings, Finding{
			Check:       MCPToolPoisoningCheckName,
			RuleID:      "TP1",
			Category:    CategoryMCPToolPoisoning,
			Severity:    SeverityError,
			Confidence:  0.9,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("TP1: markdown comment in frontmatter field %q hides content from human review", f.name),
			Remediation: "remove markdown comments from frontmatter",
		})
	}

	if c.zeroWidth.MatchString(f.value) {
		findings = append(findings, Finding{
			Check:       MCPToolPoisoningCheckName,
			RuleID:      "TP2",
			Category:    CategoryMCPToolPoisoning,
			Severity:    SeverityCritical,
			Confidence:  0.95,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("TP2: zero-width / invisible unicode in frontmatter field %q", f.name),
			Remediation: "normalise the field to plain ASCII (or visible characters); strip zero-width codepoints",
		})
	}

	if hasMixedScriptWord(f.value) {
		findings = append(findings, Finding{
			Check:       MCPToolPoisoningCheckName,
			RuleID:      "TP2",
			Category:    CategoryMCPToolPoisoning,
			Severity:    SeverityWarning,
			Confidence:  0.75,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("TP2: mixed-script (potential homoglyph) characters in frontmatter field %q", f.name),
			Remediation: "use a single script per word; verify identifiers are pure ASCII",
		})
	}

	if c.dataURI.MatchString(f.value) {
		findings = append(findings, Finding{
			Check:       MCPToolPoisoningCheckName,
			RuleID:      "TP1",
			Category:    CategoryMCPToolPoisoning,
			Severity:    SeverityError,
			Confidence:  0.9,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("TP1: data: URI base64 payload in frontmatter field %q", f.name),
			Remediation: "remove the data URI; metadata must not embed opaque payloads",
		})
	}

	if loc := c.base64Blob.FindStringIndex(f.value); loc != nil {
		// Only flag if the blob has at least mid-range entropy — full
		// uppercase-only or punctuation-heavy strings false-positive
		// otherwise. We approximate with a "mixed alphabet" check.
		blob := f.value[loc[0]:loc[1]]
		if hasMixedAlphabet(blob) {
			findings = append(findings, Finding{
				Check:       MCPToolPoisoningCheckName,
				RuleID:      "TP1",
				Category:    CategoryMCPToolPoisoning,
				Severity:    SeverityWarning,
				Confidence:  0.6,
				File:        "SKILL.md",
				Message:     fmt.Sprintf("TP1: opaque base64-shaped blob in frontmatter field %q", f.name),
				Remediation: "audit the encoded value; metadata should not contain encoded payloads",
			})
		}
	}

	// TP3: parameter-description-style injection verbs inside any
	// frontmatter field. Detected here because Quiver doesn't have
	// a separate "parameters" subtree — every frontmatter value is
	// effectively a parameter description from the LLM's POV.
	if c.injectionVerb.MatchString(f.value) {
		findings = append(findings, Finding{
			Check:       MCPToolPoisoningCheckName,
			RuleID:      "TP3",
			Category:    CategoryMCPToolPoisoning,
			Severity:    SeverityError,
			Confidence:  0.85,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("TP3: prompt-injection phrasing inside frontmatter field %q", f.name),
			Remediation: "rewrite the field; never include 'ignore previous instructions', forged role tokens, or override directives in metadata",
		})
	}

	return findings
}

// hasMixedScriptWord reports whether any whitespace-delimited word in s
// mixes Latin with Cyrillic or Greek — the canonical homoglyph attack.
func hasMixedScriptWord(s string) bool {
	for word := range strings.FieldsSeq(s) {
		var hasLatin, hasCyrillicOrGreek bool
		for _, r := range word {
			switch {
			case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
				hasLatin = true
			case (r >= 0x0400 && r <= 0x04FF) || (r >= 0x0370 && r <= 0x03FF):
				hasCyrillicOrGreek = true
			}
		}
		if hasLatin && hasCyrillicOrGreek {
			return true
		}
	}
	return false
}

// hasMixedAlphabet reports whether s uses both upper- and lower-case
// letters plus digits — a cheap proxy for "looks like base64 rather
// than a constant identifier".
func hasMixedAlphabet(s string) bool {
	var upper, lower, digit bool
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		}
		if upper && lower && digit {
			return true
		}
	}
	return false
}
