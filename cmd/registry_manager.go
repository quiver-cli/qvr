package cmd

import (
	"context"
	"fmt"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/registry"
)

// newRegistryManager wires a Manager with the configured cache TTL applied.
// All cmd-layer callers should use this instead of registry.NewManager so the
// `cache.index_ttl` config setting actually takes effect on Index reads
// (issue #46). Unparseable / unset TTLs silently fall back to the default —
// surfacing a config error here would force every command to handle it,
// which would be noise for a knob with a sane default.
func newRegistryManager(gc git.GitClient) *registry.Manager {
	mgr := registry.NewManager(gc)
	if cfg, err := config.Load(); err == nil {
		if ttl, perr := config.ParseCacheTTL(cfg.Cache.IndexTTL); perr == nil {
			mgr.CacheTTL = ttl
		}
	}
	return mgr
}

// maybeRefreshRegistryForSkill best-effort fetches the registry that
// publishes canonicalName so the next Install / FindSkill sees a
// just-published ref. Non-fatal: network failures log a warning and the
// caller proceeds against the cached index so offline flows still resolve.
//
// Used by `qvr switch` and `qvr upgrade --to <ref>` to close the surprise
// where a tag pushed seconds ago is invisible until the user manually runs
// `qvr registry update`. `qvr upgrade` (no --to) has had this behaviour
// inline for a while; this helper unifies the three call sites. Issue #107.
func maybeRefreshRegistryForSkill(ctx context.Context, mgr *registry.Manager, canonicalName, op string) {
	loc, err := mgr.FindSkill(canonicalName)
	if err != nil {
		// Skill isn't locatable in the current registry set; let the
		// caller's Install / FindSkill surface the error itself with
		// the message it picks for the operation.
		return
	}
	if _, uerr := mgr.Update(ctx, loc.RegistryName); uerr != nil {
		printer.Warning(fmt.Sprintf("%s: refresh %s failed (%v); using cached index", op, loc.RegistryName, uerr))
	}
}

// refreshAllIndexes invalidates the cached index for every configured
// registry. The next read goes through Manager.Index, which rebuilds from
// the bare clone and writes a fresh cache file back. Used by `--refresh`
// on the read commands so users can force a local rebuild without going
// to the network (which `qvr registry update` would).
//
// Errors on individual invalidations are swallowed — the next read will
// rebuild regardless, so a failed delete just means the cache might still
// be served (one more time) before the rebuild fires from the TTL/commit
// pin path.
func refreshAllIndexes() {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	for name := range cfg.Registries {
		_ = registry.Invalidate(name)
	}
}
