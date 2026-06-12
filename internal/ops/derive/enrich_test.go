package derive_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/google/uuid"
)

// lockEntry builds a code-review entry pinned to the given registry/ref/commit
// (the standard fixture across the enrichment tests).
func lockEntry(registry, ref, commit string) *model.LockEntry {
	return &model.LockEntry{
		Name:        "code-review",
		Registry:    registry,
		Source:      "https://github.com/" + registry + "/skills.git",
		Ref:         ref,
		Commit:      commit,
		SubtreeHash: "sha256:6d478",
		Targets:     []string{"claude"},
	}
}

// writeLockEntry writes a qvr.lock at dir/qvr.lock holding the entry. Uses the
// real model writer so the test exercises the same on-disk shape
// EnrichSkillIdentity reads.
func writeLockEntry(t *testing.T, dir string, e *model.LockEntry) {
	t.Helper()
	l := model.NewLockFile(filepath.Join(dir, model.LockFileName))
	l.Put(e)
	if err := l.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
}

// skillSpan returns the first SKILL span, or fails.
func skillSpan(t *testing.T, spans []derive.Span) *derive.Span {
	t.Helper()
	for i := range spans {
		if spans[i].Kind == derive.KindSkill {
			return &spans[i]
		}
	}
	t.Fatalf("no SKILL span in %d spans", len(spans))
	return nil
}

// skillRows is a minimal claude session that loads the code-review skill,
// stamped with the given working directory so the resolver can find its lock.
// When baseDir is non-empty the session also carries the harness-injected
// isMeta skill-body line ("Base directory for this skill: <baseDir>") — the
// load-path evidence observed in real Claude Code stores (2026-06-11) that
// proof-gated enrichment verifies against the locked worktree.
func skillRows(sid uuid.UUID, workingDir, baseDir string) []*ops.RawTrace {
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"review my code"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":2},"content":[`+
			`{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"skill":"code-review"}}]}}`),
	}
	if baseDir != "" {
		rows = append(rows,
			row(sid, 2, `{"type":"user","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_skill","content":"Launching skill: code-review"}]}}`),
			row(sid, 3, `{"type":"user","isMeta":true,"timestamp":"2026-06-02T00:00:03.000Z","message":{"role":"user","content":[{"type":"text","text":"Base directory for this skill: `+baseDir+`\n\n# code review\n\n## Instructions\n"}]}}`),
		)
	}
	for _, r := range rows {
		r.WorkingDirectory = workingDir
	}
	return rows
}

// enriched derives the session and runs proof-gated enrichment, returning the
// SKILL span.
func enriched(t *testing.T, rows []*ops.RawTrace) *derive.Span {
	t.Helper()
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	derive.EnrichSkillIdentity(d.Spans, rows, nil)
	return skillSpan(t, d.Spans)
}

