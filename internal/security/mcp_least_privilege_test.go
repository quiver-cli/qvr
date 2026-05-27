package security

import (
	"context"
	"testing"

	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/pkg/skillspec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func skillWithAllowedTools(allowed string, meta map[string]string) *model.Skill {
	return &model.Skill{
		Skill: skillspec.Skill{
			Frontmatter: skillspec.Frontmatter{
				Name:         "demo",
				Description:  "demo",
				AllowedTools: allowed,
				Metadata:     meta,
			},
		},
		Name: "demo",
		Dir:  ".",
	}
}

func TestMCPLeastPrivilege_LP2_Wildcard(t *testing.T) {
	skill := skillWithAllowedTools("*", nil)
	files := []FileEntry{
		{Path: "main.py", Content: `import subprocess; subprocess.run(["ls"])`},
	}
	findings := NewMCPLeastPrivilegeCheck().Run(context.Background(), skill, files)
	require.NotEmpty(t, findings)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "LP2" {
			hit = true
			assert.Equal(t, SeverityError, f.Severity)
		}
	}
	assert.True(t, hit, "expected LP2 wildcard finding")
}

func TestMCPLeastPrivilege_LP3_NoDeclaredButCapabilitiesPresent(t *testing.T) {
	skill := skillWithAllowedTools("", nil)
	files := []FileEntry{
		{Path: "main.py", Content: `import requests; requests.get("https://x")`},
	}
	findings := NewMCPLeastPrivilegeCheck().Run(context.Background(), skill, files)
	require.NotEmpty(t, findings)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "LP3" {
			hit = true
		}
	}
	assert.True(t, hit, "expected LP3 when capabilities exist but no permissions declared")
}

func TestMCPLeastPrivilege_LP1_UndeclaredCapability(t *testing.T) {
	skill := skillWithAllowedTools("Read", nil) // declares file_read only
	files := []FileEntry{
		{Path: "main.py", Content: `import subprocess; subprocess.run(["ls"])`}, // exercises shell
	}
	findings := NewMCPLeastPrivilegeCheck().Run(context.Background(), skill, files)
	var hit bool
	for _, f := range findings {
		if f.RuleID == "LP1" && f.Line > 0 {
			hit = true
		}
	}
	assert.True(t, hit, "expected LP1 for undeclared shell capability")
}

func TestMCPLeastPrivilege_LP4_OverDeclared(t *testing.T) {
	skill := skillWithAllowedTools("Bash Network", nil)
	files := []FileEntry{
		// No code that exercises either capability.
		{Path: "SKILL.md", Content: "# clean"},
	}
	findings := NewMCPLeastPrivilegeCheck().Run(context.Background(), skill, files)
	var hits int
	for _, f := range findings {
		if f.RuleID == "LP4" {
			hits++
		}
	}
	assert.Equal(t, 2, hits, "expected one LP4 per over-declared permission")
}

func TestMCPLeastPrivilege_ParsesParenScopedTool(t *testing.T) {
	// `Bash(go test:*)` is scoped Bash; it still declares shell.
	skill := skillWithAllowedTools("Bash(go:*)", nil)
	files := []FileEntry{
		{Path: "main.py", Content: `import subprocess; subprocess.run(["go", "test"])`},
	}
	findings := NewMCPLeastPrivilegeCheck().Run(context.Background(), skill, files)
	for _, f := range findings {
		assert.NotEqual(t, "LP1", f.RuleID, "Bash(scope) should satisfy the shell declaration: got %+v", f)
	}
}

func TestMCPLeastPrivilege_MetadataPermissions(t *testing.T) {
	skill := skillWithAllowedTools("", map[string]string{"permissions": "network, env"})
	files := []FileEntry{
		{Path: "main.py", Content: `import httpx; httpx.get("https://x"); import os; os.getenv("X")`},
	}
	findings := NewMCPLeastPrivilegeCheck().Run(context.Background(), skill, files)
	for _, f := range findings {
		assert.NotEqual(t, "LP1", f.RuleID, "metadata.permissions should satisfy network+env: got %+v", f)
		assert.NotEqual(t, "LP3", f.RuleID, "metadata.permissions counts as declared: got %+v", f)
	}
}

func TestMCPLeastPrivilege_NoFindingsOnCleanSkill(t *testing.T) {
	skill := skillWithAllowedTools("", nil)
	files := []FileEntry{{Path: "SKILL.md", Content: "# clean prose only\n"}}
	findings := NewMCPLeastPrivilegeCheck().Run(context.Background(), skill, files)
	assert.Empty(t, findings, "clean skill with no code should produce no findings")
}
