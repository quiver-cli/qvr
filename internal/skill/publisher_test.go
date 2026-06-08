package skill_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogit "github.com/go-git/go-git/v5"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
)

func TestPublish_Success(t *testing.T) {
	h := newHarness(t)
	// Start with an empty remote so publish is the first commit touching skills/.
	remote := seedRemote(t, map[string]string{"placeholder": `---
name: placeholder
description: seed skill so the registry has a default branch.
---
# seed
`})
	h.addRegistry(t, "acme", remote)

	skillDir := writeLocalSkill(t, "my-skill", "My published skill.")

	p := skill.NewPublisher(git.NewGoGitClient())
	result, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: skillDir,
		Registry:  "acme",
		Message:   "initial publish",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Skill != "my-skill" {
		t.Errorf("skill = %s", result.Skill)
	}
	if result.Registry != "acme" {
		t.Errorf("registry = %s", result.Registry)
	}

	// Verify the remote got the new skill.
	tmp := t.TempDir()
	check := filepath.Join(tmp, "check")
	if _, err := gogit.PlainClone(check, false, &gogit.CloneOptions{URL: remote}); err != nil {
		t.Fatalf("verify clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(check, "skills", "my-skill", "SKILL.md")); err != nil {
		t.Errorf("skill missing in remote: %v", err)
	}
	if _, err := os.Stat(filepath.Join(check, "skills", "my-skill", "NOTES.md")); err != nil {
		t.Errorf("supplemental file missing in remote: %v", err)
	}
}

func TestPublish_DryRun(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"seed": `---
name: seed
description: seed.
---
# seed
`})
	h.addRegistry(t, "acme", remote)

	skillDir := writeLocalSkill(t, "my-skill", "Some skill.")

	p := skill.NewPublisher(git.NewGoGitClient())
	result, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: skillDir,
		Registry:  "acme",
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !result.DryRun {
		t.Error("expected DryRun=true")
	}

	// Remote should still not contain the skill.
	tmp := t.TempDir()
	check := filepath.Join(tmp, "check")
	if _, err := gogit.PlainClone(check, false, &gogit.CloneOptions{URL: remote}); err != nil {
		t.Fatalf("verify clone: %v", err)
	}
	if _, err := os.Stat(filepath.Join(check, "skills", "my-skill")); !os.IsNotExist(err) {
		t.Errorf("dry-run wrote skill: %v", err)
	}
}

func TestPublish_Lints(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"seed": `---
name: seed
description: x
---
# seed
`})
	h.addRegistry(t, "acme", remote)

	// Skill with empty description → publish lint gate rejects.
	bad := filepath.Join(t.TempDir(), "bad-skill")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatalf("mkdir bad: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, "SKILL.md"), []byte("---\nname: bad-skill\ndescription: \n---\n# bad\n"), 0o644); err != nil {
		t.Fatalf("write bad SKILL.md: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: bad,
		Registry:  "acme",
	})
	if err == nil || !strings.Contains(err.Error(), "lint failed") {
		t.Errorf("expected lint failure, got %v", err)
	}
}

// TestPublish_AutoCreatesBranch pins the bug #14 fix: publishing to a
// --branch that doesn't yet exist on origin branches from the registry
// default and pushes the new branch in one step, instead of erroring
// "branch not found on origin" and forcing the user to drop into raw git.
func TestPublish_AutoCreatesBranch(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"seed": `---
name: seed
description: seed skill.
---
# seed
`})
	h.addRegistry(t, "acme", remote)

	skillDir := writeLocalSkill(t, "my-skill", "Auto-branch target.")

	p := skill.NewPublisher(git.NewGoGitClient())
	result, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: skillDir,
		Registry:  "acme",
		Branch:    "feature/auto-create",
		Message:   "publish via auto-created branch",
	})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if result.Branch != "feature/auto-create" {
		t.Errorf("branch = %q, want feature/auto-create", result.Branch)
	}

	// Verify the new branch exists on origin and carries the skill.
	tmp := t.TempDir()
	check := filepath.Join(tmp, "check")
	if _, err := gogit.PlainClone(check, false, &gogit.CloneOptions{
		URL:           remote,
		ReferenceName: "refs/heads/feature/auto-create",
	}); err != nil {
		t.Fatalf("clone auto-created branch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(check, "skills", "my-skill", "SKILL.md")); err != nil {
		t.Errorf("skill missing on auto-created branch: %v", err)
	}
}

