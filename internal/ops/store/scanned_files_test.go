package store

import (
	"context"
	"testing"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/google/uuid"
)

// TestScannedFiles_RoundTrip pins the ledger contract: upsert + per-agent get,
// NULL session ids, and last-write-wins refresh.
func TestScannedFiles_RoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	rows := []*ScannedFile{
		{AgentName: "claude-code", SourcePath: "/a.jsonl", Size: 10, MtimeMs: 100, Status: ScanStatusIngested, SessionID: sid},
		{AgentName: "claude-code", SourcePath: "/b.jsonl", Size: 20, MtimeMs: 200, Status: ScanStatusSkipped}, // no session id
		{AgentName: "codex", SourcePath: "/c.jsonl", Size: 30, MtimeMs: 300, Status: ScanStatusError},
	}
	for _, r := range rows {
		if err := st.UpsertScannedFile(ctx, r); err != nil {
			t.Fatalf("upsert %s: %v", r.SourcePath, err)
		}
	}

	claude, err := st.GetScannedFiles(ctx, "claude-code")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(claude) != 2 {
		t.Fatalf("want 2 claude rows, got %d", len(claude))
	}
	a := claude["/a.jsonl"]
	if a == nil || a.Size != 10 || a.MtimeMs != 100 || a.Status != ScanStatusIngested || a.SessionID != sid {
		t.Errorf("ingested row did not round-trip: %+v", a)
	}
	b := claude["/b.jsonl"]
	if b == nil || b.Status != ScanStatusSkipped || b.SessionID != uuid.Nil {
		t.Errorf("skipped row (NULL session) did not round-trip: %+v", b)
	}
}

// TestScannedFiles_RefreshReplaces pins last-write-wins: re-scanning a file
// with new stats replaces its ledger row.
func TestScannedFiles_RefreshReplaces(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	if err := st.UpsertScannedFile(ctx, &ScannedFile{
		AgentName: "claude-code", SourcePath: "/b.jsonl",
		Size: 20, MtimeMs: 200, Status: ScanStatusSkipped,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := st.UpsertScannedFile(ctx, &ScannedFile{
		AgentName: "claude-code", SourcePath: "/b.jsonl",
		Size: 25, MtimeMs: 250, Status: ScanStatusIngested, SessionID: sid,
	}); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	claude, err := st.GetScannedFiles(ctx, "claude-code")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got := claude["/b.jsonl"]; got == nil || got.Size != 25 || got.Status != ScanStatusIngested {
		t.Errorf("refresh did not replace: %+v", got)
	}
}

// TestScannedFiles_SurviveDeleteSession is the churn-loop regression anchor:
// pruning a session must not erase the file's ledger row.
func TestScannedFiles_SurviveDeleteSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	if err := st.UpsertScannedFile(ctx, &ScannedFile{
		AgentName: "claude-code", SourcePath: "/a.jsonl",
		Size: 10, MtimeMs: 100, Status: ScanStatusIngested, SessionID: sid,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := st.DeleteSession(ctx, sid); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	ledger, err := st.GetScannedFiles(ctx, "claude-code")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if ledger["/a.jsonl"] == nil {
		t.Error("scanned_files row erased by DeleteSession — scan would re-ingest forever")
	}
}

// TestDeleteRawBySourcePath pins the document-layout replacement primitive:
// only the named file's rows go, other paths and agents are untouched.
func TestDeleteRawBySourcePath(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	sid := uuid.New()

	appendRaw := func(agent, path string, n int) {
		t.Helper()
		rows := make([]*ops.RawTrace, 0, n)
		for i := range n {
			rows = append(rows, &ops.RawTrace{
				AgentName:  agent,
				SessionID:  sid,
				Source:     ops.RawSourceTranscript,
				SourcePath: path,
				ByteOffset: int64(i * 10),
				Raw:        []byte(`{"line":` + string(rune('0'+i)) + `}`),
			})
		}
		if err := st.AppendRawTraces(ctx, rows, nil); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	appendRaw("opencode", "/store/ses_1.json", 2)
	appendRaw("opencode", "/store/ses_2.json", 1)
	appendRaw("hermes", "/store/ses_1.json", 1)

	n, err := st.DeleteRawBySourcePath(ctx, "opencode", "/store/ses_1.json")
	if err != nil {
		t.Fatalf("delete by source: %v", err)
	}
	if n != 2 {
		t.Errorf("want 2 rows deleted, got %d", n)
	}
	left, err := st.QueryRawTraces(ctx, &RawTraceFilter{SessionID: &sid})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(left) != 2 {
		t.Errorf("want 2 surviving rows (other path + other agent), got %d", len(left))
	}
}
