package git

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	gogit "github.com/go-git/go-git/v5"
	gogitcfg "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

var (
	// ErrRepoNotFound is what classifyNetworkErr maps a network failure to
	// when the remote reports the repository missing or unreadable.
	ErrRepoNotFound = errors.New("repository not found")
	// ErrCloneFailed is the fallback sentinel for clone subprocess failures
	// that aren't a missing repository.
	ErrCloneFailed = errors.New("clone failed")
	// ErrFetchFailed is the fallback sentinel for fetch and ls-remote
	// subprocess failures that aren't a missing repository.
	ErrFetchFailed = errors.New("fetch failed")
	// ErrPushFailed is the fallback sentinel for push subprocess failures
	// that aren't a missing repository.
	ErrPushFailed = errors.New("push failed")
	// ErrRefNotFound is returned when a ref (branch, tag, hash, or
	// revision) cannot be resolved in the local repository.
	ErrRefNotFound = errors.New("reference not found")
	// ErrBlobNotFound is returned by ReadBlob when the file does not exist
	// (or cannot be read) at the given ref and path.
	ErrBlobNotFound = errors.New("blob not found")
	// ErrTreeNotFound is returned by the tree-walking reads when the tree
	// at the given ref and path does not exist.
	ErrTreeNotFound = errors.New("tree not found")
	// ErrAlreadyExists is returned by the clone operations when the
	// destination path already exists on disk.
	ErrAlreadyExists = errors.New("repository already exists at path")
)

// GoGitClient implements GitClient using a hybrid strategy:
//
//   - Network operations (BareClone, Clone, Fetch, LsRemote, Push) shell out
//     to the system `git` binary so the user's credential helpers, SSH agent,
//     and SSO integrations "just work" for private repositories. Credentials
//     never enter qvr's address space.
//   - Local operations (ListBranches, ListTags, HeadCommit, DefaultBranch,
//     ReadBlob, ListTree) use go-git. They're faster, have no subprocess
//     overhead, and don't need auth.
type GoGitClient struct{}

// NewGoGitClient creates a new GoGitClient.
func NewGoGitClient() *GoGitClient {
	return &GoGitClient{}
}

// SubdirClone produces a partial, sparse-checkout clone of url at the given
// dest, materializing only the files under subpath at ref. Designed for
// "install one skill from a multi-skill repo" — never downloads blobs outside
// the subpath, and stays small even on 1GB+ source repos.
//
// We shell out to git rather than go-git because go-git's local-clone path
// chokes on large packfiles (pack rename failures) and doesn't speak partial
// clone (`--filter=blob:none`).
func (g *GoGitClient) SubdirClone(ctx context.Context, url, ref, subpath, dest string) error {
	if _, err := os.Stat(dest); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("create dest parent: %w", err)
	}
	// 1. Clone without a checkout, filtering blobs lazily so we never download
	//    files outside the subpath. `--no-tags` skips the full tag set; we
	//    only need the requested ref. We pass "--" to terminate option parsing
	//    so a hostile URL can't be interpreted as a flag.
	if _, err := runGit(ctx, "clone", "--no-checkout", "--filter=blob:none", "--no-tags", "--", url, dest); err != nil {
		_ = os.RemoveAll(dest)
		return classifyNetworkErr(err, ErrCloneFailed)
	}
	// 2. Restrict the working tree to subpath. `set --no-cone <path>` accepts
	//    a single path; the user's subpath may be deeply nested.
	if _, err := runGit(ctx, "-C", dest, "sparse-checkout", "init", "--no-cone"); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("sparse-checkout init: %w", err)
	}
	if _, err := runGit(ctx, "-C", dest, "sparse-checkout", "set", "--no-cone", "/"+strings.TrimPrefix(subpath, "/")); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("sparse-checkout set: %w", err)
	}
	// 3. Materialize the files at ref. This is the step that pulls down the
	//    blobs the partial clone deferred. `--detach` avoids creating a local
	//    branch tracking ref — we don't intend to commit from this clone.
	if strings.HasPrefix(ref, "-") {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("invalid ref %q: must not start with '-'", ref)
	}
	if _, err := runGit(ctx, "-C", dest, "checkout", "--detach", ref); err != nil {
		_ = os.RemoveAll(dest)
		return fmt.Errorf("checkout %s: %w", ref, err)
	}
	return nil
}

