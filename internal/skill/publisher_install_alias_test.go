package skill_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// seedAliasWorktreeForEject mirrors seedSharedWorktreeForEject but lays out
// the worktree under the canonical name (matching how the installer
// actually keys aliased entries on disk) and sets entry.Canonical so the
// lock entry advertises the alias. SKILL.md's frontmatter `name:` is the
// canonical, because that's what the registry published. Returns the
// alias-keyed lock entry.
func seedAliasWorktreeForEject(t *testing.T, alias, canonical, registryName string) *model.LockEntry {
	t.Helper()
	quiverHome := testEnv(t)
	const fakeSHA = "abcdef0123456789abcdef0123456789abcdef01"
	worktreeDir := filepath.Join(quiverHome, "worktrees", registryName, canonical, fakeSHA[:7])
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	skillMD := "---\nname: " + canonical + "\ndescription: An ejectable test skill used by the publish alias test.\n---\n\n# " + canonical + "\n"
	if err := os.WriteFile(filepath.Join(worktreeDir, "SKILL.md"), []byte(skillMD), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	return &model.LockEntry{
		Name:          alias,
		Canonical:     canonical,
		Registry:      registryName,
		Source:        "git@example.com:" + registryName + ".git",
		Ref:           "main",
		Commit:        fakeSHA,
		InstallCommit: fakeSHA,
		Targets:       []string{"claude"},
	}
}

// TestPublishInstalled_AliasedEject_ValidatesCanonical is the #104
// regression. Pre-fix, publishing an aliased eject failed with
// `name "shared" must match directory name "my-info"` because the
// validator was comparing SKILL.md's canonical name against the eject
// dir's alias-named directory. The published artifact carries the
// canonical name, so validation must compare frontmatter against
// entry.Canonical (not the dir basename) for aliased entries.
func TestPublishInstalled_AliasedEject_ValidatesCanonical(t *testing.T) {
	entry := seedAliasWorktreeForEject(t, "my-info", "shared", "reg-a")
	projectRoot := t.TempDir()

	if _, err := skill.EjectToTarget(skill.EjectRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
	}); err != nil {
		t.Fatalf("eject aliased: %v", err)
	}

	// Sanity-check the setup mirrors the bug shape: dir basename is the
	// alias, SKILL.md says the canonical.
	editAbs := filepath.Join(projectRoot, entry.EditPath)
	if filepath.Base(editAbs) != "my-info" {
		t.Fatalf("eject dir basename = %q, want my-info", filepath.Base(editAbs))
	}
	content, err := os.ReadFile(filepath.Join(editAbs, "SKILL.md"))
	if err != nil {
		t.Fatalf("read ejected SKILL.md: %v", err)
	}
	if !contains(content, "name: shared") {
		t.Fatalf("ejected SKILL.md did not carry canonical name: %s", content)
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	res, err := p.PublishInstalled(context.Background(), skill.PublishInstalledRequest{
		Entry:       entry,
		ProjectRoot: projectRoot,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("PublishInstalled aliased (dry-run): %v — issue #104 regression", err)
	}
	if !res.DryRun {
		t.Errorf("DryRun = false, want true")
	}
	if res.Skill != "my-info" {
		t.Errorf("Skill = %q, want my-info (alias is preserved as the local name)", res.Skill)
	}
}

// contains is a tiny []byte/substring check kept local so the test stays
// off the `strings` import (helpers_test.go has containsBytes too but
// not exported).
func contains(haystack []byte, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	n := []byte(needle)
	for i := 0; i+len(n) <= len(haystack); i++ {
		match := true
		for j := range n {
			if haystack[i+j] != n[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
