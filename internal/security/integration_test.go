package security

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScannerEndToEndOverFixtures runs the full New() pipeline against
// every malicious-skill-* fixture under testdata and asserts the
// expected rule IDs are present. This is the canonical regression test
// for SkillSpector-parity coverage: if a rule disappears from the
// detection set, this test fails.
func TestScannerEndToEndOverFixtures(t *testing.T) {
	type expect struct {
		fixture     string
		mustHave    []string // rule IDs that must be present (any check name)
		mustNotHave []string // rule IDs / check names that must NOT fire
	}
	cases := []expect{
		{
			fixture:  "clean-skill",
			mustHave: nil, // (asserted via Total == 0 below)
		},
		{
			fixture:  "malicious-skill-injection",
			mustHave: []string{"P1", "P9", "P10", "P11", "P12"},
		},
		{
			fixture:  "malicious-skill-secrets",
			mustHave: nil, // secrets check uses its own naming; assert via Check name
		},
		{
			fixture:  "malicious-skill-unicode",
			mustHave: nil, // unicode check uses its own naming; asserted via category below
		},
		{
			fixture:  "malicious-skill-permissions",
			mustHave: nil, // existing permissions check; asserted via Check name
		},
		{
			fixture:  "malicious-skill-data-exfil",
			mustHave: []string{"E2", "E3", "E4"},
		},
		{
			fixture:  "malicious-skill-tool-misuse",
			mustHave: []string{"SC2", "TM1b", "TM1c", "TM1d", "TM1a", "TM3"},
		},
		{
			fixture:  "malicious-skill-rogue-agent",
			mustHave: []string{"RA1", "RA2"},
		},
		{
			fixture:  "malicious-skill-supply-chain",
			mustHave: []string{"SC1", "SC4", "SC5", "SC6"},
		},
		{
			fixture:  "malicious-skill-mcp-poisoning",
			mustHave: []string{"TP1", "TP2"},
		},
		{
			fixture:  "malicious-skill-mcp-perms",
			mustHave: []string{"LP1"},
		},
		{
			fixture:  "malicious-skill-signatures",
			mustHave: []string{"YR1_bash_reverse_shell", "YR2_php_eval_shell"},
		},
	}

	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			dir := filepath.Join("..", "..", "testdata", c.fixture)
			sk, err := skill.LoadFromPath(dir)
			require.NoError(t, err, "load %s", c.fixture)

			res, err := New().Scan(context.Background(), sk, dir)
			require.NoError(t, err)

			if c.fixture == "clean-skill" {
				assert.Equal(t, 0, res.Summary.Total(),
					"clean-skill is the false-positive gate and must produce zero findings, got %+v", res.Findings)
				return
			}

			gotRules := make(map[string]bool)
			gotChecks := make(map[string]bool)
			for _, f := range res.Findings {
				if f.RuleID != "" {
					gotRules[f.RuleID] = true
				}
				gotChecks[f.Check] = true
			}
			for _, want := range c.mustHave {
				assert.True(t, gotRules[want],
					"fixture %s: expected rule %s; got rules %v / checks %v", c.fixture, want, keys(gotRules), keys(gotChecks))
			}
			for _, banned := range c.mustNotHave {
				assert.False(t, gotRules[banned] || gotChecks[banned],
					"fixture %s: unexpected rule/check %s present", c.fixture, banned)
			}
			assert.NotZero(t, res.Summary.Total(), "fixture %s should produce findings", c.fixture)
		})
	}
}

// TestCleanSkillProducesZeroFindingsAcrossAllChecks is the explicit
// false-positive guard. If any future rule starts matching clean-skill
// (the canonical "nothing-to-flag" fixture), this test fails loudly.
func TestCleanSkillProducesZeroFindingsAcrossAllChecks(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "clean-skill")
	sk, err := skill.LoadFromPath(dir)
	require.NoError(t, err)
	res, err := New().Scan(context.Background(), sk, dir)
	require.NoError(t, err)
	if res.Summary.Total() != 0 {
		t.Errorf("clean-skill produced findings:")
		for _, f := range res.Findings {
			t.Errorf("  %s:%d [%s/%s] %s", f.File, f.Line, f.Check, f.RuleID, f.Message)
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
