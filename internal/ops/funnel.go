package ops

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/privacy"
)

// SessionStore is the subset of the store package the funnel needs.
// Defining it locally lets us avoid an import cycle (internal/ops →
// internal/ops/store → internal/ops) and lets tests inject fakes.
type SessionStore interface {
	SaveEvent(ctx context.Context, e *Event) error
	GetSession(ctx context.Context, id uuid.UUID) (*Session, error)
	UpsertSession(ctx context.Context, s *Session) error
	AppendSelfAudit(ctx context.Context, entry *SelfAuditEntry) error
}

// SelfAuditEntry mirrors store.SelfAudit but stays in the ops package
// to keep the SessionStore interface import-cycle-free. The cmd layer
// converts between the two when wiring up the real store.
type SelfAuditEntry struct {
	ID        uuid.UUID
	Timestamp time.Time
	Action    string
	Actor     string
	Result    string
	ErrorMsg  string
	Details   map[string]any
}

// Self-audit action constants mirrored here so the funnel doesn't
// import internal/ops/store.
const (
	SelfAuditHookError        = "hook_error"
	SelfAuditUnattributedDrop = "unattributed_drop"

	SelfAuditResultSuccess = "success"
	SelfAuditResultError   = "error"
)

// FunnelDeps bundles everything the funnel needs. Explicitly passed
// rather than globalled so tests can swap each piece.
type FunnelDeps struct {
	Config   *config.Config
	Adapter  Adapter
	Resolver Resolver
	Privacy  privacy.Checker
	Store    SessionStore

	// Clock is the time source; defaults to time.Now when nil.
	Clock func() time.Time
}

// Funnel runs the five-stage pipeline for one hook invocation:
//
//  1. Parse    — adapter.ParseEvent(hookType, rawData)
//  2. Resolve  — resolver.Attribute(event) → drop if unattributed
//  3. Privacy  — checker.Evaluate + Apply (sensitive → strip; secret → redact)
//  4. Logging  — ApplyLoggingLevel trims per-agent caps
//  5. Persist  — store.UpsertSession + SaveEvent
//
// The return value is nil on successful persist, a drop (intentional
// no-op recorded via self_audit), or any error that genuinely prevented
// a self_audit from being written.
type Funnel struct {
	deps FunnelDeps
}

// NewFunnel constructs a Funnel. Returns an error if mandatory deps
// are missing. Adapter is allowed to be nil here because the cmd layer
// resolves the adapter per-call via GetAdapter; pass nil to defer.
func NewFunnel(deps FunnelDeps) (*Funnel, error) {
	if deps.Config == nil {
		return nil, errors.New("funnel: Config required")
	}
	if deps.Store == nil {
		return nil, errors.New("funnel: Store required")
	}
	if deps.Resolver == nil {
		return nil, errors.New("funnel: Resolver required")
	}
	if deps.Privacy == nil {
		return nil, errors.New("funnel: Privacy required")
	}
	if deps.Clock == nil {
		deps.Clock = func() time.Time { return time.Now().UTC() }
	}
	return &Funnel{deps: deps}, nil
}

