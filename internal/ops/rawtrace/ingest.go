package rawtrace

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/redact"
	"github.com/quiver-cli/qvr/internal/ops/store"
)

// IngestParams configures a one-shot ingest of an already-produced transcript.
type IngestParams struct {
	Agent      string // required: which agent's transcript format this is
	Path       string // the transcript/rollout file
	SessionID  string // optional: override the correlated session id
	WorkingDir string // optional: override the recorded working directory
}

// Ingest records an already-produced transcript as a captured session WITHOUT
// any live hook — the recovery/QA/CI counterpart to hook-driven Capture. It is
// the answer to "capture one session without persistently editing the live
// agent config" (#148): point it at a codex rollout or a claude session
// transcript and it stores the raw rows and derives spans, exactly as a hook
// firing would have.
//
// Like Capture it is cursor-based and idempotent: re-ingesting the same file
// stores only rows past the last byte offset, so importing a transcript that
// has since grown appends the new tail rather than duplicating. Unlike Capture
// it never applies the skill-only retention gate — an explicit ingest is a
// deliberate import, so the session is always kept even if it used no skill.
func Ingest(ctx context.Context, s Store, p IngestParams) (*Result, error) {
	if p.Agent == "" {
		return nil, fmt.Errorf("rawtrace: ingest requires an agent")
	}
	path, err := filepath.Abs(expandHome(p.Path))
	if err != nil {
		return nil, fmt.Errorf("rawtrace: resolve path: %w", err)
	}
	if !fileExists(path) {
		return nil, fmt.Errorf("rawtrace: ingest source is not a file: %s", path)
	}

	sniff := sniffTranscript(path)
	rawID := firstNonEmpty(p.SessionID, sniff.sessionID, uuidInName(path), path)
	sessionID, agentSessionID := resolveSession(p.Agent, rawID)
	wd := firstNonEmpty(p.WorkingDir, sniff.cwd)

	res := &Result{SessionID: sessionID, TranscriptPath: path}

	lines, newOffset, err := tailTranscript(ctx, s, p.Agent, path)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	rows := make([]*ops.RawTrace, 0, len(lines))
	for _, ln := range lines {
		rows = append(rows, &ops.RawTrace{
			AgentName:        p.Agent,
			SessionID:        sessionID,
			AgentSessionID:   agentSessionID,
			Source:           ops.RawSourceTranscript,
			SourcePath:       path,
			WorkingDirectory: wd,
			ByteOffset:       ln.offset,
			CapturedAt:       now,
			Raw:              redact.Bytes(ln.bytes),
		})
	}
	res.LinesStored = len(lines)

	if len(rows) > 0 {
		cursor := &store.RawCursor{
			AgentName:  p.Agent,
			SourcePath: path,
			ByteOffset: newOffset,
			SessionID:  sessionID,
		}
		if err := s.AppendRawTraces(ctx, rows, cursor); err != nil {
			return nil, err
		}
	}

	// Always (re-)derive: a fresh ingest derives the new rows, and a no-op
	// re-ingest still refreshes spans against the current deriver.
	n, _, derr := persistSpans(ctx, s, sessionID, p.Agent)
	res.SpansStored = n
	res.SpanError = derr
	return res, nil
}

// transcriptSniff holds the session id and working directory recovered from a
// transcript's own content, so an ingest correlates and scopes the session the
// same way a live hook capture would have.
type transcriptSniff struct {
	sessionID string
	cwd       string
}

// sniffTranscript reads the leading rows of a transcript looking for the agent's
// own session id and cwd. Handles both the claude shape (top-level sessionId /
// cwd) and the codex rollout shape (payload.id / payload.cwd in session_meta).
// Best-effort: a file we can't read or parse yields an empty sniff.
func sniffTranscript(path string) transcriptSniff {
	f, err := os.Open(path)
	if err != nil {
		return transcriptSniff{}
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // transcript lines can be large
	var out transcriptSniff
	for i := 0; i < 200 && sc.Scan(); i++ {
		var v struct {
			SessionID   string `json:"session_id"`
			SessionIDCC string `json:"sessionId"`
			Cwd         string `json:"cwd"`
			Payload     struct {
				ID        string `json:"id"`
				SessionID string `json:"session_id"`
				Cwd       string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &v) != nil {
			continue
		}
		if out.sessionID == "" {
			out.sessionID = firstNonEmpty(v.SessionID, v.SessionIDCC, v.Payload.ID, v.Payload.SessionID)
		}
		if out.cwd == "" {
			out.cwd = firstNonEmpty(v.Cwd, v.Payload.Cwd)
		}
		if out.sessionID != "" && out.cwd != "" {
			break
		}
	}
	return out
}

var uuidRe = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// uuidInName extracts a uuid embedded in a transcript filename — claude names a
// transcript "<session-uuid>.jsonl"; codex rollouts embed the uuid in the file
// name too. Returns "" when the name carries none.
func uuidInName(path string) string {
	return uuidRe.FindString(filepath.Base(path))
}

// SniffAgent guesses which agent produced a transcript from its first row shape:
// codex rollouts are a stream of {type: session_meta|turn_context|event_msg|
// response_item} envelopes; claude transcripts are {type: user|assistant|...}
// rows carrying a message object. Returns "" when it can't tell, so the caller
// can require an explicit --agent.
func SniffAgent(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for i := 0; i < 50 && sc.Scan(); i++ {
		var v struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
			Message json.RawMessage `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &v) != nil {
			continue
		}
		switch v.Type {
		case "session_meta", "turn_context", "event_msg", "response_item":
			return "codex"
		case "user", "assistant", "system", "summary":
			return "claude-code"
		}
		if len(v.Payload) > 0 {
			return "codex"
		}
		if len(v.Message) > 0 {
			return "claude-code"
		}
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
