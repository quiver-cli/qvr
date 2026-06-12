package security

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuiltinRulesCompile(t *testing.T) {
	rules := BuiltinRules()
	require.NotEmpty(t, rules, "builtin rule set must be non-empty")
	_, err := rules.compile()
	require.NoError(t, err, "all built-in rules must compile cleanly")
}

func TestBuiltinRulesUniqueIDs(t *testing.T) {
	seen := make(map[string]string)
	for _, r := range BuiltinRules() {
		if prev, ok := seen[r.ID]; ok {
			t.Errorf("duplicate rule id %q (also in category %s)", r.ID, prev)
		}
		seen[r.ID] = string(r.Category)
	}
}

func TestRuleSetFilterByCategory(t *testing.T) {
	rules := BuiltinRules()
	pi := rules.FilterByCategory(CategoryPromptInjection)
	require.NotEmpty(t, pi)
	for _, r := range pi {
		assert.Equal(t, CategoryPromptInjection, r.Category)
	}
}

func TestRuleSetExcludeCategory(t *testing.T) {
	rules := BuiltinRules()
	notPI := rules.ExcludeCategory(CategoryPromptInjection)
	for _, r := range notPI {
		assert.NotEqual(t, CategoryPromptInjection, r.Category,
			"exclude must drop %s rules", CategoryPromptInjection)
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, name string
		want          bool
	}{
		{"", "", true},
		{"*.py", "foo.py", true},
		{"*.py", "nested/foo.py", true},
		{"*.py", "foo.txt", false},
		{"**/*.py", "a/b/c.py", true},
		{"**/*.py", "c.py", true},
		{"**/SKILL.md", "SKILL.md", true},
		{"**/SKILL.md", "nested/SKILL.md", true},
		{"**/SKILL.md", "skill.txt", false},
		{"requirements.txt", "requirements.txt", true},
		{"requirements.txt", "a/requirements.txt", true},
	}
	for _, c := range cases {
		t.Run(c.pattern+"_"+c.name, func(t *testing.T) {
			assert.Equal(t, c.want, matchGlob(c.pattern, c.name))
		})
	}
}

func TestRuleAppliesTo(t *testing.T) {
	r := Rule{Globs: []string{"**/*.py"}}
	assert.True(t, r.AppliesTo("a/b/c.py"))
	assert.False(t, r.AppliesTo("foo.md"))

	r2 := Rule{} // no globs → applies everywhere
	assert.True(t, r2.AppliesTo("anything"))
}
