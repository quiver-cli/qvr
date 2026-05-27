package security

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRequirementsTxt(t *testing.T) {
	content := `# pinned
requests==2.32.0

# unpinned
flask
django>=4.0

# wildcard
pyyaml==*
`
	deps := parseRequirementsTxt(FileEntry{Path: "requirements.txt", Content: content})
	require.Len(t, deps, 4)
	assert.Equal(t, "requests", deps[0].Name)
	assert.True(t, deps[0].Pinned)
	assert.Equal(t, "flask", deps[1].Name)
	assert.False(t, deps[1].Pinned)
	assert.Equal(t, "django", deps[2].Name)
	assert.False(t, deps[2].Pinned)
	assert.Equal(t, "pyyaml", deps[3].Name)
	assert.False(t, deps[3].Pinned)
}

func TestParsePackageJSON(t *testing.T) {
	content := `{
  "name": "x",
  "dependencies": {
    "lodash": "^4.17.20",
    "express": "4.18.1",
    "anything": "latest"
  }
}`
	deps := parsePackageJSON(FileEntry{Path: "package.json", Content: content})
	require.Len(t, deps, 3)
	assert.Equal(t, "lodash", deps[0].Name)
	assert.False(t, deps[0].Pinned, "caret-prefixed versions are not exact pins")
	assert.Equal(t, "express", deps[1].Name)
	assert.True(t, deps[1].Pinned)
	assert.Equal(t, "anything", deps[2].Name)
	assert.False(t, deps[2].Pinned, "'latest' is not a pinned version")
}

func TestParseGoMod(t *testing.T) {
	content := `module foo

go 1.22

require (
	github.com/x/y v1.2.3
	example.com/z v0.0.0-20230101000000-aaaaaaaaaaaa
)

require github.com/single v1.0.0
`
	deps := parseGoMod(FileEntry{Path: "go.mod", Content: content})
	require.Len(t, deps, 3)
	assert.Equal(t, "github.com/x/y", deps[0].Name)
	assert.True(t, deps[0].Pinned)
}

func TestDependencyChecker_SC4_KnownVulnPinned(t *testing.T) {
	files := []FileEntry{
		{Path: "requirements.txt", Content: "pyyaml==5.3.1\n"},
	}
	findings := NewDependencyCheck().Run(context.Background(), nil, files)
	require.NotEmpty(t, findings)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "SC4" {
			hit = true
			assert.Equal(t, SeverityCritical, f.Severity)
			assert.Contains(t, f.Message, "CVE-2020-14343")
		}
	}
	assert.True(t, hit)
}

func TestDependencyChecker_SC1_Unpinned(t *testing.T) {
	files := []FileEntry{{Path: "requirements.txt", Content: "flask\n"}}
	findings := NewDependencyCheck().Run(context.Background(), nil, files)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "SC1" {
			hit = true
		}
	}
	assert.True(t, hit, "expected SC1 for unpinned requirement")
}

func TestDependencyChecker_SC5_Abandoned(t *testing.T) {
	files := []FileEntry{{Path: "requirements.txt", Content: "pycrypto==2.6.1\n"}}
	findings := NewDependencyCheck().Run(context.Background(), nil, files)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "SC5" {
			hit = true
		}
	}
	assert.True(t, hit, "expected SC5 for abandoned package")
}

func TestDependencyChecker_SC6_Typosquat(t *testing.T) {
	files := []FileEntry{{Path: "requirements.txt", Content: "requets==2.0.0\n"}}
	findings := NewDependencyCheck().Run(context.Background(), nil, files)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "SC6" {
			hit = true
			assert.Contains(t, f.Message, "requests")
		}
	}
	assert.True(t, hit, "expected SC6 for typosquat")
}

func TestDependencyChecker_NoFindingsOnCleanRequirements(t *testing.T) {
	files := []FileEntry{
		{Path: "requirements.txt", Content: "requests==2.32.0\nclick==8.1.7\n"},
	}
	findings := NewDependencyCheck().Run(context.Background(), nil, files)
	for _, f := range findings {
		assert.NotEqual(t, "SC1", f.RuleID)
		assert.NotEqual(t, "SC4", f.RuleID)
		assert.NotEqual(t, "SC6", f.RuleID)
	}
}

func TestVersionLE(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.0", true},
		{"1.0.0", "1.0.1", true},
		{"1.0.1", "1.0.0", false},
		{"5.3.1", "5.3.1", true},
		{"5.3.0", "5.3.1", true},
		{"5.4.0", "5.3.1", false},
		{"1.2.3-rc1", "1.2.3", true},
	}
	for _, c := range cases {
		t.Run(c.a+"_LE_"+c.b, func(t *testing.T) {
			assert.Equal(t, c.want, versionLE(c.a, c.b))
		})
	}
}

func TestEditDistance(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"requests", "requets", 1},
		{"requests", "requests", 0},
		{"requests", "request", 1},
		{"abc", "xyz", 3},
	}
	for _, c := range cases {
		t.Run(c.a+"_"+c.b, func(t *testing.T) {
			assert.Equal(t, c.want, editDistance(c.a, c.b))
		})
	}
}
