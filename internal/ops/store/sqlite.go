package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/raks097/quiver/internal/ops"
	"github.com/raks097/quiver/internal/ops/store/migrations"

	_ "modernc.org/sqlite"
)

// OpenOptions configures a new sqliteStore. Zero-value is valid: the
// DB is opened at the given path with the production pragma set.
type OpenOptions struct {
	// Path is the SQLite file. Required.
	Path string

	// BusyTimeoutMs — how long to wait before returning "database is
	// locked". Defaults to 5000ms.
	BusyTimeoutMs int

	// ReadOnly flips the connection string to immutable=1 + query_only.
	// Used by read-only consumers (e.g. `qvr ops logs`).
	ReadOnly bool
}

// Open returns a *sqliteStore. Applies migrations on first open.
func Open(ctx context.Context, opts OpenOptions) (Store, error) {
	if opts.Path == "" {
		return nil, errors.New("store: Path is required")
	}
	if opts.BusyTimeoutMs == 0 {
		opts.BusyTimeoutMs = 5000
	}

	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o755); err != nil {
		return nil, fmt.Errorf("store: mkdir: %w", err)
	}

	dsn := buildDSN(opts)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open: %w", err)
	}

	// SQLite + WAL still serialises writers; a single connection
	// keeps concurrent SaveEvents orderly without hitting "database
	// is locked" under load. Readers are fast enough that the queue
	// is imperceptible.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	if !opts.ReadOnly {
		if _, err := migrations.Apply(ctx, db); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("store: migrate: %w", err)
		}
	}

	return &sqliteStore{db: db, path: opts.Path}, nil
}

// buildDSN builds the modernc.org/sqlite connection string with the
// production pragma set: foreign keys enforced, WAL journal mode for
// concurrent readers, configurable busy timeout, and synchronous=normal
// (safe with WAL, faster than full).
func buildDSN(opts OpenOptions) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(wal)")
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", opts.BusyTimeoutMs))
	q.Add("_pragma", "synchronous(normal)")
	if opts.ReadOnly {
		q.Add("_pragma", "query_only(1)")
	}
	return "file:" + opts.Path + "?" + q.Encode()
}

// sqliteStore is the production Store impl.
type sqliteStore struct {
	db   *sql.DB
	path string

	closed bool
	mu     sync.Mutex
}

func (s *sqliteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}

// --- SaveEvent ---

const saveEventSQL = `INSERT INTO audit_events(
  id, session_id, agent_session_id, sequence, timestamp, duration_ms,
  agent_name, agent_version, working_directory,
  skill_name, skill_registry, skill_commit, skill_path,
  action_type, tool_name, result_status, error_message,
  payload, diff_content, raw_event, is_sensitive,
  subagent_id, subagent_type
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

func (s *sqliteStore) SaveEvent(ctx context.Context, e *ops.Event) error {
	if e == nil {
		return errors.New("store: nil event")
	}
	if e.ID == uuid.Nil {
		e.ID = uuid.New()
	}
	if e.SessionID == uuid.Nil {
		return errors.New("store: event missing session_id")
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	ts := e.Timestamp.UTC()

	_, err := s.db.ExecContext(ctx, saveEventSQL,
		e.ID.String(), e.SessionID.String(), nullableString(e.AgentSessionID),
		e.Sequence, ts, e.DurationMs,
		e.AgentName, nullableString(e.AgentVersion), nullableString(e.WorkingDirectory),
		e.SkillName, nullableString(e.SkillRegistry), nullableString(e.SkillCommit), nullableString(e.SkillPath),
		string(e.ActionType), nullableString(e.ToolName), string(e.ResultStatus), nullableString(e.ErrorMessage),
		nullableJSON(e.Payload), nullableString(e.DiffContent), nullableJSON(e.RawEvent), boolToInt(e.IsSensitive),
		nullableString(e.SubagentID), nullableString(e.SubagentType),
	)
	if err != nil {
		return fmt.Errorf("store: save event: %w", err)
	}
	return nil
}

// --- QueryEvents / StreamEvents ---

func (s *sqliteStore) QueryEvents(ctx context.Context, f *EventFilter) ([]*ops.Event, error) {
	where, args := f.build()
	limit := f.effectiveLimit()
	q := `SELECT ` + eventColumns + ` FROM audit_events ` + where +
		` ORDER BY timestamp DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: query events: %w", err)
	}
	defer rows.Close()

	var out []*ops.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *sqliteStore) StreamEvents(ctx context.Context, f *EventFilter, fn func(*ops.Event) error) error {
	// Stream via cursor pagination so we never load the whole table.
	// The caller passes a filter with no cursor; we re-enter with the
	// tail of each page.
	filter := *f
	if filter.Limit == 0 {
		filter.Limit = 500
	}
	for {
		page, err := s.QueryEvents(ctx, &filter)
		if err != nil {
			return err
		}
		for _, ev := range page {
			if err := fn(ev); err != nil {
				return err
			}
		}
		if len(page) < filter.Limit {
			return nil
		}
		tail := page[len(page)-1]
		filter.Cursor = &Cursor{Timestamp: tail.Timestamp, ID: tail.ID}
	}
}

