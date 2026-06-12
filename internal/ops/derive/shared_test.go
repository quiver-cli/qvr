package derive

import "testing"

// TestPathSkillRef_TokenBoundaries pins the path-token extraction across the
// text shapes derivers feed it: shell commands, compact JSON arguments, and
// bare paths. The JSON cases are the regression: a whitespace-only token class
// swallowed the surrounding JSON syntax and produced unresolvable load paths
// (observed in real session stores, 2026-06-11).
func TestPathSkillRef_TokenBoundaries(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantName string
		wantLoad bool
		wantPath string
	}{
		{
			name:     "shell command",
			text:     `sed -n '1,40p' .codex/skills/code-review/SKILL.md`,
			wantName: "code-review",
			wantLoad: true,
			wantPath: ".codex/skills/code-review/SKILL.md",
		},
		{
			name:     "compact JSON file_path argument",
			text:     `{"file_path":"/home/u/p/.claude/skills/qa-demo/SKILL.md"}`,
			wantName: "qa-demo",
			wantLoad: true,
			wantPath: "/home/u/p/.claude/skills/qa-demo/SKILL.md",
		},
		{
			name:     "escaped JSON inside a string value",
			text:     `{"cmd":"cat /u/.quiver/worktrees/raks/code-review/94e539b/skills/code-review/SKILL.md\",..."}`,
			wantName: "code-review",
			wantLoad: true,
			wantPath: "/u/.quiver/worktrees/raks/code-review/94e539b/skills/code-review/SKILL.md",
		},
		{
			name:     "bare directory path (no SKILL.md)",
			text:     `/home/u/p/.claude/skills/frontend-design`,
			wantName: "frontend-design",
			wantLoad: false,
			wantPath: "/home/u/p/.claude/skills/frontend-design",
		},
		{
			name:     "no skill reference",
			text:     `ls -la ./src`,
			wantName: "",
		},
		{
			// Agents resolve the agent-dir symlink and read the store
			// directly; a local install's subtree has no skills/<name>/
			// segment, so the store layout is its own signal (observed in
			// real codex rollouts, 2026-06-11).
			name:     "resolved local-install worktree path",
			text:     `{"cmd": "sed -n '1,220p' /Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/SKILL.md", "workdir": "/tmp"}`,
			wantName: "qvr-probe",
			wantLoad: true,
			wantPath: "/Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/SKILL.md",
		},
		{
			name:     "worktree supporting-file read is not a load",
			text:     `sh /Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/scripts/stamp.sh`,
			wantName: "qvr-probe",
			wantLoad: false,
			wantPath: "/Users/u/.quiver/worktrees/_local/qvr-probe/17dd2d4/scripts/stamp.sh",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, isLoad, path := pathSkillRef(tt.text, nil)
			if name != tt.wantName {
				t.Fatalf("name = %q, want %q", name, tt.wantName)
			}
			if name == "" {
				return
			}
			if isLoad != tt.wantLoad {
				t.Errorf("isLoad = %v, want %v", isLoad, tt.wantLoad)
			}
			if path != tt.wantPath {
				t.Errorf("path = %q, want %q", path, tt.wantPath)
			}
		})
	}
}