// TestEnrichSkillIdentity_FromProjectLock is the core of #146 under the
// proof-gated model: a claude skill whose injected base directory resolves
// into the locked worktree is enriched with full identity — and skill.version
// is present, which IS the verified signal.
func TestEnrichSkillIdentity_FromProjectLock(t *testing.T) {
	// Isolate the global-lock fallback at an empty temp home so it can't hit
	// the developer's real ~/.quiver.
	t.Setenv("QVR_HOME", t.TempDir())

	proj := t.TempDir()
	e := lockEntry("raks", "v0.2.0", "94e539be7d6a01774d723a7c25513af0f070de7b")
	writeLockEntry(t, proj, e)
	skillMD := seedWorktree(t, e)

	rows := skillRows(uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"), proj, filepath.Dir(skillMD))
	sp := enriched(t, rows)
	want := map[string]string{
		"skill.name":         "code-review",
		"skill.registry":     "raks",
		"skill.version":      "v0.2.0",
		"skill.commit":       "94e539be7d6a01774d723a7c25513af0f070de7b",
		"skill.subtree_hash": "sha256:6d478",
		"skill.source":       "https://github.com/raks/skills.git",
	}
	for k, v := range want {
		if got := sp.Attributes[k]; got != v {
			t.Errorf("%s = %v, want %q", k, got, v)
		}
	}
	if sp.Attributes["skill.load_path"] != filepath.Dir(skillMD) {
		t.Errorf("isMeta base dir should be captured as skill.load_path, got %v", sp.Attributes["skill.load_path"])
	}
}

// TestEnrichSkillIdentity_Collision proves the whole point of the issue: the
// SAME bare skill name resolves to DIFFERENT identities depending on which
// project's lock (and worktree) the loaded path proves, so name collisions
// across registries are distinguishable.
func TestEnrichSkillIdentity_Collision(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	projA := t.TempDir()
	projB := t.TempDir()
	eA := lockEntry("raks", "v0.2.0", "aaaaaaa")
	eB := lockEntry("acme", "v9.9.9", "bbbbbbb")
	writeLockEntry(t, projA, eA)
	writeLockEntry(t, projB, eB)
	mdA := seedWorktree(t, eA)
	mdB := seedWorktree(t, eB)

	a := enriched(t, skillRows(uuid.New(), projA, filepath.Dir(mdA)))
	b := enriched(t, skillRows(uuid.New(), projB, filepath.Dir(mdB)))

	if a.Attributes["skill.name"] != b.Attributes["skill.name"] {
		t.Fatal("precondition: both should carry the same bare skill.name")
	}
	if a.Attributes["skill.registry"] == b.Attributes["skill.registry"] {
		t.Errorf("collision not resolved: both registries = %v", a.Attributes["skill.registry"])
	}
	if a.Attributes["skill.registry"] != "raks" || b.Attributes["skill.registry"] != "acme" {
		t.Errorf("wrong registries: A=%v B=%v", a.Attributes["skill.registry"], b.Attributes["skill.registry"])
	}
}

// TestEnrichSkillIdentity_NotInLock confirms identity is never fabricated: a
// skill absent from every lock keeps only its bare name (no skill.version —
// unverified by definition).
func TestEnrichSkillIdentity_NotInLock(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	proj := t.TempDir() // no qvr.lock written
	sp := enriched(t, skillRows(uuid.New(), proj, ""))
	if sp.Attributes["skill.name"] != "code-review" {
		t.Errorf("skill.name lost: %v", sp.Attributes["skill.name"])
	}
	for _, k := range []string{"skill.registry", "skill.version", "skill.commit", "skill.subtree_hash"} {
		if _, ok := sp.Attributes[k]; ok {
			t.Errorf("fabricated %s for a skill not in any lock: %v", k, sp.Attributes[k])
		}
	}
}

// TestEnrichSkillIdentity_SnapshotPinsHistory pins the temporal-lineage fix:
// symlink-origin evidence (a claude base-dir path) re-derived AFTER a version
// move must keep the ingest-time identity from the session's snapshot — not
// re-resolve through the moved symlink and rewrite history.
func TestEnrichSkillIdentity_SnapshotPinsHistory(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())
	proj := t.TempDir()

	// The lock and the agent-dir symlink now point at v2 (post-switch state).
	e2 := lockEntry("raks", "v2.0.0", "bbbbbb2")
	writeLockEntry(t, proj, e2)
	v2dir := filepath.Dir(seedWorktree(t, e2))
	link := filepath.Join(proj, ".claude", "skills", "code-review")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(v2dir, link); err != nil {
		t.Fatal(err)
	}

	// The session ran on v1; its first ingest froze that proof.
	snap := map[string]*model.LockEntry{
		"code-review": lockEntry("raks", "v1.0.0", "aaaaaa1"),
	}

	rows := skillRows(uuid.New(), proj, link) // base dir = the symlink path
	d, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	derive.EnrichSkillIdentity(d.Spans, rows, snap)
	sp := skillSpan(t, d.Spans)
	if sp.Attributes["skill.version"] != "v1.0.0" {
		t.Errorf("snapshot must pin history: version = %v, want v1.0.0 (symlink now points at v2)",
			sp.Attributes["skill.version"])
	}
	if sp.Attributes["skill.commit"] != "aaaaaa1" {
		t.Errorf("snapshot commit lost: %v", sp.Attributes["skill.commit"])
	}
}

// TestEnrichSkillIdentity_GlobalFallback confirms a skill installed globally
// (no project lock entry) still resolves — with proof — via the user-global
// lock.
func TestEnrichSkillIdentity_GlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	e := lockEntry("global-reg", "v1.0.0", "ccccccc")
	writeLockEntry(t, home, e)
	md := seedWorktree(t, e)

	proj := t.TempDir() // project has no qvr.lock
	sp := enriched(t, skillRows(uuid.New(), proj, filepath.Dir(md)))
	if sp.Attributes["skill.registry"] != "global-reg" {
		t.Errorf("global fallback failed: skill.registry = %v", sp.Attributes["skill.registry"])
	}
	if sp.Attributes["skill.version"] != "v1.0.0" {
		t.Errorf("proven global skill must carry skill.version, got %v", sp.Attributes["skill.version"])
	}
}
