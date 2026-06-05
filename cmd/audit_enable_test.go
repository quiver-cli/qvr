package cmd

import (
	"context"
	"os"
	"testing"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/ops"
	"github.com/quiver-cli/qvr/internal/output"
)

// TestAuditEnable_CreatesDatabase pins #144: `qvr audit enable` must actually
// create + migrate skillops.db (as its help advertises), not defer schema
// creation to the first captured event.
func TestAuditEnable_CreatesDatabase(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())
	withCapturingPrinter(t, output.FormatText)

	if err := setAuditEnabled(context.Background(), true); err != nil {
		t.Fatalf("enable: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load cfg: %v", err)
	}
	if !cfg.Ops.Enabled {
		t.Error("ops.enabled should be true after enable")
	}
	dbPath := ops.DBPath(cfg)
	if _, err := os.Stat(dbPath); err != nil {
		t.Fatalf("skillops.db should exist at %s after enable: %v", dbPath, err)
	}
}

// TestAuditDisable_LeavesDatabase verifies disable does not create or require a
// database — it only flips config.
func TestAuditDisable_LeavesDatabase(t *testing.T) {
	t.Setenv("QVR_HOME", t.TempDir())
	withCapturingPrinter(t, output.FormatText)

	if err := setAuditEnabled(context.Background(), false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load cfg: %v", err)
	}
	if cfg.Ops.Enabled {
		t.Error("ops.enabled should be false after disable")
	}
	if _, err := os.Stat(ops.DBPath(cfg)); !os.IsNotExist(err) {
		t.Errorf("disable should not create a database, got stat err=%v", err)
	}
}