// TestPublish_NoCreateBranchRefuses confirms the escape hatch: with
// --no-create-branch, publishing to a new branch keeps the old
// "branch not found on origin" behaviour.
func TestPublish_NoCreateBranchRefuses(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"seed": `---
name: seed
description: seed skill.
---
# seed
`})
	h.addRegistry(t, "acme", remote)

	skillDir := writeLocalSkill(t, "my-skill", "x")
	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath:      skillDir,
		Registry:       "acme",
		Branch:         "feature/strict",
		Message:        "should fail",
		NoCreateBranch: true,
	})
	if err == nil || !strings.Contains(err.Error(), "not found on origin") {
		t.Errorf("expected not-found error with --no-create-branch, got %v", err)
	}
}

func TestPublish_UnknownRegistry(t *testing.T) {
	testEnv(t)
	skillDir := writeLocalSkill(t, "my-skill", "x")
	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: skillDir,
		Registry:  "nope",
	})
	if err == nil {
		t.Error("expected error for missing registry")
	}
}

func TestPublish_NoRegistryConfigured(t *testing.T) {
	testEnv(t)
	skillDir := writeLocalSkill(t, "my-skill", "x")
	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.Publish(context.Background(), skill.PublishRequest{LocalPath: skillDir})
	if !errors.Is(err, skill.ErrPublishNoRegistry) {
		t.Errorf("expected ErrPublishNoRegistry, got %v", err)
	}
}

// TestPublish_RefusesOverwriteWithoutForce is the regression guard for issue
// #72: greenfield publish used to silently overwrite an existing same-name
// skill in the registry, opening a squat/clobber attack on shared registries.
// Default behavior now refuses; --force opts into the overwrite.
func TestPublish_RefusesOverwriteWithoutForce(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"shared-name": `---
name: shared-name
description: alice's version.
---
# alice
`,
	})
	h.addRegistry(t, "acme", remote)

	// Bob attempts to publish a DIFFERENT skill with the same name.
	skillDir := filepath.Join(t.TempDir(), "shared-name")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: shared-name
description: bob's totally different version.
---
# bob
`), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: skillDir,
		Registry:  "acme",
	})
	if err == nil {
		t.Fatal("expected refusal on same-name overwrite without --force, got nil")
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("error should mention --force, got %v", err)
	}

	// With --force, the publish should succeed.
	res, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: skillDir,
		Registry:  "acme",
		Message:   "bob takes over",
		Force:     true,
	})
	if err != nil {
		t.Fatalf("publish with --force: %v", err)
	}
	if res == nil || res.Commit == "" {
		t.Errorf("expected successful publish with --force, got %+v", res)
	}
}

func TestPublish_NothingToDo(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"my-skill": `---
name: my-skill
description: already in registry.
---
# my-skill
`,
	})
	h.addRegistry(t, "acme", remote)

	// Create a local copy with identical contents.
	skillDir := filepath.Join(t.TempDir(), "my-skill")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir skillDir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: my-skill
description: already in registry.
---
# my-skill
`), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	_, err := p.Publish(context.Background(), skill.PublishRequest{
		LocalPath: skillDir,
		Registry:  "acme",
	})
	if err == nil || !strings.Contains(err.Error(), "nothing to publish") {
		t.Errorf("expected 'nothing to publish', got %v", err)
	}

	// Ensure the bare cache registration isn't a stale pointer.
	bareHead, _ := git.NewGoGitClient().HeadCommit(registry.RegistryPath("acme"))
	remoteHead, _ := git.NewGoGitClient().HeadCommit(remote)
	if bareHead == "" || remoteHead == "" {
		t.Skip("head resolution unsupported in this environment")
	}
	if bareHead != remoteHead {
		t.Errorf("bare cache is stale: bareHead=%s remoteHead=%s", bareHead, remoteHead)
	}
}
