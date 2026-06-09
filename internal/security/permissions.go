package security

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/astra-sh/qvr/internal/model"
)

// PermissionsCheckName is the [Check.Name] of the permissions check.
const PermissionsCheckName = "permissions"

// dangerousShellPatterns target shell snippets that are unambiguously
// destructive or evasive in any context. The list is conservative — we
// only flag commands that have no plausible benign use inside a skill.
// Each one produces an error-severity finding.
var dangerousShellPatterns = []struct {
	name string
	re   *regexp.Regexp
	hint string
}{
	{"rm_recursive_root", regexp.MustCompile(`\brm\s+-[a-zA-Z]*r[a-zA-Z]*f?[a-zA-Z]*\s+(?:/|~|\$HOME|\.\.\s|\*\s|\*$)`), "recursive deletion of root, home, parent, or wildcard paths"},
	{"curl_pipe_shell", regexp.MustCompile(`(?i)(?:curl|wget)\s[^|]*\|\s*(?:sudo\s+)?(?:bash|sh|zsh|fish)\b`), "piping fetched content directly into a shell"},
	{"chmod_world_writable", regexp.MustCompile(`\bchmod\s+(?:-R\s+)?(?:0?777|a\+rwx)\b`), "world-writable permissions"},
	{"eval_substitution", regexp.MustCompile(`\beval\s*[\$\x60(]`), "evaluating dynamically-constructed shell commands"},
	{"fork_bomb", regexp.MustCompile(`:\(\)\s*\{\s*:\s*\|\s*:\s*&\s*\}\s*;\s*:`), "fork bomb"},
}

type permissionsCheck struct{}

// NewPermissionsCheck returns a check that flags three categories of
// permission risk:
//
//   - files inside the skill with the executable bit set. Quiver
//     never runs them, but the host agent (Claude Code, etc.) might;
//     a reviewer should confirm the script is intended.
//   - SKILL.md frontmatter declaring unrestricted `allowed-tools: Bash`
//     (no parenthesised pattern restriction).
//   - dangerous shell snippets embedded in any text file (recursive
//     deletes, curl-pipe-bash, world-writable chmod, eval of dynamic
//     shell, fork bomb).
func NewPermissionsCheck() Check { return permissionsCheck{} }

func (permissionsCheck) Name() string { return PermissionsCheckName }

func (permissionsCheck) Run(_ context.Context, skill *model.Skill, files []FileEntry) []Finding {
	var findings []Finding

	for _, f := range files {
		if f.IsSymlink {
			// Don't report symlink mode bits as "executable" — lstat
			// returns 0o777 for every symlink regardless of target
			// (issue #40). Surface dangling/cycle symlinks as their
			// own info finding so reviewers still see the anomaly.
			if f.SymlinkBroken {
				findings = append(findings, Finding{
					Check:       PermissionsCheckName,
					RuleID:      "PERM_SYMLINK_BROKEN",
					Severity:    SeverityInfo,
					File:        f.Path,
					Message:     fmt.Sprintf("symlink %s -> %s is dangling or cyclic", f.Path, f.SymlinkTarget),
					Remediation: "remove the broken symlink or fix its target",
				})
			}
			continue
		}
		if f.Executable() {
			findings = append(findings, Finding{
				Check:       PermissionsCheckName,
				RuleID:      "PERM_EXEC_BIT",
				Severity:    SeverityWarning,
				File:        f.Path,
				Message:     fmt.Sprintf("%s is marked executable (mode %s)", f.Path, f.Mode.Perm()),
				Remediation: "qvr never runs files inside a skill, but the host agent might; confirm the script is intended",
			})
		}
		if f.Content == "" {
			continue
		}
		lines := strings.Split(f.Content, "\n")
		for lineIdx, line := range lines {
			for _, p := range dangerousShellPatterns {
				if !p.re.MatchString(line) {
					continue
				}
				findings = append(findings, Finding{
					Check:       PermissionsCheckName,
					RuleID:      "PERM_" + strings.ToUpper(p.name),
					Severity:    SeverityError,
					File:        f.Path,
					Line:        lineIdx + 1,
					Message:     "dangerous shell pattern: " + p.hint,
					Remediation: "remove or constrain the command; it has no legitimate use inside a skill",
				})
			}
		}
	}

	if skill != nil {
		if msg, hit := unrestrictedBash(skill.Frontmatter.AllowedTools); hit {
			findings = append(findings, Finding{
				Check:       PermissionsCheckName,
				RuleID:      "PERM_BASH_UNRESTRICTED",
				Severity:    SeverityWarning,
				File:        "SKILL.md",
				Message:     msg,
				Remediation: "scope Bash with a pattern, e.g. `Bash(go test*)`, or drop it entirely",
			})
		}
	}

	return findings
}

// unrestrictedBash inspects the space-delimited `allowed-tools` string
// for a `Bash` entry that lacks a parenthesised pattern. agentskills.io
// allows `Bash(go test:*)` for scoped permission; the bare `Bash` form
// grants arbitrary shell.
func unrestrictedBash(allowed string) (string, bool) {
	for tool := range strings.FieldsSeq(allowed) {
		if strings.EqualFold(tool, "Bash") {
			return "frontmatter declares unrestricted `Bash` in allowed-tools", true
		}
	}
	return "", false
}
