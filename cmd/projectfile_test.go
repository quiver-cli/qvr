package cmd

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
)

// setupProjectFileTest stands up an isolated quiver home + project cwd, seeds a
// config default target, registers a registry named "acme" backed by a bare
// repo containing one skill ("code-review"), and resets the package-level flags
// the add/sync/remove/edit commands read. Returns the project root and the
// registry name. The qvr.toml coordinate for the seeded skill is
// "acme/code-review".
func setupProjectFileTest(t *testing.T) (project, registryName string) {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project = t.TempDir()
	t.Chdir(project)
	resetPrinter(t)

	addTargets, addGlobal, addForce, addFrozen, addNoScan, addAs, addAll, addLocal = nil, false, false, false, true, "", false, ""
	syncGlobal, syncDryRun, syncNoScan, syncLocked, syncFrozen, syncCheck = false, false, true, false, false, false
	removeGlobal, removeForce = false, false
	t.Cleanup(func() {
		addTargets, addGlobal, addForce, addFrozen, addNoScan, addAs, addAll, addLocal = nil, false, false, false, false, "", false, ""
		syncGlobal, syncDryRun, syncNoScan, syncLocked, syncFrozen, syncCheck = false, false, false, false, false, false
		removeGlobal, removeForce = false, false
	})

	remote := seedImportRemote(t, "code-review")
	mgr := newRegistryManager(git.NewGoGitClient())
	if _, err := mgr.Add(context.Background(), "acme", remote); err != nil {
		t.Fatalf("register registry: %v", err)
	}
	return project, "acme"
}

func readProj(t *testing.T, project string) *model.ProjectFile {
	t.Helper()
	proj, err := model.ReadProjectFile(model.DefaultProjectPath(project))
	if err != nil {
		t.Fatalf("read qvr.toml: %v", err)
	}
	return proj
}

func TestAdd_DualWritesProjectFile(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	const coord = "acme/code-review"
	if got := readProj(t, project).Skills[coord]; got != "main" {
		t.Fatalf("qvr.toml [skills] = %+v, want %s=main", readProj(t, project).Skills, coord)
	}

	// Idempotent re-add (Force) leaves qvr.toml byte-identical (quiet diff).
	before, _ := os.ReadFile(model.DefaultProjectPath(project))
	addForce = true
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("re-add: %v", err)
	}
	after, _ := os.ReadFile(model.DefaultProjectPath(project))
	if !bytes.Equal(before, after) {
		t.Fatalf("re-add churned qvr.toml:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestAdd_GlobalWritesNoProjectFile(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	addGlobal = true
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add --global: %v", err)
	}
	if _, err := os.Stat(model.DefaultProjectPath(project)); !os.IsNotExist(err) {
		t.Fatalf("global add must not create a project qvr.toml (stat err: %v)", err)
	}
}

