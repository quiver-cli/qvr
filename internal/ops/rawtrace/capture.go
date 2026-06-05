// Package rawtrace is the lossless capture path for agent traces. A hook firing
// is treated as a trigger + pointer, not a data source: each firing tails the
// agent's own transcript/rollout file from the last byte offset and stores the
// new lines verbatim, and also stores the raw hook payload. Nothing is parsed,
// typed, normalized, or truncated — the bytes are kept exactly as the agent
// wrote them, so any downstream view (spans, attribution, dashboards) can be
// derived later without information loss.
//
// The mechanism is hook-driven, cursor-based transcript tailing: the byte
// offset consumed per file is persisted in SQLite (one atomic tx per capture),
// so each firing resumes exactly where the last left off.
package rawtrace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/ops/derive"
	"github.com/quiver-cli/qvr/internal/ops/redact"
	"github.com/quiver-cli/qvr/internal/ops/store"
)

// Store is the persistence surface capture needs (defined here, the consumer,
// per the project's interface-in-consumer convention).
type Store interface {
	GetRawCursor(ctx context.Context, agent, sourcePath string) (int64, error)
	AppendRawTraces(ctx context.Context, rows []*ops.RawTrace, cursor *store.RawCursor) error
	QueryRawTraces(ctx context.Context, f *store.RawTraceFilter) ([]*ops.RawTrace, error)
	ReplaceSessionSpans(ctx context.Context, sessionID uuid.UUID, rows []*store.SpanRow) error
	DeleteSession(ctx context.Context, sessionID uuid.UUID) (int64, error)
}

// hookPayload is the minimal slice of any agent hook payload we read to locate
// the transcript and correlate the session. Every other field stays untouched
// inside the stored raw bytes.
type hookPayload struct {
	SessionID           string `json:"session_id"`
	TranscriptPath      string `json:"transcript_path"`
	AgentTranscriptPath string `json:"agent_transcript_path"`
	Cwd                 string `json:"cwd"`
	HookEventName       string `json:"hook_event_name"`
}

// Result reports what a single Capture call stored, for diagnostics/tests.
type Result struct {
	SessionID      uuid.UUID
	TranscriptPath string
	LinesStored    int
	HookStored     bool
	SpansStored    int
	SpanError      error // non-nil if span re-derivation failed (capture still succeeded)
	Pruned         bool  // session dropped by the skill-only retention gate
}

// Capture ingests one hook firing: it stores the raw hook payload and tails the
// agent's transcript for any new lines. It never returns an error for "nothing
// to capture" (empty payload, missing transcript); those are normal and yield a
// Result with zero counts. Errors are reserved for genuine store/IO failures.
func Capture(ctx context.Context, s Store, agent, hookType string, payload []byte) (*Result, error) {
	var hp hookPayload
	// Best-effort decode: a malformed/empty payload still gets stored raw; we
	// just won't have a transcript pointer or session id to go with it.
	_ = json.Unmarshal(payload, &hp)

	if hookType == "" {
		hookType = hp.HookEventName
	}
	sessionID, agentSessionID := resolveSession(agent, hp.SessionID)
	res := &Result{SessionID: sessionID}

	now := time.Now().UTC()
	var rows []*ops.RawTrace
	var cursor *store.RawCursor

	// 1. Tail the transcript, if we can locate it.
	if path := resolveTranscriptPath(agent, &hp, sessionID); path != "" {
		res.TranscriptPath = path
		lines, newOffset, err := tailTranscript(ctx, s, agent, path)
		if err != nil {
			return nil, err
		}
		for _, ln := range lines {
			rows = append(rows, &ops.RawTrace{
				AgentName:        agent,
				SessionID:        sessionID,
				AgentSessionID:   agentSessionID,
				Source:           ops.RawSourceTranscript,
				SourcePath:       path,
				WorkingDirectory: hp.Cwd,
				ByteOffset:       ln.offset,
				CapturedAt:       now,
				// Anonymize secrets at capture so redaction trickles into every
				// derived view. Only the secret value is masked — reasoning,
				// structure, and JSON validity are preserved. ByteOffset still
				// points at the original file position (provenance); the tail
				// cursor advances over the original bytes, not the redacted copy.
				Raw: redact.Bytes(ln.bytes),
			})
		}
		res.LinesStored = len(lines)
		cursor = &store.RawCursor{
			AgentName:  agent,
			SourcePath: path,
			ByteOffset: newOffset,
			SessionID:  sessionID,
		}
	}

	// 2. Store the raw hook payload verbatim (skip genuinely empty payloads —
	//    nothing to preserve, and some agents fire hooks with no stdin).
	if len(bytes.TrimSpace(payload)) > 0 {
		rows = append(rows, &ops.RawTrace{
			AgentName:        agent,
			SessionID:        sessionID,
			AgentSessionID:   agentSessionID,
			Source:           ops.RawSourceHookPayload,
			WorkingDirectory: hp.Cwd,
			HookType:         hookType,
			CapturedAt:       now,
			Raw:              redact.Bytes(payload),
		})
		res.HookStored = true
	}

	if len(rows) == 0 && cursor == nil {
		return res, nil
	}
	if err := s.AppendRawTraces(ctx, rows, cursor); err != nil {
		return nil, err
	}

	// Re-derive and persist this session's spans whenever new transcript lines
	// landed — and also on a session-completion firing, so the skill-only
	// retention gate below can judge the finished session even if this firing
	// brought no new lines. Spans are a regenerable projection stored alongside
	// raw (parity + later deriver improvements); a derive failure must never
	// fail capture.
	completion := isSessionCompletionHook(hookType)
	if res.LinesStored > 0 || completion {
		n, hasSkill, derr := persistSpans(ctx, s, sessionID, agent)
		res.SpansStored = n
		res.SpanError = derr

		// Skill-only retention: Quiver keeps skill-attributed sessions, not
		// generic transcripts. When a session completes with no skill usage,
		// drop it whole (raw + spans + cursor). Only sessions whose agent has a
		// deriver are eligible — for an agent we can't yet derive, absence of a
		// skill span is unprovable, so we never delete its data.
		// derr == nil guards against a failed derivation: a query/persist error
		// yields hasSkill==false without a clean read, so acting on it would
		// delete a session we never actually proved skill-free.
		if completion && !hasSkill && derr == nil {
			if _, ok := derive.Get(agent); ok {
				if _, perr := s.DeleteSession(ctx, sessionID); perr == nil {
					res.Pruned = true
					res.SpansStored = 0
				}
				// A prune failure is non-fatal; capture already succeeded.
			}
		}
	}
	return res, nil
}

