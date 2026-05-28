package cmd

import (
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/model"
)

// TestLoadScopedLocks_MutexFlags verifies --all and --global error out
// together rather than silently letting one win. The doctor/list/outdated
// commands all share this helper, so the contract has to be loud.
func TestLoadScopedLocks_MutexFlags(t *testing.T) {
	if _, err := loadScopedLocks(t.TempDir(), true, true); err == nil {
		t.Fatal("expected error when both --global and --all set")
	}
}

// TestLoadScopedLocks_Scopes checks the three valid combinations return
// the expected scope labels in order.
func TestLoadScopedLocks_Scopes(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()

	cases := []struct {
		name           string
		global, all    bool
		wantScopeOrder []string
	}{
		{"project only", false, false, []string{"project"}},
		{"global only", true, false, []string{"global"}},
		{"all unions both", false, true, []string{"project", "global"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locks, err := loadScopedLocks(project, tc.global, tc.all)
			if err != nil {
				t.Fatalf("loadScopedLocks: %v", err)
			}
			if len(locks) != len(tc.wantScopeOrder) {
				t.Fatalf("len = %d, want %d", len(locks), len(tc.wantScopeOrder))
			}
			for i, want := range tc.wantScopeOrder {
				if locks[i].Scope != want {
					t.Errorf("[%d] scope = %q, want %q", i, locks[i].Scope, want)
				}
			}
		})
	}
}

// TestFindEntryAcrossLocks_AmbiguousIsError verifies that an entry present
// in both project and global locks under --all reports an actionable error
// rather than silently picking one — disable/enable would mutate the wrong
// scope otherwise.
func TestFindEntryAcrossLocks_AmbiguousIsError(t *testing.T) {
	dir := t.TempDir()
	project := model.NewLockFile(filepath.Join(dir, "project.lock"))
	project.Put(&model.LockEntry{Name: "tdd"})
	global := model.NewLockFile(filepath.Join(dir, "global.lock"))
	global.Put(&model.LockEntry{Name: "tdd"})

	locks := []scopedLock{
		{Scope: "project", Lock: project},
		{Scope: "global", Lock: global},
	}
	_, _, err := findEntryAcrossLocks("tdd", locks)
	if err == nil {
		t.Fatal("expected ambiguity error, got nil")
	}
}

// TestFindEntryAcrossLocks_FoundInOne returns the entry and correct scope
// when only one lock has it.
func TestFindEntryAcrossLocks_FoundInOne(t *testing.T) {
	dir := t.TempDir()
	project := model.NewLockFile(filepath.Join(dir, "project.lock"))
	global := model.NewLockFile(filepath.Join(dir, "global.lock"))
	global.Put(&model.LockEntry{Name: "tdd", Ref: "v2"})

	locks := []scopedLock{
		{Scope: "project", Lock: project},
		{Scope: "global", Lock: global},
	}
	entry, scope, err := findEntryAcrossLocks("tdd", locks)
	if err != nil {
		t.Fatalf("findEntryAcrossLocks: %v", err)
	}
	if entry.Ref != "v2" {
		t.Errorf("ref = %q, want v2", entry.Ref)
	}
	if scope.Scope != "global" {
		t.Errorf("scope = %q, want global", scope.Scope)
	}
}
