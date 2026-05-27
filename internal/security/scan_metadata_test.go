package security

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raks097/quiver/internal/skill"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScanResultStampsScannedAt asserts the new ScannedAt field is
// present and shaped per the public contract: ISO 8601 with a numeric
// timezone offset (no `Z` shortcut).
func TestScanResultStampsScannedAt(t *testing.T) {
	fixed := time.Date(2026, 5, 27, 23, 18, 31, 944085000, time.UTC)
	prev := now
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = prev })

	dir := filepath.Join("..", "..", "testdata", "clean-skill")
	sk, err := skill.LoadFromPath(dir)
	require.NoError(t, err)
	res, err := New().Scan(context.Background(), sk, dir)
	require.NoError(t, err)

	assert.Equal(t, "2026-05-27T23:18:31.944085+00:00", res.ScannedAt,
		"ScannedAt must be RFC3339-with-numeric-offset (no Z) per the JSON contract")
}

// TestScanResultEmitsComponents asserts the typed file inventory is
// populated and at least classifies SKILL.md correctly.
func TestScanResultEmitsComponents(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "valid-skill")
	sk, err := skill.LoadFromPath(dir)
	require.NoError(t, err)
	res, err := New().Scan(context.Background(), sk, dir)
	require.NoError(t, err)

	require.NotEmpty(t, res.Components, "Components must be populated")
	var skillMD *Component
	var shellScript *Component
	for i := range res.Components {
		c := &res.Components[i]
		if c.Path == "SKILL.md" {
			skillMD = c
		}
		if strings.HasSuffix(c.Path, ".sh") {
			shellScript = c
		}
	}
	require.NotNil(t, skillMD, "expected SKILL.md in components, got %v", res.Components)
	assert.Equal(t, "markdown", skillMD.Type)
	assert.GreaterOrEqual(t, skillMD.Lines, 1)
	assert.False(t, skillMD.Executable)
	assert.Greater(t, skillMD.SizeBytes, int64(0))

	if shellScript != nil {
		assert.Equal(t, "shell", shellScript.Type)
	}
}

// TestFindingSeverityIsOneOfFourBuckets asserts every finding's
// severity is one of the four summary bucket names. Severity is the
// canonical bucket label on the wire — no separate `tag` field is
// emitted (it would be a redundant projection of the same value).
func TestFindingSeverityIsOneOfFourBuckets(t *testing.T) {
	dir := filepath.Join("..", "..", "testdata", "malicious-skill-secrets")
	sk, err := skill.LoadFromPath(dir)
	require.NoError(t, err)
	res, err := New().Scan(context.Background(), sk, dir)
	require.NoError(t, err)
	require.NotEmpty(t, res.Findings)
	for _, f := range res.Findings {
		assert.Contains(t,
			[]Severity{SeverityCritical, SeverityError, SeverityWarning, SeverityInfo},
			f.Severity,
			"finding %s: severity must be one of the four summary buckets", f.RuleID)
	}
}

// TestScanResultJSONContract is the explicit wire-shape regression
// guard for the public CLI contract. If any of the named fields
// disappears, this test fails so the contract change is intentional.
func TestScanResultJSONContract(t *testing.T) {
	fixed := time.Date(2026, 5, 27, 23, 18, 31, 944085000, time.UTC)
	prev := now
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = prev })

	dir := filepath.Join("..", "..", "testdata", "malicious-skill-injection")
	sk, err := skill.LoadFromPath(dir)
	require.NoError(t, err)
	res, err := New().Scan(context.Background(), sk, dir)
	require.NoError(t, err)

	raw, err := json.Marshal(res)
	require.NoError(t, err)
	body := string(raw)
	for _, key := range []string{`"path"`, `"skill"`, `"scanned_at"`, `"checks"`, `"components"`, `"findings"`, `"summary"`, `"severity"`} {
		assert.Contains(t, body, key, "JSON contract: missing %s", key)
	}
	assert.NotContains(t, body, `"tag"`, "tag is redundant with severity and must not appear on the wire")
}