// BareClone clones url as a bare repository at path by shelling out to the
// system git, with breadth/depth governed by opts: the default is a
// latest-only clone of the remote's default branch, while AllRefs mirrors
// every branch and tag (but never refs/pull/*, unlike `--mirror`).
func (g *GoGitClient) BareClone(ctx context.Context, url, path string, opts CloneOptions) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, path)
	}
	args := []string{"clone"}
	if opts.AllRefs {
		// Full mode: every branch and tag, so any version is installable. We
		// deliberately do NOT use `--mirror`. `--mirror` maps the remote's
		// ENTIRE ref namespace (+refs/*:refs/*), which on GitHub repos pulls in
		// `refs/pull/*` — one or two refs for every PR ever opened, plus the
		// unreachable objects they carry. On a busy repo that's thousands of
		// refs of pure overhead (anthropics/skills: ~1,600 PR refs vs 14
		// branches) that grows forever and has nothing to do with installable
		// versions. A plain bare clone takes refs/heads/* and refs/tags/* and
		// ignores pull refs.
		args = append(args, "--bare")
		if opts.Depth > 0 {
			// `--depth` alone implies `--single-branch` (drops tags + other
			// branches), so `--no-single-branch` restores the full ref set
			// while still skipping deep history.
			args = append(args, fmt.Sprintf("--depth=%d", opts.Depth), "--no-single-branch")
		}
	} else {
		// Fast default: a bare clone of ONLY the remote's default branch. No
		// tags, no other branches — so a repo whose non-default branches carry
		// heavy assets costs almost nothing to register. The go-git indexer
		// still sees every skill (they live on the default branch) and can read
		// SKILL.md from the tip's tree.
		args = append(args, "--bare", "--single-branch")
		if opts.Depth > 0 {
			args = append(args, fmt.Sprintf("--depth=%d", opts.Depth))
		}
	}
	// `--` terminates option parsing so a hostile URL can't be read as a flag.
	args = append(args, "--", url, path)
	if _, err := runGit(ctx, args...); err != nil {
		return classifyNetworkErr(err, ErrCloneFailed)
	}
	if opts.AllRefs {
		// `git clone --bare` configures `+refs/heads/*:refs/heads/*` but no tags
		// refspec, so a later `git fetch` would only follow tags reachable from
		// fetched branches. Add an explicit tags refspec so `qvr registry
		// update` keeps every tag (including ones off any branch) — without it
		// we'd silently miss newly-published versions. We intentionally do NOT
		// add refs/pull/* — excluding PR refs is the whole point of not using
		// --mirror.
		if err := configureFullRefspec(path); err != nil {
			_ = os.RemoveAll(path)
			return err
		}
	} else {
		// A bare single-branch clone leaves remote.origin.fetch unset, so a
		// later `git fetch origin` (qvr registry update) would update nothing.
		// Wire up a single-branch refspec for the default branch so updates work
		// and stay scoped to that one branch.
		if err := configureSingleBranchFetch(path); err != nil {
			_ = os.RemoveAll(path)
			return err
		}
	}
	return nil
}

// configureFullRefspec ensures a full (AllRefs) bare clone fetches every branch
// and tag on update — and nothing else. `git clone --bare` already writes the
// branches refspec; we set heads+tags so versions published off any branch are
// still picked up. The absence of any refs/pull/* refspec is deliberate: that's
// what keeps full registries from re-pulling PR refs.
//
// This rewrites the local repo's config in-process via go-git rather than
// spawning `git config` subprocesses (#209/#203) — the repo is local, no auth
// is involved, and the proven precedent is internal/git/worktree.go's
// setOriginURL / setBranchTracking. Setting the Fetch slice wholesale is the
// deterministic equivalent of the old `--replace-all` + `--add`.
func configureFullRefspec(repoPath string) error {
	return setOriginFetch(repoPath, []gogitcfg.RefSpec{
		"+refs/heads/*:refs/heads/*",
		"+refs/tags/*:refs/tags/*",
	})
}

// configureSingleBranchFetch sets remote.origin.fetch to a single-branch
// refspec for the bare clone's current default branch, so `git fetch origin`
// updates exactly that branch (git doesn't configure a refspec for bare
// single-branch clones on its own). In-process via go-git (#209) — see
// configureFullRefspec.
func configureSingleBranchFetch(repoPath string) error {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	def, err := headBranchShort(repo)
	if err != nil || def == "" {
		return fmt.Errorf("could not determine default branch to configure fetch")
	}
	spec := gogitcfg.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/heads/%s", def, def))
	return setOriginFetchOn(repo, []gogitcfg.RefSpec{spec})
}

