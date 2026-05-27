package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretsCheckFiresOnHighPrecisionPatterns(t *testing.T) {
	files := []FileEntry{
		{Path: "SKILL.md", Content: "instructions: use the AKIAIOSFODNN7EXAMPLE key when authenticating\n"},
		{Path: "scripts/leak.sh", Content: "GH_TOKEN=ghp_abcdef1234567890ABCDEFGHIJKLMNOPqrst\n"},
		{Path: "scripts/jwt.txt", Content: "tok=eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ4In0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c\n"},
		{Path: "private.pem", Content: "-----BEGIN RSA PRIVATE KEY-----\nMIIE...\n-----END RSA PRIVATE KEY-----\n"},
	}
	check := NewSecretsCheck()
	findings := check.Run(context.Background(), nil, files)
	require.NotEmpty(t, findings)

	byFile := map[string]int{}
	for _, f := range findings {
		assert.Equal(t, SeverityCritical, f.Severity)
		assert.Equal(t, SecretsCheckName, f.Check)
		assert.Greater(t, f.Line, 0, "secrets findings must carry a line number")
		assert.NotEmpty(t, f.Remediation)
		byFile[f.File]++
	}
	assert.Equal(t, 1, byFile["SKILL.md"])
	assert.Equal(t, 1, byFile["scripts/leak.sh"])
	assert.Equal(t, 1, byFile["scripts/jwt.txt"])
	assert.GreaterOrEqual(t, byFile["private.pem"], 1)
}

func TestSecretsCheckSkipsLooseAssignmentShapes(t *testing.T) {
	// `password = X` is intentionally NOT a secrets-check finding —
	// too many false positives (e.g. Go expressions). Privacy still
	// uses it for redaction.
	files := []FileEntry{
		{Path: "SKILL.md", Content: "password = loadPassword()\napi_key=abc123xyz\n"},
	}
	findings := NewSecretsCheck().Run(context.Background(), nil, files)
	assert.Empty(t, findings, "loose assignment patterns must not produce findings: got %v", findings)
}

func TestSecretsCheckIgnoresBinaryAndEmpty(t *testing.T) {
	files := []FileEntry{
		{Path: "blob.bin", IsBinary: true, Content: ""},
		{Path: "huge.txt", Truncated: true, Content: ""},
		{Path: "SKILL.md", Content: "clean content\n"},
	}
	findings := NewSecretsCheck().Run(context.Background(), nil, files)
	assert.Empty(t, findings)
}

func TestSecretsCheckClean(t *testing.T) {
	files := []FileEntry{
		{Path: "SKILL.md", Content: "# clean skill\n\nDo a thing. No credentials here.\n"},
		{Path: "scripts/safe.sh", Content: "#!/bin/bash\necho hello\n"},
	}
	findings := NewSecretsCheck().Run(context.Background(), nil, files)
	assert.Empty(t, findings, "clean content must produce zero findings")
}

func TestSecretsCheckLineNumbersAccurate(t *testing.T) {
	content := "line 1\n" +
		"line 2\n" +
		"AKIAIOSFODNN7EXAMPLE on line 3\n" +
		"line 4\n"
	findings := NewSecretsCheck().Run(context.Background(), nil, []FileEntry{
		{Path: "x.md", Content: content},
	})
	require.Len(t, findings, 1)
	assert.Equal(t, 3, findings[0].Line)
}