// Ingest runs the full pipeline for one raw hook invocation. Returns:
//   - nil on successful persist.
//   - nil on intentional drop (unattributed event, parse error, or
//     per-agent disabled); the drop is recorded via self_audit so it
//     shows up in `qvr ops audit`.
//   - a non-nil error only when the pipeline itself broke (e.g. the
//     store write failed). Callers usually log to stderr and exit
//     non-zero.
//
// Callers SHOULD check Enabled() before calling Ingest to keep the
// disabled path allocation-free.
func (f *Funnel) Ingest(ctx context.Context, hookType string, rawData []byte) error {
	adapter := f.deps.Adapter
	if adapter == nil {
		return f.recordHookError(ctx, "no adapter configured for funnel", nil)
	}

	// Stage 1: parse.
	event, err := adapter.ParseEvent(ctx, hookType, rawData)
	if err != nil {
		return f.recordHookError(ctx, fmt.Sprintf("parse (%s): %v", adapter.Name(), err), map[string]any{
			"adapter":   adapter.Name(),
			"hook_type": hookType,
		})
	}
	if event == nil {
		return f.recordHookError(ctx, "adapter returned nil event", nil)
	}
	// Per-agent disable check. The funnel still runs the parse so
	// the adapter can validate, but persistence is suppressed. This
	// mirrors the "ops.enabled=false" silent no-op semantics.
	if !EnabledForAgent(f.deps.Config, event.AgentName) {
		return nil
	}

	// Stage 2: resolve.
	attr, ok := f.deps.Resolver.Attribute(event)
	if !ok {
		return f.recordUnattributed(ctx, event)
	}
	ensureFields(event, attr)

	// Stage 3: privacy. Evaluate returns a Decision; Apply mutates
	// the event according to it. IsSensitive mirrors to the event
	// so persistence records the bit.
	decision := f.deps.Privacy.Evaluate(event)
	event.IsSensitive = decision.IsSensitive
	privacy.Apply(event, decision)

	// Stage 4: logging-level truncation. Operates post-privacy so
	// stripped sensitive events incur zero cost here.
	ApplyLoggingLevel(event, AgentLoggingLevel(f.deps.Config, event.AgentName), LoggingCaps{
		StdoutMaxChars:  f.deps.Config.Ops.Logging.StdoutMaxChars,
		StderrMaxChars:  f.deps.Config.Ops.Logging.StderrMaxChars,
		ContextMaxChars: f.deps.Config.Ops.Logging.ContextMaxChars,
		ContentHash:     f.deps.Config.Ops.Logging.ContentHash,
	})

	// Stage 5: session update + persist. We upsert the session row
	// first so SaveEvent's FK to sessions(id) is satisfied.
	if err := f.persistSessionAndEvent(ctx, event); err != nil {
		return fmt.Errorf("funnel: persist: %w", err)
	}
	return nil
}

// persistSessionAndEvent does the session counter bump + event insert.
// It is two Exec calls, not one tx, because SessionStore intentionally
// exposes both as independent methods (the store package wraps them
// in SetMaxOpenConns(1) serialisation instead of an explicit BEGIN).
// If the session upsert fails, we abort before SaveEvent to keep the
// FK happy.
func (f *Funnel) persistSessionAndEvent(ctx context.Context, e *Event) error {
	session, err := f.deps.Store.GetSession(ctx, e.SessionID)
	if err != nil {
		return fmt.Errorf("get session: %w", err)
	}
	if session == nil {
		session = NewSession(e.AgentName, e.AgentSessionID, f.deps.Clock())
		session.ID = e.SessionID
		session.WorkingDirectory = e.WorkingDirectory
	}
	session.RecordEvent(e)
	if e.ActionType == ActionSessionEnd && session.EndedAt == nil {
		ended := f.deps.Clock()
		session.EndedAt = &ended
	}
	if err := f.deps.Store.UpsertSession(ctx, session); err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	if err := f.deps.Store.SaveEvent(ctx, e); err != nil {
		return fmt.Errorf("save event: %w", err)
	}
	return nil
}

// recordHookError appends a self_audit row. The caller returns nil on
// top of this (the drop is the intended behaviour) UNLESS writing the
// audit itself failed — in which case we bubble the error up so the
// caller can at least log it.
func (f *Funnel) recordHookError(ctx context.Context, msg string, details map[string]any) error {
	entry := &SelfAuditEntry{
		ID:        uuid.New(),
		Timestamp: f.deps.Clock(),
		Action:    SelfAuditHookError,
		Result:    SelfAuditResultError,
		ErrorMsg:  msg,
		Details:   details,
	}
	if err := f.deps.Store.AppendSelfAudit(ctx, entry); err != nil {
		return fmt.Errorf("funnel: append self_audit: %w", err)
	}
	return nil
}

// recordUnattributed appends a self_audit for an event we couldn't
// attribute to any skill. The event itself is dropped.
func (f *Funnel) recordUnattributed(ctx context.Context, e *Event) error {
	entry := &SelfAuditEntry{
		ID:        uuid.New(),
		Timestamp: f.deps.Clock(),
		Action:    SelfAuditUnattributedDrop,
		Actor:     e.AgentName,
		Result:    SelfAuditResultSuccess, // drop is the correct behaviour
		Details: map[string]any{
			"action_type":      string(e.ActionType),
			"agent_session_id": e.AgentSessionID,
			"working_dir":      e.WorkingDirectory,
		},
	}
	if err := f.deps.Store.AppendSelfAudit(ctx, entry); err != nil {
		return fmt.Errorf("funnel: append self_audit: %w", err)
	}
	return nil
}
