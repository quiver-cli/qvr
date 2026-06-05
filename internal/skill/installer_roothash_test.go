package skill_test

import (
	"path/filepath"
	"testing"

	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/skill"
)

// installRootAndVerify installs `root-app` from a seeded remote, returns its
// lock entry, and asserts the install recorded a non-empty subtree hash that
// the verifier (disk-side) agrees with — i.e. install-side and verify-side
// digests match. This is the core of issues #151/#154: a path:"." skill used
// to install with an empty subtreeHash and could never be verified.
func installRootAndVerify(t *testing.T, h *installerTestHarness) *model.LockEntry {
	t.Helper()
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "root-app", Targets: []string{"claude"}, ProjectRoot: h.project,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(filepath.Join(h.project, model.LockFileName))
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("root-app")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	if entry.Path != "." {
		t.Fatalf("precondition: path = %q, want '.'", entry.Path)
	}
	if entry.SubtreeHash == "" {
		t.Fatal("root-layout skill installed with EMPTY subtreeHash (#151/#154 regression)")
	}
	res := skill.VerifySingleEntry(entry, h.project)
	if res.Status != skill.VerifyStatusOK {
		t.Fatalf("verify status = %q (msg: %s, drift: %+v), want ok — install hash must match disk hash",
			res.Status, res.Message, res.Drift)
	}
	return entry
}

// TestRootHash_LoneRootRepoVerifies covers the plain single-skill root repo
// (repro A in #154): the repo root IS the skill. It must hash the whole repo
// and verify clean.
func TestRootHash_LoneRootRepoVerifies(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"prompt.txt":          "do the thing\n",
	})
	h.addRegistry(t, "solo", remote)

	entry := installRootAndVerify(t, h)
	if entry.RootCoexists {
		t.Error("lone root should not be RootCoexists")
	}
}

// TestRootHash_CoexistRootRepoVerifies covers the root skill of a multi-skill
// registry (the rootCoexists case from #153/#154): its hash must be scoped to
// SKILL.md + content dirs — matching the sparse worktree — so the digest the
// installer records equals the digest the verifier recomputes from disk. A
// whole-repo hash here would drift forever against the narrowed worktree.
func TestRootHash_CoexistRootRepoVerifies(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":                "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md":     "# guide\n",
		"a/SKILL.md":              "---\nname: a\ndescription: a sibling skill.\n---\n",
		"bin/app.sh":              "#!/bin/sh\necho hi\n",
		"test/fixtures/creds.env": "SECRET=AKIAIOSFODNN7EXAMPLE\n",
	})
	h.addRegistry(t, "multi", remote)

	entry := installRootAndVerify(t, h)
	if !entry.RootCoexists {
		t.Error("root coexisting with siblings should be RootCoexists=true")
	}
}

// TestRootHash_RefreshKeepsCoexistScope guards the Pull/Switch refresh path:
// RefreshSubtreeHash must recompute a coexist root's hash with the SAME scope
// it was installed with, otherwise a no-op pull would re-seal the entry with a
// whole-repo hash and the next verify would report phantom drift.
func TestRootHash_RefreshKeepsCoexistScope(t *testing.T) {
	h := newHarness(t)
	remote := seedFilesRemote(t, map[string]string{
		"SKILL.md":            "---\nname: root-app\ndescription: the root skill.\n---\n# root\n",
		"references/guide.md": "# guide\n",
		"a/SKILL.md":          "---\nname: a\ndescription: a sibling skill.\n---\n",
		"bin/app.sh":          "#!/bin/sh\necho hi\n",
	})
	h.addRegistry(t, "multi", remote)
	entry := installRootAndVerify(t, h)

	installed := entry.SubtreeHash
	if err := skill.RefreshSubtreeHash(entry); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if entry.SubtreeHash != installed {
		t.Errorf("refresh changed coexist hash: install=%s refresh=%s — scope not preserved",
			installed, entry.SubtreeHash)
	}
	if res := skill.VerifySingleEntry(entry, h.project); res.Status != skill.VerifyStatusOK {
		t.Errorf("verify after refresh = %q (%s)", res.Status, res.Message)
	}
}
