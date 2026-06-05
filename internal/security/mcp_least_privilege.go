package security

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/quiver-cli/qvr/internal/model"
)

// MCPLeastPrivilegeCheckName is the [Check.Name] of the MCP
// least-privilege check.
const MCPLeastPrivilegeCheckName = "mcp_least_privilege"

// permissionAliases maps permission tokens (whatever spelling the user
// uses in SKILL.md frontmatter) to the canonical [Capability] they
// declare. Anything that doesn't appear in the map is treated as
// undeclared.
var permissionAliases = map[string]Capability{
	"bash": CapShell, "shell": CapShell, "shell_execute": CapShell,
	"exec": CapExec, "execute": CapExec,
	"network": CapNetwork, "http": CapNetwork, "fetch": CapNetwork, "websearch": CapNetwork, "webfetch": CapNetwork,
	"read": CapFileRead, "file_read": CapFileRead, "fs_read": CapFileRead,
	"write": CapFileWrite, "file_write": CapFileWrite, "fs_write": CapFileWrite, "edit": CapFileWrite,
	"env": CapEnvAccess, "env_access": CapEnvAccess, "environment": CapEnvAccess,
}

// wildcardPermissionTokens is the set of frontmatter values that mean
// "everything" — flagged as LP2 regardless of context.
var wildcardPermissionTokens = map[string]bool{
	"*": true, "all": true, "any": true, "full": true,
}

type mcpLeastPrivilegeCheck struct{}

// NewMCPLeastPrivilegeCheck returns the MCP least-privilege check. It
// compares the capabilities the skill's code exercises with the
// permissions declared in SKILL.md frontmatter (allowed-tools +
// metadata.permissions) and emits findings when they disagree.
//
// Rule IDs:
//
//   - LP1: code exercises a capability not covered by declared
//     permissions (skill does more than it claims)
//   - LP2: declared permissions contain a wildcard
//   - LP3: no declared permissions at all, but capabilities detected
//   - LP4: permission declared, no corresponding capability detected
//     (over-claim — staged for future abuse or stale)
func NewMCPLeastPrivilegeCheck() Check { return mcpLeastPrivilegeCheck{} }

func (mcpLeastPrivilegeCheck) Name() string { return MCPLeastPrivilegeCheckName }

func (mcpLeastPrivilegeCheck) Run(_ context.Context, skill *model.Skill, files []FileEntry) []Finding {
	if skill == nil {
		return nil
	}

	declared, wildcardHit, wildcardToken := parseDeclaredPermissions(skill)
	exercised := DetectCapabilityLocations(files)

	var findings []Finding

	if wildcardHit {
		findings = append(findings, Finding{
			Check:       MCPLeastPrivilegeCheckName,
			RuleID:      "LP2",
			Category:    CategoryMCPLeastPrivilege,
			Severity:    SeverityError,
			Confidence:  0.95,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("LP2: wildcard permission %q in allowed-tools — disables least-privilege boundary", wildcardToken),
			Remediation: "replace `*`/`all`/`any` with an explicit list of required tools or permissions",
		})
	}

	if len(declared) == 0 && len(exercised) > 0 {
		caps := make([]string, 0, len(exercised))
		for c := range exercised {
			caps = append(caps, string(c))
		}
		sort.Strings(caps)
		findings = append(findings, Finding{
			Check:       MCPLeastPrivilegeCheckName,
			RuleID:      "LP3",
			Category:    CategoryMCPLeastPrivilege,
			Severity:    SeverityWarning,
			Confidence:  0.85,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("LP3: skill exercises capabilities (%s) but declares no permissions", strings.Join(caps, ", ")),
			Remediation: "add an `allowed-tools` field (and metadata.permissions if applicable) listing the capabilities this skill needs",
		})
	}

	// LP1: code uses capability X but declared set doesn't cover X.
	for cap, site := range exercised {
		if declared[cap] {
			continue
		}
		if wildcardHit {
			// Already reported LP2 — don't double-bill.
			continue
		}
		findings = append(findings, Finding{
			Check:       MCPLeastPrivilegeCheckName,
			RuleID:      "LP1",
			Category:    CategoryMCPLeastPrivilege,
			Severity:    SeverityError,
			Confidence:  0.8,
			File:        site.File,
			Line:        site.Line,
			Message:     fmt.Sprintf("LP1: code exercises %s capability, but no matching permission declared in SKILL.md", cap),
			Remediation: fmt.Sprintf("add %s (or the appropriate alias) to allowed-tools, or remove the code path that requires it", cap),
		})
	}

	// LP4: capability declared but never exercised in code.
	for cap := range declared {
		if _, used := exercised[cap]; used {
			continue
		}
		findings = append(findings, Finding{
			Check:       MCPLeastPrivilegeCheckName,
			RuleID:      "LP4",
			Category:    CategoryMCPLeastPrivilege,
			Severity:    SeverityInfo,
			Confidence:  0.6,
			File:        "SKILL.md",
			Message:     fmt.Sprintf("LP4: %s declared in allowed-tools but never exercised by skill code", cap),
			Remediation: fmt.Sprintf("remove %s from allowed-tools if the corresponding code path is no longer present", cap),
		})
	}

	return findings
}

// parseDeclaredPermissions extracts the capability set declared by the
// skill's frontmatter. It accepts the agentskills.io space-delimited
// form (`allowed-tools: Bash Read`) and the parenthesised-pattern form
// (`Bash(go:*)` — still grants shell). Wildcard tokens flip the
// wildcard flag.
func parseDeclaredPermissions(skill *model.Skill) (set map[Capability]bool, wildcard bool, wildcardToken string) {
	set = make(map[Capability]bool, 4)
	if skill == nil {
		return
	}

	for _, tok := range strings.Fields(skill.Frontmatter.AllowedTools) {
		raw := strings.ToLower(tok)
		// Strip any parenthesised scope so `Bash(go:*)` → `bash`.
		if idx := strings.Index(raw, "("); idx >= 0 {
			raw = raw[:idx]
		}
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if wildcardPermissionTokens[raw] {
			wildcard = true
			wildcardToken = raw
			continue
		}
		if c, ok := permissionAliases[raw]; ok {
			set[c] = true
		}
	}

	// metadata.permissions — accept a single string, comma-separated.
	if perm, ok := skill.Frontmatter.Metadata["permissions"]; ok {
		for _, tok := range splitPermissionList(perm) {
			raw := strings.ToLower(strings.TrimSpace(tok))
			if raw == "" {
				continue
			}
			if wildcardPermissionTokens[raw] {
				wildcard = true
				wildcardToken = raw
				continue
			}
			if c, ok := permissionAliases[raw]; ok {
				set[c] = true
			}
		}
	}
	return
}

// splitPermissionList tolerates either ` `-separated or `,`-separated
// permission lists in metadata.permissions; both occur in the wild.
func splitPermissionList(s string) []string {
	if strings.Contains(s, ",") {
		return strings.Split(s, ",")
	}
	return strings.Fields(s)
}