// headBranchShort returns the short name of the branch HEAD symbolically points
// to (e.g. "main"), matching `git symbolic-ref --short HEAD`. It reads HEAD
// without resolving the commit, so it works on a bare clone whose HEAD is a
// symref to refs/heads/<default>.
func headBranchShort(repo *gogit.Repository) (string, error) {
	ref, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		return "", err
	}
	if ref.Type() != plumbing.SymbolicReference {
		return "", fmt.Errorf("HEAD is not a symbolic reference")
	}
	return ref.Target().Short(), nil
}

// setOriginFetch opens the local repo and replaces remote.origin.fetch with the
// given refspecs, preserving the origin URL.
func setOriginFetch(repoPath string, fetch []gogitcfg.RefSpec) error {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	return setOriginFetchOn(repo, fetch)
}

// setOriginFetchOn replaces remote.origin.fetch on an already-open repo,
// preserving the existing origin URL (creating the remote entry if absent).
func setOriginFetchOn(repo *gogit.Repository, fetch []gogitcfg.RefSpec) error {
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	if cfg.Remotes == nil {
		cfg.Remotes = map[string]*gogitcfg.RemoteConfig{}
	}
	r, ok := cfg.Remotes["origin"]
	if !ok {
		r = &gogitcfg.RemoteConfig{Name: "origin"}
		cfg.Remotes["origin"] = r
	}
	r.Fetch = fetch
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("configure fetch refspec: %w", err)
	}
	return nil
}

// IsFullClone reports whether the bare repo at repoPath was cloned in `--full`
// mode (contains every branch and tag, so any version is installable). A
// single-branch "latest only" registry returns false. Used to decide whether a
// missing version means "re-fetch with --full" vs "this ref truly doesn't
// exist." Local-only; never errors out loud (treats any problem as "not full").
//
// Detection is by fetch refspec breadth: a full clone configures a wildcard
// refspec (+refs/heads/*:... and +refs/tags/*:...), whereas a single-branch
// clone pins one fully-qualified branch with no wildcard. A `*` in any
// configured fetch refspec therefore means full. This also recognises older
// registries that were cloned with `--mirror` (+refs/*:refs/*) as full.
func IsFullClone(repoPath string) bool {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return false
	}
	cfg, err := repo.Config()
	if err != nil {
		return false
	}
	r, ok := cfg.Remotes["origin"]
	if !ok {
		return false
	}
	for _, spec := range r.Fetch {
		if strings.Contains(string(spec), "*") {
			return true
		}
	}
	return false
}

// Clone performs a full working-tree clone of url at path by shelling out
// to the system git, so the user's credential helpers handle auth. Returns
// ErrAlreadyExists when path is already present on disk.
func (g *GoGitClient) Clone(ctx context.Context, url, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, path)
	}
	if _, err := runGit(ctx, "clone", "--", url, path); err != nil {
		return classifyNetworkErr(err, ErrCloneFailed)
	}
	return nil
}

// Fetch updates the bare repository at repoPath from origin via the system
// git, pruning refs deleted upstream. It fetches the clone's own configured
// refspec (full vs latest-only) and keeps shallow clones shallow.
func (g *GoGitClient) Fetch(ctx context.Context, repoPath string) error {
	// Update using the clone's OWN configured refspec rather than a hardcoded
	// `+refs/*:refs/*`. That keeps a registry in whatever mode it was cloned in:
	// a full registry has wildcard heads + tags refspecs and so syncs every
	// branch and tag (but not refs/pull/*); a latest-only registry has the
	// single-branch refspec BareClone wrote, so it stays scoped to its default
	// branch instead of silently ballooning into a full clone on the first
	// update. `--prune` removes refs deleted upstream.
	args := []string{"-C", repoPath, "fetch", "--prune"}
	// If the registry was cold-started shallow, keep updates shallow too —
	// otherwise a plain fetch back-fills the deep history we deliberately
	// skipped, undoing the fast-cold-start property on the first update.
	if isShallowRepo(repoPath) {
		args = append(args, "--depth=1")
	}
	args = append(args, "origin")
	if _, err := runGit(ctx, args...); err != nil {
		return classifyNetworkErr(err, ErrFetchFailed)
	}
	return nil
}

