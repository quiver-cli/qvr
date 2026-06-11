package derive_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/google/uuid"
)

// writeGlobalLock writes a code-review entry into the user-global lock (under
// QVR_HOME) so EnrichSkillIdentity resolves it via the global fallback —
// codex rows carry no working directory, so the project lock isn't consulted.
func writeGlobalLock(t *testing.T, home string, e *model.LockEntry) {
	t.Helper()
	l := model.NewLockFile(filepath.Join(home, model.LockFileName))
	l.Put(e)
	if err := l.Write(); err != nil {
		t.Fatalf("write global lock: %v", err)
	}
}

// codexSkillLoadRows is a minimal codex session whose single tool call reads a
// skill's SKILL.md at filePath — the native "load" signal. The path is what
// EnrichSkillIdentity verifies against.
func codexSkillLoadRows(sid uuid.UUID, filePath string) []*ops.RawTrace {
	args, _ := json.Marshal(map[string]string{"cmd": "sed -n '1,40p' " + filePath})
	line, _ := json.Marshal(map[string]any{
		"timestamp": "2026-06-02T15:32:03.518Z",
		"type":      "response_item",
		"payload": map[string]any{
			"type":      "function_call",
			"name":      "exec_command",
			"arguments": string(args),
			"call_id":   "call_skill",
		},
	})
	return []*ops.RawTrace{codexRow(sid, 0, string(line))}
}

// seedWorktree creates the on-disk worktree the lock entry pins, with the skill
// living at <worktree>/<entry.Path>/SKILL.md (the standard registry layout).
// Returns the absolute SKILL.md path the agent would load.
func seedWorktree(t *testing.T, e *model.LockEntry) string {
	t.Helper()
	root := registry.WorktreePath(e.Registry, e.Name, registry.ShortSHA(e.Commit))
	dir := filepath.Join(root, e.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	skillMD := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(skillMD, []byte("---\nname: code-review\ndescription: x\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return skillMD
}

func crEntry(commit string) *model.LockEntry {
	return &model.LockEntry{
		Name:        "code-review",
		Registry:    "raks",
		Source:      "https://github.com/raks/skills.git",
		Ref:         "v0.2.0",
		Commit:      commit,
		Path:        "skills/code-review",
		SubtreeHash: "sha256:6d478",
		Targets:     []string{"claude"},
	}
}

// TestEnrich_Claude_NoPathIsUnverified pins #149 for claude: the Skill tool call
// carries no load path, so identity is a name-keyed guess — attached, but flagged
// skill.verified=false rather than presented as authoritative.
func TestEnrich_Claude_NoPathIsUnverified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	writeGlobalLock(t, home, crEntry("94e539be7d6a01774d723a7c25513af0f070de7b"))

	rows := skillRows(uuid.New(), "") // claude Skill tool, no working dir
	d, _ := derive.DeriveSession(rows)
	spans := d.Spans
	derive.EnrichSkillIdentity(spans, rows)

	sp := skillSpan(t, spans)
	if sp.Attributes["skill.registry"] != "raks" {
		t.Errorf("name-keyed guess should still attach identity, got registry=%v", sp.Attributes["skill.registry"])
	}
	if v, _ := sp.Attributes["skill.verified"].(bool); v {
		t.Error("claude skill (no load path) must be skill.verified=false — qvr cannot prove the loaded copy")
	}
}

// TestEnrich_Codex_LoadPathInWorktreeIsVerified pins #149 for codex: when the
// loaded file resolves into the locked worktree, identity is asserted AND
// marked verified.
func TestEnrich_Codex_LoadPathInWorktreeIsVerified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	e := crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")
	writeGlobalLock(t, home, e)
	skillMD := seedWorktree(t, e) // the real locked artifact on disk

	rows := codexSkillLoadRows(uuid.New(), skillMD)
	d, _ := derive.DeriveSession(rows)
	spans := d.Spans
	derive.EnrichSkillIdentity(spans, rows)

	sp := skillSpan(t, spans)
	if v, _ := sp.Attributes["skill.verified"].(bool); !v {
		t.Errorf("load from the locked worktree must be skill.verified=true; attrs=%v", sp.Attributes)
	}
	if sp.Attributes["skill.commit"] != e.Commit {
		t.Errorf("verified load should assert the locked commit, got %v", sp.Attributes["skill.commit"])
	}
}

// TestEnrich_Codex_ShadowingEjectIsUnverified is the core of #149: a same-named
// skill loaded from a path OUTSIDE the locked worktree (a global eject) must not
// be reported as the locked artifact — no commit/registry is asserted and the
// span is skill.verified=false.
func TestEnrich_Codex_ShadowingEjectIsUnverified(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	e := crEntry("94e539be7d6a01774d723a7c25513af0f070de7b")
	writeGlobalLock(t, home, e)
	seedWorktree(t, e) // the locked copy exists...

	// ...but the agent loaded a DIFFERENT copy: a global eject under ~/.claude.
	ejectMD := filepath.Join(home, ".claude", "skills", "code-review", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(ejectMD), 0o755); err != nil {
		t.Fatalf("mkdir eject: %v", err)
	}
	if err := os.WriteFile(ejectMD, []byte("---\nname: code-review\ndescription: drifted\n---\n"), 0o644); err != nil {
		t.Fatalf("write eject: %v", err)
	}

	rows := codexSkillLoadRows(uuid.New(), ejectMD)
	d, _ := derive.DeriveSession(rows)
	spans := d.Spans
	derive.EnrichSkillIdentity(spans, rows)

	sp := skillSpan(t, spans)
	if v, _ := sp.Attributes["skill.verified"].(bool); v {
		t.Errorf("an eject outside the worktree must be skill.verified=false; attrs=%v", sp.Attributes)
	}
	for _, k := range []string{"skill.commit", "skill.registry", "skill.subtree_hash"} {
		if _, ok := sp.Attributes[k]; ok {
			t.Errorf("must not attest %s for a copy the agent provably did not load: %v", k, sp.Attributes[k])
		}
	}
	if sp.Attributes["skill.name"] != "code-review" {
		t.Errorf("the bare name should survive, got %v", sp.Attributes["skill.name"])
	}
}
