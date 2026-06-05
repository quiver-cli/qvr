package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
)

// seedImportRemote stands up a bare git repo containing a single skill at
// `skills/<name>/SKILL.md` on the `main` branch. Returns the bare repo path so
// the test can use it as a clone URL.
//
// This mirrors internal/skill/helpers_test.go's seedRemote but lives in
// package cmd so we don't have to re-export the test helper from a separate
// package.
func seedImportRemote(t *testing.T, name string) string {
	t.Helper()
	remote := filepath.Join(t.TempDir(), name+"-remote.git")
	if _, err := gogit.PlainInit(remote, true); err != nil {
		t.Fatalf("init remote: %v", err)
	}

	seed := t.TempDir()
	sr, err := gogit.PlainInit(seed, false)
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if _, err := sr.CreateRemote(&gogitcfg.RemoteConfig{Name: "origin", URLs: []string{remote}}); err != nil {
		t.Fatalf("create remote: %v", err)
	}
	wt, err := sr.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	skillDir := filepath.Join(seed, "skills", name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: import-test fixture\n---\n# " + name + "\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if _, err := wt.Add(filepath.Join("skills", name, "SKILL.md")); err != nil {
		t.Fatalf("git add: %v", err)
	}
	if _, err := wt.Commit("seed", &gogit.CommitOptions{
		Author: &object.Signature{Name: "T", Email: "t@t", When: time.Now()},
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	head, err := sr.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if err := sr.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"), head.Hash(),
	)); err != nil {
		t.Fatalf("set main: %v", err)
	}
	if err := sr.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gogitcfg.RefSpec{"refs/heads/main:refs/heads/main"},
	}); err != nil {
		t.Fatalf("push: %v", err)
	}
	rr, err := gogit.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	if err := rr.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main"),
	)); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	return remote
}

func resetImportFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		importGlobal = false
		importForce = false
		importFrozen = false
		importNoScan = false
		importTargets = nil
	})
	importGlobal = false
	importForce = false
	importFrozen = false
	// Always disable scans for import tests — the scanner needs a configured
	// LLM provider that isn't available in unit tests, and the import path
	// under test runs the same gate add does (covered by add's tests).
	importNoScan = true
	importTargets = nil
}

func setupImportProject(t *testing.T) string {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	// Seed a config so default_target is set — without it the import command
	// errors out before reaching the install loop.
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	project := t.TempDir()
	t.Chdir(project)
	return project
}

