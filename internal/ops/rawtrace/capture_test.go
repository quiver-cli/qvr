package rawtrace_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/rawtrace"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// newStore opens a throwaway SQLite store under t.TempDir().
func newStore(t *testing.T) store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), store.OpenOptions{
		Path: filepath.Join(t.TempDir(), "ops.db"),
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// TestIngest_TailsTranscriptVerbatim is the core guarantee: native transcript
// lines are stored byte-for-byte, and a second pass over a grown file only
// picks up newly appended lines (cursor-based idempotent tailing).
func TestIngest_TailsTranscriptVerbatim(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	transcript := filepath.Join(t.TempDir(), "session.jsonl")

	// Two native lines, deliberately with nested structure + a thinking block
	// so we can prove nothing is flattened or truncated.
	line1 := `{"type":"user","sessionId":"550e8400-e29b-41d4-a716-446655440000","cwd":"/tmp/proj","message":{"role":"user","content":"fix the bug"}}`
	line2 := `{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"let me reason about this carefully"},{"type":"tool_use","name":"Edit"}]}}`
	if err := os.WriteFile(transcript, []byte(line1+"\n"+line2+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{Agent: "claude-code", Path: transcript})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if res.LinesStored != 2 {
		t.Fatalf("first ingest: want 2 transcript lines, got %d", res.LinesStored)
	}

	sid := res.SessionID
	rows := queryRows(t, s, sid)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if got := string(rows[0].Raw); got != line1 {
		t.Errorf("row0 not verbatim:\n got: %s\nwant: %s", got, line1)
	}
	if got := string(rows[1].Raw); got != line2 {
		t.Errorf("row1 not verbatim:\n got: %s\nwant: %s", got, line2)
	}
	// The session id sniffed from the transcript must drive correlation, and
	// the cwd must scope the rows.
	if rows[0].SessionID.String() != "550e8400-e29b-41d4-a716-446655440000" {
		t.Errorf("session id not taken from transcript: %s", rows[0].SessionID)
	}
	if rows[0].WorkingDirectory != "/tmp/proj" {
		t.Errorf("cwd not sniffed from transcript: %q", rows[0].WorkingDirectory)
	}
	assertDenseMonotonicSeq(t, rows)

	// Append a third line; a second pass must store ONLY the new line.
	line3 := `{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`
	appendLine(t, transcript, line3)

	res2, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{Agent: "claude-code", Path: transcript})
	if err != nil {
		t.Fatalf("second ingest: %v", err)
	}
	if res2.LinesStored != 1 {
		t.Fatalf("second ingest: want 1 new transcript line, got %d", res2.LinesStored)
	}

	rows = queryRows(t, s, sid)
	if len(rows) != 3 {
		t.Fatalf("after second ingest: want 3 rows, got %d", len(rows))
	}
	if !hasVerbatimTranscriptLine(rows, line3) {
		t.Error("third transcript line not captured verbatim on second pass")
	}
}

// TestIngest_UnchangedFileIsNoOp pins cursor idempotency: re-ingesting the
// same file adds zero rows.
func TestIngest_UnchangedFileIsNoOp(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte(`{"type":"user","message":{"content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	first, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{Agent: "claude-code", Path: transcript})
	if err != nil {
		t.Fatalf("ingest: %v", err)
	}
	if first.LinesStored != 1 {
		t.Fatalf("want 1 line, got %d", first.LinesStored)
	}
	again, err := rawtrace.Ingest(ctx, s, rawtrace.IngestParams{Agent: "claude-code", Path: transcript})
	if err != nil {
		t.Fatalf("re-ingest: %v", err)
	}
	if again.LinesStored != 0 {
		t.Errorf("re-ingest of unchanged file stored %d lines, want 0", again.LinesStored)
	}
	if len(queryRows(t, s, first.SessionID)) != 1 {
		t.Error("re-ingest duplicated rows")
	}
}

// TestIngest_MissingFileErrors proves a nonexistent source is a real error,
// not a silent empty result.
func TestIngest_MissingFileErrors(t *testing.T) {
	s := newStore(t)
	_, err := rawtrace.Ingest(context.Background(), s, rawtrace.IngestParams{
		Agent: "claude-code", Path: "/does/not/exist.jsonl",
	})
	if err == nil {
		t.Fatal("expected error for missing transcript")
	}
}

// appendLine appends one line (plus a trailing newline) to an existing file.
func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(line + "\n"); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
}

// assertDenseMonotonicSeq asserts every row's Seq equals its index, i.e. the
// sequence numbers form a dense monotonic run.
func assertDenseMonotonicSeq(t *testing.T, rows []*ops.RawTrace) {
	t.Helper()
	for i, r := range rows {
		if r.Seq != i {
			t.Errorf("row %d: want seq %d, got %d", i, i, r.Seq)
		}
	}
}

// hasVerbatimTranscriptLine reports whether any transcript row stores want
// byte-for-byte.
func hasVerbatimTranscriptLine(rows []*ops.RawTrace, want string) bool {
	for _, r := range rows {
		if r.Source == ops.RawSourceTranscript && string(r.Raw) == want {
			return true
		}
	}
	return false
}

func queryRows(t *testing.T, s store.Store, sid uuid.UUID) []*ops.RawTrace {
	t.Helper()
	rows, err := s.QueryRawTraces(context.Background(), &store.RawTraceFilter{SessionID: &sid})
	if err != nil {
		t.Fatalf("query raw traces: %v", err)
	}
	return rows
}
