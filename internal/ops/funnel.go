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

	// BackfillSkill stamps a session's still-provisional events (those
	// bearing SkillPending) with a resolved skill name.
	BackfillSkill(ctx context.Context, sessionID uuid.UUID, skill string) (int64, error)
	// DeleteSession removes a session and its events (skill-less prune).
	DeleteSession(ctx context.Context, id uuid.UUID) (int64, error)
	// DeleteSkilllessSessions sweeps orphaned skill-less sessions older
	// than the cutoff (backstop for sessions that never emit a clean end).
	DeleteSkilllessSessions(ctx context.Context, olderThan time.Time) (int64, error)
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
	SelfAuditHookError = "hook_error"

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

	// Notify, when set, receives a short human-readable note for each
	// observable outcome (recorded-pending, attributed, pruned). The cmd
	// layer points it at stderr so a dropped/provisional event is no longer
	// invisible (issue #137). Nil → silent.
	Notify func(string)
}

// Funnel runs the five-stage pipeline for one hook invocation:
//
//  1. Parse    — adapter.ParseEvent(hookType, rawData)
//  2. Resolve  — attribute to a skill the event directly references (no drop)
//  3. Privacy  — checker.Evaluate + Apply (sensitive → strip; secret → redact)
//  4. Logging  — ApplyLoggingLevel trims per-agent caps
//  5. Persist  — session-carry attribution + UpsertSession + SaveEvent
//
// Attribution is session-level: every event is recorded (provisionally
// under SkillPending if its session has no skill yet); once any event in a
// session references a skill — including a skill path or `qvr read <skill>`
// mined out of a command_exec — the whole trace is back-filled to it. A
// session that ends without ever referencing a skill is pruned by default
// (noise), and kept only when ops.retain_skill_less_sessions is set (see
// PruneSkilllessSessions).
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
//   - nil on successful persist (including a provisional SkillPending row
//     and an intentional skill-less session prune).
//   - nil on intentional no-op (parse error or per-agent disabled); parse
//     errors are recorded via self_audit so they show up in `qvr audit`.
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

	// Stage 2: attribute this event to a skill it *directly* references —
	// a path inside an installed skill's directory, or an explicit skill
	// tool-call (event.SkillRef). Unlike before, a no-match is NOT dropped:
	// the event is recorded provisionally and attributed at the session
	// level in persistSessionAndEvent (the persistent carrier that survives
	// one-shot _hook processes). event.SkillName left empty here means
	// "this event references no skill on its own".
	if attr, ok := f.deps.Resolver.Attribute(event); ok {
		ensureFields(event, attr)
	} else if event.SkillRef != "" {
		ensureFields(event, f.deps.Resolver.AttributeByName(event.SkillRef))
	} else {
		ensureFields(event, Attribution{})
	}

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
	isNew := session == nil
	if isNew {
		session = NewSession(e.AgentName, e.AgentSessionID, f.deps.Clock())
		session.ID = e.SessionID
		session.WorkingDirectory = e.WorkingDirectory
	}

	// own is the skill this event references on its own (set by Stage 2),
	// if any. hadSkill records whether the session was already attributed
	// before this event — it gates the one-time back-fill of earlier rows.
	own := e.SkillName
	hadSkill := len(session.SkillsTouched) > 0
	if own != "" {
		session.AddSkillTouched(own)
	}

	// Skill-less pruning is the default: the audit surface tracks
	// skill-attributed activity, so a session that ends having touched no
	// installed skill is noise and is discarded at its end. This is only
	// correct because attribution now also fires on command_exec events (a
	// shell-first agent's `qvr read <skill>` / `cat .../skills/<skill>/…`),
	// so a genuine skill session is no longer misread as skill-less and
	// pruned — which was the real #138 failure. Opt out with
	// ops.retain_skill_less_sessions to keep everything under the sentinel.
	prune := PruneSkilllessSessions(f.deps.Config)

	// On any session end, opportunistically sweep orphaned skill-less
	// sessions (the backstop for agents that crash without a clean end).
	// Best-effort: a sweep failure must never fail event ingest.
	if prune && e.ActionType == ActionSessionEnd {
		_, _ = f.deps.Store.DeleteSkilllessSessions(ctx, f.deps.Clock().Add(-DefaultSkilllessSweep))
	}

	// Session-end pruning: a session that reaches its end without ever
	// referencing a skill is noise — discard it and its events rather than
	// recording the terminal event. (A still-open skill-less session lingers
	// provisionally until end-or-sweep, above.) Only when explicitly opted in.
	if prune && e.ActionType == ActionSessionEnd && len(session.SkillsTouched) == 0 {
		if !isNew {
			if _, err := f.deps.Store.DeleteSession(ctx, session.ID); err != nil {
				return fmt.Errorf("prune skill-less session: %w", err)
			}
		}
		f.notify(fmt.Sprintf("session ended with no skill referenced — discarded (%s)", e.AgentName))
		return nil
	}

	// Per-event skill_name is the session's primary (first-referenced)
	// skill, so the whole trace groups together; events seen before any
	// skill carry the pending sentinel (skill_name is NOT NULL) until
	// back-filled. skills_touched holds the full set, so a multi-skill
	// session still surfaces under every skill it touched.
	primary := SkillPending
	if len(session.SkillsTouched) > 0 {
		primary = session.SkillsTouched[0]
	}
	if e.SkillName != primary {
		// Carried/provisional attribution: keep the primary's name but
		// don't leave another skill's registry/commit/path stamped on it.
		e.SkillName = primary
		e.SkillRegistry, e.SkillCommit, e.SkillPath = "", "", ""
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

	// Back-fill: this event just gave the session its first skill — stamp
	// the earlier provisional rows so the entire trace carries it.
	if own != "" && !hadSkill {
		n, err := f.deps.Store.BackfillSkill(ctx, session.ID, primary)
		if err != nil {
			return fmt.Errorf("backfill skill: %w", err)
		}
		f.notify(fmt.Sprintf("attributed session to %q (back-filled %d earlier event(s))", primary, n))
	} else if primary == SkillPending {
		f.notify("recorded event under " + SkillPending + " — session has not referenced a skill yet")
	}
	return nil
}

// notify forwards a one-line observability note to the configured sink
// (stderr in production). No-op when Notify is nil.
func (f *Funnel) notify(msg string) {
	if f.deps.Notify != nil {
		f.deps.Notify(msg)
	}
}

// recordHookError appends a self_audit row. The caller returns nil on
// top of this (the drop is the intended behaviour) UNLESS writing the
// audit itself failed — in which case we bubble the error up so the
// caller can at least log it.
func (f *Funnel) recordHookError(ctx context.Context, msg string, details map[string]any) error {
	// Tag the row with the agent (the adapter's name) so `qvr audit status`
	// can count errors per agent. Unknown when no adapter is configured.
	actor := ""
	if f.deps.Adapter != nil {
		actor = f.deps.Adapter.Name()
	}
	entry := &SelfAuditEntry{
		ID:        uuid.New(),
		Timestamp: f.deps.Clock(),
		Action:    SelfAuditHookError,
		Actor:     actor,
		Result:    SelfAuditResultError,
		ErrorMsg:  msg,
		Details:   details,
	}
	if err := f.deps.Store.AppendSelfAudit(ctx, entry); err != nil {
		return fmt.Errorf("funnel: append self_audit: %w", err)
	}
	return nil
}
