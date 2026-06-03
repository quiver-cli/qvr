package ops

import "sync"

// installers is the process-wide map of registered hook installers, keyed by
// agent name. In the raw-only model the registry's sole purpose is hook
// installation (`qvr audit install-hooks`) and status — capture no longer
// parses, so there is no per-agent parser to register. Guarded by a mutex so
// init() registration and runtime lookup are safe across goroutines.
var (
	adapterMu  sync.RWMutex
	installers = map[string]HookInstaller{}
)

// Register adds a hook installer to the registry. If one with the same Name
// already exists it is replaced — keeps test fixtures simple. Per-agent
// packages call this from an init() function.
func Register(h HookInstaller) {
	if h == nil {
		return
	}
	adapterMu.Lock()
	defer adapterMu.Unlock()
	installers[h.Name()] = h
}