func (s *sqliteStore) GetEventsBySession(ctx context.Context, sessionID uuid.UUID) ([]*ops.Event, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+eventColumns+` FROM audit_events WHERE session_id = ? ORDER BY sequence ASC`,
		sessionID.String(),
	)
	if err != nil {
		return nil, fmt.Errorf("store: events by session: %w", err)
	}
	defer rows.Close()

	var out []*ops.Event
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// --- Sessions ---

const upsertSessionSQL = `INSERT INTO sessions(
  id, agent_session_id, agent_name, started_at, ended_at,
  working_directory, project_name,
  total_actions, files_read, files_written, commands_executed,
  errors, sensitive_actions, blocked_actions, skills_touched
) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
  ended_at = excluded.ended_at,
  working_directory = excluded.working_directory,
  project_name = excluded.project_name,
  total_actions = excluded.total_actions,
  files_read = excluded.files_read,
  files_written = excluded.files_written,
  commands_executed = excluded.commands_executed,
  errors = excluded.errors,
  sensitive_actions = excluded.sensitive_actions,
  blocked_actions = excluded.blocked_actions,
  skills_touched = excluded.skills_touched`

func (s *sqliteStore) UpsertSession(ctx context.Context, sess *ops.Session) error {
	if sess == nil {
		return errors.New("store: nil session")
	}
	if sess.ID == uuid.Nil {
		return errors.New("store: session missing id")
	}
	_, err := s.db.ExecContext(ctx, upsertSessionSQL,
		sess.ID.String(), nullableString(sess.AgentSessionID), sess.AgentName,
		sess.StartedAt.UTC(), nullableTime(sess.EndedAt),
		nullableString(sess.WorkingDirectory), nullableString(sess.ProjectName),
		sess.TotalActions, sess.FilesRead, sess.FilesWritten, sess.CommandsExecuted,
		sess.Errors, sess.SensitiveActions, sess.BlockedActions,
		encodeSkillsTouched(sess.SkillsTouched),
	)
	if err != nil {
		return fmt.Errorf("store: upsert session: %w", err)
	}
	return nil
}

func (s *sqliteStore) GetSession(ctx context.Context, id uuid.UUID) (*ops.Session, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id.String(),
	)
	sess, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("store: get session: %w", err)
	}
	return sess, nil
}

