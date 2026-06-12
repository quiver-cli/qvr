package ops

import "strings"

// SkillRefFromTool extracts the name of an explicitly-invoked skill from a
// tool-call's name + arguments, or "" when the call is not a skill
// invocation. It centralises the per-agent rules so every adapter that has
// a skill-as-tool-call signal is a one-liner and the rules are tested in
// one place.
//
// Coverage (see the audit plan): Claude Code and Codex invoke skills via a
// tool literally named "Skill"; OpenCode and GitHub Copilot run a skill as a
// tool named "skill"/"skills" (or a "skills_"-prefixed variant); Hermes loads
// a skill via its "skill_view" tool with {"name":"<skill>"} (observed live,
// 2026-06-11). Cursor does not surface a discrete skill tool-call — it relies
// on the universal path-based signal in the resolver, so it never calls this.
//
// toolName is matched case-insensitively; the skill name is read from the
// first present of a small set of argument keys.
func SkillRefFromTool(toolName string, args map[string]any) string {
	switch {
	case strings.EqualFold(toolName, "Skill"),
		strings.EqualFold(toolName, "skill"),
		strings.EqualFold(toolName, "skills"),
		strings.EqualFold(toolName, "skill_view"),
		strings.HasPrefix(strings.ToLower(toolName), "skills_"):
		return firstStringArg(args, "skill", "name", "command", "id")
	default:
		return ""
	}
}

// firstStringArg returns the first present non-empty string value among keys.
func firstStringArg(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
