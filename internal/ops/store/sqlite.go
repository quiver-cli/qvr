package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/astra-sh/qvr/internal/ops/store/migrations"
	"github.com/google/uuid"

	// Register the pure-Go "sqlite" driver with database/sql.
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
	// Used by read-only consumers (e.g. `qvr audit logs`).
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

	// SQLite + WAL still serialises writers; a single connection keeps
	// concurrent captures orderly without hitting "database is locked"
	// under load. Readers are fast enough that the queue is imperceptible.
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

// DeleteRawBefore sweeps raw rows captured before cutoff.
func (s *sqliteStore) DeleteRawBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM raw_traces WHERE captured_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, fmt.Errorf("store: delete raw before: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteSession removes a session's raw rows, derived spans, and tailing cursor
// in one tx. Returns the number of raw rows deleted.
func (s *sqliteStore) DeleteSession(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	id := sessionID.String()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: delete session tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.ExecContext(ctx, `DELETE FROM raw_traces WHERE session_id = ?`, id)
	if err != nil {
		return 0, fmt.Errorf("store: delete session raw: %w", err)
	}
	n, _ := res.RowsAffected()

	if _, err := tx.ExecContext(ctx, `DELETE FROM spans WHERE session_id = ?`, id); err != nil {
		return 0, fmt.Errorf("store: delete session spans: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_meta WHERE session_id = ?`, id); err != nil {
		return 0, fmt.Errorf("store: delete session meta: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM trace_cursors WHERE session_id = ?`, id); err != nil {
		return 0, fmt.Errorf("store: delete session cursor: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: delete session commit: %w", err)
	}
	return n, nil
}

// Stats reports raw-trace counts and DB size for `qvr audit db stats`.
func (s *sqliteStore) Stats(ctx context.Context) (*StoreStats, error) {
	var out StoreStats

	counts := map[string]*int64{
		"SELECT COUNT(*) FROM raw_traces":                   &out.RawTraceCount,
		"SELECT COUNT(DISTINCT session_id) FROM raw_traces": &out.SessionCount,
		"SELECT COUNT(*) FROM self_audits":                  &out.SelfAuditCount,
	}
	for q, dst := range counts {
		if err := s.db.QueryRowContext(ctx, q).Scan(dst); err != nil {
			return nil, fmt.Errorf("store: stats (%q): %w", q, err)
		}
	}

	var oldest, newest sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(captured_at), MAX(captured_at) FROM raw_traces`,
	).Scan(&oldest, &newest); err != nil {
		return nil, fmt.Errorf("store: stats (range): %w", err)
	}
	if oldest.Valid {
		if t, err := parseSQLiteTime(oldest.String); err == nil {
			out.OldestTrace = &t
		}
	}
	if newest.Valid {
		if t, err := parseSQLiteTime(newest.String); err == nil {
			out.NewestTrace = &t
		}
	}

	if info, err := os.Stat(s.path); err == nil {
		out.DBSizeBytes = info.Size()
	}
	return &out, nil
}

// parseSQLiteTime handles the formats modernc.org/sqlite emits when DATETIME
// values surface as strings via aggregates.
func parseSQLiteTime(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
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
