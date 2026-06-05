package skill_test

import (
	"context"
	"testing"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

func skillMD(name string) string {
	return "---\nname: " + name + "\ndescription: the " + name + " skill.\n---\n# " + name + "\n"
}

// TestPublish_TwoSkillsShareTag_NoCollision is the direct #152 repro: in a
// multi-skill registry, two distinct skills each making their debut v0.1.0
// release must NOT collide. Pre-fix the second publish died with
// "tag v0.1.0 already exists on the target registry"; now each lands a
// per-skill-namespaced tag.
func TestPublish_TwoSkillsShareTag_NoCollision(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"seed": skillMD("seed")})
	h.addRegistry(t, "mreg", remote)
	pub := skill.NewPublisher(git.NewGoGitClient())
	ctx := context.Background()

	alpha := writeLocalSkill(t, "alpha", "the alpha skill")
	rA, err := pub.Publish(ctx, skill.PublishRequest{
		LocalPath: alpha, Registry: "mreg", Branch: "main", Tag: "v0.1.0",
	})
	if err != nil {
		t.Fatalf("publish alpha@v0.1.0: %v", err)
	}
	if rA.Tag != "alpha/v0.1.0" {
		t.Errorf("alpha tag = %q, want alpha/v0.1.0", rA.Tag)
	}

	beta := writeLocalSkill(t, "beta", "the beta skill")
	rB, err := pub.Publish(ctx, skill.PublishRequest{
		LocalPath: beta, Registry: "mreg", Branch: "main", Tag: "v0.1.0",
	})
	if err != nil {
		t.Fatalf("publish beta@v0.1.0 collided with alpha's tag (#152 regression): %v", err)
	}
	if rB.Tag != "beta/v0.1.0" {
		t.Errorf("beta tag = %q, want beta/v0.1.0", rB.Tag)
	}

	tags, err := git.NewGoGitClient().ListTags(remote)
	if err != nil {
		t.Fatalf("list tags: %v", err)
	}
	got := map[string]bool{}
	for _, tg := range tags {
		got[tg.Name] = true
	}
	for _, want := range []string{"alpha/v0.1.0", "beta/v0.1.0"} {
		if !got[want] {
			t.Errorf("tag %q missing from registry; got %v", want, got)
		}
	}
}

// TestInstall_PerSkillNamespacedTagsResolve proves the consumer side of #152:
// each skill's versions are attributed only to it, and `qvr add` resolves the
// namespaced tag transparently — both for "latest" and an explicit @version.
func TestInstall_PerSkillNamespacedTagsResolve(t *testing.T) {
	skills := map[string]string{"alpha": skillMD("alpha"), "beta": skillMD("beta")}
	tags := []string{"alpha/v0.1.0", "beta/v0.1.0", "beta/v0.2.0"}

	// alpha (latest) → alpha/v0.1.0; beta@v0.1.0 (explicit) → beta/v0.1.0.
	t.Run("latest and explicit", func(t *testing.T) {
		h := newHarness(t)
		remote := seedRemoteWithTags(t, skills, tags...)
		h.addRegistry(t, "mreg", remote)

		entryA := installAndGet(t, h, "alpha")
		if entryA.Ref != "alpha/v0.1.0" {
			t.Errorf("alpha resolved ref = %q, want alpha/v0.1.0 (latest, not cross-attributed to beta's tags)", entryA.Ref)
		}

		entryB := installAndGet(t, h, "beta@v0.1.0")
		if entryB.Ref != "beta/v0.1.0" {
			t.Errorf("beta@v0.1.0 resolved ref = %q, want beta/v0.1.0 (explicit namespaced)", entryB.Ref)
		}
	})

	// beta (latest) → beta/v0.2.0: per-skill latest, uncontaminated by alpha.
	t.Run("per-skill latest", func(t *testing.T) {
		h := newHarness(t)
		remote := seedRemoteWithTags(t, skills, tags...)
		h.addRegistry(t, "mreg", remote)

		entryB := installAndGet(t, h, "beta")
		if entryB.Ref != "beta/v0.2.0" {
			t.Errorf("beta latest ref = %q, want beta/v0.2.0", entryB.Ref)
		}
	})
}

// installAndGet installs a skill reference and returns its lock entry.
func installAndGet(t *testing.T, h *installerTestHarness, ref string) *model.LockEntry {
	t.Helper()
	name, _, err := skill.ParseReference(ref)
	if err != nil {
		t.Fatalf("parse %q: %v", ref, err)
	}
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: ref, Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install %q: %v", ref, err)
	}
	lock, err := model.ReadLockFile(h.project + "/" + model.LockFileName)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		t.Fatalf("lock get %q: %v", name, err)
	}
	return entry
}