// DeepenToFull turns a latest-only (shallow, single-branch) bare clone into a
// full clone in place — the in-place counterpart to a fresh `--full` BareClone.
// Steps: rewrite the fetch refspec to the all-heads + all-tags wildcards (so
// IsFullClone flips true and future updates stay broad), then fetch every ref,
// unshallowing when the clone was shallow. PR refs are never configured, so the
// deepen stays free of refs/pull/* just like a fresh full clone.
func (g *GoGitClient) DeepenToFull(ctx context.Context, repoPath string) error {
	if IsFullClone(repoPath) {
		// Already full — still fetch so a deepen-on-a-full-registry behaves like
		// an update rather than a silent no-op, but skip the refspec rewrite.
		return g.Fetch(ctx, repoPath)
	}
	if err := configureFullRefspec(repoPath); err != nil {
		return err
	}
	// Fetch all heads + tags per the just-written refspec. `--unshallow` removes
	// the shallow marker and back-fills history when the clone was cold-started
	// shallow; on a full-history clone it's invalid, so only pass it when shallow.
	args := []string{"-C", repoPath, "fetch", "--prune", "--tags"}
	if isShallowRepo(repoPath) {
		args = append(args, "--unshallow")
	}
	args = append(args, "origin")
	if _, err := runGit(ctx, args...); err != nil {
		return classifyNetworkErr(err, ErrFetchFailed)
	}
	return nil
}

// isShallowRepo reports whether the git repo at repoPath was cloned shallow,
// by probing for git's `shallow` marker file. Bare repos keep it at
// <repo>/shallow; non-bare repos at <repo>/.git/shallow.
func isShallowRepo(repoPath string) bool {
	if _, err := os.Stat(filepath.Join(repoPath, "shallow")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(repoPath, ".git", "shallow")); err == nil {
		return true
	}
	return false
}

// FetchWorktree fetches origin into a non-bare worktree, updating
// refs/remotes/origin/* (NOT refs/heads/*, which would clobber the user's
// local branches). Used by the syncer during pull/upgrade.
func (g *GoGitClient) FetchWorktree(ctx context.Context, worktreePath string) error {
	_, err := runGit(ctx, "-C", worktreePath, "fetch", "--prune", "--tags", "--force",
		"origin",
		"+refs/heads/*:refs/remotes/origin/*",
		"+refs/tags/*:refs/tags/*",
	)
	if err != nil {
		return classifyNetworkErr(err, ErrFetchFailed)
	}
	return nil
}

// Push pushes the given refspecs from repoPath to the named remote. Used by
// the publisher and syncer; routed through the GitClient so the user's
// credential helper handles auth for private registries.
//
// When more than one refspec is supplied, the push is sent atomically
// (`git push --atomic`) so a partial failure (e.g. branch accepted, tag
// rejected — or vice versa) leaves neither ref landed on the remote
// instead of an orphan pair (issue #75). Single-refspec pushes go through
// unchanged so the `git protocol v0` happy path for older receive-packs
// isn't disturbed.
func (g *GoGitClient) Push(ctx context.Context, repoPath, remote string, refSpecs []string) error {
	if remote == "" {
		remote = "origin"
	}
	args := []string{"-C", repoPath, "push"}
	if len(refSpecs) > 1 {
		args = append(args, "--atomic")
	}
	args = append(args, remote)
	args = append(args, refSpecs...)
	if _, err := runGit(ctx, args...); err != nil {
		return classifyNetworkErr(err, ErrPushFailed)
	}
	return nil
}

// classifyNetworkErr inspects a git subprocess error and maps the well-known
// failure modes (missing repo, auth required) to typed sentinels so callers
// can render useful messages.
func classifyNetworkErr(err error, fallback error) error {
	msg := err.Error()
	lower := strings.ToLower(msg)
	mentionsRepo := strings.Contains(lower, "repository")
	switch {
	case mentionsRepo && strings.Contains(lower, "not found"),
		strings.Contains(lower, "could not read from remote"),
		strings.Contains(lower, "does not appear to be a git repository"):
		return fmt.Errorf("%w: %s", ErrRepoNotFound, msg)
	case strings.Contains(lower, "terminal prompts disabled"),
		strings.Contains(lower, "authentication failed"),
		strings.Contains(lower, "permission denied"),
		strings.Contains(lower, "could not read username"):
		return fmt.Errorf("%w: authentication required — configure git credentials (e.g. `gh auth login`, SSH key, or credential helper): %s",
			fallback, msg)
	}
	return fmt.Errorf("%w: %s", fallback, msg)
}

