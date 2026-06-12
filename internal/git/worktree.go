package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
)

var (
	// ErrWorktreeExists is returned by Add when the destination worktree
	// path already exists on disk.
	ErrWorktreeExists = errors.New("worktree path already exists")
	// ErrWorktreeNotFound is returned by Remove when the given path does
	// not exist or is not a directory.
	ErrWorktreeNotFound = errors.New("worktree not found")
	// ErrBareNotFound is returned by Add when the bare repository the
	// worktree should be provisioned from does not exist.
	ErrBareNotFound = errors.New("bare repository not found")
)

// Worktree represents a provisioned worktree linked to a bare repository.
type Worktree struct {
	Path   string
	Branch string
	Commit string
}

// WorktreeManager handles git worktree operations.
type WorktreeManager interface {
	Add(bareRepoPath, worktreePath, ref string) error
	Remove(worktreePath string) error
	List(worktreesRoot string) ([]Worktree, error)
	SetSparseCheckout(worktreePath string, paths []string) error
	SetSparseCheckoutPatterns(worktreePath string, patterns []string) error
	ReapplySparseCheckout(worktreePath string) error
	Checkout(worktreePath, ref string) error
	CreateBranchFromHEAD(worktreePath, newBranch string) error
	CreateBranchFromRef(worktreePath, newBranch, fromRef string) error
}

// GoGitWorktree implements WorktreeManager by cloning bare repos into
// working trees. go-git does not support native `git worktree`, so we
// approximate it with a local clone whose origin is rewritten to the bare
// repo's upstream URL. This keeps push/pull direct to the real remote and
// leaves the bare repo as the pure index cache.
type GoGitWorktree struct{}

// NewGoGitWorktree constructs a GoGitWorktree.
func NewGoGitWorktree() *GoGitWorktree { return &GoGitWorktree{} }

// Add provisions a new worktree at worktreePath from bareRepoPath at ref.
// The resulting repo has a working tree checked out at ref and its origin
// points at the bare repo's upstream URL when one is configured.
func (w *GoGitWorktree) Add(bareRepoPath, worktreePath, ref string) error {
	if _, err := os.Stat(worktreePath); err == nil {
		return fmt.Errorf("%w: %s", ErrWorktreeExists, worktreePath)
	}
	if _, err := os.Stat(bareRepoPath); err != nil {
		return fmt.Errorf("%w: %s", ErrBareNotFound, bareRepoPath)
	}
	if err := os.MkdirAll(filepath.Dir(worktreePath), 0o755); err != nil {
		return fmt.Errorf("create worktree parent: %w", err)
	}

	upstreamURL := readBareUpstreamURL(bareRepoPath)

	// Clone from the bare path as a local repo, hardlinking the bare's object
	// files (`git clone --local`) instead of copying them. Git objects are
	// content-addressed and immutable — never modified in place, only added or
	// removed — so hardlinking is safe: the worktree and the bare share the
	// same object blobs on disk at zero extra cost, and identical skill content
	// across different commits is stored once. Unlike `--shared` (alternates),
	// hardlinked objects are normal files go-git reads directly. Falls back to
	// a full go-git copy clone if system git is unavailable or on a different
	// filesystem (git then copies; still correct, just not deduped).
	if err := localHardlinkClone(bareRepoPath, worktreePath); err != nil {
		_ = os.RemoveAll(worktreePath)
		if _, gerr := gogit.PlainClone(worktreePath, false, &gogit.CloneOptions{
			URL: bareRepoPath,
		}); gerr != nil {
			return fmt.Errorf("clone worktree: %w", gerr)
		}
	}

	wtRepo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		return fmt.Errorf("open worktree: %w", err)
	}

	// Rewrite origin URL so push/pull goes directly to the real upstream.
	// Fallback: if the bare has no configured upstream (e.g., in tests that
	// cloned from a local path that already evaporated), leave origin as the
	// bare path — this still allows intra-test push/pull.
	if upstreamURL != "" {
		if err := setOriginURL(wtRepo, upstreamURL); err != nil {
			return fmt.Errorf("rewrite origin URL: %w", err)
		}
	}

	if ref != "" {
		if err := checkoutRef(wtRepo, worktreePath, ref); err != nil {
			_ = os.RemoveAll(worktreePath)
			return err
		}
	}
	return nil
}

// localHardlinkClone clones the bare repo into dest as a working repo, with
// object files hardlinked from the bare (`git clone --local`, the default for
// local-path sources) rather than copied. dest must not already exist. Used by
// Add to deduplicate the registry object database across worktrees while
// keeping the objects as ordinary files go-git can read.
func localHardlinkClone(bare, dest string) error {
	_, err := runGit(context.Background(), "clone", "--local", bare, dest)
	return err
}

