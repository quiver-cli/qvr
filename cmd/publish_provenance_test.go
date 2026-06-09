package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
)

// setupEjectedRegistrySkill installs code-review from a local bare registry
// ("acme"), ejects it to edit mode, and commits a local tweak so a tagged
// publish has something to push. Scanning is enabled in config so the publish
// gate records an attestation. Returns the project root.
func setupEjectedRegistrySkill(t *testing.T) string {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{
		DefaultTarget: "claude",
		Security:      config.SecurityConfig{ScanOnInstall: true, BlockSeverity: "critical"},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)
	resetPrinter(t)

	addTargets, addGlobal, addForce, addFrozen, addNoScan, addAs, addAll, addLocal = nil, false, false, false, true, "", false, ""
	t.Cleanup(func() {
		addTargets, addGlobal, addForce, addFrozen, addNoScan, addAs, addAll, addLocal = nil, false, false, false, false, "", false, ""
	})

	remote := seedImportRemote(t, "code-review")
	mgr := newRegistryManager(git.NewGoGitClient())
	if _, err := mgr.Add(context.Background(), "acme", remote); err != nil {
		t.Fatalf("register registry: %v", err)
	}
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := runEdit(editCmd, []string{"code-review"}); err != nil {
		t.Fatalf("edit: %v", err)
	}

	md := filepath.Join(project, ".claude", "skills", "code-review", "SKILL.md")
	f, err := os.OpenFile(md, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open SKILL.md: %v", err)
	}
	if _, err := f.WriteString("\nfork tweak\n"); err != nil {
		t.Fatalf("append SKILL.md: %v", err)
	}
	_ = f.Close()
	return project
}

func publishedEntry(t *testing.T, project string) *model.LockEntry {
	t.Helper()
	lock, err := model.ReadLockFile(filepath.Join(project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	e, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("get code-review: %v", err)
	}
	return e
}

// TestPublish_ForkMigrateTag_PreservesForkedFromAndScan is the #243 guard:
// `publish --fork --migrate --tag` graduates the entry back to consume mode
// via auto-uneject, and the rewritten entry must keep the fork provenance
// (forkedFrom, sourceUpstream) and the publish gate's scan attestation —
// pre-fix the auto-uneject re-install erased all three.
func TestPublish_ForkMigrateTag_PreservesForkedFromAndScan(t *testing.T) {
	project := setupEjectedRegistrySkill(t)

	forkDir := filepath.Join(t.TempDir(), "platform", "fork-skills.git")
	if err := os.MkdirAll(forkDir, 0o755); err != nil {
		t.Fatalf("mkdir fork: %v", err)
	}
	if _, err := gogit.PlainInit(forkDir, true); err != nil {
		t.Fatalf("init fork: %v", err)
	}

	resetPublishFlags(t)
	publishNoScan = false // let the gate run so a scan attestation is recorded
	publishFork = forkDir
	publishMigrate = true
	publishTag = "v0.1.0"
	publishMessage = "fork"
	publishAutoCommit = true
	publishCmd.SetContext(context.Background())

	if err := runPublish(publishCmd, []string{"code-review"}); err != nil {
		t.Fatalf("publish --fork --migrate --tag: %v", err)
	}

	e := publishedEntry(t, project)
	if e.IsEdit() {
		t.Errorf("entry still in edit mode — auto-uneject did not run; provenance restore untested")
	}
	if e.ForkedFrom == "" {
		t.Errorf("forkedFrom empty after --fork --migrate — lineage erased (#243)")
	}
	if e.SourceUpstream == "" {
		t.Errorf("sourceUpstream empty after --fork --migrate (#243)")
	}
	if e.Verification == nil || e.Verification.Scan == nil {
		t.Fatalf("verification.scan missing after tagged publish — provenance shows 'not recorded' (#243); entry: %+v", e)
	}
	if e.Verification.Scan.Decision != "allowed" {
		t.Errorf("scan decision = %q, want allowed", e.Verification.Scan.Decision)
	}
}

// TestPublish_SameRegistryTag_KeepsScanRecord covers the non-fork half of
// #243: a plain tagged publish (same-registry graduation) also re-installs
// the entry and must carry the gate's scan record forward.
func TestPublish_SameRegistryTag_KeepsScanRecord(t *testing.T) {
	project := setupEjectedRegistrySkill(t)

	resetPublishFlags(t)
	publishNoScan = false
	publishTag = "v0.2.0"
	publishMessage = "release"
	publishAutoCommit = true
	publishCmd.SetContext(context.Background())

	if err := runPublish(publishCmd, []string{"code-review"}); err != nil {
		t.Fatalf("publish --tag: %v", err)
	}

	e := publishedEntry(t, project)
	if e.IsEdit() {
		t.Errorf("entry still in edit mode — auto-uneject did not run")
	}
	if e.Verification == nil || e.Verification.Scan == nil {
		t.Fatalf("verification.scan missing after tagged publish (#243); entry: %+v", e)
	}
}
