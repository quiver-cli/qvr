package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/skill"
)

// setupCreatedSkillProject scaffolds a `qvr create`d (edit-mode, sourceless)
// skill in a fresh project and registers a local bare registry named "acme".
// Returns the registry's remote path.
func setupCreatedSkillProject(t *testing.T, name string) (remote string) {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	if err := config.Save(&config.Config{DefaultTarget: "claude"}); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	t.Chdir(t.TempDir())
	resetPrinter(t)

	t.Cleanup(func() {
		createStandalone = false
		createType = "simple"
		createTarget = "claude"
		createGlobal = false
	})
	createStandalone = false
	createType = "simple"
	createTarget = "claude"
	if err := runCreateProjectScoped(name); err != nil {
		t.Fatalf("create: %v", err)
	}

	remote = seedImportRemote(t, "seed-skill")
	mgr := newRegistryManager(git.NewGoGitClient())
	if _, err := mgr.Add(context.Background(), "acme", remote); err != nil {
		t.Fatalf("registry add: %v", err)
	}
	return remote
}

// TestRunPublish_CreatedSkill_RegistryRoutesToGreenfield is the #242 fix: a
// skill scaffolded with `qvr create` (edit-mode lock entry, no Source URL)
// published by name with --registry must route to greenfield path mode and
// land in the registry — pre-fix it hit installed mode and died with "no
// Source URL" while silently ignoring --registry.
func TestRunPublish_CreatedSkill_RegistryRoutesToGreenfield(t *testing.T) {
	remote := setupCreatedSkillProject(t, "my-skill")
	resetPublishFlags(t)
	publishRegistry = "acme"
	publishMessage = "add my-skill"
	publishCmd.SetContext(context.Background())

	if err := runPublish(publishCmd, []string{"my-skill"}); err != nil {
		t.Fatalf("publish my-skill --registry acme: %v (#242)", err)
	}

	gc := git.NewGoGitClient()
	blob, err := gc.ReadBlob(remote, "HEAD", "skills/my-skill/SKILL.md")
	if err != nil {
		t.Fatalf("registry remote missing skills/my-skill/SKILL.md after publish: %v", err)
	}
	if !strings.Contains(string(blob), "name: my-skill") {
		t.Errorf("published SKILL.md content unexpected: %q", blob)
	}
	got := stderrString(t)
	if !strings.Contains(got, "qvr.lock still tracks the local edit copy") {
		t.Errorf("stderr = %q, want the consume-mode follow-up hint", got)
	}
}

// TestRunPublish_CreatedSkill_NoRegistry_ErrorNamesWorkingCommand: without
// --registry the entry still routes to installed mode, and the no-source
// error must point at the command that now works instead of only --fork.
func TestRunPublish_CreatedSkill_NoRegistry_ErrorNamesWorkingCommand(t *testing.T) {
	setupCreatedSkillProject(t, "my-skill")
	resetPublishFlags(t)
	publishCmd.SetContext(context.Background())

	err := runPublish(publishCmd, []string{"my-skill"})
	if err == nil {
		t.Fatal("publish without --registry or --fork returned nil; want the no-source refusal")
	}
	if !strings.Contains(err.Error(), "--registry") {
		t.Errorf("error = %q, want it to name the `qvr publish <name> --registry` remedy (#242)", err.Error())
	}
	if !strings.Contains(err.Error(), "--fork") {
		t.Errorf("error = %q, want it to keep the --fork alternative", err.Error())
	}
	// The skill must not have routed to greenfield (no registry to land in).
	if !strings.Contains(err.Error(), "no Source URL") {
		t.Errorf("error = %q, want the installed-mode no-source refusal", err.Error())
	}
	_ = skill.ErrPublishNoSource // documents which sentinel the message comes from
}
