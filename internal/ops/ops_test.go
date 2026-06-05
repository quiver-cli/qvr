package ops

import (
	"testing"

	"github.com/quiver-cli/qvr/internal/config"
)

func TestEnabled_NilConfigReturnsFalse(t *testing.T) {
	if Enabled(nil) {
		t.Errorf("nil config should be disabled")
	}
}

func TestEnabled_ReflectsGlobalFlag(t *testing.T) {
	cfg := &config.Config{}
	if Enabled(cfg) {
		t.Errorf("default zero config should be disabled")
	}
	cfg.Ops.Enabled = true
	if !Enabled(cfg) {
		t.Errorf("enabled flag should flip Enabled()")
	}
}

func TestDBPath_DefaultLocation(t *testing.T) {
	t.Setenv("QUIVER_HOME", "/tmp/test-quiver-home")
	got := DBPath(nil)
	want := "/tmp/test-quiver-home/skillops.db"
	if got != want {
		t.Errorf("DBPath=%q want %q", got, want)
	}
}

func TestDBPath_Override(t *testing.T) {
	cfg := &config.Config{}
	cfg.Ops.DBPath = "/custom/path/ops.db"
	if got := DBPath(cfg); got != "/custom/path/ops.db" {
		t.Errorf("override ignored; got %q", got)
	}
}
