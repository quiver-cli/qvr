package derive_test

import (
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/derive"
)

// writeLock writes a qvr.lock at dir/qvr.lock holding one code-review entry
// pinned to the given registry/ref/commit, and returns nothing (the file is
// the fixture). Uses the real model writer so the test exercises the same
// on-disk shape EnrichSkillIdentity reads.
func writeLock(t *testing.T, dir, registry, ref, commit string) {
	t.Helper()
	l := model.NewLockFile(filepath.Join(dir, model.LockFileName))
	l.Put(&model.LockEntry{
		Name:        "code-review",
		Registry:    registry,
		Source:      "https://github.com/" + registry + "/skills.git",
		Ref:         ref,
		Commit:      commit,
		SubtreeHash: "sha256:6d478",
		Targets:     []string{"claude"},
	})
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
func skillRows(sid uuid.UUID, workingDir string) []*ops.RawTrace {
	rows := []*ops.RawTrace{
		row(sid, 0, `{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"review my code"}}`),
		row(sid, 1, `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":2},"content":[`+
			`{"type":"tool_use","id":"toolu_skill","name":"Skill","input":{"skill":"code-review"}}]}}`),
	}
	for _, r := range rows {
		r.WorkingDirectory = workingDir
	}
	return rows
}

// TestEnrichSkillIdentity_FromProjectLock is the core of #146: a skill loaded
// by bare name is enriched with full identity (registry/version/commit/hash)
// resolved from the calling project's qvr.lock.
func TestEnrichSkillIdentity_FromProjectLock(t *testing.T) {
	// Isolate the global-lock fallback at an empty temp home so it can't hit
	// the developer's real ~/.quiver.
	t.Setenv("QVR_HOME", t.TempDir())

	proj := t.TempDir()
	writeLock(t, proj, "raks", "v0.2.0", "94e539be7d6a01774d723a7c25513af0f070de7b")

	rows := skillRows(uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"), proj)
	spans, err := derive.DeriveSession(rows)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	derive.EnrichSkillIdentity(spans, rows)

	sp := skillSpan(t, spans)
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
}

// TestEnrichSkillIdentity_Collision proves the whole point of the issue: the
// SAME bare skill name resolves to DIFFERENT identities depending on which
// project's lock is consulted, so name collisions across registries are now
// distinguishable.
func TestEnrichSkillIdentity_Collision(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	projA := t.TempDir()
	projB := t.TempDir()
	writeLock(t, projA, "raks", "v0.2.0", "aaaaaaa")
	writeLock(t, projB, "acme", "v9.9.9", "bbbbbbb")

	rowsA := skillRows(uuid.New(), projA)
	rowsB := skillRows(uuid.New(), projB)

	spansA, _ := derive.DeriveSession(rowsA)
	spansB, _ := derive.DeriveSession(rowsB)
	derive.EnrichSkillIdentity(spansA, rowsA)
	derive.EnrichSkillIdentity(spansB, rowsB)

	a := skillSpan(t, spansA)
	b := skillSpan(t, spansB)
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
// skill absent from every lock keeps only its bare name.
func TestEnrichSkillIdentity_NotInLock(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())

	proj := t.TempDir() // no qvr.lock written
	rows := skillRows(uuid.New(), proj)
	spans, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(spans, rows)

	sp := skillSpan(t, spans)
	if sp.Attributes["skill.name"] != "code-review" {
		t.Errorf("skill.name lost: %v", sp.Attributes["skill.name"])
	}
	for _, k := range []string{"skill.registry", "skill.version", "skill.commit", "skill.subtree_hash"} {
		if _, ok := sp.Attributes[k]; ok {
			t.Errorf("fabricated %s for a skill not in any lock: %v", k, sp.Attributes[k])
		}
	}
}

// TestEnrichSkillIdentity_GlobalFallback confirms a skill installed globally
// (no project lock entry) still resolves via the user-global lock.
func TestEnrichSkillIdentity_GlobalFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QVR_HOME", home)
	writeLock(t, home, "global-reg", "v1.0.0", "ccccccc")

	proj := t.TempDir() // project has no qvr.lock
	rows := skillRows(uuid.New(), proj)
	spans, _ := derive.DeriveSession(rows)
	derive.EnrichSkillIdentity(spans, rows)

	sp := skillSpan(t, spans)
	if sp.Attributes["skill.registry"] != "global-reg" {
		t.Errorf("global fallback failed: skill.registry = %v", sp.Attributes["skill.registry"])
	}
}
