package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Scan-ledger statuses. The ledger records the outcome of looking at one
// native session-store file, so an unchanged file is never re-examined.
const (
	ScanStatusIngested = "ingested"
	ScanStatusSkipped  = "skipped_no_skill"
	ScanStatusError    = "error"
)

// ScannedFile is one row of the discovery scan ledger: the stat fingerprint
// (size, mtime) of a native session-store file the last time a scan looked at
// it, plus what the scan decided. Keyed by file, not session, so it survives
// DeleteSession — a skipped skill-less file must not re-derive every scan.
type ScannedFile struct {
	AgentName  string    `json:"agent_name"`
	SourcePath string    `json:"source_path"`
	Size       int64     `json:"size"`
	MtimeMs    int64     `json:"mtime_ms"`
	Status     string    `json:"status"` // ScanStatusIngested | ScanStatusSkipped | ScanStatusError
	SessionID  uuid.UUID `json:"session_id,omitempty"`
	ScannedAt  time.Time `json:"scanned_at"`
}

// GetScannedFiles returns the whole ledger for one agent, keyed by source
// path. One query per agent keeps a scan over hundreds of files cheap.
func (s *sqliteStore) GetScannedFiles(ctx context.Context, agent string) (map[string]*ScannedFile, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT agent_name, source_path, size, mtime_ms, status, session_id, scanned_at
		   FROM scanned_files WHERE agent_name = ?`, agent)
	if err != nil {
		return nil, fmt.Errorf("store: get scanned files: %w", err)
	}
	defer rows.Close()

	out := map[string]*ScannedFile{}
	for rows.Next() {
		var (
			f   ScannedFile
			sid sql.NullString
		)
		if err := rows.Scan(&f.AgentName, &f.SourcePath, &f.Size, &f.MtimeMs,
			&f.Status, &sid, &f.ScannedAt); err != nil {
			return nil, fmt.Errorf("store: scan scanned file: %w", err)
		}
		if sid.Valid && sid.String != "" {
			if id, perr := uuid.Parse(sid.String); perr == nil {
				f.SessionID = id
			}
		}
		f.ScannedAt = f.ScannedAt.UTC()
		out[f.SourcePath] = &f
	}
	return out, rows.Err()
}

// UpsertScannedFile records (or refreshes) one file's ledger row.
func (s *sqliteStore) UpsertScannedFile(ctx context.Context, f *ScannedFile) error {
	if f == nil {
		return fmt.Errorf("store: nil scanned file")
	}
	if f.ScannedAt.IsZero() {
		f.ScannedAt = time.Now().UTC()
	}
	var sid sql.NullString
	if f.SessionID != uuid.Nil {
		sid = sql.NullString{String: f.SessionID.String(), Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO scanned_files(
		   agent_name, source_path, size, mtime_ms, status, session_id, scanned_at
		 ) VALUES (?,?,?,?,?,?,?)`,
		f.AgentName, f.SourcePath, f.Size, f.MtimeMs, f.Status, sid, f.ScannedAt.UTC())
	if err != nil {
		return fmt.Errorf("store: upsert scanned file: %w", err)
	}
	return nil
}

// DeleteRawBySourcePath removes every raw row ingested from one source file.
// Returns the number of raw rows deleted.
func (s *sqliteStore) DeleteRawBySourcePath(ctx context.Context, agent, sourcePath string) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM raw_traces WHERE agent_name = ? AND source_path = ?`,
		agent, sourcePath)
	if err != nil {
		return 0, fmt.Errorf("store: delete raw by source: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
