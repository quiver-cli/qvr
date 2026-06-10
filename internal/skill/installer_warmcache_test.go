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

const cacheSentinelHash = "sha256:cafecafecafecafecafecafecafecafecafecafecafecafecafecafecafecafe"

// tamperGlobalIdentityCache overwrites the single global identity-cache record
// with a sentinel hash, so a subsequent install that READS the cache is
// observable (its recorded hash becomes the sentinel) while one that recomputes
// produces the real hash.
func tamperGlobalIdentityCache(t *testing.T, sentinel string) {
	t.Helper()
	root := registry.IdentityCacheRoot()
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("no identity cache file found to tamper")
	}
	if err := os.WriteFile(found, []byte(`{"subtreeHash":"`+sentinel+`"}`), 0o644); err != nil {
		t.Fatalf("tamper identity cache: %v", err)
	}
}

// TestInstall_GlobalIdentityCacheReusedAcrossProjects is the load-bearing
// hot-path proof: the canonical hash of an immutable (commit, subtree) is global,
// so a FRESH project installing a skill at an already-materialized commit reuses
// the recorded hash from ~/.quiver instead of re-walking the subtree. This is the
// path the benchmark's "hot" exercises (fresh project, warm global cache) — one a
// project-lockfile gate structurally cannot satisfy.
func TestInstall_GlobalIdentityCacheReusedAcrossProjects(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	// Install in project A → materializes the content dir and populates the
	// global identity cache.
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"},
		ProjectRoot: h.project, LockPath: filepath.Join(h.project, model.LockFileName),
	}); err != nil {
		t.Fatalf("install A: %v", err)
	}
	tamperGlobalIdentityCache(t, cacheSentinelHash)

	// Fresh project B (no prior lock entry) against the warm global cache: the
	// install must reuse the cached (sentinel) hash, proving it didn't recompute.
	projB := t.TempDir()
	lockB := filepath.Join(projB, model.LockFileName)
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"},
		ProjectRoot: projB, LockPath: lockB,
	}); err != nil {
		t.Fatalf("install B: %v", err)
	}
	lock, err := model.ReadLockFile(lockB)
	if err != nil {
		t.Fatalf("read lock B: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get B: %v", err)
	}
	if entry.SubtreeHash != cacheSentinelHash {
		t.Errorf("fresh-project install recomputed the hash (%s); expected global-cache reuse of %s", entry.SubtreeHash, cacheSentinelHash)
	}
}

// tamperProvenanceCache rewrites the single global provenance-cache record's
// recorded author with a sentinel, so an install that READS the cache is
// observable (its recorded CommitAuthor becomes the sentinel) while one that
// recomputes produces the real author.
func tamperProvenanceCache(t *testing.T, sentinelAuthor string) {
	t.Helper()
	root := registry.ProvenanceCacheRoot()
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("no provenance cache file found to tamper")
	}
	if err := os.WriteFile(found, []byte(`{"hasProvenance":false,"author":"`+sentinelAuthor+`"}`), 0o644); err != nil {
		t.Fatalf("tamper provenance cache: %v", err)
	}
}

// TestInstall_ProvenanceCacheReusedAcrossProjects proves the dominant hot-path
// win: a fresh project installing a skill at an already-seen commit reuses the
// cached provenance + author from ~/.quiver instead of respawning several `git`
// processes (signature verification + log walks).
func TestInstall_ProvenanceCacheReusedAcrossProjects(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"},
		ProjectRoot: h.project, LockPath: filepath.Join(h.project, model.LockFileName),
	}); err != nil {
		t.Fatalf("install A: %v", err)
	}
	const sentinelAuthor = "Sentinel Author <sentinel@example.com>"
	tamperProvenanceCache(t, sentinelAuthor)

	projB := t.TempDir()
	lockB := filepath.Join(projB, model.LockFileName)
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"},
		ProjectRoot: projB, LockPath: lockB,
	}); err != nil {
		t.Fatalf("install B: %v", err)
	}
	lock, err := model.ReadLockFile(lockB)
	if err != nil {
		t.Fatalf("read lock B: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get B: %v", err)
	}
	if entry.AuthorIdentity() != sentinelAuthor {
		t.Errorf("fresh-project install recomputed provenance/author (%q); expected global-cache reuse of %q", entry.AuthorIdentity(), sentinelAuthor)
	}
}

// TestInstall_RequireSignedBypassesProvenanceCache guards the security bypass: a
// require_signed install must NOT trust a cached provenance status — it re-checks
// signatures fresh. A poisoned cache claiming "verified" must not let an unsigned
// skill through.
func TestInstall_RequireSignedBypassesProvenanceCache(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)

	// First install (no policy) populates the provenance cache with the real
	// (unsigned → none) status.
	if _, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"},
		ProjectRoot: h.project, LockPath: filepath.Join(h.project, model.LockFileName),
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	// Poison the cache to claim a verified signature.
	root := registry.ProvenanceCacheRoot()
	var found string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			found = p
		}
		return nil
	})
	if found == "" {
		t.Fatal("no provenance cache file to poison")
	}
	if err := os.WriteFile(found, []byte(`{"hasProvenance":true,"provider":"git","signatureStatus":"verified","signer":"x","author":"a"}`), 0o644); err != nil {
		t.Fatalf("poison cache: %v", err)
	}

	// A require_signed install in a fresh project must re-verify (the skill is
	// actually unsigned) and reject — not trust the poisoned "verified" cache.
	projB := t.TempDir()
	_, err := h.installer.Install(skill.InstallRequest{
		Skill: "code-review", Targets: []string{"claude"},
		ProjectRoot: projB, LockPath: filepath.Join(projB, model.LockFileName),
		RequireSigned: true,
	})
	if err == nil {
		t.Fatal("require_signed install trusted a poisoned 'verified' cache for an unsigned skill")
	}
}

// TestInstall_FrozenRecomputesIgnoringGlobalCache guards that the global cache
// does NOT weaken --frozen: its drift gate must recompute a fresh hash from the
// registry, never trust a memo, so a corrupted/stale cache can't mask drift.
func TestInstall_FrozenRecomputesIgnoringGlobalCache(t *testing.T) {
	h := newHarness(t)
	remote := seedRemote(t, map[string]string{"code-review": codeReviewSkill})
	h.addRegistry(t, "acme", remote)
	lockPath := filepath.Join(h.project, model.LockFileName)
	req := skill.InstallRequest{Skill: "code-review", Targets: []string{"claude"}, ProjectRoot: h.project, LockPath: lockPath}

	if _, err := h.installer.Install(req); err != nil {
		t.Fatalf("install: %v", err)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	entry, err := lock.Get("code-review")
	if err != nil {
		t.Fatalf("lock get: %v", err)
	}
	realHash := entry.SubtreeHash
	if realHash == "" {
		t.Fatal("first install recorded no SubtreeHash")
	}

	// Poison the global cache; --frozen must ignore it and recompute the real hash.
	tamperGlobalIdentityCache(t, cacheSentinelHash)
	req.Frozen = true
	if _, err := h.installer.Install(req); err != nil {
		t.Fatalf("frozen reinstall: %v", err)
	}
	lock2, err := model.ReadLockFile(lockPath)
	if err != nil {
		t.Fatalf("re-read lock: %v", err)
	}
	entry2, err := lock2.Get("code-review")
	if err != nil {
		t.Fatalf("lock get 2: %v", err)
	}
	if entry2.SubtreeHash != realHash {
		t.Errorf("--frozen trusted the poisoned cache (got %s); expected a fresh recompute of %s", entry2.SubtreeHash, realHash)
	}
}
