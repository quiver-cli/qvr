package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/raks097/quiver/internal/config"
)

// DefaultCacheTTL is how long an index cache entry is considered fresh before
// the next read triggers a local rebuild from the bare clone. The rebuild does
// not touch the network — it only re-walks the existing bare repo at HEAD; an
// explicit `qvr registry update` is still required to pull new commits from
// upstream. Freshness is measured against the cache's embedded `Generated`
// field (set on WriteCache), not the file's mtime, so a `touch` does not
// expire the cache.
const DefaultCacheTTL = time.Hour

// ErrCacheMiss signals the cache file for a registry does not exist.
var ErrCacheMiss = errors.New("cache miss")

// IndexCache is the on-disk cache of a registry's *index* — the catalog of
// skills the registry offers (names, descriptions, paths, refs), derived by
// walking the bare clone at HEAD. It holds no skill files; the bare clone
// remains the source of truth. Persisted as JSON per registry at
// ~/.quiver/cache/index/<name>.json.
type IndexCache struct {
	Registry  string            `json:"registry"`
	Commit    string            `json:"commit"`
	Generated time.Time         `json:"generated"`
	Skills    []SkillIndexEntry `json:"skills"`
	Skipped   []SkippedSkill    `json:"skipped,omitempty"`
}

// IsStale reports whether the cache is older than ttl.
func (c *IndexCache) IsStale(ttl time.Duration) bool {
	return time.Since(c.Generated) > ttl
}

// CacheDir returns the directory holding index caches.
func CacheDir() string {
	return filepath.Join(config.Dir(), "cache", "index")
}

// CachePath returns the path to the cache file for a registry.
func CachePath(name string) string {
	return filepath.Join(CacheDir(), name+".json")
}

// ReadCache loads the cache for a registry. Returns ErrCacheMiss if the file
// does not exist.
func ReadCache(name string) (*IndexCache, error) {
	data, err := os.ReadFile(CachePath(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrCacheMiss
		}
		return nil, fmt.Errorf("read cache %s: %w", name, err)
	}
	var c IndexCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse cache %s: %w", name, err)
	}
	return &c, nil
}

// WriteCache persists the cache using an atomic tmp+rename, so a concurrent
// ReadCache never sees a half-written file.
func WriteCache(c *IndexCache) error {
	if c == nil || c.Registry == "" {
		return errors.New("cache: registry name required")
	}
	if err := os.MkdirAll(CacheDir(), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	final := CachePath(c.Registry)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write cache tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename cache: %w", err)
	}
	return nil
}

// Invalidate removes the cache file for a registry. No-op if absent.
func Invalidate(name string) error {
	err := os.Remove(CachePath(name))
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("invalidate cache %s: %w", name, err)
	}
	return nil
}
