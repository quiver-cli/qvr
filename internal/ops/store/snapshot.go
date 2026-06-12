package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// LockSnapshotRow is one (session, skill) identity frozen at first ingest —
// the proof enrichment established while the session's filesystem state
// (symlinks, locks) still matched run time. See migration 0005 for why.
type LockSnapshotRow struct {
	SkillName   string
	Registry    string
	Ref         string
	Commit      string
	SubtreeHash string
	Source      string
	Canonical   string
}

// PutLockSnapshots freezes identities for a session. Write-once per
// (session, skill): INSERT OR IGNORE keeps the FIRST ingest's proof even if a
// later pass re-runs with a moved lock.
func (s *sqliteStore) PutLockSnapshots(ctx context.Context, sessionID uuid.UUID, rows []*LockSnapshotRow) error {
	if len(rows) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: lock snapshot tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().UnixMilli()
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO session_lock_snapshot
			(session_id, skill_name, registry, ref, commit_sha, subtree_hash, source, canonical, snapped_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			sessionID.String(), r.SkillName, r.Registry, r.Ref, r.Commit, r.SubtreeHash, r.Source, r.Canonical, now); err != nil {
			return fmt.Errorf("store: put lock snapshot: %w", err)
		}
	}
	return tx.Commit()
}

// GetLockSnapshots returns a session's frozen identities keyed by skill name
// (empty map when none).
func (s *sqliteStore) GetLockSnapshots(ctx context.Context, sessionID uuid.UUID) (map[string]*LockSnapshotRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT skill_name, registry, ref, commit_sha, subtree_hash, source, canonical
		FROM session_lock_snapshot WHERE session_id = ?`, sessionID.String())
	if err != nil {
		return nil, fmt.Errorf("store: get lock snapshots: %w", err)
	}
	defer rows.Close()
	out := map[string]*LockSnapshotRow{}
	for rows.Next() {
		r := &LockSnapshotRow{}
		if err := rows.Scan(&r.SkillName, &r.Registry, &r.Ref, &r.Commit, &r.SubtreeHash, &r.Source, &r.Canonical); err != nil {
			return nil, fmt.Errorf("store: scan lock snapshot: %w", err)
		}
		out[r.SkillName] = r
	}
	return out, rows.Err()
}