// ListBranches returns all local branch refs in the repository at repoPath,
// read with go-git (no subprocess, no network).
func (g *GoGitClient) ListBranches(repoPath string) ([]RefInfo, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	iter, err := repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}

	var refs []RefInfo
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		refs = append(refs, RefInfo{
			Name: ref.Name().Short(),
			Hash: ref.Hash().String(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterate branches: %w", err)
	}
	return refs, nil
}

// ListTags returns all tag refs in the repository at repoPath, read with
// go-git. Annotated tags are peeled so Hash is the target commit, not the
// tag object.
func (g *GoGitClient) ListTags(repoPath string) ([]RefInfo, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	iter, err := repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}

	var refs []RefInfo
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		hash := ref.Hash()
		// Resolve annotated tags to their target commit
		tagObj, err := repo.TagObject(hash)
		if err == nil {
			commit, err := tagObj.Commit()
			if err == nil {
				hash = commit.Hash
			}
		}
		refs = append(refs, RefInfo{
			Name:  ref.Name().Short(),
			Hash:  hash.String(),
			IsTag: true,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}
	return refs, nil
}

// RefVersions returns every branch and tag in the repo resolved to its target
// commit, enriched with the commit's committer time and subject line. Results
// are sorted newest-commit-first so the dashboard's version tree reads like a
// release timeline. Annotated tags are dereferenced to the commit they wrap.
// This is a concrete read helper (not on the GitClient interface) used only by
// the read-only dashboard, so it stays off the mockable surface.
func (g *GoGitClient) RefVersions(repoPath string) ([]RefVersion, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	// commitMeta resolves a hash (possibly an annotated-tag object) to its
	// underlying commit's time and subject. A ref we can't resolve still gets
	// listed — just without time/subject — so a partially-corrupt repo never
	// blanks the whole version tree.
	commitMeta := func(h plumbing.Hash) (time.Time, string, plumbing.Hash) {
		hash := h
		if tagObj, terr := repo.TagObject(h); terr == nil {
			if c, cerr := tagObj.Commit(); cerr == nil {
				hash = c.Hash
			}
		}
		c, cerr := repo.CommitObject(hash)
		if cerr != nil {
			return time.Time{}, "", hash
		}
		subject := c.Message
		if i := strings.IndexByte(subject, '\n'); i >= 0 {
			subject = subject[:i]
		}
		return c.Committer.When, strings.TrimSpace(subject), hash
	}

	var out []RefVersion

	branches, err := repo.Branches()
	if err != nil {
		return nil, fmt.Errorf("list branches: %w", err)
	}
	if err := branches.ForEach(func(ref *plumbing.Reference) error {
		when, subject, hash := commitMeta(ref.Hash())
		out = append(out, RefVersion{
			Name: ref.Name().Short(), Hash: hash.String(), IsTag: false,
			Time: when, Subject: subject,
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterate branches: %w", err)
	}

	tags, err := repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	if err := tags.ForEach(func(ref *plumbing.Reference) error {
		when, subject, hash := commitMeta(ref.Hash())
		out = append(out, RefVersion{
			Name: ref.Name().Short(), Hash: hash.String(), IsTag: true,
			Time: when, Subject: subject,
		})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}

	// Newest commit first; ties (same commit time) fall back to name so the
	// order is stable across runs.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// CommitGraph walks the commit ancestry reachable from tips (branch/tag names
// or commit hashes) and returns each commit as a CommitNode carrying its parent
// hashes, committer time, and subject — the data the dashboard's git-tree
// version view lays out into lanes. The walk is breadth-first from every tip,
// deduped by hash, and bounded to at most `limit` nodes (<=0 means unbounded)
// so a deep history can't blow up the read; when the bound is hit, frontier
// nodes' parent hashes simply point outside the returned set and the frontend
// renders them as roots. Tips are resolved through resolveRef (so refs and
// annotated tags both work) and any tip that won't resolve is skipped rather
// than failing the whole graph, so a partially-corrupt repo still yields a
// usable tree. Concrete read helper (not on the GitClient interface), used only
// by the read-only dashboard.
func (g *GoGitClient) CommitGraph(repoPath string, tips []string, limit int) ([]CommitNode, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	seen := make(map[plumbing.Hash]bool)
	var queue []plumbing.Hash
	enqueue := func(h plumbing.Hash) {
		if !seen[h] {
			seen[h] = true
			queue = append(queue, h)
		}
	}
	for _, t := range tips {
		if t == "" {
			continue
		}
		if h, rerr := resolveRef(repo, t); rerr == nil {
			enqueue(h)
		}
	}

	var out []CommitNode
	for len(queue) > 0 {
		if limit > 0 && len(out) >= limit {
			break
		}
		h := queue[0]
		queue = queue[1:]
		c, cerr := repo.CommitObject(h)
		if cerr != nil {
			continue // unresolvable tip/parent — skip, keep the rest of the graph
		}
		subject := c.Message
		if i := strings.IndexByte(subject, '\n'); i >= 0 {
			subject = subject[:i]
		}
		parents := make([]string, 0, len(c.ParentHashes))
		for _, p := range c.ParentHashes {
			parents = append(parents, p.String())
			enqueue(p)
		}
		out = append(out, CommitNode{
			Hash:    h.String(),
			Parents: parents,
			Time:    c.Committer.When,
			Subject: strings.TrimSpace(subject),
		})
	}

	// Newest commit first; stable hash tie-break for deterministic output.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].Hash < out[j].Hash
	})
	return out, nil
}

// LsRemote lists refs from a remote URL without cloning by shelling out to
// `git ls-remote`. Peeled annotated-tag lines (`^{}`) are folded in so tags
// map to their commit hash.
func (g *GoGitClient) LsRemote(ctx context.Context, url string) (*RemoteRefInfo, error) {
	out, err := runGit(ctx, "ls-remote", "--", url)
	if err != nil {
		return nil, classifyNetworkErr(err, ErrFetchFailed)
	}
	return parseLsRemote(bytes.NewReader(out))
}

// RemoteDefaultBranch queries the remote's HEAD symref via
// `git ls-remote --symref <url> HEAD`. The output's first line is
// "ref: refs/heads/<name>\tHEAD" when the remote has a default branch.
// Returns "" (no error) for empty repos / hosts that omit the symref so
// the caller can fall through to the next fallback (issue #95).
func (g *GoGitClient) RemoteDefaultBranch(ctx context.Context, url string) (string, error) {
	out, err := runGit(ctx, "ls-remote", "--symref", "--", url, "HEAD")
	if err != nil {
		return "", classifyNetworkErr(err, ErrFetchFailed)
	}
	return parseSymrefHead(bytes.NewReader(out)), nil
}

// parseSymrefHead pulls the branch name out of `git ls-remote --symref`
// output. The first line of a populated remote looks like
// "ref: refs/heads/main\tHEAD"; we trim the prefix and the trailing
// "\tHEAD". Returns "" when no such line is present (empty repos, or
// hosts that don't include the symref header at all).
func parseSymrefHead(r io.Reader) string {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4*1024), 64*1024)
	for scanner.Scan() {
		line := scanner.Text()
		const prefix = "ref: "
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		body := strings.TrimPrefix(line, prefix)
		// Body is "refs/heads/<name>\tHEAD". Drop the trailing target —
		// we only care that HEAD is the one being resolved (callers ask
		// specifically for HEAD), and the branch name lives in the ref.
		if tab := strings.IndexByte(body, '\t'); tab >= 0 {
			body = body[:tab]
		}
		body = strings.TrimSpace(body)
		const headsPrefix = "refs/heads/"
		if after, ok := strings.CutPrefix(body, headsPrefix); ok {
			return after
		}
		// Non-branch HEAD (e.g. detached, tag symref) — caller treats
		// as "no signal" and falls through.
		return ""
	}
	return ""
}

// parseLsRemote parses the output of `git ls-remote`. Each line is
// "<40-hex-hash>\t<ref-name>". Peeled annotated-tag refs appear as
// "refs/tags/v1.0.0^{}" and are normalised by dropping the `^{}` suffix so
// the final map resolves annotated tags to their commit hash (matching
// go-git's LsRemote semantics).
func parseLsRemote(r io.Reader) (*RemoteRefInfo, error) {
	result := &RemoteRefInfo{Refs: make(map[string]string)}
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		hash, ref := parts[0], parts[1]
		// Peeled tag: prefer the commit hash over the tag-object hash.
		ref = strings.TrimSuffix(ref, "^{}")
		result.Refs[ref] = hash
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse ls-remote: %w", err)
	}
	return result, nil
}

// HeadCommit returns the full commit hash HEAD resolves to in the repository
// at repoPath, read with go-git.
func (g *GoGitClient) HeadCommit(repoPath string) (string, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	return head.Hash().String(), nil
}

// ResolveRef resolves a ref (branch, tag, or hash) to a full commit hash by
// trying each ref namespace in turn — local branch, tag (peeled to commit if
// it's an annotated tag), then a generic revision parse covering hashes,
// remote-tracking refs, and abbreviations. Returns the canonical commit hash
// or an error if none of the namespaces match.
func (g *GoGitClient) ResolveRef(repoPath, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("ref is empty")
	}
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}
	if r, err := repo.Reference(plumbing.NewBranchReferenceName(ref), true); err == nil {
		return r.Hash().String(), nil
	}
	if r, err := repo.Reference(plumbing.NewTagReferenceName(ref), true); err == nil {
		hash := r.Hash()
		if tagObj, err := repo.TagObject(hash); err == nil {
			if commit, err := tagObj.Commit(); err == nil {
				hash = commit.Hash
			}
		}
		return hash.String(), nil
	}
	if resolved, err := repo.ResolveRevision(plumbing.Revision(ref)); err == nil && resolved != nil {
		return resolved.String(), nil
	}
	return "", fmt.Errorf("%w: %q", ErrRefNotFound, ref)
}

