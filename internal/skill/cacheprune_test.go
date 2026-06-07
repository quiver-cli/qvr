package skill_test

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
)

func countCacheFiles(t *testing.T, root string) int {
	t.Helper()
	n := 0
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !strings.HasPrefix(d.Name(), ".") {
			n++
		}
		return nil
	})
	return n
}

// TestPruneAuxCaches_KeepsReachableThenSweepsOrphans drives the full lifecycle:
// a reachable install must keep its blob/identity/provenance records, and once
// the install is no longer referenced by any lock those records become orphans
// that prune reclaims.
func TestPruneAuxCaches_KeepsReachableThenSweepsOrphans(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	lockPath := filepath.Join(h.project, model.LockFileName)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"},
		ProjectRoot: h.project, LockPath: lockPath,
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	registry.TouchProject(lockPath) // make the project reachable to prune

	// All three derived caches should now hold records.
	if n := countCacheFiles(t, registry.BlobStoreRoot()); n == 0 {
		t.Fatal("expected blobs after install")
	}
	if n := countCacheFiles(t, registry.IdentityCacheRoot()); n == 0 {
		t.Fatal("expected identity records after install")
	}
	if n := countCacheFiles(t, registry.ProvenanceCacheRoot()); n == 0 {
		t.Fatal("expected provenance records after install")
	}

	// Reachable install: prune must remove nothing (this also proves the live
	// key derivation matches what Install wrote).
	res, err := skill.PruneAuxCaches(false)
	if err != nil {
		t.Fatalf("prune (reachable): %v", err)
	}
	if res.Total() != 0 {
		t.Errorf("prune removed %d records for a reachable install: %+v", res.Total(), res)
	}

	// Make the install unreachable: forget the project and drop its lock.
	registry.ForgetProject(lockPath)
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove lock: %v", err)
	}

	// Dry-run reports the orphans without deleting.
	dry, err := skill.PruneAuxCaches(true)
	if err != nil {
		t.Fatalf("prune (dry): %v", err)
	}
	if dry.IdentityRemoved == 0 || dry.ProvenanceRemoved == 0 || dry.BlobsRemoved == 0 {
		t.Errorf("dry-run should report orphans in all caches: %+v", dry)
	}
	if countCacheFiles(t, registry.BlobStoreRoot()) == 0 {
		t.Error("dry-run deleted blobs (should not touch disk)")
	}
	if countCacheFiles(t, registry.IdentityCacheRoot()) == 0 {
		t.Error("dry-run deleted identity cache (should not touch disk)")
	}
	if countCacheFiles(t, registry.ProvenanceCacheRoot()) == 0 {
		t.Error("dry-run deleted provenance cache (should not touch disk)")
	}

	// Real run reclaims everything.
	got, err := skill.PruneAuxCaches(false)
	if err != nil {
		t.Fatalf("prune (orphan): %v", err)
	}
	if got.IdentityRemoved == 0 || got.ProvenanceRemoved == 0 || got.BlobsRemoved == 0 {
		t.Errorf("expected all caches pruned, got %+v", got)
	}
	for _, root := range []string{registry.BlobStoreRoot(), registry.IdentityCacheRoot(), registry.ProvenanceCacheRoot()} {
		if n := countCacheFiles(t, root); n != 0 {
			t.Errorf("cache %s not emptied after prune: %d files remain", root, n)
		}
	}
}
