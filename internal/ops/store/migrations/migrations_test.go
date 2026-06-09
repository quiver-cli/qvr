package migrations

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func openMem(t *testing.T) *sql.DB {
	t.Helper()
	// Each test gets its own file so WAL works and multiple Open()
	// calls see the same schema. In-memory would give each connection
	// a fresh db.
	path := filepath.Join(t.TempDir(), "m.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestLoad_ReturnsSortedMigrations(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(ms) == 0 {
		t.Fatalf("expected at least one migration")
	}
	for i := 1; i < len(ms); i++ {
		if ms[i-1].Version >= ms[i].Version {
			t.Errorf("migrations not sorted ascending: %d then %d", ms[i-1].Version, ms[i].Version)
		}
	}
}

func TestLoad_InitHasContent(t *testing.T) {
	ms, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(ms) == 0 {
		t.Fatalf("Load() returned no migrations; expected at least the init")
	}
	if ms[0].Version != 1 {
		t.Errorf("first migration should be version 1; got %d", ms[0].Version)
	}
	if len(ms[0].SQL) < 100 {
		t.Errorf("init migration looks empty; got %d bytes", len(ms[0].SQL))
	}
}

func TestApply_CreatesExpectedTables(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	ran, err := Apply(ctx, db)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(ran) == 0 {
		t.Fatalf("expected migrations to run")
	}

	wantTables := []string{"audit_events", "sessions", "skill_versions", "self_audits", "schema_migrations"}
	for _, name := range wantTables {
		var got string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
		).Scan(&got)
		if err == sql.ErrNoRows {
			t.Errorf("missing table %q", name)
			continue
		}
		if err != nil {
			t.Errorf("query for %q: %v", name, err)
		}
	}
}

func TestApply_Idempotent(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	first, err := Apply(ctx, db)
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	second, err := Apply(ctx, db)
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(first) == 0 {
		t.Errorf("first apply ran no migrations")
	}
	if len(second) != 0 {
		t.Errorf("second apply should be no-op; ran %d", len(second))
	}
}

func TestApply_RecordsVersion(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	if _, err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	ms, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if count != len(ms) {
		t.Errorf("expected %d version rows; got %d", len(ms), count)
	}
}

func TestApply_ExpectedIndexes(t *testing.T) {
	db := openMem(t)
	if _, err := Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"idx_events_skill_ts",
		"idx_events_action",
		"idx_events_sensitive",
		"idx_events_session_seq",
		"idx_events_ts",
		"idx_events_agent_ts",
		"idx_sessions_started",
		"idx_sessions_agent",
		"idx_self_audits_ts",
		"idx_self_audits_action",
	}
	for _, name := range want {
		var got string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name,
		).Scan(&got)
		if err == sql.ErrNoRows {
			t.Errorf("missing index %q", name)
		} else if err != nil {
			t.Errorf("query for index %q: %v", name, err)
		}
	}
}

func TestApply_ConcurrentSafe(t *testing.T) {
	// Production uses a single *sql.DB with MaxOpenConns=1, which
	// trivially serialises Apply() against itself inside a process.
	// This test exercises that shape: two goroutines sharing one DB.
	path := filepath.Join(t.TempDir(), "concurrent.db")
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(wal)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := Apply(context.Background(), db)
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent apply: %v", err)
		}
	}

	// Exactly N version rows, no duplicates (PK constraint would
	// have blown up earlier if we'd somehow inserted twice).
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	ms, err := Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if count != len(ms) {
		t.Errorf("expected %d version rows; got %d", len(ms), count)
	}
}

func TestApply_RollsBackOnError(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY, applied_at DATETIME NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	// Inject a migration that will fail: bad SQL inside the tx.
	// applyOne should roll back the whole migration, including the
	// schema_migrations insert.
	err := applyOne(ctx, db, Migration{
		Version: 999,
		Name:    "0999_bad",
		SQL:     `CREATE TABLE bad_one(x INTEGER); CREATE TABLE bad_one(x INTEGER);`, // duplicate
	})
	if err == nil {
		t.Fatalf("expected error from bad migration")
	}
	var ver int
	row := db.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = 999`)
	if err := row.Scan(&ver); err != nil {
		t.Fatal(err)
	}
	if ver != 0 {
		t.Errorf("bad migration recorded as applied; got %d rows", ver)
	}
}
