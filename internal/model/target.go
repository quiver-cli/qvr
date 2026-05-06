package model

// Target represents an AI agent that can consume skills.
type Target struct {
	Name      string `json:"name"`
	LocalDir  string `json:"local_dir"`
	GlobalDir string `json:"global_dir"`
}

// Targets maps target identifiers to their directory configuration.
var Targets = map[string]Target{
	"claude":   {Name: "Claude Code", LocalDir: ".claude/skills", GlobalDir: "~/.claude/skills"},
	"cursor":   {Name: "Cursor", LocalDir: ".cursor/rules", GlobalDir: "~/.cursor/rules"},
	"copilot":  {Name: "GitHub Copilot", LocalDir: ".github/copilot/skills", GlobalDir: "~/.github/copilot/skills"},
	"codex":    {Name: "OpenAI Codex CLI", LocalDir: ".codex/skills", GlobalDir: "~/.codex/skills"},
	"windsurf": {Name: "Windsurf", LocalDir: ".windsurf/skills", GlobalDir: "~/.windsurf/skills"},
	"project":  {Name: "Generic Agent", LocalDir: ".agent/skills", GlobalDir: "~/.agent/skills"},
}

// TargetNames returns all valid target names sorted alphabetically.
func TargetNames() []string {
	return []string{"claude", "codex", "copilot", "cursor", "project", "windsurf"}
}
