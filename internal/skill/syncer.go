package skill

import (
	"context"
	"errors"
	"fmt"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
)

var (
	ErrPushNoChanges = errors.New("no local changes to push")
	ErrDivergence    = errors.New("local and upstream histories have diverged; resolve manually")
	ErrPinnedToTag   = errors.New("skill is pinned to a tag; use 'qvr upgrade' or 'qvr switch' to move it")
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

// PushOptions controls the behaviour of Syncer.Push.
type PushOptions struct {
	Message     string
	Author      string
	AuthorEmail string
	AllowEmpty  bool
}

// Syncer implements pull/push/status/switch for installed skills.
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
func (s *Syncer) Status(entry *model.LockEntry) (*SyncStatus, error) {
	st := &SyncStatus{Name: entry.Name, Branch: entry.Ref, Commit: entry.ResolvedSHA}
	if entry.Source == "link" {
		// Link-installed skills aren't tracked via git.
		st.Message = "link"
		return st, nil
	}
	repo, err := gogit.PlainOpen(entry.Worktree)
	if err != nil {
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

// Push stages all changes in the worktree, commits with the caller's message
// (when there's anything to commit), and pushes to origin. Returns the new
// commit hash on success.
//
// A push on a clean tree without AllowEmpty returns ErrPushNoChanges — we
// treat "nothing to do" as a distinct, non-fatal state callers can report.
func (s *Syncer) Push(ctx context.Context, entry *model.LockEntry, opts PushOptions) (string, error) {
	if entry.Source == "link" {
		return "", errors.New("cannot push a link install — edit the source directly")
	}
	if opts.Message == "" {
		opts.Message = fmt.Sprintf("qvr: update %s", entry.Name)
	}
	if opts.Author == "" {
		opts.Author = "quiver"
	}
	if opts.AuthorEmail == "" {
		opts.AuthorEmail = "quiver@localhost"
	}

	repo, err := gogit.PlainOpen(entry.Worktree)
	if err != nil {
		return "", fmt.Errorf("open worktree: %w", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree handle: %w", err)
	}
	status, err := wt.Status()
	if err != nil {
		return "", fmt.Errorf("status: %w", err)
	}
	if status.IsClean() && !opts.AllowEmpty {
		return "", ErrPushNoChanges
	}

	if !status.IsClean() {
		if err := wt.AddWithOptions(&gogit.AddOptions{All: true}); err != nil {
			return "", fmt.Errorf("stage changes: %w", err)
		}
	}
	commit, err := wt.Commit(opts.Message, &gogit.CommitOptions{
		Author: &object.Signature{
			Name:  opts.Author,
			Email: opts.AuthorEmail,
			When:  time.Now(),
		},
		AllowEmptyCommits: opts.AllowEmpty,
	})
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}

	branch := entry.Ref
	if branch == "" {
		if head, err := repo.Head(); err == nil && head.Name().IsBranch() {
			branch = head.Name().Short()
		}
	}
	if branch == "" {
		return "", fmt.Errorf("cannot push: branch is empty and HEAD is detached")
	}
	refspec := fmt.Sprintf("refs/heads/%s:refs/heads/%s", branch, branch)
	if err := s.Git.Push(ctx, entry.Worktree, "origin", []string{refspec}); err != nil {
		return "", fmt.Errorf("push: %w", err)
	}
	return commit.String(), nil
}

// Pull fetches origin and fast-forwards the worktree. When local history has
// diverged (both sides have new commits), Pull refuses to move HEAD and
// returns ErrDivergence so the user can resolve the situation themselves.
// Any working-tree dirtiness is similarly treated as non-fast-forward — we do
// not clobber uncommitted edits.
func (s *Syncer) Pull(ctx context.Context, entry *model.LockEntry) (string, error) {
	if entry.Source == "link" {
		return "", errors.New("cannot pull a link install — it has no upstream")
	}
	repo, err := gogit.PlainOpen(entry.Worktree)
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
	if err := s.Git.FetchWorktree(ctx, entry.Worktree); err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	// go-git's storer caches pack-file indexes at PlainOpen time, so a repo
	// handle opened before `git fetch` doesn't see objects in packs the fetch
	// just wrote. Re-open after fetch so ref resolution + commit walks find
	// the freshly-arrived remote tip. Manifests as "ancestor check: object
	// not found" in batch `qvr pull` runs only (issue #8).
	repo, err = gogit.PlainOpen(entry.Worktree)
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
	_ = s.Worktree.ReapplySparseCheckout(entry.Worktree)
	return remoteRef.Hash().String(), nil
}

// CreateEditBranch creates a new local branch at the worktree and switches
// onto it. If `newBranch` already exists on origin (e.g. from a prior
// `qvr push` session), the local branch is planted at the remote tip rather
// than the worktree's current HEAD — otherwise the local branch trails
// origin and a later push is non-fast-forward, which tempts the user into
// `git push --force` and silently loses upstream commits (bug #15).
//
// Returns the updated lock entry plus a human-readable warning string
// (empty when the happy path applies). Callers own ApplySwitch +
// lock.Put + lock.Write.
func (s *Syncer) CreateEditBranch(ctx context.Context, entry *model.LockEntry, newBranch string) (*model.LockEntry, string, error) {
	if entry.Source == "link" {
		return nil, "", errors.New("cannot edit a link install — modify the source directly")
	}

	var warning string
	fromOriginTip := false
	localExists := branchExistsLocally(entry.Worktree, newBranch)

	// Fetch origin so we can tell whether the edit branch already exists
	// upstream. Best-effort: an offline or credential-less run falls through
	// to branching from local HEAD, with a soft warning so the user knows
	// the upstream check was skipped.
	if s.Git != nil {
		if err := s.Git.FetchWorktree(ctx, entry.Worktree); err != nil {
			warning = fmt.Sprintf("could not fetch origin before edit (%s); branching from local HEAD", err)
		} else if repo, err := gogit.PlainOpen(entry.Worktree); err == nil {
			remoteRef := plumbing.NewRemoteReferenceName("origin", newBranch)
			if rr, rerr := repo.Reference(remoteRef, true); rerr == nil {
				fromOriginTip = true
				warning = fmt.Sprintf("branch %q already exists on origin at %s; checked out origin tip (pass --branch for a fresh branch)", newBranch, shortHash(rr.Hash().String()))
			}
		}
	}

	switch {
	case localExists:
		// Local branch survived from a prior `qvr edit` session (usually
		// followed by `qvr switch`/`upgrade` that moved HEAD away). Switch
		// back onto it rather than hard-erroring from CreateBranch* with
		// "branch already exists" (bug #21, regression of #15). Preserves
		// any local-only commits; the user can `qvr pull` to fast-forward.
		if err := s.Worktree.Checkout(entry.Worktree, newBranch); err != nil {
			return nil, warning, fmt.Errorf("checkout existing edit branch %s: %w", newBranch, err)
		}
		if !fromOriginTip {
			warning = fmt.Sprintf("local branch %q already exists; switched onto it (pass --branch for a fresh branch)", newBranch)
		}
	case fromOriginTip:
		if err := s.Worktree.CreateBranchFromRef(entry.Worktree, newBranch, newBranch); err != nil {
			return nil, warning, fmt.Errorf("create edit branch from origin/%s: %w", newBranch, err)
		}
	default:
		if err := s.Worktree.CreateBranchFromHEAD(entry.Worktree, newBranch); err != nil {
			return nil, warning, fmt.Errorf("create edit branch: %w", err)
		}
	}
	commit, err := s.Git.HeadCommit(entry.Worktree)
	if err != nil {
		return nil, warning, fmt.Errorf("head commit: %w", err)
	}
	updated := *entry
	updated.Ref = newBranch
	updated.ResolvedSHA = commit
	updated.UpdatedAt = time.Now().UTC()
	return &updated, warning, nil
}

// Switch moves the worktree to a different ref. The lock entry is updated to
// reflect the new branch and commit. The worktree directory is renamed to
// match the new ref; callers must refresh symlinks via Installer.Install.
// (In practice the cmd layer handles that orchestration.)
//
// If the ref isn't resolvable locally, Switch fetches origin once with tag
// refs and retries — lets `qvr upgrade` pick up tags published since the
// worktree was first cloned without forcing the user to run a separate fetch.
func (s *Syncer) Switch(ctx context.Context, entry *model.LockEntry, newRef string) (*model.LockEntry, error) {
	if entry.Source == "link" {
		return nil, errors.New("cannot switch a link install")
	}
	err := s.Worktree.Checkout(entry.Worktree, newRef)
	if err != nil && errors.Is(err, git.ErrRefNotFound) {
		if fetchErr := s.Git.FetchWorktree(ctx, entry.Worktree); fetchErr == nil {
			err = s.Worktree.Checkout(entry.Worktree, newRef)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("checkout %s: %w", newRef, err)
	}
	commit, err := s.Git.HeadCommit(entry.Worktree)
	if err != nil {
		return nil, fmt.Errorf("head commit: %w", err)
	}
	updated := *entry
	updated.Ref = newRef
	updated.ResolvedSHA = commit
	updated.UpdatedAt = time.Now().UTC()
	return &updated, nil
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

// branchExistsLocally reports whether refs/heads/<branch> exists in the
// worktree's git storage. Used by CreateEditBranch to choose between
// "create new branch" and "re-adopt an existing one".
func branchExistsLocally(worktreePath, branch string) bool {
	repo, err := gogit.PlainOpen(worktreePath)
	if err != nil {
		return false
	}
	_, err = repo.Reference(plumbing.NewBranchReferenceName(branch), false)
	return err == nil
}