func TestRemove_DropsProjectFileEntry_AndSyncDoesNotResurrect(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	const coord = "acme/code-review"
	if _, ok := readProj(t, project).Skills[coord]; !ok {
		t.Fatalf("precondition: %s should be in qvr.toml", coord)
	}

	if err := runRemove(removeCmd, []string{"code-review"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, ok := readProj(t, project).Skills[coord]; ok {
		t.Fatalf("remove must drop %s from qvr.toml", coord)
	}

	// Regression guard: sync must NOT re-install the removed skill (case-C only
	// fires when qvr.toml still declares it).
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(project, config.Dir(), false))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if _, gerr := lock.Get("code-review"); gerr == nil {
		t.Fatal("sync resurrected a removed skill — qvr.toml/lock removal did not stick")
	}
}

func TestSync_SelfSufficient_WhenProjectFileAbsent(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Drop qvr.toml — sync must still reconcile from the lock alone, and (normal
	// mode) re-synthesise qvr.toml from the lock.
	if err := os.Remove(model.DefaultProjectPath(project)); err != nil {
		t.Fatalf("rm qvr.toml: %v", err)
	}
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync without qvr.toml: %v", err)
	}
	// The skill is still installed (lock-driven reconcile unaffected).
	lock, _ := model.ReadLockFile(model.DefaultLockPath(project, config.Dir(), false))
	if _, gerr := lock.Get("code-review"); gerr != nil {
		t.Fatalf("sync from lock alone lost the skill: %v", gerr)
	}
	// Case-D synthesis re-created qvr.toml from the lock.
	if got := readProj(t, project).Skills["acme/code-review"]; got != "main" {
		t.Fatalf("sync should synthesise qvr.toml from the lock; got %+v", readProj(t, project).Skills)
	}
	// ...and routing policy is reconstructed (not dropped) — default-targets is
	// the union of the consumed skills' targets, so a lost qvr.toml doesn't
	// silently degrade `qvr add` routing.
	if got := readProj(t, project).Project.DefaultTargets; len(got) != 1 || got[0] != "claude" {
		t.Fatalf("synthesis must reconstruct default-targets from the lock; got %v", got)
	}
}

// TestSync_Synthesis_ReconstructsRoutingPolicy reproduces the dropped-policy bug:
// a project routes to claude+codex, qvr.toml goes missing, and a plain sync must
// rebuild [project].default-targets from the union of the lock's per-skill
// targets rather than emitting an empty [project].
func TestSync_Synthesis_ReconstructsRoutingPolicy(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runTargetAdd(targetAddCmd, []string{"codex", "claude"}); err != nil {
		t.Fatalf("target add: %v", err)
	}
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	// Sanity: the install landed in both agents (per-skill targets carry the union).
	lock, _ := model.ReadLockFile(model.DefaultLockPath(project, config.Dir(), false))
	e, _ := lock.Get("code-review")
	if len(e.Targets) != 2 {
		t.Fatalf("precondition: skill should be installed into 2 targets, got %v", e.Targets)
	}

	if err := os.Remove(model.DefaultProjectPath(project)); err != nil {
		t.Fatalf("rm qvr.toml: %v", err)
	}
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}

	got := readProj(t, project).Project.DefaultTargets
	if len(got) != 2 || got[0] != "claude" || got[1] != "codex" {
		t.Fatalf("default-targets not reconstructed from lock union: got %v, want [claude codex]", got)
	}
	// And name/version are seeded (no empty [project]).
	if p := readProj(t, project).Project; p.Version != "0.1.0" || p.Name == "" {
		t.Fatalf("synthesis left [project] metadata empty: name=%q version=%q", p.Name, p.Version)
	}
}

func TestSync_Frozen_CreatesNoProjectFile(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := os.Remove(model.DefaultProjectPath(project)); err != nil {
		t.Fatalf("rm qvr.toml: %v", err)
	}
	syncFrozen = true
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync --frozen: %v", err)
	}
	if _, err := os.Stat(model.DefaultProjectPath(project)); !os.IsNotExist(err) {
		t.Fatalf("sync --frozen must not create qvr.toml (stat err: %v)", err)
	}
}

func TestSync_CaseC_InstallsSkillDeclaredOnlyInProjectFile(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}
	lockPath := model.DefaultLockPath(project, config.Dir(), false)

	// Simulate a hand-edit / merge where qvr.toml still declares the skill but
	// the lock lacks it (e.g. a teammate added it; you pulled their qvr.toml).
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if err := lock.Remove("code-review"); err != nil {
		t.Fatalf("remove from lock: %v", err)
	}
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}
	if _, ok := readProj(t, project).Skills["acme/code-review"]; !ok {
		t.Fatal("precondition: qvr.toml should still declare acme/code-review")
	}

	// Case C: sync resolves + installs the declared-but-unlocked skill.
	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync (case C): %v", err)
	}
	lock, _ = model.ReadLockFile(lockPath)
	if _, gerr := lock.Get("code-review"); gerr != nil {
		t.Fatalf("sync case-C did not install the qvr.toml-declared skill: %v", gerr)
	}
}

