package ops

import (
	"fmt"
	"sort"
	"sync"
)

// adapterRegistry is the process-wide map of registered adapters.
// Guarded by a mutex so init() registration and runtime lookup are
// safe across goroutines (tests sometimes Register/Unregister during
// setup).
var (
	adapterMu sync.RWMutex
	adapters  = map[string]Adapter{}
)

// Register adds an adapter to the registry. If an adapter with the
// same Name already exists, it is replaced — this keeps test fixtures
// simple (register a fake, run, restore). Callers typically register
// from an init() function in the adapter's own subpackage.
func Register(a Adapter) {
	if a == nil {
		return
	}
	adapterMu.Lock()
	defer adapterMu.Unlock()
	adapters[a.Name()] = a
}

// Unregister removes an adapter by name. Primarily for test cleanup.
func Unregister(name string) {
	adapterMu.Lock()
	defer adapterMu.Unlock()
	delete(adapters, name)
}

// GetAdapter returns the adapter registered under name, or (nil,
// ErrUnknownAdapter) if none. The funnel wraps the error into a
// self_audit entry.
func GetAdapter(name string) (Adapter, error) {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	a, ok := adapters[name]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAdapter, name)
	}
	return a, nil
}

// ListAdapters returns the currently-registered adapter names in
// sorted order. Used by `qvr ops doctor` and similar diagnostics.
func ListAdapters() []string {
	adapterMu.RLock()
	defer adapterMu.RUnlock()
	out := make([]string, 0, len(adapters))
	for name := range adapters {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
