package skill_test

import (
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/skill"
)

// countingGit wraps a GitClient and counts ResolveRef calls, delegating all
// other methods to the embedded interface value.
type countingGit struct {
	git.GitClient
	resolveRefN int64
}

func (c *countingGit) ResolveRef(repoPath, ref string) (string, error) {
	atomic.AddInt64(&c.resolveRefN, 1)
	return c.GitClient.ResolveRef(repoPath, ref)
}

// TestResolveRefCached_CollapsesBatchResolution proves the whole-repo perf fix:
// installing N skills from one registry at one ref resolves the ref→SHA exactly
// ONCE, not 2×N times. Before resolveRefCached + the resolveInstall plan memo, a
// fresh gogit.PlainOpen + ResolveRef ran for every skill in BOTH the
// PrematerializeBatch pre-pass and the serial InstallInto loop — the dominant
// non-network cost of `qvr add --all`.
func TestResolveRefCached_CollapsesBatchResolution(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{
		"alpha": testSkill("alpha"),
		"bravo": testSkill("bravo"),
		"delta": testSkill("delta"),
		"echo":  testSkill("echo"),
	})
	h.addRegistry(t, "acme", remote)

	counter := &countingGit{GitClient: h.installer.Git}
	h.installer.Git = counter

	lockPath := filepath.Join(h.project, model.LockFileName)
	mkReq := func(name string) skill.InstallRequest {
		return skill.InstallRequest{
			Skill: name, Targets: []string{"claude"}, Registry: "acme",
			ProjectRoot: h.project, LockPath: lockPath,
		}
	}
	reqs := []skill.InstallRequest{mkReq("alpha"), mkReq("bravo"), mkReq("delta"), mkReq("echo")}

	// Mirror the cmd/add.go --all flow: pre-pass, then serial install into a
	// single shared lock.
	h.installer.PrematerializeBatch(reqs)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	for _, r := range reqs {
		if _, err := h.installer.InstallInto(r, lock); err != nil {
			t.Fatalf("InstallInto %s: %v", r.Skill, err)
		}
	}
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	// 4 skills, one registry, one default ref → exactly one underlying
	// ResolveRef. Anything ≥ the skill count means the per-skill/per-pass
	// resolution redundancy regressed.
	if got := atomic.LoadInt64(&counter.resolveRefN); got != 1 {
		t.Errorf("ResolveRef called %d times for a 4-skill same-ref batch; want 1 (resolution cache collapse)", got)
	}
}
