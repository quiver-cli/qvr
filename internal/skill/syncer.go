package skill

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
)

var (
	ErrDivergence  = errors.New("local and upstream histories have diverged; resolve manually")
	ErrPinnedToTag = errors.New("skill is pinned to a tag; use 'qvr switch' (or its 'upgrade'/'pull' aliases) to move it")
)

// SyncStatus summarises one installed skill's git state for `qvr status`.
type SyncStatus struct {
	Name    string `json:"name"`
	Branch  string `json:"branch"`
	Commit  string `json:"commit"`
	Dirty   bool   `json:"dirty"`
	Ahead   int    `json:"ahead"`
	Behind  int    `json:"behind"`
	Broken  bool   `json:"broken,omitempty"`
	Message string `json:"message,omitempty"`
}

// Syncer implements pull/status for installed skills.
type Syncer struct {
	Worktree git.WorktreeManager
	Git      git.GitClient
}

// NewSyncer wires default dependencies.
func NewSyncer(wt git.WorktreeManager, gc git.GitClient) *Syncer {
	return &Syncer{Worktree: wt, Git: gc}
}

// Status reports the git state for the given entry. Purely local — no network,
// no fetches — so `qvr status` stays fast.
//
// projectRoot lets mode:edit entries resolve their (project-relative) EditPath
// to a real on-disk repo. Pass "" when no project is in scope; mode:edit
// entries with a relative EditPath then resolve against the caller's cwd.
func (s *Syncer) Status(entry *model.LockEntry, projectRoot string) (*SyncStatus, error) {
	st := &SyncStatus{Name: entry.Name, Branch: entry.Ref, Commit: entry.Commit}
	if entry.IsLink() {
		// Link-installed skills aren't tracked via git.
		st.Message = "link"
		return st, nil
	}
	if entry.IsLocal() {
		// Immutable local copies (`qvr add --local`) are frozen and have no
		// git history — nothing to git-status. Report the mode rather than
		// falling through to a git-open that has nothing to open.
		st.Message = "local"
		return st, nil
	}
	// mode:edit entries authoritatively live at <projectRoot>/<EditPath>
	// (a real git repo). Shared entries live in the bare-clone worktree.
	// Previously Status only looked at EntryWorktreePath, so file edits in
	// the ejected dir were invisible to `qvr status` / `qvr diff` / `qvr
	// lock verify` (issue #69).
	repoPath := ResolveSkillRepoPath(entry, projectRoot)
	if repoPath == "" {
		repoPath = EntryWorktreePath(entry)
	}
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		// Edit-mode entries scaffolded via `qvr init` have no .git/ until
		// the user runs `git init` themselves — the directory IS the
		// skill, no git history required. Pre-#117 we surfaced this as
		// state=broken which made the most basic init→status flow look
		// like an integrity failure. Now we report "edit" so the user
		// sees the mode rather than a phantom defect.
		if entry.IsEdit() && repoPath != "" {
			if _, statErr := os.Stat(repoPath); statErr == nil {
				st.Message = "edit"
				return st, nil
			}
		}
		// Worktree-free consume install (#204): the content dir has no .git/,
		// so there's nothing to git-status. It's immutable and frozen
		// read-only, so it's never "dirty" in the git sense — report the locked
		// ref/commit. Content tampering surfaces via `qvr lock verify`, not here.
		if !entry.IsEdit() {
			skillDir := EffectiveTarget(entry, projectRoot)
			if skillDir != "" {
				if _, statErr := os.Stat(filepath.Join(skillDir, "SKILL.md")); statErr == nil {
					st.Message = "shared"
					return st, nil
				}
			}
		}
		st.Broken = true
		st.Message = fmt.Sprintf("worktree unreadable: %v", err)
		return st, nil
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("worktree handle: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	st.Dirty = !status.IsClean()

	head, err := repo.Head()
	if err == nil {
		st.Commit = head.Hash().String()
		if head.Name().IsBranch() {
			st.Branch = head.Name().Short()
		}
	}

	ahead, behind, _ := computeAheadBehind(repo, st.Branch)
	st.Ahead = ahead
	st.Behind = behind
	return st, nil
}

// Pull fetches origin and fast-forwards the worktree. When local history has
// diverged (both sides have new commits), Pull refuses to move HEAD and
// returns ErrDivergence so the user can resolve the situation themselves.
// Any working-tree dirtiness is similarly treated as non-fast-forward — we do
// not clobber uncommitted edits.
func (s *Syncer) Pull(ctx context.Context, entry *model.LockEntry) (string, error) {
	if entry.IsLink() {
		return "", errors.New("cannot pull a link install — it has no upstream")
	}
	if entry.IsLocal() {
		return "", errors.New("cannot pull a local install — it has no upstream; edit the source folder and re-run `qvr add --local`")
	}
	repo, err := gogit.PlainOpen(EntryWorktreePath(entry))
	if err != nil {
		return "", fmt.Errorf("open worktree: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree handle: %w", err)
	}
	// Refuse to pull into a dirty tree — a fast-forward-checkout would either
	// clobber changes or fail with an opaque error. Clear signal, clear recovery.
	status, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("status: %w", err)
	}
	if !status.IsClean() {
		return "", fmt.Errorf("%w: worktree has uncommitted changes", ErrDivergence)
	}

	// Fetch remote branch into refs/remotes/origin/<branch>.
	branch := entry.Ref
	if branch == "" {
		if head, err := repo.Head(); err == nil && head.Name().IsBranch() {
			branch = head.Name().Short()
		}
	}
	if branch == "" {
		return "", fmt.Errorf("cannot pull: branch is empty and HEAD is detached")
	}
	// Pull only makes sense for branches. If the pinned ref resolves as a tag
	// (no matching local branch, but a matching tag), surface a clear sentinel
	// the CLI can treat as a non-fatal skip — moving off a tag is an explicit
	// upgrade/switch, not a fast-forward pull.
	if _, err := repo.Reference(plumbing.NewTagReferenceName(branch), true); err == nil {
		if _, berr := repo.Reference(plumbing.NewBranchReferenceName(branch), true); berr != nil {
			return "", fmt.Errorf("%w: %s", ErrPinnedToTag, branch)
		}
	}
	if err := s.Git.FetchWorktree(ctx, EntryWorktreePath(entry)); err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	// go-git's storer caches pack-file indexes at PlainOpen time, so a repo
	// handle opened before `git fetch` doesn't see objects in packs the fetch
	// just wrote. Re-open after fetch so ref resolution + commit walks find
	// the freshly-arrived remote tip. Manifests as "ancestor check: object
	// not found" in batch `qvr pull` runs only (issue #8).
	repo, err = gogit.PlainOpen(EntryWorktreePath(entry))
	if err != nil {
		return "", fmt.Errorf("reopen worktree after fetch: %w", err)
	}

	localRef, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return "", fmt.Errorf("resolve local branch: %w", err)
	}
	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return "", fmt.Errorf("resolve remote branch: %w", err)
	}
	if localRef.Hash() == remoteRef.Hash() {
		return localRef.Hash().String(), nil
	}
	// Fast-forward check: local must be an ancestor of remote.
	isAncestor, err := isAncestorCommit(repo, localRef.Hash(), remoteRef.Hash())
	if err != nil {
		return "", fmt.Errorf("ancestor check: %w", err)
	}
	if !isAncestor {
		return "", fmt.Errorf("%w: local %s vs remote %s", ErrDivergence,
			shortHash(localRef.Hash().String()), shortHash(remoteRef.Hash().String()))
	}

	// The installed subtree is frozen read-only for immutability. A
	// fast-forward rewrites working-tree files, so unlock it, advance, then
	// re-freeze at the new content — the shared install stays immutable
	// between operations.
	subtree := filepath.Join(EntryWorktreePath(entry), entry.Path)
	setSubtreeWritable(subtree)
	defer setSubtreeReadOnly(subtree)

	// Fast-forward: move branch ref, then check out to update working tree.
	if err := repo.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName(branch), remoteRef.Hash(),
	)); err != nil {
		return "", fmt.Errorf("advance branch: %w", err)
	}
	if err := wt.Checkout(&gogit.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Force:  true,
	}); err != nil {
		return "", fmt.Errorf("checkout: %w", err)
	}
	// Re-apply sparse if configured — go-git's Checkout populates files
	// outside the configured sparse paths, so leaning on git to retrim
	// keeps the worktree consistent after a fast-forward pull.
	_ = s.Worktree.ReapplySparseCheckout(EntryWorktreePath(entry))
	return remoteRef.Hash().String(), nil
}