// DefaultBranch returns the name of the default branch: the branch HEAD
// points at, or — when HEAD is detached — a best-effort pick favouring
// "main"/"master" over the first branch found, falling back to "main".
func (g *GoGitClient) DefaultBranch(repoPath string) (string, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return "", fmt.Errorf("open repo: %w", err)
	}

	head, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}

	if head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	// HEAD is detached; best-effort branch discovery — fall back to "main" on any error
	branches, err := repo.Branches()
	if err != nil {
		return "main", nil
	}
	var fallback string
	_ = branches.ForEach(func(ref *plumbing.Reference) error { //nolint:errcheck // best-effort
		if fallback == "" {
			fallback = ref.Name().Short()
		}
		if ref.Name().Short() == "main" || ref.Name().Short() == "master" {
			fallback = ref.Name().Short()
		}
		return nil
	})
	if fallback != "" {
		return fallback, nil
	}
	return "main", nil
}

// ReadBlob reads the file at filePath from the git object store at ref using
// go-git — no checkout, no working tree. Returns ErrRefNotFound when ref
// doesn't resolve and ErrBlobNotFound when the file isn't there.
func (g *GoGitClient) ReadBlob(repoPath, ref, filePath string) ([]byte, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	hash, err := resolveRef(repo, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve ref %q: %v", ErrRefNotFound, ref, err)
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: get commit: %v", ErrBlobNotFound, err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w: get tree: %v", ErrBlobNotFound, err)
	}

	file, err := tree.File(filePath)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, filePath)
	}

	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrBlobNotFound, filePath, err)
	}
	defer reader.Close()

	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("%w: read %s: %v", ErrBlobNotFound, filePath, err)
	}
	return data, nil
}

