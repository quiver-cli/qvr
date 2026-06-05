package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/skill"
)

// seedTaggedPullRemote stands up a bare remote with a single skill on `main`
// plus a lightweight tag, so a pull test can install pinned to the tag.
func seedTaggedPullRemote(t *testing.T, name, tag string) string {
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
	body := "---\nname: " + name + "\ndescription: pull-test fixture\n---\n# " + name + "\n"
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
		plumbing.NewBranchReferenceName("main"), head.Hash())); err != nil {
		t.Fatalf("set main: %v", err)
	}
	if err := sr.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewTagReferenceName(tag), head.Hash())); err != nil {
		t.Fatalf("set tag: %v", err)
	}
	if err := sr.Push(&gogit.PushOptions{RemoteName: "origin", RefSpecs: []gogitcfg.RefSpec{
		"refs/heads/main:refs/heads/main",
		gogitcfg.RefSpec("refs/tags/" + tag + ":refs/tags/" + tag),
	}}); err != nil {
		t.Fatalf("push: %v", err)
	}
	rr, err := gogit.PlainOpen(remote)
	if err != nil {
		t.Fatalf("open remote: %v", err)
	}
	if err := rr.Storer.SetReference(plumbing.NewSymbolicReference(
		plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatalf("set HEAD: %v", err)
	}
	return remote
}

// installTagPinned registers the remote and installs the skill pinned to the
// tag into the current project, returning the project root.
func installTagPinned(t *testing.T, name, tag string) {
	t.Helper()
	gc := git.NewGoGitClient()
	mgr := newRegistryManager(gc)
	remote := seedTaggedPullRemote(t, name, tag)
	if _, err := mgr.Add(context.Background(), "acme", remote); err != nil {
		t.Fatalf("registry add: %v", err)
	}
	project, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	inst := skill.NewInstaller(mgr, git.NewGoGitWorktree(), gc)
	if _, err := inst.Install(skill.InstallRequest{
		Skill:       name + "@" + tag,
		Targets:     []string{"claude"},
		ProjectRoot: project,
	}); err != nil {
		t.Fatalf("install tag-pinned: %v", err)
	}
}

// resetPullFlags drives the consolidated `switch` command in --tip mode (the
// former `pull`, issue #160). A direct RunE call has no CalledAs(), so the tip
// mode is selected by the flag rather than the alias name.
func resetPullFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		repointGlobal = false
		repointTip = false
	})
	repointGlobal = false
	repointTip = true
}

// TestRunPull_TagPinned_ErrorsToStderr is the #129 / AC-LIFE-4 guard: a pull
// of a tag-pinned skill must (1) exit non-zero and (2) emit its refusal to
// stderr, not stdout — pre-fix it printed via printer.Info (stdout) and
// returned nil, so a CI `qvr pull && deploy` ran on a no-op pull.
func TestRunPull_TagPinned_ErrorsToStderr(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetPullFlags(t)
	installTagPinned(t, "demo", "v1.0.0")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	prev := printer
	printer = &output.Printer{Out: stdout, Err: stderr, Format: output.FormatText}
	t.Cleanup(func() { printer = prev })

	err := runSwitch(switchCmd, []string{"demo"})
	if err == nil {
		t.Fatal("pull of a tag-pinned skill returned nil; want non-zero exit (#129)")
	}
	if !errors.Is(err, errTextHandled) {
		t.Errorf("pull error = %v, want errTextHandled", err)
	}
	if stdout.Len() != 0 {
		t.Errorf("refusal leaked to stdout: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "pinned to a tag") {
		t.Errorf("stderr = %q, want it to mention the tag pin", stderr.String())
	}
}

// TestRunPull_TagPinned_JSONExitsNonZero confirms the JSON path mirrors the
// text path: the results array lands on stdout AND the command exits non-zero.
func TestRunPull_TagPinned_JSONExitsNonZero(t *testing.T) {
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetPullFlags(t)
	installTagPinned(t, "demo", "v1.0.0")

	stdout := withCapturingPrinter(t, "json")
	err := runSwitch(switchCmd, []string{"demo"})
	if !errors.Is(err, errJSONHandled) {
		t.Fatalf("pull --output json error = %v, want errJSONHandled (#129)", err)
	}
	var results []map[string]string
	if jerr := json.Unmarshal(stdout.Bytes(), &results); jerr != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", jerr, stdout.String())
	}
	if len(results) != 1 || results[0]["status"] != "skipped" {
		t.Errorf("results = %+v, want one skipped entry", results)
	}
}
