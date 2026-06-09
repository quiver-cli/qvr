package security

import (
	"context"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/pkg/skillspec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSeverityRank(t *testing.T) {
	cases := []struct {
		s    Severity
		want int
	}{
		{SeverityInfo, 0},
		{SeverityWarning, 1},
		{SeverityError, 2},
		{SeverityCritical, 3},
		{Severity("nonsense"), -1},
		{Severity(""), -1},
	}
	for _, c := range cases {
		t.Run(string(c.s), func(t *testing.T) {
			assert.Equal(t, c.want, c.s.Rank())
		})
	}
}

func TestParseSeverity(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		for _, name := range []string{"info", "warning", "error", "critical"} {
			s, err := ParseSeverity(name)
			require.NoError(t, err)
			assert.Equal(t, Severity(name), s)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		_, err := ParseSeverity("nope")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid severity")
	})
}

func TestSummary(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityCritical}, {Severity: SeverityCritical},
		{Severity: SeverityError},
		{Severity: SeverityWarning}, {Severity: SeverityWarning}, {Severity: SeverityWarning},
		{Severity: SeverityInfo},
	}
	s := summarise(findings)
	assert.Equal(t, 2, s.Critical)
	assert.Equal(t, 1, s.Error)
	assert.Equal(t, 3, s.Warning)
	assert.Equal(t, 1, s.Info)
	assert.Equal(t, 7, s.Total())
	assert.Equal(t, SeverityCritical, s.MaxSeverity())

	empty := summarise(nil)
	assert.Equal(t, 0, empty.Total())
	assert.Equal(t, Severity(""), empty.MaxSeverity())
}

func TestSortFindingsBySeverityThenFileLine(t *testing.T) {
	findings := []Finding{
		{Check: "z", Severity: SeverityInfo, File: "a.md", Line: 1},
		{Check: "secrets", Severity: SeverityCritical, File: "b.md", Line: 5},
		{Check: "secrets", Severity: SeverityCritical, File: "b.md", Line: 1},
		{Check: "permissions", Severity: SeverityError, File: "a.md"},
		{Check: "unicode", Severity: SeverityCritical, File: "a.md", Line: 2},
	}
	sortFindings(findings)
	// Highest severity first; among same severity, by check then file then line.
	require.Len(t, findings, 5)
	assert.Equal(t, SeverityCritical, findings[0].Severity)
	assert.Equal(t, SeverityCritical, findings[1].Severity)
	assert.Equal(t, SeverityCritical, findings[2].Severity)
	assert.Equal(t, SeverityError, findings[3].Severity)
	assert.Equal(t, SeverityInfo, findings[4].Severity)
	// Within critical, "secrets" sorts before "unicode" alphabetically.
	assert.Equal(t, "secrets", findings[0].Check)
	assert.Equal(t, "secrets", findings[1].Check)
	assert.Equal(t, 1, findings[0].Line)
	assert.Equal(t, 5, findings[1].Line)
	assert.Equal(t, "unicode", findings[2].Check)
}

// TestSortFindingsIsTotalOrder pins the issue #234 fix: findings that tie on
// (severity, check, file, line) must still sort deterministically rather than
// keeping their input (Go map-iteration) order. Two LP4-style findings differ
// only by RuleID/Message; feeding the same set in two different input orders
// must produce the same output order, so the downstream reportSHA is stable.
func TestSortFindingsIsTotalOrder(t *testing.T) {
	// Same severity/check/file/line — only Message (and RuleID) differ, mirroring
	// the mcp_least_privilege LP4 findings that churned reportSHA.
	mk := func() []Finding {
		return []Finding{
			{Check: "mcp_least_privilege", RuleID: "LP4", Severity: SeverityInfo, File: "SKILL.md", Message: "LP4: shell declared but never exercised"},
			{Check: "mcp_least_privilege", RuleID: "LP4", Severity: SeverityInfo, File: "SKILL.md", Message: "LP4: file_read declared but never exercised"},
			{Check: "mcp_least_privilege", RuleID: "LP4", Severity: SeverityInfo, File: "SKILL.md", Message: "LP4: file_write declared but never exercised"},
		}
	}
	a := mk()
	sortFindings(a)
	// Reverse the input and sort again — a total order yields the identical slice.
	b := mk()
	b[0], b[2] = b[2], b[0]
	sortFindings(b)

	require.Len(t, a, 3)
	assert.Equal(t, a, b, "sortFindings must be a total order independent of input order")
	// Messages must be in ascending order (the canonical tie-break).
	assert.Equal(t, "LP4: file_read declared but never exercised", a[0].Message)
	assert.Equal(t, "LP4: file_write declared but never exercised", a[1].Message)
	assert.Equal(t, "LP4: shell declared but never exercised", a[2].Message)
}

func TestFilter(t *testing.T) {
	findings := []Finding{
		{Severity: SeverityInfo},
		{Severity: SeverityWarning},
		{Severity: SeverityError},
		{Severity: SeverityCritical},
	}
	assert.Len(t, Filter(findings, SeverityInfo), 4)
	assert.Len(t, Filter(findings, SeverityWarning), 3)
	assert.Len(t, Filter(findings, SeverityError), 2)
	assert.Len(t, Filter(findings, SeverityCritical), 1)
}

// stubCheck lets us exercise the pipeline without depending on the
// production checks (which have their own tests).
type stubCheck struct {
	name string
	out  []Finding
}

func (s stubCheck) Name() string { return s.name }
func (s stubCheck) Run(_ context.Context, _ *model.Skill, _ []FileEntry) []Finding {
	return s.out
}

func TestScannerRunsAllChecksAndAggregates(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "# clean\n")

	skill := &model.Skill{
		Skill: skillspec.Skill{Frontmatter: skillspec.Frontmatter{Name: "demo", Description: "demo skill"}},
		Dir:   dir,
		Name:  "demo",
	}

	s := NewWithChecks(
		stubCheck{name: "a", out: []Finding{{Check: "a", Severity: SeverityWarning, Message: "hello"}}},
		stubCheck{name: "b", out: []Finding{{Check: "b", Severity: SeverityCritical, Message: "boom"}}},
		stubCheck{name: "c", out: nil},
	)
	res, err := s.Scan(context.Background(), skill, dir)
	require.NoError(t, err)
	assert.Equal(t, []string{"a", "b", "c"}, res.Checks)
	require.Len(t, res.Findings, 2)
	assert.Equal(t, SeverityCritical, res.Findings[0].Severity)
	assert.Equal(t, "demo", res.Skill)
	assert.Equal(t, 1, res.Summary.Critical)
	assert.Equal(t, 1, res.Summary.Warning)
}

func TestScannerRespectsContextCancellation(t *testing.T) {
	dir := t.TempDir()
	mustWriteFile(t, dir, "SKILL.md", "# x\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	s := NewWithChecks(stubCheck{name: "a"})
	_, err := s.Scan(ctx, &model.Skill{Dir: dir, Name: "x"}, dir)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestNewRegistersBuiltinChecks(t *testing.T) {
	s := New()
	checks := s.Checks()
	// Order isn't load-bearing for callers — they filter by name —
	// but we assert membership to keep the public surface stable.
	assert.ElementsMatch(t,
		[]string{
			"prompt_injection", "secrets", "unicode", "permissions",
			"patterns", "mcp_tool_poisoning", "mcp_least_privilege",
			"signatures", "dependencies", "coverage",
		},
		checks)
}
