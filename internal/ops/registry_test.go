package ops

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type fakeAdapter struct{ name string }

func (f *fakeAdapter) Name() string { return f.name }
func (f *fakeAdapter) ParseEvent(context.Context, string, []byte) (*Event, error) {
	return nil, nil
}

// Helper to reset the global registry between tests. Returns a
// restore func to call in t.Cleanup.
func snapshotAdapters(t *testing.T) func() {
	t.Helper()
	adapterMu.Lock()
	saved := make(map[string]Adapter, len(adapters))
	for k, v := range adapters {
		saved[k] = v
	}
	adapterMu.Unlock()
	return func() {
		adapterMu.Lock()
		defer adapterMu.Unlock()
		adapters = saved
	}
}

func TestRegister_RoundTrip(t *testing.T) {
	t.Cleanup(snapshotAdapters(t))
	a := &fakeAdapter{name: "fake"}
	Register(a)
	got, err := GetAdapter("fake")
	if err != nil {
		t.Fatalf("GetAdapter: %v", err)
	}
	if got.Name() != "fake" {
		t.Errorf("expected fake; got %q", got.Name())
	}
}

func TestRegister_NilIgnored(t *testing.T) {
	t.Cleanup(snapshotAdapters(t))
	Register(nil)
	list := ListAdapters()
	for _, n := range list {
		if n == "" {
			t.Errorf("nil adapter registered under empty name")
		}
	}
}

func TestRegister_ReplacesExisting(t *testing.T) {
	t.Cleanup(snapshotAdapters(t))
	Register(&fakeAdapter{name: "dup"})
	Register(&fakeAdapter{name: "dup"})
	list := ListAdapters()
	count := 0
	for _, n := range list {
		if n == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected single registration; got %d", count)
	}
}

func TestUnregister(t *testing.T) {
	t.Cleanup(snapshotAdapters(t))
	Register(&fakeAdapter{name: "ephemeral"})
	Unregister("ephemeral")
	_, err := GetAdapter("ephemeral")
	if !errors.Is(err, ErrUnknownAdapter) {
		t.Errorf("expected ErrUnknownAdapter; got %v", err)
	}
}

func TestGetAdapter_UnknownReturnsSentinel(t *testing.T) {
	t.Cleanup(snapshotAdapters(t))
	_, err := GetAdapter("totally-not-a-real-adapter-xyz")
	if !errors.Is(err, ErrUnknownAdapter) {
		t.Errorf("expected ErrUnknownAdapter; got %v", err)
	}
}

func TestListAdapters_SortedStable(t *testing.T) {
	t.Cleanup(snapshotAdapters(t))
	// Reset to a known set.
	adapterMu.Lock()
	adapters = map[string]Adapter{}
	adapterMu.Unlock()
	Register(&fakeAdapter{name: "zebra"})
	Register(&fakeAdapter{name: "alpha"})
	Register(&fakeAdapter{name: "mike"})
	got := ListAdapters()
	want := []string{"alpha", "mike", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d; got %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("sort drift: got %v, want %v", got, want)
		}
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	t.Cleanup(snapshotAdapters(t))
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			Register(&fakeAdapter{name: "concurrent"})
			_, _ = GetAdapter("concurrent")
		}(i)
		go func(i int) {
			defer wg.Done()
			_ = ListAdapters()
		}(i)
	}
	wg.Wait()
}
