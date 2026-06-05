package canonical_test

import (
	"testing"

	"github.com/quiver-cli/qvr/internal/canonical"
)

// TestHashSubtree_dotPathIsRepoRoot is the unit-level guard for #151/#154: a
// `path: "."` subpath must hash the whole repo, not die with
// "locate subtree \".\": directory not found". Its digest must equal the
// empty-subpath (whole-repo) digest.
func TestHashSubtree_dotPathIsRepoRoot(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		"SKILL.md":            "---\nname: root\n---\nbody\n",
		"references/guide.md": "# guide\n",
	})
	dot, err := canonical.HashSubtree(repo, ".")
	if err != nil {
		t.Fatalf("hash path='.': %v (the #151/#154 'locate subtree' dead-end)", err)
	}
	root, err := canonical.HashSubtree(repo, "")
	if err != nil {
		t.Fatalf("hash path='': %v", err)
	}
	if dot.SubtreeHash != root.SubtreeHash {
		t.Errorf("path='.' digest %s != whole-repo digest %s", dot.SubtreeHash, root.SubtreeHash)
	}
}

// rootScope mirrors model.SkillScopePaths(".", true). Inlined here to keep the
// canonical package free of a model import.
var rootScope = []string{"SKILL.md", "references", "scripts", "assets"}

// TestHashScoped_matchesDiskOverSameFiles proves the install-side scoped git
// hash equals the verify-side disk hash over a worktree narrowed to the same
// scope. The repo carries siblings + app code; scoping to SKILL.md + content
// dirs must exclude them. We emulate the sparse worktree with a second repo
// that contains ONLY the scoped files (.git is excluded from both digests).
func TestHashScoped_matchesDiskOverSameFiles(t *testing.T) {
	full := buildRepo(t, map[string]string{
		"SKILL.md":                "---\nname: root\n---\nbody\n",
		"references/guide.md":     "# guide\n",
		"scripts/run.sh":          "echo hi\n",
		"a/SKILL.md":              "---\nname: a\n---\n",  // sibling — excluded
		"bin/app":                 "binary\n",             // app code — excluded
		"test/fixtures/creds.env": "SECRET=AKIAEXAMPLE\n", // app fixture — excluded
	})
	scoped, err := canonical.HashScoped(full, rootScope)
	if err != nil {
		t.Fatalf("HashScoped: %v", err)
	}

	// A worktree sparse-checked-out to the scope contains exactly these files.
	narrow := buildRepo(t, map[string]string{
		"SKILL.md":            "---\nname: root\n---\nbody\n",
		"references/guide.md": "# guide\n",
		"scripts/run.sh":      "echo hi\n",
	})
	disk, err := canonical.HashSubtreeFromDisk(narrow)
	if err != nil {
		t.Fatalf("HashSubtreeFromDisk: %v", err)
	}
	if scoped.SubtreeHash != disk {
		t.Errorf("scoped git hash %s != disk hash over same files %s — install/verify would disagree",
			scoped.SubtreeHash, disk)
	}
}

// TestHashScoped_absentScopeEntrySkipped guards the "assets/ doesn't exist"
// case: a missing scope dir must be silently skipped, not error.
func TestHashScoped_absentScopeEntrySkipped(t *testing.T) {
	repo := buildRepo(t, map[string]string{
		"SKILL.md": "---\nname: root\n---\nbody\n", // no references/scripts/assets
	})
	id, err := canonical.HashScoped(repo, rootScope)
	if err != nil {
		t.Fatalf("HashScoped with absent content dirs: %v", err)
	}
	if id.SubtreeHash == "" {
		t.Fatal("empty hash")
	}
}