// computeAheadBehind counts how many commits the local branch is ahead and
// behind of refs/remotes/origin/<branch>. Returns zeros if the remote ref is
// missing (e.g. never fetched).
func computeAheadBehind(repo *gogit.Repository, branch string) (ahead, behind int, err error) {
	if branch == "" {
		return 0, 0, nil
	}
	localRef, err := repo.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		return 0, 0, nil
	}
	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return 0, 0, nil
	}
	ahead, err = countCommits(repo, localRef.Hash(), remoteRef.Hash())
	if err != nil {
		return 0, 0, err
	}
	behind, err = countCommits(repo, remoteRef.Hash(), localRef.Hash())
	if err != nil {
		return 0, 0, err
	}
	return ahead, behind, nil
}

// countCommits counts commits reachable from `from` but not from `base`.
// Rough ahead/behind proxy; enough for a status readout.
func countCommits(repo *gogit.Repository, from, base plumbing.Hash) (int, error) {
	if from == base {
		return 0, nil
	}
	commits, err := repo.Log(&gogit.LogOptions{From: from})
	if err != nil {
		return 0, fmt.Errorf("log from %s: %w", from.String(), err)
	}
	baseReach := make(map[plumbing.Hash]struct{})
	baseCommits, err := repo.Log(&gogit.LogOptions{From: base})
	if err != nil {
		return 0, fmt.Errorf("log from %s: %w", base.String(), err)
	}
	_ = baseCommits.ForEach(func(c *object.Commit) error {
		baseReach[c.Hash] = struct{}{}
		return nil
	})

	count := 0
	err = commits.ForEach(func(c *object.Commit) error {
		if _, ok := baseReach[c.Hash]; ok {
			return errStop
		}
		count++
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return 0, err
	}
	return count, nil
}

var errStop = errors.New("stop")

// isAncestorCommit returns true if `ancestor` is an ancestor of `head`.
func isAncestorCommit(repo *gogit.Repository, ancestor, head plumbing.Hash) (bool, error) {
	if ancestor == head {
		return true, nil
	}
	iter, err := repo.Log(&gogit.LogOptions{From: head})
	if err != nil {
		return false, err
	}
	found := false
	err = iter.ForEach(func(c *object.Commit) error {
		if c.Hash == ancestor {
			found = true
			return errStop
		}
		return nil
	})
	if err != nil && !errors.Is(err, errStop) {
		return false, err
	}
	return found, nil
}

func shortHash(h string) string {
	if len(h) <= 7 {
		return h
	}
	return h[:7]
}