func TestSync_CaseB_LockWinsAndWarns(t *testing.T) {
	project, _ := setupProjectFileTest(t)
	if err := runAdd(addCmd, []string{"code-review"}); err != nil {
		t.Fatalf("add: %v", err)
	}

	// Hand-edit qvr.toml to a ref the lock disagrees with (a merge/edit divergence).
	proj := readProj(t, project)
	proj.PutSkill("acme/code-review", "v9")
	if err := proj.Write(); err != nil {
		t.Fatalf("edit qvr.toml: %v", err)
	}

	// Capture output to assert the warning prints and the summary is honest.
	out, errb := &bytes.Buffer{}, &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: out, Err: errb, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	if err := runSync(syncCmd, nil); err != nil {
		t.Fatalf("sync: %v", err)
	}
	combined := out.String() + errb.String()

	// Lock wins: the lock keeps its ref unchanged.
	lock, _ := model.ReadLockFile(model.DefaultLockPath(project, config.Dir(), false))
	e, _ := lock.Get("code-review")
	if e.Ref != "main" {
		t.Fatalf("case B must keep the lock ref; got %q, want main", e.Ref)
	}
	// ...and warns about the divergence.
	if !strings.Contains(combined, "qvr.toml requests") {
		t.Fatalf("case B must warn about the ref conflict; output:\n%s", combined)
	}
	// ...and does NOT falsely claim parity.
	if strings.Contains(combined, "Already in sync") {
		t.Fatalf("summary must not claim 'Already in sync' when a case-B conflict was surfaced; output:\n%s", combined)
	}
}

func TestSetProjectFileSkillRef_UpsertsAndGuardsCreate(t *testing.T) {
	dir := t.TempDir()
	projPath := model.DefaultProjectPath(dir)

	// No qvr.toml yet → a switch must NOT create one (adoption is add/sync's job).
	if err := setProjectFileSkillRef(projPath, "acme/x", "v1"); err != nil {
		t.Fatalf("upsert (absent): %v", err)
	}
	if _, err := os.Stat(projPath); !os.IsNotExist(err) {
		t.Fatal("setProjectFileSkillRef must not create qvr.toml when absent")
	}

	// With an existing file, it upserts the ref.
	proj := model.NewProjectFile(projPath)
	proj.PutSkill("acme/x", "v1")
	if err := proj.Write(); err != nil {
		t.Fatal(err)
	}
	if err := setProjectFileSkillRef(projPath, "acme/x", "v2"); err != nil {
		t.Fatalf("upsert (existing): %v", err)
	}
	if got := readProj(t, dir).Skills["acme/x"]; got != "v2" {
		t.Fatalf("ref = %q, want v2", got)
	}
}

func TestInit_ScaffoldsProjectFile(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	project := t.TempDir()
	t.Chdir(project)
	resetPrinter(t)
	initType, initTarget, initGlobal, initStandalone = "simple", "claude", false, false
	t.Cleanup(func() { initType, initTarget, initGlobal, initStandalone = "simple", "claude", false, false })

	if err := runInitProjectScoped("my-skill"); err != nil {
		t.Fatalf("init: %v", err)
	}

	raw, err := os.ReadFile(model.DefaultProjectPath(project))
	if err != nil {
		t.Fatalf("qvr.toml not scaffolded: %v", err)
	}
	if !strings.Contains(string(raw), "Reserved for future milestones") {
		t.Errorf("scaffold missing reserved-section banner:\n%s", raw)
	}
	proj := readProj(t, project)
	if proj.Project.Version != "0.1.0" {
		t.Errorf("version = %q, want 0.1.0", proj.Project.Version)
	}
	if len(proj.Project.DefaultTargets) != 1 || proj.Project.DefaultTargets[0] != "claude" {
		t.Errorf("default-targets = %v, want [claude]", proj.Project.DefaultTargets)
	}
	// A greenfield init scaffolds an edit-mode skill (no coordinate), so [skills]
	// stays empty.
	if len(proj.Skills) != 0 {
		t.Errorf("greenfield init should leave [skills] empty, got %+v", proj.Skills)
	}

	// Re-running init must not clobber an existing qvr.toml.
	before, _ := os.ReadFile(model.DefaultProjectPath(project))
	_ = scaffoldProjectFile(project)
	after, _ := os.ReadFile(model.DefaultProjectPath(project))
	if !bytes.Equal(before, after) {
		t.Error("scaffoldProjectFile clobbered an existing qvr.toml")
	}
}
