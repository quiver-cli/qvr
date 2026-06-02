// Package store is the SQLite-backed persistence layer for SkillOps.
// It is a thin seam over database/sql — no ORM, no codegen, no magic.
// Schema lives in migrations/; query logic lives in filter.go;
// row-scanning helpers live in scan.go.
package store

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
)

// Store is the persistence contract the SkillOps funnel depends on.
// Every method takes ctx so retention sweeps and long-running exports
// can be cancelled. Callers must not hold a returned event past the
// next Store call — scan buffers are reused.
type Store interface {
	// SaveEvent persists one event + its session update in one tx.
	// The caller is responsible for having called Evaluate on the
	// privacy checker first; the store never reads the raw payload
	// for classification, only for storage.
	SaveEvent(ctx context.Context, e *ops.Event) error

	// QueryEvents returns matching events ordered by (timestamp desc,
	// id desc). Use StreamEvents for large result sets.
	QueryEvents(ctx context.Context, f *EventFilter) ([]*ops.Event, error)

	// StreamEvents calls fn for each row; returns on first non-nil
	// error from fn or on iteration error. Used by export.
	StreamEvents(ctx context.Context, f *EventFilter, fn func(*ops.Event) error) error

	// GetEventsBySession returns every event in a session, ordered by
	// sequence ascending (chronological within-session).
	GetEventsBySession(ctx context.Context, sessionID uuid.UUID) ([]*ops.Event, error)

	// GetSession returns a single session by ID, or (nil, nil) if
	// not found.
	GetSession(ctx context.Context, id uuid.UUID) (*ops.Session, error)

	// UpsertSession writes or updates a session row by primary key.
	// Safe to call repeatedly; used by the funnel to bump counters.
	UpsertSession(ctx context.Context, s *ops.Session) error

	// ListSessions returns sessions whose started_at falls in
	// [since, until], optionally filtered to one agent (empty = all
	// agents). Nil bounds are ignored. Sorted descending.
	ListSessions(ctx context.Context, since, until *time.Time, agent string, limit int) ([]*ops.Session, error)

	// UpsertSkillVersion records (registry, name, commit) with a
	// first-seen timestamp. Subsequent upserts for the same triple
	// are no-ops — first_seen_at is immutable.
	UpsertSkillVersion(ctx context.Context, sv *ops.SkillVersion) error

	// DeleteEventsBefore sweeps events older than cutoff. Returns
	// the count deleted. Sessions are not touched — they remain as
	// summary records even after their events are purged.
	DeleteEventsBefore(ctx context.Context, cutoff time.Time) (int64, error)

	// BackfillSkill stamps a session's still-provisional events (those
	// bearing ops.SkillPending) with a resolved skill name. Returns the
	// number of rows updated.
	BackfillSkill(ctx context.Context, sessionID uuid.UUID, skill string) (int64, error)

	// DeleteSession removes a session and all of its events (used to
	// discard a session that ended without referencing any skill).
	// Returns the number of events deleted.
	DeleteSession(ctx context.Context, id uuid.UUID) (int64, error)

	// DeleteSkilllessSessions sweeps sessions that touched no skill and
	// started before olderThan, plus their events. Returns the number of
	// sessions deleted.
	DeleteSkilllessSessions(ctx context.Context, olderThan time.Time) (int64, error)

	// CountSessions returns the number of recorded sessions for an agent
	// (empty agent = all agents).
	CountSessions(ctx context.Context, agent string) (int64, error)

	// CountEvents returns the number of recorded events for an agent
	// (empty agent = all agents).
	CountEvents(ctx context.Context, agent string) (int64, error)

	// CountSelfAuditErrors returns the number of self_audit rows with an
	// error result for an agent (matched on the actor column; empty agent =
	// all agents). Surfaces hook parse/ingest failures in `qvr audit status`.
	CountSelfAuditErrors(ctx context.Context, agent string) (int64, error)

	// AppendSelfAudit records an internal-state event (hook_error,
	// unattributed_drop, purge, config_change). Returns nil on success
	// or the underlying DB/context error; callers that treat self-audit
	// writes as best-effort should check the error but may choose to
	// log-and-continue rather than propagate.
	AppendSelfAudit(ctx context.Context, entry *SelfAudit) error

	// Stats returns counts and DB size, suitable for `qvr ops db stats`.
	Stats(ctx context.Context) (*StoreStats, error)

	// Close releases the underlying database handle. Idempotent.
	Close() error
}

// EventFilter composes predicates for QueryEvents / StreamEvents. Nil
// or empty fields are ignored. See filter.go for the SQL translation.
type EventFilter struct {
	Since, Until    *time.Time
	Agents          []string
	Skills          []string
	Actions         []ops.ActionType
	Results         []ops.ResultStatus
	FilePatterns    []string
	CommandPatterns []string
	SessionID       *uuid.UUID
	IsSensitive     *bool
	Limit           int
	Cursor          *Cursor
}

// Cursor pages through (timestamp, id)-sorted results. Callers pass
// the tail value of one page to get the next.
type Cursor struct {
	Timestamp time.Time
	ID        uuid.UUID
}

// StoreStats summarises DB contents for diagnostics.
type StoreStats struct {
	EventCount        int64
	SessionCount      int64
	SkillVersionCount int64
	SelfAuditCount    int64
	SensitiveCount    int64
	DBSizeBytes       int64
	OldestEvent       *time.Time
	NewestEvent       *time.Time
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
	ActionHookError        = "hook_error"
	ActionUnattributedDrop = "unattributed_drop"
	ActionPurge            = "purge"
	ActionConfigChange     = "config_change"
	ActionAdapterInstall   = "adapter_install"
	ActionAdapterUninstall = "adapter_uninstall"
)

// Common self-audit result values.
const (
	ResultAudit_Success = "success"
	ResultAudit_Error   = "error"
)