func TestRunImport_RegistersUnknownURLAndInstallsSkill(t *testing.T) {
	project := setupImportProject(t)
	resetImportFlags(t)

	remote := seedImportRemote(t, "code-review")
	manifestPath := filepath.Join(project, "skills.txt")
	if err := os.WriteFile(manifestPath, []byte(remote+"  code-review  main\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	if err := runImport(importCmd, []string{manifestPath}); err != nil {
		t.Fatalf("runImport: %v\nstderr: %s", err, stderr.String())
	}

	// 1. The registry must now be in user config.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Registries) == 0 {
		t.Fatalf("import did not register the manifest URL; config: %+v", cfg)
	}

	// 2. The lock must contain the imported skill.
	lock, err := model.ReadLockFile(filepath.Join(project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if _, err := lock.Get("code-review"); err != nil {
		t.Fatalf("expected code-review in lock; got: %v", err)
	}

	// 3. Stdout should include the per-line "Registered" + "Imported" markers
	// (Printer.Success writes to Out).
	if !strings.Contains(stdout.String(), "Registered registry") {
		t.Errorf("missing Registered marker; stdout: %q\nstderr: %q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "Imported code-review") {
		t.Errorf("missing Imported marker; stdout: %q\nstderr: %q", stdout.String(), stderr.String())
	}
}

func TestRunImport_ReusesExistingRegistryByURL(t *testing.T) {
	project := setupImportProject(t)
	resetImportFlags(t)

	remote := seedImportRemote(t, "code-review")
	// Pre-register the same URL under a custom name (real registry add — the
	// bare clone + index need to exist or the later install resolution
	// fails). Import must reuse this alias rather than fail with "already
	// exists" or rename it.
	{
		mgr := newRegistryManager(git.NewGoGitClient())
		if _, err := mgr.Add(context.Background(), "my-private-name", remote); err != nil {
			t.Fatalf("pre-register: %v", err)
		}
	}

	manifestPath := filepath.Join(project, "skills.txt")
	// Manifest also asks for --registry-alias=different — must be ignored
	// because the URL is already registered as my-private-name.
	body := remote + "  code-review  main  --registry-alias=different\n"
	if err := os.WriteFile(manifestPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	if err := runImport(importCmd, []string{manifestPath}); err != nil {
		t.Fatalf("runImport: %v\nstderr: %s", err, stderr.String())
	}

	// The config must still have exactly the one pre-existing registration.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Registries) != 1 {
		t.Errorf("expected exactly 1 registry, got %d: %#v", len(cfg.Registries), cfg.Registries)
	}
	if _, ok := cfg.Registries["my-private-name"]; !ok {
		t.Errorf("original registration disappeared: %#v", cfg.Registries)
	}
	if _, ok := cfg.Registries["different"]; ok {
		t.Errorf("import should not register the manifest's --registry-alias when URL is already known")
	}

	// Skill should still install via the existing alias.
	lock, err := model.ReadLockFile(filepath.Join(project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("expected code-review in lock; got: %v", err)
	}
	if entry.Registry != "my-private-name" {
		t.Errorf("lock entry registered to %q, want my-private-name", entry.Registry)
	}
}

func TestRunImport_RoundTrip_ExportThenImport(t *testing.T) {
	// Phase 1: project A — register + add the skill the conventional way.
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	projectA := t.TempDir()
	t.Chdir(projectA)

	resetImportFlags(t)
	resetExportFlags(t)

	remote := seedImportRemote(t, "code-review")
	manifestSeed := filepath.Join(projectA, "skills.txt")
	if err := os.WriteFile(manifestSeed, []byte(remote+"  code-review  main\n"), 0o644); err != nil {
		t.Fatalf("write seed manifest: %v", err)
	}

	{
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		prev := printer
		printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
		t.Cleanup(func() { printer = prev })
		if err := runImport(importCmd, []string{manifestSeed}); err != nil {
			t.Fatalf("phase-1 runImport: %v\nstderr: %s", err, stderr.String())
		}
	}

	// Phase 2: export projectA's lock to a manifest file.
	exportPath := filepath.Join(t.TempDir(), "exported.txt")
	resetExportFlags(t)
	exportOutputFile = exportPath
	{
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		prev := printer
		printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
		t.Cleanup(func() { printer = prev })
		if err := runExport(exportCmd, nil); err != nil {
			t.Fatalf("phase-2 runExport: %v", err)
		}
	}

	// Phase 3: import into a brand-new project B with a fresh QUIVER_HOME.
	// This proves the manifest is self-contained — the URL gets registered
	// from scratch and the skill installs.
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("reseed config: %v", err)
	}
	projectB := t.TempDir()
	t.Chdir(projectB)
	resetImportFlags(t)

	{
		stdout := &bytes.Buffer{}
		stderr := &bytes.Buffer{}
		prev := printer
		printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
		t.Cleanup(func() { printer = prev })
		if err := runImport(importCmd, []string{exportPath}); err != nil {
			t.Fatalf("phase-3 runImport: %v\nstderr: %s", err, stderr.String())
		}
	}

	// Project B's lock must now contain the same skill.
	lockB, err := model.ReadLockFile(filepath.Join(projectB, model.LockFileName))
	if err != nil {
		t.Fatalf("read project B lock: %v", err)
	}
	if _, err := lockB.Get("code-review"); err != nil {
		t.Fatalf("project B missing imported skill: %v", err)
	}
}

func TestRunImport_RejectsManifestParseErrors(t *testing.T) {
	project := setupImportProject(t)
	resetImportFlags(t)

	bad := filepath.Join(project, "bad.txt")
	// Two columns instead of three.
	if err := os.WriteFile(bad, []byte("https://x.git  s\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	err := runImport(importCmd, []string{bad})
	if err == nil {
		t.Fatal("expected an error on a manifest with only parse failures and no usable entries")
	}
	if !strings.Contains(stderr.String(), "expected at least 3 fields") {
		t.Errorf("missing parse-error message on stderr; got %q", stderr.String())
	}
}
