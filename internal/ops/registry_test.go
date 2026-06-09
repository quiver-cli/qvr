package ops

import (
	"maps"
	"sync"
	"testing"
)

// fakeInstaller is a minimal HookInstaller for registry tests.
type fakeInstaller struct{ name string }

func (f *fakeInstaller) Name() string        { return f.name }
func (f *fakeInstaller) DisplayName() string { return f.name }
func (f *fakeInstaller) Detect() (DetectionResult, error) {
	return DetectionResult{}, nil
}
func (f *fakeInstaller) Install(InstallOptions) (InstallResult, error) {
	return InstallResult{}, nil
}
func (f *fakeInstaller) Uninstall(UninstallOptions) (UninstallResult, error) {
	return UninstallResult{}, nil
}
func (f *fakeInstaller) Status() (HookStatus, error) { return HookStatus{}, nil }

// snapshotInstallers resets the global registry between tests.
func snapshotInstallers(t *testing.T) func() {
	t.Helper()
	adapterMu.Lock()
	saved := make(map[string]HookInstaller, len(installers))
	maps.Copy(saved, installers)
	adapterMu.Unlock()
	return func() {
		adapterMu.Lock()
		defer adapterMu.Unlock()
		installers = saved
	}
}

func TestRegister_RoundTrip(t *testing.T) {
	t.Cleanup(snapshotInstallers(t))
	Register(&fakeInstaller{name: "fake"})
	got, ok := GetInstaller("fake")
	if !ok {
		t.Fatal("GetInstaller: not found")
	}
	if got.Name() != "fake" {
		t.Errorf("expected fake; got %q", got.Name())
	}
}

func TestRegister_NilIgnored(t *testing.T) {
	t.Cleanup(snapshotInstallers(t))
	Register(nil)
	for _, inst := range ListInstallers() {
		if inst.Name() == "" {
			t.Errorf("nil installer registered under empty name")
		}
	}
}

func TestRegister_ReplacesExisting(t *testing.T) {
	t.Cleanup(snapshotInstallers(t))
	Register(&fakeInstaller{name: "dup"})
	Register(&fakeInstaller{name: "dup"})
	count := 0
	for _, inst := range ListInstallers() {
		if inst.Name() == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected single registration; got %d", count)
	}
}

func TestListInstallers_SortedStable(t *testing.T) {
	t.Cleanup(snapshotInstallers(t))
	adapterMu.Lock()
	installers = map[string]HookInstaller{}
	adapterMu.Unlock()
	Register(&fakeInstaller{name: "zebra"})
	Register(&fakeInstaller{name: "alpha"})
	Register(&fakeInstaller{name: "mike"})
	got := ListInstallers()
	want := []string{"alpha", "mike", "zebra"}
	if len(got) != len(want) {
		t.Fatalf("len=%d want %d", len(got), len(want))
	}
	for i := range got {
		if got[i].Name() != want[i] {
			t.Errorf("sort drift: got %v at %d, want %v", got[i].Name(), i, want[i])
		}
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	t.Cleanup(snapshotInstallers(t))
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			Register(&fakeInstaller{name: "concurrent"})
			_, _ = GetInstaller("concurrent")
		}()
		go func() {
			defer wg.Done()
			_ = ListInstallers()
		}()
	}
	wg.Wait()
}