// ListTree lists the immediate entries of the tree at ref and path (repo
// root when path is empty) using go-git, with each entry's Path made
// repo-root-relative. Returns ErrTreeNotFound when the tree doesn't exist.
func (g *GoGitClient) ListTree(repoPath, ref, path string) ([]TreeEntry, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	hash, err := resolveRef(repo, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve ref %q: %v", ErrRefNotFound, ref, err)
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: get commit: %v", ErrTreeNotFound, err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w: get root tree: %v", ErrTreeNotFound, err)
	}

	// Navigate to subtree if path is specified
	if path != "" {
		tree, err = tree.Tree(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrTreeNotFound, path, err)
		}
	}

	var entries []TreeEntry
	for _, entry := range tree.Entries {
		fullPath := entry.Name
		if path != "" {
			fullPath = path + "/" + entry.Name
		}
		entries = append(entries, TreeEntry{
			Name:  entry.Name,
			Path:  fullPath,
			IsDir: entry.Mode == filemode.Dir,
			Hash:  entry.Hash.String(),
		})
	}
	return entries, nil
}

// ListBlobsRecursive returns every blob reachable from path at ref using
// go-git's recursive tree walk, with repo-root-relative paths and IsDir
// always false. An empty or blobless tree yields an empty slice, not an
// error.
func (g *GoGitClient) ListBlobsRecursive(repoPath, ref, path string) ([]TreeEntry, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}

	hash, err := resolveRef(repo, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve ref %q: %v", ErrRefNotFound, ref, err)
	}

	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: get commit: %v", ErrTreeNotFound, err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w: get root tree: %v", ErrTreeNotFound, err)
	}

	if path != "" {
		tree, err = tree.Tree(path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrTreeNotFound, path, err)
		}
	}

	// tree.Files() yields every blob reachable from this tree, recursively,
	// with names relative to the tree root. Prefix the base path back on so
	// callers always get repo-root-relative paths.
	var entries []TreeEntry
	err = tree.Files().ForEach(func(f *object.File) error {
		full := f.Name
		if path != "" {
			full = path + "/" + f.Name
		}
		name := full
		if i := strings.LastIndex(full, "/"); i >= 0 {
			name = full[i+1:]
		}
		entries = append(entries, TreeEntry{
			Name:  name,
			Path:  full,
			IsDir: false,
			Hash:  f.Hash.String(),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk blobs: %w", err)
	}
	return entries, nil
}

// ListSubmodulePaths returns the repo-root-relative paths of every gitlink
// (mode 160000 / submodule) tree entry at ref. These entries are commit
// pointers, not blobs, so tree.Files() — and therefore ListBlobsRecursive —
// never surfaces them; this walk exists so the registry indexer can diagnose
// "a nested repo was committed instead of its files" (#241).
func (g *GoGitClient) ListSubmodulePaths(repoPath, ref string) ([]string, error) {
	repo, err := gogit.PlainOpen(repoPath)
	if err != nil {
		return nil, fmt.Errorf("open repo: %w", err)
	}
	hash, err := resolveRef(repo, ref)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve ref %q: %v", ErrRefNotFound, ref, err)
	}
	commit, err := repo.CommitObject(hash)
	if err != nil {
		return nil, fmt.Errorf("%w: get commit: %v", ErrTreeNotFound, err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("%w: get root tree: %v", ErrTreeNotFound, err)
	}
	var paths []string
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, entry, werr := walker.Next()
		if werr == io.EOF {
			break
		}
		if werr != nil {
			return nil, fmt.Errorf("walk tree: %w", werr)
		}
		if entry.Mode == filemode.Submodule {
			paths = append(paths, name)
		}
	}
	return paths, nil
}

// resolveRef resolves a ref string (branch name, tag, "HEAD", or hash) to a commit hash.
func resolveRef(repo *gogit.Repository, ref string) (plumbing.Hash, error) {
	if ref == "HEAD" {
		head, err := repo.Head()
		if err != nil {
			return plumbing.ZeroHash, err
		}
		return head.Hash(), nil
	}

	// Try as a branch
	branchRef, err := repo.Reference(plumbing.NewBranchReferenceName(ref), true)
	if err == nil {
		return branchRef.Hash(), nil
	}

	// Try as a tag
	tagRef, err := repo.Reference(plumbing.NewTagReferenceName(ref), true)
	if err == nil {
		// Resolve annotated tag
		tagObj, err := repo.TagObject(tagRef.Hash())
		if err == nil {
			commit, err := tagObj.Commit()
			if err == nil {
				return commit.Hash, nil
			}
		}
		return tagRef.Hash(), nil
	}

	// Try as a hash — full (40 char) or an abbreviated short SHA. Skill spans
	// record skill.commit as a 7-char short SHA when identity is proved through
	// a store worktree path, so the version-graph walk passes short SHAs as tips;
	// resolving them is what lets the lineage graph render for historical
	// versions (the common case after a pin moves forward).
	if isHexString(ref) {
		if len(ref) == 40 {
			return plumbing.NewHash(ref), nil
		}
		if len(ref) >= 4 && len(ref) < 40 {
			if h, ok := resolveShortHash(repo, ref); ok {
				return h, nil
			}
		}
	}

	return plumbing.ZeroHash, fmt.Errorf("cannot resolve ref %q", ref)
}

// isHexString reports whether s is non-empty and all lowercase hex digits — the
// shape of a (possibly abbreviated) commit hash.
func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// resolveShortHash expands an abbreviated commit-hash prefix to its full hash by
// scanning commit objects. It resolves only when exactly one commit matches: an
// ambiguous prefix (two commits sharing it — possible in a large registry, where
// git itself errors) returns false so the caller falls back rather than
// silently decorating the wrong commit. O(commits); reached only for hex strings
// that aren't a branch or tag (actual short SHAs), and only on the read-only
// dashboard's version-graph path.
func resolveShortHash(repo *gogit.Repository, prefix string) (plumbing.Hash, bool) {
	iter, err := repo.CommitObjects()
	if err != nil {
		return plumbing.ZeroHash, false
	}
	defer iter.Close()

	errStop := errors.New("stop")
	var found plumbing.Hash
	count := 0
	walkErr := iter.ForEach(func(c *object.Commit) error {
		if strings.HasPrefix(c.Hash.String(), prefix) {
			found = c.Hash
			count++
			if count > 1 {
				return errStop // ambiguous — stop and refuse to guess
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errStop) {
		return plumbing.ZeroHash, false
	}
	return found, count == 1
}