// Remove deletes a worktree directory.
func (w *GoGitWorktree) Remove(worktreePath string) error {
	info, err := os.Stat(worktreePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrWorktreeNotFound, worktreePath)
		}
		return fmt.Errorf("stat worktree: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %s is not a directory", ErrWorktreeNotFound, worktreePath)
	}
	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree: %w", err)
	}
	return nil
}

// List enumerates worktrees under worktreesRoot. Any directory that holds a
// valid git repository is reported; malformed entries are silently skipped so
// a single corrupt worktree doesn't break `qvr list`.
func (w *GoGitWorktree) List(worktreesRoot string) ([]Worktree, error) {
	entries, err := os.ReadDir(worktreesRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read worktrees root: %w", err)
	}

	var out []Worktree
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := filepath.Join(worktreesRoot, e.Name())
		repo, err := gogit.PlainOpen(path)
		if err != nil {
			continue
		}
		head, err := repo.Head()
		if err != nil {
			continue
		}
		branch := ""
		if head.Name().IsBranch() {
			branch = head.Name().Short()
		}
		out = append(out, Worktree{
			Path:   path,
			Branch: branch,
			Commit: head.Hash().String(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// SetSparseCheckout enables legacy (non-cone) sparse checkout by setting
// core.sparseCheckout=true, writing gitignore-style patterns to
// .git/info/sparse-checkout, and running read-tree to populate
// skip-worktree bits and prune files outside the patterns. The legacy path
// is used instead of `git sparse-checkout init --cone` because cone mode
// flips on extensions.worktreeConfig, which go-git's PlainOpen refuses
// ("core.repositoryformatversion does not support extension: worktreeconfig").
// Since the rest of the codebase opens the worktree through go-git, we stay
// on the pre-extension code path. Post-fix: `git status` is clean right
// after install — this is the cure for bug #13.
//
// Paths are relative to the worktree root; "." or "" (or an empty slice)
// selects everything and skips sparse setup entirely.
func (w *GoGitWorktree) SetSparseCheckout(worktreePath string, paths []string) error {
	cleaned := sanitizeSparsePaths(paths)
	if len(cleaned) == 0 {
		return nil
	}
	ctx := context.Background()
	if _, err := runGit(ctx, "-C", worktreePath, "config", "core.sparseCheckout", "true"); err != nil {
		return fmt.Errorf("enable sparse checkout: %w", err)
	}
	if err := writeSparsePatternsFile(worktreePath, cleaned); err != nil {
		return err
	}
	if _, err := runGit(ctx, "-C", worktreePath, "read-tree", "-mu", "HEAD"); err != nil {
		return fmt.Errorf("apply sparse: %w", err)
	}
	return nil
}

// SetSparseCheckoutPatterns scopes a worktree to an explicit set of repo-root
// patterns. Unlike SetSparseCheckout (which only expresses whole-directory
// subtrees), each pattern here is materialised as both a literal anchor
// (`/<p>`, matching a top-level file like SKILL.md) and a subtree glob
// (`/<p>/**`, matching a content directory like references/). This lets a
// caller scope a skill to e.g. {"SKILL.md","references","scripts","assets"}
// without knowing which entries are files and which are directories.
//
// An empty pattern set is a no-op (everything stays checked out), matching
// SetSparseCheckout's "nil = no narrowing" contract.
func (w *GoGitWorktree) SetSparseCheckoutPatterns(worktreePath string, patterns []string) error {
	cleaned := sanitizeSparsePaths(patterns)
	if len(cleaned) == 0 {
		return nil
	}
	ctx := context.Background()
	if _, err := runGit(ctx, "-C", worktreePath, "config", "core.sparseCheckout", "true"); err != nil {
		return fmt.Errorf("enable sparse checkout: %w", err)
	}
	if err := writeSparsePatternLines(worktreePath, cleaned); err != nil {
		return err
	}
	if _, err := runGit(ctx, "-C", worktreePath, "read-tree", "-mu", "HEAD"); err != nil {
		return fmt.Errorf("apply sparse: %w", err)
	}
	return nil
}

// writeSparsePatternLines writes, for each entry, an anchored literal pattern
// and an anchored subtree glob so the entry is included whether it is a file
// or a directory.
func writeSparsePatternLines(worktreePath string, patterns []string) error {
	dir := filepath.Join(worktreePath, ".git", "info")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sparse info dir: %w", err)
	}
	var b strings.Builder
	for _, p := range patterns {
		fmt.Fprintf(&b, "/%s\n/%s/**\n", p, p)
	}
	return os.WriteFile(filepath.Join(worktreePath, ".git", "info", "sparse-checkout"), []byte(b.String()), 0o644)
}

// ReapplySparseCheckout re-runs read-tree against HEAD so files that a
// go-git Checkout just repopulated outside the configured sparse paths get
// trimmed again. A no-op when core.sparseCheckout isn't set.
func (w *GoGitWorktree) ReapplySparseCheckout(worktreePath string) error {
	if !sparseCheckoutEnabled(worktreePath) {
		return nil
	}
	if _, err := runGit(context.Background(), "-C", worktreePath, "read-tree", "-mu", "HEAD"); err != nil {
		return fmt.Errorf("reapply sparse: %w", err)
	}
	return nil
}

// writeSparsePatternsFile writes gitignore-style patterns that select a
// directory and everything beneath it. Each sparse path gets a
// `/<path>/**` line, anchored to the worktree root so two skills named
// `code-review` under different parents can't collide.
func writeSparsePatternsFile(worktreePath string, paths []string) error {
	dir := filepath.Join(worktreePath, ".git", "info")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create sparse info dir: %w", err)
	}
	var b strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&b, "/%s/**\n", p)
	}
	return os.WriteFile(filepath.Join(worktreePath, ".git", "info", "sparse-checkout"), []byte(b.String()), 0o644)
}

