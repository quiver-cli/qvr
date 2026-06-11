package rawtrace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/ops/rawtrace"
)

const gateUser = `{"type":"user","sessionId":"550e8400-e29b-41d4-a716-446655440000","cwd":"/tmp/proj","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"do the thing"}}`
const gatePlainAssistant = `{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":3},"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/x"}},{"type":"text","text":"done"}]}}`
const gateSkillAssistant = `{"type":"assistant","timestamp":"2026-06-02T00:00:02.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":10,"output_tokens":3},"content":[{"type":"tool_use","id":"t2","name":"Skill","input":{"command":"code-review"}},{"type":"text","text":"ok"}]}}`

// TestIngest_SkillGate pins derive-then-skip-persist: a gated ingest of a
// provably skill-less session stores NOTHING (no rows, no cursor), while a
// skill-bearing one persists normally.
func TestIngest_SkillGate(t *testing.T) {
	cases := []struct {
		name      string
		lines     string
		wantSkip  bool
		wantLines int
	}{
		{"skill-less session is skipped pre-persist", gateUser + "\n" + gatePlainAssistant + "\n", true, 0},
		{"skill session is persisted", gateUser + "\n" + gateSkillAssistant + "\n", false, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			ctx := context.Background()
			transcript := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(transcript, []byte(tc.lines), 0o644); err != nil {
				t.Fatal(err)
			}
			res, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{
				Agent: "claude", Path: transcript, SkillGate: true,
			})
			if err != nil {
				t.Fatalf("ingest: %v", err)
			}
			if res.Skipped != tc.wantSkip {
				t.Errorf("Skipped = %v, want %v", res.Skipped, tc.wantSkip)
			}
			rows := queryRows(t, s, res.SessionID)
			if len(rows) != tc.wantLines {
				t.Errorf("stored rows = %d, want %d", len(rows), tc.wantLines)
			}
		})
	}
}

// TestIngest_SkillGate_SelfHealing pins the cursor contract behind the gate: a
// skipped file leaves the cursor at 0, so when the session later grows and
// gains skill usage, the WHOLE file re-derives and persists in full.
func TestIngest_SkillGate_SelfHealing(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(gateUser+"\n"+gatePlainAssistant+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	p := rawtrace.IngestParams{Agent: "claude", Path: transcript, SkillGate: true}
	first, err := rawtrace.Ingest(ctx, s, p)
	if err != nil {
		t.Fatalf("first ingest: %v", err)
	}
	if !first.Skipped {
		t.Fatal("first pass should skip the skill-less session")
	}

	// The session resumes and loads a skill.
	appendLine(t, transcript, gateSkillAssistant)
	second, err := rawtrace.Ingest(ctx, s, p)
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if second.Skipped {
		t.Fatal("grown skill-bearing session must pass the gate")
	}
	if second.LinesStored != 3 {
		t.Errorf("want full file (3 lines) persisted from offset 0, got %d", second.LinesStored)
	}
}

// TestIngest_SkillGate_KeptSessionsIngestDeltas pins: once a session has
// stored rows, gated re-ingests append its new tail unconditionally — the
// keep decision is not re-litigated per pass.
func TestIngest_SkillGate_KeptSessionsIngestDeltas(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(gateUser+"\n"+gateSkillAssistant+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := rawtrace.IngestParams{Agent: "claude", Path: transcript, SkillGate: true}
	if _, err := rawtrace.Ingest(ctx, s, p); err != nil {
		t.Fatalf("first ingest: %v", err)
	}

	// New tail with no skill content: must still land (delta of a kept session).
	appendLine(t, transcript, gatePlainAssistant)
	res, err := rawtrace.Ingest(ctx, s, p)
	if err != nil {
		t.Fatalf("delta ingest: %v", err)
	}
	if res.Skipped || res.LinesStored != 1 {
		t.Errorf("delta of a kept session: Skipped=%v lines=%d, want false/1", res.Skipped, res.LinesStored)
	}
}

// TestIngest_UngatedKeepsEverything pins explicit-import semantics: without
// the gate, a skill-less session is stored (a deliberate ingest is kept).
func TestIngest_UngatedKeepsEverything(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(gateUser+"\n"+gatePlainAssistant+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{Agent: "claude", Path: transcript})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if res.Skipped || res.LinesStored != 2 {
		t.Errorf("ungated ingest: Skipped=%v lines=%d, want false/2", res.Skipped, res.LinesStored)
	}
}
