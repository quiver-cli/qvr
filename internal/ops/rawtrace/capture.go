// Package rawtrace is the lossless capture path for agent traces. Agents are
// the capture infrastructure: each one already persists its own session
// transcript/rollout on disk, and qvr ingests those files directly. Each ingest
// tails the file from the last byte offset and stores the new lines verbatim —
// nothing is parsed, typed, normalized, or truncated, so any downstream view
// (spans, attribution, dashboards) can be derived later without information
// loss.
//
// The mechanism is cursor-based transcript tailing: the byte offset consumed
// per file is persisted in SQLite (one atomic tx per ingest), so each pass
// resumes exactly where the last left off.
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

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// Store is the persistence surface capture needs (defined here, the consumer,
// per the project's interface-in-consumer convention).
type Store interface {
	GetRawCursor(ctx context.Context, agent, sourcePath string) (int64, error)
	AppendRawTraces(ctx context.Context, rows []*ops.RawTrace, cursor *store.RawCursor) error
	QueryRawTraces(ctx context.Context, f *store.RawTraceFilter) ([]*ops.RawTrace, error)
	ReplaceSessionDerivation(ctx context.Context, meta *store.SessionMetaRow, rows []*store.SpanRow) error
	DeleteSession(ctx context.Context, sessionID uuid.UUID) (int64, error)
	ReplaceSourceRawTraces(ctx context.Context, agent, sourcePath string, rows []*ops.RawTrace) error
	PutLockSnapshots(ctx context.Context, sessionID uuid.UUID, rows []*store.LockSnapshotRow) error
	GetLockSnapshots(ctx context.Context, sessionID uuid.UUID) (map[string]*store.LockSnapshotRow, error)
}

// Result reports what a single ingest stored, for diagnostics/tests.
type Result struct {
	SessionID      uuid.UUID
	TranscriptPath string
	LinesStored    int
	SpansStored    int
	SpanError      error // non-nil if span re-derivation failed (ingest still succeeded)
	Skipped        bool  // the skill gate proved the session skill-less; nothing was stored
}

// Rederive regenerates one session's persisted projection (unified session
// meta + spans) from its stored raw rows. It is the backfill primitive behind
// `qvr audit rederive`: ingest runs derivation inline on new lines, but
// sessions captured before a deriver existed (or by an older deriver version)
// keep stale/empty projections until this replays them. It returns the span
// count and whether the session used any skill. It is a no-op that returns
// (0, false, nil) when the agent has no registered deriver — it never wipes a
// projection in that case.
func Rederive(ctx context.Context, s Store, sessionID uuid.UUID) (int, bool, error) {
	return persistDerivation(ctx, s, sessionID)
}