// Checkout switches the worktree to ref. Re-applies any sparse config after
// the files settle so a switch between versions doesn't resurrect trimmed
// files.
func (w *GoGitWorktree) Checkout(worktreePath, ref string) error {
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		return fmt.Errorf("open worktree: %w", err)
	}
	if err := checkoutRef(repo, worktreePath, ref); err != nil {
		return err
	}
	return w.ReapplySparseCheckout(worktreePath)
}

// CreateBranchFromHEAD creates a new local branch at the current HEAD commit,
// switches the worktree onto it, and configures origin tracking so a later
// push/pull round-trip works without extra flags. The branch must not already
// exist.
func (w *GoGitWorktree) CreateBranchFromHEAD(worktreePath, newBranch string) error {
	if newBranch == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		return fmt.Errorf("open worktree: %w", err)
	}
	head, err := repo.Head()
	if err != nil {
		return fmt.Errorf("resolve HEAD: %w", err)
	}
	return w.createBranchAt(repo, worktreePath, newBranch, head.Hash())
}

// CreateBranchFromRef creates a new local branch pointing at the commit
// fromRef resolves to (branch name, remote-tracking ref, tag, or hash), then
// checks the worktree out onto it with tracking configured. The new branch
// must not already exist.
//
// Used by `qvr edit` to recover the upstream tip when
// `qvr/<user>/<skill>` already exists on origin (bug #15), and by
// `qvr publish` to branch from the registry default when the user asks to
// publish to a brand-new branch (bug #14).
func (w *GoGitWorktree) CreateBranchFromRef(worktreePath, newBranch, fromRef string) error {
	if newBranch == "" {
		return fmt.Errorf("branch name cannot be empty")
	}
	if fromRef == "" {
		return fmt.Errorf("source ref cannot be empty")
	}
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		return fmt.Errorf("open worktree: %w", err)
	}
	hash, err := resolveBranchSource(repo, fromRef)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", fromRef, err)
	}
	return w.createBranchAt(repo, worktreePath, newBranch, hash)
}

// createBranchAt is the shared body of CreateBranchFromHEAD and
// CreateBranchFromRef: refuse if the branch already exists, plant a ref at
// hash, wire up origin tracking, check the worktree out, re-apply sparse so
// switching to a different commit doesn't populate files outside the
// configured sparse paths.
func (w *GoGitWorktree) createBranchAt(repo *gogit.Repository, worktreePath, newBranch string, hash plumbing.Hash) error {
	newRef := plumbing.NewBranchReferenceName(newBranch)
	if _, err := repo.Reference(newRef, false); err == nil {
		return fmt.Errorf("branch %q already exists", newBranch)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(newRef, hash)); err != nil {
		return fmt.Errorf("create branch: %w", err)
	}
	if err := setBranchTracking(repo, newBranch); err != nil {
		return fmt.Errorf("configure branch tracking: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree handle: %w", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{Branch: newRef, Force: true}); err != nil {
		return fmt.Errorf("checkout new branch: %w", err)
	}
	return w.ReapplySparseCheckout(worktreePath)
}

// resolveBranchSource resolves ref (local branch, remote-tracking branch,
// tag, or hash) to a commit hash suitable for planting a new branch at. The
// order matches what a user would try interactively.
func resolveBranchSource(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	if r, err := repo.Reference(plumbing.NewBranchReferenceName(ref), true); err == nil {
		return r.Hash(), nil
	}
	if r, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", ref), true); err == nil {
		return r.Hash(), nil
	}
	if r, err := repo.Reference(plumbing.NewTagReferenceName(ref), true); err == nil {
		hash := r.Hash()
		if tagObj, err := repo.TagObject(hash); err == nil {
			if commit, err := tagObj.Commit(); err == nil {
				hash = commit.Hash
			}
		}
		return hash, nil
	}
	if resolved, err := repo.ResolveRevision(plumbing.Revision(ref)); err == nil && resolved != nil {
		return *resolved, nil
	}
	return plumbing.ZeroHash, fmt.Errorf("%w: %q", ErrRefNotFound, ref)
}