// isSessionCompletionHook reports whether a hook event marks the end of an
// agent's response — the point at which the skill-only retention gate can judge
// whether the session used a skill. Both Claude Code and Codex fire "Stop".
func isSessionCompletionHook(hookType string) bool {
	return hookType == "Stop"
}

// Rederive regenerates one session's persisted spans from its stored raw rows.
// It is the backfill primitive behind `qvr audit rederive`: capture runs span
// derivation inline on new lines, but sessions captured before a deriver
// existed (or by an older deriver version) keep stale/empty spans until this
// replays the projection. It returns the span count and whether the session
// used any skill (so callers can apply the skill-only retention gate). It is a
// no-op that returns (0, false, nil) when the agent has no registered deriver —
// it never wipes spans in that case.
func Rederive(ctx context.Context, s Store, sessionID uuid.UUID, agent string) (int, bool, error) {
	return persistSpans(ctx, s, sessionID, agent)
}

// persistSpans re-derives the whole session from its stored raw rows and
// replaces the session's persisted spans with the result. Re-deriving the full
// session (not just the new lines) is what lets turns that span multiple hook
// firings resolve correctly; span ids are deterministic, so the replace is
// idempotent. It also reports whether any derived span is skill-attributed.
func persistSpans(ctx context.Context, s Store, sessionID uuid.UUID, agent string) (int, bool, error) {
	rows, err := s.QueryRawTraces(ctx, &store.RawTraceFilter{
		SessionID: &sessionID,
		Sources:   []string{ops.RawSourceTranscript},
	})
	if err != nil {
		return 0, false, err
	}
	spans, err := derive.DeriveSession(rows)
	if err != nil {
		// No registered deriver for this agent is not an error worth failing on.
		return 0, false, nil
	}
	// Promote full skill identity (registry/commit/version/hash) from the
	// project's qvr.lock onto skill-attributed spans before they're persisted,
	// so name collisions across registries/versions stay distinguishable (#146).
	derive.EnrichSkillIdentity(spans, rows)
	hasSkill := false
	for _, sp := range spans {
		if name, ok := sp.Attributes["skill.name"].(string); ok && name != "" {
			hasSkill = true
			break
		}
	}
	out := make([]*store.SpanRow, 0, len(spans))
	for _, sp := range spans {
		attrs, _ := json.Marshal(sp.Attributes)
		out = append(out, &store.SpanRow{
			SpanID:         sp.SpanID,
			TraceID:        sp.TraceID,
			ParentSpanID:   sp.ParentSpanID,
			SessionID:      sessionID,
			AgentName:      agent,
			Kind:           sp.Kind,
			Name:           sp.Name,
			StartMs:        sp.StartMs,
			EndMs:          sp.EndMs,
			Attributes:     string(attrs),
			DeriverVersion: derive.Version,
		})
	}
	return len(out), hasSkill, s.ReplaceSessionSpans(ctx, sessionID, out)
}

