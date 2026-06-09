package security

import (
	"context"
	"strings"

	"github.com/astra-sh/qvr/internal/model"
)

// UnicodeCheckName is the [Check.Name] of the unicode check.
const UnicodeCheckName = "unicode"

type unicodeCheck struct{}

// NewUnicodeCheck returns a check that flags hidden / invisible
// unicode that is a known vector for SKILL.md tampering: zero-width
// characters that hide payload bytes, bidi overrides ("Trojan Source"
// class), and Unicode Tag characters that can smuggle ASCII through
// the supplementary plane.
//
// Mixed-script confusables (Latin + Cyrillic in the same word) are
// flagged as a warning rather than critical — those legitimately
// appear in multilingual prose.
func NewUnicodeCheck() Check { return unicodeCheck{} }

func (unicodeCheck) Name() string { return UnicodeCheckName }

func (unicodeCheck) Run(_ context.Context, _ *model.Skill, files []FileEntry) []Finding {
	var findings []Finding
	for _, f := range files {
		if f.Content == "" {
			continue
		}
		lines := strings.Split(f.Content, "\n")
		for lineIdx, line := range lines {
			lineNum := lineIdx + 1

			if cat, r, ok := firstHiddenRune(line); ok {
				findings = append(findings, Finding{
					Check:       UnicodeCheckName,
					RuleID:      ruleIDForUnicode(cat),
					Severity:    SeverityCritical,
					File:        f.Path,
					Line:        lineNum,
					Message:     formatHiddenRune(cat, r),
					Remediation: "remove the hidden character or replace it with a visible equivalent",
				})
			}

			if confusable, ok := firstMixedScriptWord(line); ok {
				findings = append(findings, Finding{
					Check:       UnicodeCheckName,
					RuleID:      "UNI_HOMOGLYPH",
					Severity:    SeverityWarning,
					File:        f.Path,
					Line:        lineNum,
					Message:     "mixed-script word may be a homoglyph attack: " + confusable,
					Remediation: "normalise the word to a single script or confirm the spelling is intended",
				})
			}
		}
	}
	return findings
}

// ruleIDForUnicode maps a hiddenCategory to its stable SARIF rule ID
// so each unicode class (zero-width, bidi-override, tag-char) gets a
// distinct entry in the SARIF rules list (issue #41). Without this,
// the SARIF viewer collapsed every unicode finding under one rule
// whose description came from whichever class hit first.
func ruleIDForUnicode(cat hiddenCategory) string {
	switch cat {
	case categoryZeroWidth:
		return "UNI_ZERO_WIDTH"
	case categoryBidiOverride:
		return "UNI_BIDI_OVERRIDE"
	case categoryTagChar:
		return "UNI_TAG_CHAR"
	}
	return "UNI_HIDDEN"
}

// hiddenCategory labels the kind of invisible/dangerous codepoint a
// finding came from. Surfaced in messages for clarity.
type hiddenCategory string

const (
	categoryZeroWidth    hiddenCategory = "zero-width character"
	categoryBidiOverride hiddenCategory = "bidirectional override"
	categoryTagChar      hiddenCategory = "Unicode tag character"
)

func firstHiddenRune(line string) (hiddenCategory, rune, bool) {
	for _, r := range line {
		switch {
		case isZeroWidth(r):
			return categoryZeroWidth, r, true
		case isBidiOverride(r):
			return categoryBidiOverride, r, true
		case isTagChar(r):
			return categoryTagChar, r, true
		}
	}
	return "", 0, false
}

func isZeroWidth(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0x2060, 0xFEFF:
		return true
	}
	return false
}

func isBidiOverride(r rune) bool {
	// LRE/RLE/PDF/LRO/RLO + isolates LRI/RLI/FSI/PDI.
	return (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069)
}

func isTagChar(r rune) bool {
	return r >= 0xE0001 && r <= 0xE007F
}

func formatHiddenRune(cat hiddenCategory, r rune) string {
	// Render the codepoint as U+XXXX hex so the message is easy to search for
	// even though the rune itself is invisible by definition.
	return "hidden " + string(cat) + " present (U+" + hexCodepoint(r) + ")"
}

func hexCodepoint(r rune) string {
	const digits = "0123456789ABCDEF"
	if r == 0 {
		return "0000"
	}
	out := make([]byte, 0, 6)
	for r > 0 {
		out = append([]byte{digits[r&0xF]}, out...)
		r >>= 4
	}
	for len(out) < 4 {
		out = append([]byte{'0'}, out...)
	}
	return string(out)
}

// firstMixedScriptWord finds the first word containing characters from
// both Latin and Cyrillic/Greek scripts. Whitespace-delimited; we don't
// split on punctuation so multi-word phrases stay coherent.
func firstMixedScriptWord(line string) (string, bool) {
	for word := range strings.FieldsSeq(line) {
		var hasLatin, hasCyrillic, hasGreek bool
		for _, r := range word {
			switch {
			case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
				hasLatin = true
			case r >= 0x0400 && r <= 0x04FF:
				hasCyrillic = true
			case (r >= 0x0370 && r <= 0x03FF) && !isCommonGreekPunct(r):
				hasGreek = true
			}
		}
		if hasLatin && (hasCyrillic || hasGreek) {
			return word, true
		}
	}
	return "", false
}

func isCommonGreekPunct(r rune) bool {
	// Greek question mark and a few punctuation codepoints — exclude so
	// quoting a Greek word in Latin prose doesn't trip the check.
	return r == 0x037E || r == 0x0387
}
