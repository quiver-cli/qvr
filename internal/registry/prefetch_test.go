package registry

import (
	"os"
	"testing"
	"time"

	"github.com/gofrs/flock"

	"github.com/astra-sh/qvr/internal/config"
)

// recordPrefetch swaps execPrefetch for a counter and restores it on cleanup.
// The returned func reports how many times a spawn was attempted. err is the
// value the stub returns (to exercise the never-fail path).
func recordPrefetch(t *testing.T, err error) func() int {
	t.Helper()
	var calls int
	old := execPrefetch
	execPrefetch = func() error {
		calls++
		return err
	}
	t.Cleanup(func() { execPrefetch = old })
	return func() int { return calls }
}

// enabledCfg is a prefetch-enabled config with one registry. The throttle stamp
// (not any local staleness signal) is the rate limiter, so no cache TTL setup is
// needed.
func enabledCfg() *config.Config {
	return &config.Config{
		Registries: map[string]config.RegistryConfig{"r1": {URL: "http://example.invalid/r1"}},
		Prefetch:   config.PrefetchConfig{Enabled: true},
	}
}

func TestPrefetch_DisabledByDefault(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	calls := recordPrefetch(t, nil)

	cfg := enabledCfg()
	cfg.Prefetch.Enabled = false // the default

	maybePrefetch(cfg)
	if got := calls(); got != 0 {
		t.Errorf("disabled prefetch spawned %d times, want 0", got)
	}
	if _, err := os.Stat(prefetchStampPath()); err == nil {
		t.Error("disabled prefetch wrote a stamp; it should be inert")
	}
}

func TestPrefetch_ChildEnvGuard(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	t.Setenv(prefetchChildEnv, "1")
	calls := recordPrefetch(t, nil)

	maybePrefetch(enabledCfg())
	if got := calls(); got != 0 {
		t.Errorf("prefetch ran inside a child (recursion guard failed): %d spawns", got)
	}
}

func TestPrefetch_NoRegistries(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	calls := recordPrefetch(t, nil)

	cfg := enabledCfg()
	cfg.Registries = nil

	maybePrefetch(cfg)
	if got := calls(); got != 0 {
		t.Errorf("prefetch spawned with no registries: %d", got)
	}
}

func TestPrefetch_ThrottleRespected(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	calls := recordPrefetch(t, nil)

	// First call on a fresh home: not recently prefetched → spawns once and
	// writes the stamp.
	maybePrefetch(enabledCfg())
	if got := calls(); got != 1 {
		t.Fatalf("first prefetch spawned %d times, want 1", got)
	}
	if _, err := os.Stat(prefetchStampPath()); err != nil {
		t.Fatalf("first prefetch did not write a stamp: %v", err)
	}

	// Second call immediately after: the fresh stamp throttles it.
	maybePrefetch(enabledCfg())
	if got := calls(); got != 1 {
		t.Errorf("prefetch ran again within the throttle window: %d spawns, want 1", got)
	}

	// Age the stamp past the throttle window → spawns again.
	old := time.Now().Add(-2 * DefaultPrefetchInterval)
	if err := os.Chtimes(prefetchStampPath(), old, old); err != nil {
		t.Fatalf("age stamp: %v", err)
	}
	maybePrefetch(enabledCfg())
	if got := calls(); got != 2 {
		t.Errorf("prefetch did not run after the throttle window expired: %d spawns, want 2", got)
	}
}

func TestPrefetch_SingleFlight(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	calls := recordPrefetch(t, nil)

	// Hold the prefetch lock as if another qvr were already refreshing.
	if err := os.MkdirAll(prefetchCacheDir(), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	held := flock.New(prefetchLockPath())
	locked, err := held.TryLock()
	if err != nil || !locked {
		t.Fatalf("could not acquire prefetch lock for test: locked=%v err=%v", locked, err)
	}
	defer func() { _ = held.Unlock() }()

	maybePrefetch(enabledCfg())
	if got := calls(); got != 0 {
		t.Errorf("prefetch ran while another holder had the lock: %d spawns, want 0", got)
	}
}

func TestPrefetch_NeverErrorsForeground(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	calls := recordPrefetch(t, os.ErrPermission) // spawn "fails"

	// Must not panic or propagate — maybePrefetch returns nothing and the
	// foreground (this test) continues normally.
	maybePrefetch(enabledCfg())
	if got := calls(); got != 1 {
		t.Errorf("expected one (failed) spawn attempt, got %d", got)
	}
}

func TestPrefetch_CustomMinInterval(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	calls := recordPrefetch(t, nil)

	cfg := enabledCfg()
	cfg.Prefetch.MinInterval = "1h"

	// First call spawns and stamps.
	maybePrefetch(cfg)
	if got := calls(); got != 1 {
		t.Fatalf("first prefetch spawned %d times, want 1", got)
	}

	// Age the stamp by 30m — still inside the 1h window → throttled.
	t30 := time.Now().Add(-30 * time.Minute)
	if err := os.Chtimes(prefetchStampPath(), t30, t30); err != nil {
		t.Fatalf("age stamp: %v", err)
	}
	maybePrefetch(cfg)
	if got := calls(); got != 1 {
		t.Errorf("prefetch ignored custom min_interval (1h): %d spawns, want 1", got)
	}

	// Age past 1h → spawns again.
	t90 := time.Now().Add(-90 * time.Minute)
	if err := os.Chtimes(prefetchStampPath(), t90, t90); err != nil {
		t.Fatalf("age stamp: %v", err)
	}
	maybePrefetch(cfg)
	if got := calls(); got != 2 {
		t.Errorf("prefetch did not honor custom min_interval expiry: %d spawns, want 2", got)
	}
}
