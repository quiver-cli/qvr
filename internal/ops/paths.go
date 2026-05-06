package ops

import (
	"path/filepath"

	"github.com/raks097/quiver/internal/config"
)

// DBPath returns the SQLite database path. The user can override via
// config.Ops.DBPath; otherwise it sits at $QUIVER_HOME/skillops.db
// (the name from sprint-3a spec §150).
func DBPath(cfg *config.Config) string {
	if cfg != nil && cfg.Ops.DBPath != "" {
		return cfg.Ops.DBPath
	}
	return filepath.Join(config.Dir(), "skillops.db")
}
