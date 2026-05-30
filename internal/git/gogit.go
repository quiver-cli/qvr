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
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
)

var (
	ErrRepoNotFound  = errors.New("repository not found")
	ErrCloneFailed   = errors.New("clone failed")
	ErrFetchFailed   = errors.New("fetch failed")
	ErrPushFailed    = errors.New("push failed")
	ErrRefNotFound   = errors.New("reference not found")
	ErrBlobNotFound  = errors.New("blob not found")
	ErrTreeNotFound  = errors.New("tree not found")
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

func (g *GoGitClient) BareClone(ctx context.Context, url, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, path)
	}
	// `--mirror` gives us a bare with all remote refs mapped directly into
	// refs/heads/* and refs/tags/* (not refs/remotes/origin/*). That's the
	// shape worktree clones from this bare need to see. `--` terminates
	// option parsing so a hostile URL can't be interpreted as a flag.
	if _, err := runGit(ctx, "clone", "--mirror", "--", url, path); err != nil {
		return classifyNetworkErr(err, ErrCloneFailed)
	}
	return nil
}

func (g *GoGitClient) Clone(ctx context.Context, url, path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, path)
	}
	if _, err := runGit(ctx, "clone", "--", url, path); err != nil {
		return classifyNetworkErr(err, ErrCloneFailed)
	}
	return nil
}

func (g *GoGitClient) Fetch(ctx context.Context, repoPath string) error {
	// Mirror-style refspecs keep local refs/heads/* and refs/tags/* in sync
	// with origin so a bare registry reflects upstream exactly. `--prune`
	// removes refs that were deleted upstream.
	_, err := runGit(ctx, "-C", repoPath, "fetch", "--prune", "--tags",
		"origin",
		"+refs/heads/*:refs/heads/*",
		"+refs/tags/*:refs/tags/*",
	)
	if err != nil {
		return classifyNetworkErr(err, ErrFetchFailed)
	}
	return nil
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
		if strings.HasPrefix(body, headsPrefix) {
			return strings.TrimPrefix(body, headsPrefix)
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

	// Try as a hash
	if len(ref) == 40 {
		return plumbing.NewHash(ref), nil
	}

	return plumbing.ZeroHash, fmt.Errorf("cannot resolve ref %q", ref)
}