// line is one complete transcript line plus its start offset in the file.
type line struct {
	offset int64
	bytes  []byte
}

// tailTranscript reads from the stored cursor to EOF and returns every COMPLETE
// line (terminated by '\n'); a trailing partial line is left unconsumed for the
// next firing. The returned offset is where the next read should resume.
func tailTranscript(ctx context.Context, s Store, agent, path string) ([]line, int64, error) {
	offset, err := s.GetRawCursor(ctx, agent, path)
	if err != nil {
		return nil, 0, err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, offset, nil
		}
		return nil, 0, fmt.Errorf("rawtrace: stat transcript: %w", err)
	}
	// File shrank since last read → it was truncated or rotated. Start over so
	// we don't read from a stale offset into unrelated bytes.
	if info.Size() < offset {
		offset = 0
	}
	if info.Size() == offset {
		return nil, offset, nil // nothing new
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, 0, fmt.Errorf("rawtrace: open transcript: %w", err)
	}
	defer f.Close()

	if _, err := f.Seek(offset, 0); err != nil {
		return nil, 0, fmt.Errorf("rawtrace: seek transcript: %w", err)
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, 0, fmt.Errorf("rawtrace: read transcript: %w", err)
	}

	lastNL := bytes.LastIndexByte(data, '\n')
	if lastNL < 0 {
		// No complete line yet; leave everything for next time.
		return nil, offset, nil
	}
	complete := data[:lastNL+1]

	var lines []line
	start := offset
	for _, raw := range bytes.SplitAfter(complete, []byte{'\n'}) {
		if len(raw) == 0 {
			continue
		}
		trimmed := bytes.TrimRight(raw, "\n")
		if len(bytes.TrimSpace(trimmed)) > 0 {
			lines = append(lines, line{
				offset: start,
				bytes:  append([]byte(nil), trimmed...),
			})
		}
		start += int64(len(raw))
	}
	return lines, offset + int64(len(complete)), nil
}

// resolveTranscriptPath locates the agent's transcript file. The hook payload's
// own pointer wins (agent_transcript_path for a subagent's log, else
// transcript_path); otherwise we derive Claude Code's canonical path from
// cwd + session id. Returns "" when no transcript can be located.
func resolveTranscriptPath(agent string, hp *hookPayload, sessionID uuid.UUID) string {
	for _, raw := range []string{hp.AgentTranscriptPath, hp.TranscriptPath} {
		if raw == "" {
			continue
		}
		p := expandHome(raw)
		if fileExists(p) {
			return p
		}
	}

	// Claude Code: ~/.claude/projects/<cwd-with-slashes-as-dashes>/<session>.jsonl
	if agent == "claude-code" && hp.SessionID != "" {
		home, err := os.UserHomeDir()
		if err == nil {
			cwd := hp.Cwd
			if cwd == "" {
				cwd, _ = os.Getwd()
			}
			slug := strings.ReplaceAll(cwd, "/", "-")
			candidate := filepath.Join(home, ".claude", "projects", slug, hp.SessionID+".jsonl")
			if fileExists(candidate) {
				return candidate
			}
		}
	}
	_ = sessionID
	return ""
}

// resolveSession derives the canonical UUID used to correlate every row of a
// session, matching the existing adapters: a parseable session id is used
// directly; any other non-empty id is hashed deterministically; an absent id
// (after env fallback) yields a fresh random UUID.
func resolveSession(agent, raw string) (uuid.UUID, string) {
	if raw == "" {
		raw = sessionEnvOverride(agent)
	}
	if raw == "" {
		return uuid.New(), ""
	}
	if parsed, err := uuid.Parse(raw); err == nil {
		return parsed, raw
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(raw)), raw
}

// sessionEnvOverride mirrors the per-agent env overrides the adapters honour, so
// a hook fired without a session id on stdin (e.g. `codex exec`) still
// correlates to the right session.
func sessionEnvOverride(agent string) string {
	switch agent {
	case "codex":
		return os.Getenv("CODEX_SESSION_ID")
	case "claude-code":
		return os.Getenv("CLAUDE_SESSION_KEY")
	default:
		return ""
	}
}

func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[2:])
		}
	}
	return p
}

func fileExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && !info.IsDir()
}
