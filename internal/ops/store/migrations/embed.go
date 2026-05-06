// Package migrations embeds the SkillOps SQL migrations and applies
// them against a *sql.DB inside a single transaction per migration.
// Migrations are numbered; the applied set is tracked in
// schema_migrations.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed *.sql
var migrationsFS embed.FS

// namePattern is the required filename format: NNNN_description.sql.
// The leading integer is the migration version.
var namePattern = regexp.MustCompile(`^(\d+)_[A-Za-z0-9_\-]+\.sql$`)

// Migration represents one numbered SQL file.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// Load reads every *.sql file under the embedded migrations/ directory,
// validates the filename format, and returns them sorted by Version.
func Load() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationsFS, ".")
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}

	var out []Migration
	seen := map[int]string{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		m := namePattern.FindStringSubmatch(name)
		if m == nil {
			return nil, fmt.Errorf("migration %q: bad filename (want NNNN_desc.sql)", name)
		}
		v, err := strconv.Atoi(m[1])
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version: %w", name, err)
		}
		if prev, ok := seen[v]; ok {
			return nil, fmt.Errorf("migration version %d appears twice: %q and %q", v, prev, name)
		}
		seen[v] = name

		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", name, err)
		}
		out = append(out, Migration{
			Version: v,
			Name:    strings.TrimSuffix(name, ".sql"),
			SQL:     string(body),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Apply runs every migration whose version is greater than the current
// schema_migrations high-water mark. Each migration runs in its own
// transaction; a failure rolls that migration back and halts the apply.
//
// Applying the same set twice is a no-op. Concurrent callers are safe
// because the version check + insert happens inside the same tx as the
// SQL body, and SQLite serialises writers.
func Apply(ctx context.Context, db *sql.DB) ([]Migration, error) {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at DATETIME NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return nil, err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	rows.Close()

	migrations, err := Load()
	if err != nil {
		return nil, err
	}

	var ran []Migration
	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := applyOne(ctx, db, m); err != nil {
			return ran, fmt.Errorf("apply %s: %w", m.Name, err)
		}
		ran = append(ran, m)
	}
	return ran, nil
}

func applyOne(ctx context.Context, db *sql.DB, m Migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Raced-apply guard: another goroutine/process may have applied
	// this version while we were waiting for the write lock. Re-check
	// inside the tx. If already applied, the migration body is
	// idempotent (every CREATE uses IF NOT EXISTS) so re-running is
	// safe — we just skip the version-row insert.
	var exists int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.Version,
	).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return tx.Commit()
	}

	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
		m.Version, time.Now().UTC(),
	); err != nil {
		return err
	}
	return tx.Commit()
}