// checkoutRef resolves ref (branch, tag, or hash) and checks it out, creating
// a local tracking branch for remote branches so push/pull are symmetric.
func checkoutRef(repo *gogit.Repository, worktreePath, ref string) error {
	wt, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("worktree: %w", err)
	}

	// Try as local branch first (already exists).
	if _, err := repo.Reference(plumbing.NewBranchReferenceName(ref), false); err == nil {
		return wt.Checkout(&gogit.CheckoutOptions{
			Branch: plumbing.NewBranchReferenceName(ref),
			Force:  true,
		})
	}

	// Try as remote branch — create a local branch tracking origin/<ref>.
	remoteBranch := plumbing.NewRemoteReferenceName("origin", ref)
	if remoteRef, err := repo.Reference(remoteBranch, true); err == nil {
		localBranch := plumbing.NewBranchReferenceName(ref)
		if err := repo.Storer.SetReference(plumbing.NewHashReference(localBranch, remoteRef.Hash())); err != nil {
			return fmt.Errorf("create local branch: %w", err)
		}
		// Persist branch tracking config so `git pull` / go-git pull work.
		if err := setBranchTracking(repo, ref); err != nil {
			return fmt.Errorf("configure branch tracking: %w", err)
		}
		return wt.Checkout(&gogit.CheckoutOptions{
			Branch: localBranch,
			Force:  true,
		})
	}

	// Try as tag (detached checkout).
	if tagRef, err := repo.Reference(plumbing.NewTagReferenceName(ref), true); err == nil {
		hash := tagRef.Hash()
		if tagObj, err := repo.TagObject(hash); err == nil {
			if commit, err := tagObj.Commit(); err == nil {
				hash = commit.Hash
			}
		}
		return wt.Checkout(&gogit.CheckoutOptions{
			Hash:  hash,
			Force: true,
		})
	}

	// Try as hash (supports full and abbreviated SHAs).
	if len(ref) >= 4 {
		if resolved, err := repo.ResolveRevision(plumbing.Revision(ref)); err == nil && resolved != nil {
			return wt.Checkout(&gogit.CheckoutOptions{Hash: *resolved, Force: true})
		}
	}

	return fmt.Errorf("%w: %q", ErrRefNotFound, ref)
}

func readBareUpstreamURL(bareRepoPath string) string {
	repo, err := gogit.PlainOpen(bareRepoPath)
	if err != nil {
		return ""
	}
	rem, err := repo.Remote("origin")
	if err != nil || len(rem.Config().URLs) == 0 {
		return ""
	}
	return rem.Config().URLs[0]
}

func setOriginURL(repo *gogit.Repository, url string) error {
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]*gogitcfg.RemoteConfig{}
	}
	if r, ok := cfg.Remotes["origin"]; ok {
		r.URLs = []string{url}
	} else {
		cfg.Remotes["origin"] = &gogitcfg.RemoteConfig{Name: "origin", URLs: []string{url}}
	}
	return repo.SetConfig(cfg)
}

func setBranchTracking(repo *gogit.Repository, branch string) error {
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	if cfg.Branches == nil {
		cfg.Branches = map[string]*gogitcfg.Branch{}
	}
	cfg.Branches[branch] = &gogitcfg.Branch{
		Name:   branch,
		Remote: "origin",
		Merge:  plumbing.NewBranchReferenceName(branch),
	}
	return repo.SetConfig(cfg)
}

// sanitizeSparsePaths normalises cone-mode sparse paths: forward slashes,
// stripped leading/trailing separators, deduped. Empty/"." entries are
// dropped (they mean "everything"), so when the slice is empty after
// cleaning the caller should treat sparse as disabled.
func sanitizeSparsePaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.Trim(filepath.ToSlash(p), "/")
		if p == "" || p == "." {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	return out
}

// sparseCheckoutEnabled reports whether core.sparseCheckout is "true" for the
// worktree at path. `git sparse-checkout reapply` errors on worktrees that
// have never been sparse-initialised; this guards that call.
func sparseCheckoutEnabled(worktreePath string) bool {
	out, err := runGit(context.Background(), "-C", worktreePath, "config", "--get", "core.sparseCheckout")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}