func (s *sqliteStore) ListSessions(ctx context.Context, since, until *time.Time, limit int) ([]*ops.Session, error) {
	if limit <= 0 {
		limit = 50
	}
	var clauses []string
	var args []any
	if since != nil {
		clauses = append(clauses, "started_at >= ?")
		args = append(args, since.UTC())
	}
	if until != nil {
		clauses = append(clauses, "started_at <= ?")
		args = append(args, until.UTC())
	}
	where := ""
	if len(clauses) > 0 {
		where = "WHERE " + joinAnd(clauses)
	}
	args = append(args, limit)

	q := `SELECT ` + sessionColumns + ` FROM sessions ` + where +
		` ORDER BY started_at DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	var out []*ops.Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

func joinAnd(clauses []string) string {
	out := ""
	for i, c := range clauses {
		if i > 0 {
			out += " AND "
		}
		out += c
	}
	return out
}

// --- Skill versions ---

const upsertSkillVersionSQL = `INSERT INTO skill_versions(
  registry, name, commit_sha, branch, content_hash, first_seen_at
) VALUES (?,?,?,?,?,?)
ON CONFLICT(registry, name, commit_sha) DO NOTHING`

func (s *sqliteStore) UpsertSkillVersion(ctx context.Context, sv *ops.SkillVersion) error {
	if sv == nil {
		return errors.New("store: nil skill version")
	}
	if sv.FirstSeenAt.IsZero() {
		sv.FirstSeenAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, upsertSkillVersionSQL,
		sv.Registry, sv.Name, sv.Commit,
		nullableString(sv.Branch), nullableString(sv.ContentHash),
		sv.FirstSeenAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("store: upsert skill version: %w", err)
	}
	return nil
}

// --- Retention / self-audits / stats ---

func (s *sqliteStore) DeleteEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM audit_events WHERE timestamp < ?`, cutoff.UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("store: delete events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

const appendSelfAuditSQL = `INSERT INTO self_audits(
  id, timestamp, action, actor, result, error_msg, details
) VALUES (?,?,?,?,?,?,?)`

func (s *sqliteStore) AppendSelfAudit(ctx context.Context, entry *SelfAudit) error {
	if entry == nil {
		return errors.New("store: nil self audit")
	}
	if entry.ID == uuid.Nil {
		entry.ID = uuid.New()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}
	var details sql.NullString
	if len(entry.Details) > 0 {
		b, err := json.Marshal(entry.Details)
		if err != nil {
			return fmt.Errorf("store: marshal self audit details: %w", err)
		}
		details = sql.NullString{String: string(b), Valid: true}
	}
	_, err := s.db.ExecContext(ctx, appendSelfAuditSQL,
		entry.ID.String(), entry.Timestamp.UTC(),
		entry.Action, nullableString(entry.Actor),
		entry.Result, nullableString(entry.ErrorMsg),
		details,
	)
	if err != nil {
		return fmt.Errorf("store: append self audit: %w", err)
	}
	return nil
}

func (s *sqliteStore) Stats(ctx context.Context) (*StoreStats, error) {
	var out StoreStats

	rows := map[string]*int64{
		"SELECT COUNT(*) FROM audit_events":                        &out.EventCount,
		"SELECT COUNT(*) FROM sessions":                            &out.SessionCount,
		"SELECT COUNT(*) FROM skill_versions":                      &out.SkillVersionCount,
		"SELECT COUNT(*) FROM self_audits":                         &out.SelfAuditCount,
		"SELECT COUNT(*) FROM audit_events WHERE is_sensitive = 1": &out.SensitiveCount,
	}
	for q, dst := range rows {
		if err := s.db.QueryRowContext(ctx, q).Scan(dst); err != nil {
			return nil, fmt.Errorf("store: stats (%q): %w", q, err)
		}
	}

	// Oldest / newest event. SQLite's aggregate MIN/MAX over a
	// DATETIME column returns the underlying TEXT rather than
	// round-tripping to time.Time, so we scan as strings and parse.
	var oldest, newest sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(timestamp), MAX(timestamp) FROM audit_events`,
	).Scan(&oldest, &newest); err != nil {
		return nil, fmt.Errorf("store: stats (range): %w", err)
	}
	if oldest.Valid {
		if t, err := parseSQLiteTime(oldest.String); err == nil {
			out.OldestEvent = &t
		}
	}
	if newest.Valid {
		if t, err := parseSQLiteTime(newest.String); err == nil {
			out.NewestEvent = &t
		}
	}

	// DB size: stat the file. Not perfectly accurate under WAL (the
	// -wal file holds pending pages) but close enough for `db stats`.
	if info, err := os.Stat(s.path); err == nil {
		out.DBSizeBytes = info.Size()
	}

	return &out, nil
}

// parseSQLiteTime handles the formats modernc.org/sqlite emits when
// DATETIME values surface as strings via aggregates. Known formats:
//   - "2006-01-02 15:04:05.999999999-07:00"
//   - "2006-01-02T15:04:05.999999999Z07:00"
//   - "2006-01-02 15:04:05+00:00"
//
// We try RFC3339 first (the driver's default write format), falling back
// to the space-separated variant.
func parseSQLiteTime(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		// Go's default time.Time.String() — modernc.org/sqlite emits
		// this when a DATETIME column surfaces via an aggregate.
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised sqlite time %q", s)
}
