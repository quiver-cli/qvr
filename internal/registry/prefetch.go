package registry

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/astra-sh/qvr/internal/config"
)

// DefaultPrefetchInterval is the throttle applied when prefetch.min_interval is
// unset: qvr won't launch a background refresh more than once per this window.
const DefaultPrefetchInterval = 30 * time.Minute

// prefetchChildEnv marks a process spawned by the prefetch path. The child runs
// `qvr registry update`; the marker stops it (or anything it shells out to) from
// recursively triggering another prefetch.
const prefetchChildEnv = "QVR_PREFETCH_CHILD"

// execPrefetch launches the detached background refresh. It's a package var so
// tests can substitute a recorder instead of spawning a real process.
var execPrefetch = spawnDetachedUpdate

// MaybePrefetch opportunistically launches a detached `qvr registry update` to
// warm registry caches ahead of the next command (#211). It is OFF by default
// and entirely best-effort: every gate that fails (disabled, running inside a
// prefetch child, no registries, throttled, another prefetch already holding
// the lock, or nothing actually stale) makes it a silent no-op. It never blocks
// the caller and never reports an error — a foreground command's behaviour and
// exit code are unaffected no matter what happens here.
func MaybePrefetch() {
	cfg, err := config.Load()
	if err != nil {
		return
	}
	maybePrefetch(cfg)
}

// maybePrefetch is the gate logic, split out so tests drive it with an explicit
// config (and the injected execPrefetch) without touching disk-loaded config.
func maybePrefetch(cfg *config.Config) {
	// A prefetch child must never spawn its own prefetch.
	if os.Getenv(prefetchChildEnv) != "" {
		return
	}
	if cfg == nil || !cfg.Prefetch.Enabled {
		return
	}
	if len(cfg.Registries) == 0 {
		return
	}

	stamp := prefetchStampPath()
	if recentlyPrefetched(stamp, prefetchInterval(cfg)) {
		return
	}

	// Single-flight: if another qvr is already prefetching, skip rather than
	// stampede. TryLock is non-blocking — we never wait. Ensure the cache dir
	// exists first so opening the lock file can't fail on a fresh home.
	_ = os.MkdirAll(prefetchCacheDir(), 0o755)
	fl := flock.New(prefetchLockPath())
	locked, err := fl.TryLock()
	if err != nil || !locked {
		return
	}
	defer func() { _ = fl.Unlock() }()

	// The throttle stamp is the rate limiter: a real `git fetch` is the only
	// way to learn whether a registry's remote moved, so there's nothing local
	// to pre-check — we just refresh at most once per interval. Stamp before
	// spawning so a second invocation in the same window throttles even while
	// the child is still running.
	_ = touchStamp(stamp)
	_ = execPrefetch()
}

// spawnDetachedUpdate starts `qvr registry update` as a detached child that
// outlives this process, with its streams silenced and the child marker set. We
// Start() and return immediately — never Wait — so the foreground command isn't
// blocked. The detach syscall is platform-specific (see prefetch_unix.go /
// prefetch_windows.go).
func spawnDetachedUpdate() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, "registry", "update") //nolint:gosec // fixed args, self-exec
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Env = append(os.Environ(), prefetchChildEnv+"=1")
	detachProcess(cmd)
	return cmd.Start()
}

func prefetchCacheDir() string {
	return filepath.Join(config.Dir(), "cache")
}

func prefetchStampPath() string {
	return filepath.Join(prefetchCacheDir(), "prefetch.stamp")
}

func prefetchLockPath() string {
	return filepath.Join(prefetchCacheDir(), "prefetch.lock")
}

// prefetchInterval is the throttle window: the configured min_interval when set
// and valid, otherwise DefaultPrefetchInterval.
func prefetchInterval(cfg *config.Config) time.Duration {
	raw := strings.TrimSpace(cfg.Prefetch.MinInterval)
	if raw == "" {
		return DefaultPrefetchInterval
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultPrefetchInterval
	}
	return d
}

// recentlyPrefetched reports whether the stamp file was touched within interval.
// A missing/unreadable stamp means "never prefetched" — not recent.
func recentlyPrefetched(path string, interval time.Duration) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return time.Since(info.ModTime()) < interval
}

// touchStamp records that a prefetch was launched now.
func touchStamp(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	now := time.Now()
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		return err
	}
	// Best-effort: ensure mtime reflects now even when the file already existed.
	return os.Chtimes(path, now, now)
}