// persistDerivation re-derives the whole session from its stored raw rows and
// replaces the session's persisted projection — its unified session_meta row
// and its spans — with the result. Re-deriving the full session (not just the
// new lines) is what lets turns that span multiple ingest passes resolve
// correctly; span ids are deterministic, so the replace is idempotent. It also
// reports whether any derived span is skill-attributed.
func persistDerivation(ctx context.Context, s Store, sessionID uuid.UUID) (int, bool, error) {
	rows, err := s.QueryRawTraces(ctx, &store.RawTraceFilter{
		SessionID: &sessionID,
		Sources:   []string{ops.RawSourceTranscript},
	})
	if err != nil {
		return 0, false, err
	}
	d, err := derive.DeriveSession(rows)
	if err != nil {
		// No registered deriver for this agent is not an error worth failing on.
		return 0, false, nil
	}
	if d == nil {
		return 0, false, nil
	}
	// Promote full skill identity (registry/commit/version/hash) from the
	// project's qvr.lock onto skill-attributed spans before they're persisted,
	// so name collisions across registries/versions stay distinguishable
	// (#146). The session's ingest-time snapshot (when one exists) pins
	// symlink-origin evidence to the resolution that was true at run time, so
	// a rederive after a version move doesn't rewrite history; the first
	// derivation that proves identity freezes it.
	snap := loadLockSnapshot(ctx, s, sessionID)
	derive.EnrichSkillIdentity(d.Spans, rows, snap)
	// Harvest on every pass: a skill first proven in a later continuation of
	// an already-snapshotted session still needs freezing. Write-once per
	// (session, skill) is enforced by the store's INSERT OR IGNORE.
	if h := derive.HarvestVerifiedIdentities(d.Spans); len(h) > 0 {
		_ = s.PutLockSnapshots(ctx, sessionID, snapshotRows(h)) // best-effort: a miss re-freezes next pass
	}
	hasSkill := hasSkillSpans(d.Spans)
	out := make([]*store.SpanRow, 0, len(d.Spans))
	for _, sp := range d.Spans {
		attrs, _ := json.Marshal(sp.Attributes)
		out = append(out, &store.SpanRow{
			SpanID:         sp.SpanID,
			TraceID:        sp.TraceID,
			ParentSpanID:   sp.ParentSpanID,
			SessionID:      sessionID,
			AgentName:      d.Meta.Agent,
			Kind:           sp.Kind,
			Name:           sp.Name,
			StartMs:        sp.StartMs,
			EndMs:          sp.EndMs,
			Attributes:     string(attrs),
			DeriverVersion: derive.Version,
		})
	}
	meta := &store.SessionMetaRow{
		SessionID:       sessionID,
		AgentName:       d.Meta.Agent,
		SourceSessionID: d.Meta.SourceSessionID,
		SourcePath:      d.Meta.SourcePath,
		WorkingDir:      d.Meta.WorkingDir,
		GitBranch:       d.Meta.GitBranch,
		Model:           d.Meta.Model,
		Title:           d.Meta.Title,
		StartedMs:       d.Meta.StartedMs,
		EndedMs:         d.Meta.EndedMs,
		Turns:           d.Meta.Turns,
		Tools:           d.Meta.Tools,
		Skills:          d.Meta.Skills,
		DeriverVersion:  derive.Version,
	}
	return len(out), hasSkill, s.ReplaceSessionDerivation(ctx, meta, out)
}

// loadLockSnapshot reads a session's frozen identities as the lock-entry map
// enrichment consumes. A read failure degrades to "no snapshot" (live
// resolution) rather than failing derivation.
func loadLockSnapshot(ctx context.Context, s Store, sessionID uuid.UUID) map[string]*model.LockEntry {
	rows, err := s.GetLockSnapshots(ctx, sessionID)
	if err != nil || len(rows) == 0 {
		return nil
	}
	out := make(map[string]*model.LockEntry, len(rows))
	for name, r := range rows {
		out[name] = &model.LockEntry{
			Name:        r.SkillName,
			Registry:    r.Registry,
			Ref:         r.Ref,
			Commit:      r.Commit,
			SubtreeHash: r.SubtreeHash,
			Source:      r.Source,
			Canonical:   r.Canonical,
		}
	}
	return out
}

// snapshotRows converts harvested identities into snapshot rows.
func snapshotRows(h map[string]*model.LockEntry) []*store.LockSnapshotRow {
	out := make([]*store.LockSnapshotRow, 0, len(h))
	for name, e := range h {
		out = append(out, &store.LockSnapshotRow{
			SkillName:   name,
			Registry:    e.Registry,
			Ref:         e.Ref,
			Commit:      e.Commit,
			SubtreeHash: e.SubtreeHash,
			Source:      e.Source,
			Canonical:   e.Canonical,
		})
	}
	return out
}

// hasSkillSpans reports whether any span carries a skill attribution — the
// signal the skill-only retention gate keys on.
func hasSkillSpans(spans []derive.Span) bool {
	for _, sp := range spans {
		if name, ok := sp.Attributes["skill.name"].(string); ok && name != "" {
			return true
		}
	}
	return false
}

// line is one complete transcript line plus its start offset in the file.
type line struct {
	offset int64
	bytes  []byte
}

// tailTranscript reads from the stored cursor to EOF and returns every COMPLETE
// line (terminated by '\n'); a trailing partial line is left unconsumed for the
// next pass. The returned offset is where the next read should resume.
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

// resolveSession derives the canonical UUID used to correlate every row of a
// session: a parseable session id is used directly; any other non-empty id is
// hashed deterministically; an absent id yields a fresh random UUID.
func resolveSession(raw string) (uuid.UUID, string) {
	if raw == "" {
		return uuid.New(), ""
	}
	if parsed, err := uuid.Parse(raw); err == nil {
		return parsed, raw
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(raw)), raw
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
