package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUnicodeCheckCatchesZeroWidth(t *testing.T) {
	cases := []struct {
		name string
		r    rune
	}{
		{"ZWSP", 0x200B},
		{"ZWNJ", 0x200C},
		{"ZWJ", 0x200D},
		{"WORD_JOINER", 0x2060},
		{"BOM", 0xFEFF},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			content := "instructions: do" + string(c.r) + " a thing\n"
			findings := NewUnicodeCheck().Run(context.Background(), nil, []FileEntry{
				{Path: "SKILL.md", Content: content},
			})
			require.NotEmpty(t, findings)
			assert.Equal(t, SeverityCritical, findings[0].Severity)
			assert.Equal(t, UnicodeCheckName, findings[0].Check)
			assert.Contains(t, findings[0].Message, "zero-width character")
		})
	}
}

func TestUnicodeCheckCatchesBidi(t *testing.T) {
	// U+202E is the classic Right-To-Left Override used in
	// Trojan-Source attacks.
	content := "// safe" + string(rune(0x202E)) + "code\n"
	findings := NewUnicodeCheck().Run(context.Background(), nil, []FileEntry{
		{Path: "SKILL.md", Content: content},
	})
	require.NotEmpty(t, findings)
	assert.Equal(t, SeverityCritical, findings[0].Severity)
	assert.Contains(t, findings[0].Message, "bidirectional override")
}

func TestUnicodeCheckCatchesTagChars(t *testing.T) {
	content := "instructions" + string(rune(0xE0041)) + " continue\n"
	findings := NewUnicodeCheck().Run(context.Background(), nil, []FileEntry{
		{Path: "SKILL.md", Content: content},
	})
	require.NotEmpty(t, findings)
	assert.Equal(t, SeverityCritical, findings[0].Severity)
	assert.Contains(t, findings[0].Message, "Unicode tag character")
}

func TestUnicodeCheckCatchesMixedScript(t *testing.T) {
	// "аpple" — first letter is Cyrillic 'а' (U+0430), looks like Latin 'a'.
	content := "use the аpple service\n" // contains Cyrillic 'а'
	findings := NewUnicodeCheck().Run(context.Background(), nil, []FileEntry{
		{Path: "SKILL.md", Content: content},
	})
	require.NotEmpty(t, findings)
	// Among findings, exactly one should be the homoglyph warning.
	var found bool
	for _, f := range findings {
		if f.Severity == SeverityWarning {
			found = true
			assert.Contains(t, f.Message, "mixed-script word")
		}
	}
	assert.True(t, found, "expected a warning-severity finding for mixed-script word")
}

func TestUnicodeCheckCleanContent(t *testing.T) {
	cases := []string{
		"# Clean SKILL.md\nThis skill formats dates.\n",
		"Supports English, español, français, and Deutsch.\n",
		"Code: foo := bar(baz)\n",
	}
	for _, c := range cases {
		findings := NewUnicodeCheck().Run(context.Background(), nil, []FileEntry{
			{Path: "x.md", Content: c},
		})
		assert.Empty(t, findings, "false positive on %q: %v", c, findings)
	}
}

func TestHexCodepoint(t *testing.T) {
	assert.Equal(t, "200B", hexCodepoint(0x200B))
	assert.Equal(t, "FEFF", hexCodepoint(0xFEFF))
	assert.Equal(t, "E0041", hexCodepoint(0xE0041))
	assert.Equal(t, "0000", hexCodepoint(0))
}

func TestUnicodeCheckIgnoresBinary(t *testing.T) {
	findings := NewUnicodeCheck().Run(context.Background(), nil, []FileEntry{
		{Path: "x.bin", IsBinary: true, Content: ""},
	})
	assert.Empty(t, findings)
}
