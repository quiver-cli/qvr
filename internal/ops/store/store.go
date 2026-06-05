// Package store is the SQLite-backed persistence layer for SkillOps.
// It is a thin seam over database/sql — no ORM, no codegen, no magic.
// Schema lives in migrations/; raw read/write logic lives in raw.go and
// raw_sessions.go; row-scanning helpers live in scan.go.
//
// The store is raw-only: capture writes verbatim transcript lines and hook
// payloads, and every read surface is derived from those rows (or from the
// derive layer). There is no normalized event/session table.
package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/quiver-cli/qvr/internal/ops"
)

// Store is the raw-only persistence contract. Every method takes ctx so
// sweeps and long-running exports can be cancelled.
type Store interface {
	// --- Capture (canonical write path) ---

	// AppendRawTraces stores verbatim agent output (transcript lines and/or
	// hook payloads) and advances the per-file tailing cursor in one tx.
	AppendRawTraces(ctx context.Context, rows []*ops.RawTrace, cursor *RawCursor) error

	// GetRawCursor returns the byte offset capture last consumed for a
	// transcript file, or 0 if it has never been tailed.
	GetRawCursor(ctx context.Context, agent, sourcePath string) (int64, error)

	// --- Reads over raw rows ---

	// QueryRawTraces returns raw rows ordered by (session_id, seq) ascending.
	QueryRawTraces(ctx context.Context, f *RawTraceFilter) ([]*ops.RawTrace, error)

	// StreamRawTraces calls fn per matching row, ordered by (session_id, seq).
	StreamRawTraces(ctx context.Context, f *RawTraceFilter, fn func(*ops.RawTrace) error) error

	// ListRawSessions returns per-session summaries derived from raw rows,
	// newest-first by first-seen time.
	ListRawSessions(ctx context.Context, f *RawSessionFilter) ([]*RawSession, error)

	// CountRawSessions / CountRawTraces count distinct sessions / rows,
	// optionally scoped to working dirs and/or one agent (empty = all).
	CountRawSessions(ctx context.Context, dirs []string, agent string) (int64, error)
	CountRawTraces(ctx context.Context, dirs []string, agent string) (int64, error)

	// LatestRawAt returns the newest capture time for an agent, or nil.
	LatestRawAt(ctx context.Context, agent string) (*time.Time, error)

	// --- Derived spans (a regenerable projection persisted for parity) ---

	// ReplaceSessionSpans atomically replaces a session's stored spans with
	// the given rows (the result of re-deriving that session).
	ReplaceSessionSpans(ctx context.Context, sessionID uuid.UUID, rows []*SpanRow) error

	// QuerySpans returns stored spans ordered by (session_id, start_ms).
	QuerySpans(ctx context.Context, f *SpanFilter) ([]*SpanRow, error)

	// SkillsForSessions returns the distinct skill.name values attributed to
	// each given session (from its SKILL-attributed spans), keyed by session id
	// string. Sessions with no skill span are absent from the map.
	SkillsForSessions(ctx context.Context, ids []string) (map[string][]string, error)

	// DeleteRawBefore sweeps raw rows captured before cutoff. Returns the
	// number of rows deleted.
	DeleteRawBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// DeleteSession removes a whole session — its raw rows, derived spans, and
	// tailing cursor — in one tx. Returns the number of raw rows deleted. Used
	// by the skill-only retention policy to drop sessions with no skill usage;
	// deleting the cursor means a session that later resumes re-tails from the
	// start and is re-captured in full.
	DeleteSession(ctx context.Context, sessionID uuid.UUID) (int64, error)

	// --- Self-audit (install/uninstall provenance + status) ---

	// CountSelfAuditErrors returns the number of self_audit rows with an
	// error result for an agent (empty agent = all agents).
	CountSelfAuditErrors(ctx context.Context, agent string) (int64, error)

	// AppendSelfAudit records an internal-state event (install/uninstall,
	// config_change, purge).
	AppendSelfAudit(ctx context.Context, entry *SelfAudit) error

	// Stats returns counts and DB size, suitable for `qvr audit db stats`.
	Stats(ctx context.Context) (*StoreStats, error)

	// Close releases the underlying database handle. Idempotent.
	Close() error
}

// StoreStats summarises DB contents for diagnostics.
type StoreStats struct {
	RawTraceCount  int64
	SessionCount   int64
	SelfAuditCount int64
	DBSizeBytes    int64
	OldestTrace    *time.Time
	NewestTrace    *time.Time
}

// SelfAudit is the internal-audit row written by AppendSelfAudit.
type SelfAudit struct {
	ID        uuid.UUID
	Timestamp time.Time
	Action    string
	Actor     string
	Result    string
	ErrorMsg  string
	Details   map[string]any
}

// Common self-audit actions. Kept as constants so callers don't typo.
const (
	ActionAdapterInstall   = "adapter_install"
	ActionAdapterUninstall = "adapter_uninstall"
)

// Common self-audit result values.
const (
	ResultAudit_Success = "success"
	ResultAudit_Error   = "error"
)
