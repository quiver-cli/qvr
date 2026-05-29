package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// Issue #65 unit slice: driftExpected pulls the recorded SubtreeHash out
// of the VerifyEntryResult's structured Drift list so the rendered
// warning can show "expected X, on disk Y". A bug here means users see
// only the on-disk hash with no anchor, which is exactly the post-fix
// regression we want to guard.
func TestDriftExpected_PullsSubtreeHashFromDrift(t *testing.T) {
	r := skill.VerifyEntryResult{
		Name:        "demo",
		Status:      skill.VerifyStatusDrift,
		SubtreeHash: "sha256:on-disk",
		Drift: []skill.VerifyDriftItem{
			{Field: "subtreeHash", Expected: "sha256:expected", Actual: "sha256:on-disk"},
		},
	}
	got := driftExpected(r)
	if got != "sha256:expected" {
		t.Errorf("driftExpected = %q, want %q", got, "sha256:expected")
	}
}

func TestDriftExpected_EmptyOnNoSubtreeHashDrift(t *testing.T) {
	// A drift result with no subtreeHash field — defensive: function
	// should return "" rather than the first arbitrary drift entry.
	r := skill.VerifyEntryResult{
		Name:   "demo",
		Status: skill.VerifyStatusDrift,
		Drift: []skill.VerifyDriftItem{
			{Field: "commitSHA", Expected: "abc", Actual: "def"},
		},
	}
	if got := driftExpected(r); got != "" {
		t.Errorf("driftExpected with no subtreeHash drift = %q, want empty", got)
	}
}

// Regression for the CodeRabbit-flagged bug on autoRegisterRegistriesFromLock:
// `qvr sync --dry-run` used to call this helper unconditionally, which
// invoked config.Save and mutated ~/.quiver/config.yaml even though
// --dry-run's contract is "no filesystem changes." The fix threads
// dryRun through; in dry-run we still announce what would be registered
// on stderr but never persist.
func TestAutoRegisterRegistriesFromLock_DryRunDoesNotWriteConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	// Empty config baseline.
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed empty config: %v", err)
	}
	cfgPath := config.Path()
	originalBytes, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read seed config: %v", err)
	}

	withCapturingPrinter(t, "text")
	lock := model.NewLockFile(filepath.Join(t.TempDir(), model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "raks",
		Source:   "git@github.com:raks097/skills.git",
		Ref:      "main",
		Commit:   "abc1234",
	})

	autoRegisterRegistriesFromLock(lock, true /*dryRun*/)

	// Confirm the config file is byte-identical — no save call fired.
	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("reread config: %v", err)
	}
	if string(after) != string(originalBytes) {
		t.Errorf("dry-run mutated config.yaml\nbefore: %s\nafter:  %s", originalBytes, after)
	}
	// And the in-memory loaded config still has no raks entry.
	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := loaded.Registries["raks"]; ok {
		t.Errorf("dry-run leaked raks into the persisted config: %+v", loaded.Registries)
	}
}

// Companion: a real (non-dry-run) sync still persists the new
// registry. Without this we'd risk a regression where the dryRun gate
// accidentally short-circuits both paths.
func TestAutoRegisterRegistriesFromLock_RealRunWritesConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("QUIVER_HOME", home)
	if err := config.Save(&config.Config{}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	withCapturingPrinter(t, "text")
	lock := model.NewLockFile(filepath.Join(t.TempDir(), model.LockFileName))
	lock.Put(&model.LockEntry{
		Name:     "demo",
		Registry: "raks",
		Source:   "git@github.com:raks097/skills.git",
		Ref:      "main",
		Commit:   "abc1234",
	})

	autoRegisterRegistriesFromLock(lock, false /*dryRun*/)

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	got, ok := loaded.Registries["raks"]
	if !ok {
		t.Fatalf("real run did not persist raks registry: %+v", loaded.Registries)
	}
	if got.URL != "git@github.com:raks097/skills.git" {
		t.Errorf("persisted URL = %q, want git@github.com:raks097/skills.git", got.URL)
	}
}
