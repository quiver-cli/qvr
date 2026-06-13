// Package git wraps all git operations behind the GitClient abstraction:
// a hybrid go-git implementation that shells out to the system git binary
// for network operations (so the user's credential helpers and SSH agent
// handle auth) and uses pure go-git for local reads, plus worktree
// management for materializing and editing installed skills.
package git

import (
	"context"
	"time"
)

// RefInfo holds information about a git reference.
type RefInfo struct {
	Name  string
	Hash  string
	IsTag bool
}

// RefVersion is a branch or tag enriched with the metadata of the commit it
// points at — the SHA, when it was authored, and the subject line. It powers
// the dashboard's per-skill version tree, where each installable ref is shown
// with a timestamp so a human can see how versions stack up over time.
type RefVersion struct {
	Name    string    `json:"name"`    // short ref name (branch or tag)
	Hash    string    `json:"hash"`    // full commit SHA the ref resolves to
	IsTag   bool      `json:"isTag"`   // tag vs branch
	Time    time.Time `json:"time"`    // committer time of the target commit
	Subject string    `json:"subject"` // first line of the commit message
}

// CommitNode is one commit in a version DAG: its hash, the hashes of its
// parents (the edges of the lineage graph), and the committer time + subject
// for labeling. It powers the dashboard's git-tree version view, where both the
// registry catalogue and a skill's observed lineage render as a real multi-lane
// commit graph. A parent hash may point outside the returned set when the walk
// is bounded by a limit — callers treat an absent parent as a graph root
// (truncated history), not an error.
type CommitNode struct {
	Hash    string    `json:"hash"`
	Parents []string  `json:"parents"`
	Time    time.Time `json:"time"`
	Subject string    `json:"subject"`
}

// RemoteRefInfo holds reference information from ls-remote.
type RemoteRefInfo struct {
	Refs map[string]string // ref name → hash
}

// TreeEntry represents an entry in a git tree object.
type TreeEntry struct {
	Name  string // File or directory name
	Path  string // Full path relative to repo root
	IsDir bool
	Hash  string
}

// CloneOptions controls what BareClone downloads. The two axes are independent:
// how many refs (breadth) and how much history per ref (depth).
type CloneOptions struct {
	// Depth bounds how much history is downloaded per fetched ref:
	//   - 0 → full history (every commit).
	//   - N → shallow clone, N commits deep (just the snapshot for N==1).
	Depth int

	// AllRefs selects breadth:
	//   - false (default) → clone ONLY the remote's default branch. No tags, no
	//     other branches. The fast cold-start path: a repo whose non-default
	//     branches carry heavy assets costs nothing to register. Installing a
	//     specific tag/older version isn't possible until re-cloned with AllRefs.
	//   - true → mirror every branch and tag (the `--full` path). Enables
	//     pinning any version, at the cost of downloading every ref's content.
	AllRefs bool
}

// GitClient provides git operations over bare and working repositories.
type GitClient interface {
	// BareClone clones a repository as a bare repo to the given path, with
	// breadth/depth governed by opts (see CloneOptions).
	BareClone(ctx context.Context, url, path string, opts CloneOptions) error

	// Clone performs a full clone to the given path.
	Clone(ctx context.Context, url, path string) error

	// SubdirClone produces a partial, sparse-checkout clone of url at dest,
	// materializing only the files under subpath at ref. Use for ad-hoc
	// "install one skill from a multi-skill repo" — never downloads blobs
	// outside the subpath, and stays small even on large source repos.
	SubdirClone(ctx context.Context, url, ref, subpath, dest string) error

	// Fetch fetches all refs from the remote in a bare repository.
	Fetch(ctx context.Context, repoPath string) error

	// DeepenToFull converts an existing latest-only (shallow, single-branch)
	// bare clone into a full clone IN PLACE: it rewrites the fetch refspec to
	// the all-branches + all-tags wildcards and fetches every ref, unshallowing
	// the history. After it returns, IsFullClone(repoPath) is true and any
	// tag/branch is installable. This backs `qvr registry add <url> --full` on a
	// registry that already exists (#184) — the deepen the install-time `--full`
	// hint promises, without a remove + re-add. A no-op on a clone that is
	// already full.
	DeepenToFull(ctx context.Context, repoPath string) error

	// FetchWorktree fetches origin into a non-bare worktree, updating
	// refs/remotes/origin/* only (does not touch local refs/heads/*).
	FetchWorktree(ctx context.Context, worktreePath string) error

	// Push pushes refspecs from repoPath to the named remote.
	Push(ctx context.Context, repoPath, remote string, refSpecs []string) error

	// ListBranches returns all branch refs in a repository.
	ListBranches(repoPath string) ([]RefInfo, error)

	// ListTags returns all tag refs in a repository.
	ListTags(repoPath string) ([]RefInfo, error)

	// LsRemote lists refs from a remote URL without cloning.
	LsRemote(ctx context.Context, url string) (*RemoteRefInfo, error)

	// RemoteDefaultBranch returns the branch name the remote considers its
	// default — what `git ls-remote --symref <url> HEAD` reports in the
	// "ref: refs/heads/<name>\tHEAD" header. Used by `qvr publish` to pick
	// the right target branch when the user didn't pass --branch and the
	// local stage's HEAD isn't authoritative (issue #95: prevents falling
	// back to a stale entry.Ref when upstream renamed master → main).
	//
	// Returns ("", nil) when the remote returns no symref line (empty
	// repos, or hosts that don't include the symref). Returns an error
	// only on transport failures — caller decides whether to fall through.
	RemoteDefaultBranch(ctx context.Context, url string) (string, error)

	// HeadCommit returns the commit hash of HEAD.
	HeadCommit(repoPath string) (string, error)

	// ResolveRef resolves a ref (branch, tag, or hash) in the repository at
	// repoPath to a full commit hash. Local-only — no network. Used by the
	// installer to derive a SHA-keyed worktree path from a human ref before
	// any worktree exists on disk, so two projects pinning the same SHA via
	// different ref labels share one worktree.
	ResolveRef(repoPath, ref string) (string, error)

	// DefaultBranch returns the name of the default branch.
	DefaultBranch(repoPath string) (string, error)

	// ReadBlob reads a file from the git object store at a given ref and path.
	ReadBlob(repoPath, ref, filePath string) ([]byte, error)

	// ListTree lists entries in a tree at a given ref and path.
	ListTree(repoPath, ref, path string) ([]TreeEntry, error)

	// ListBlobsRecursive returns every blob (file) reachable from `path` at
	// `ref`, with full repo-root-relative paths, descending into all subtrees.
	// Unlike ListTree (immediate children only), this walks the whole subtree.
	// Returned entries always have IsDir=false. An empty/blobless tree yields an
	// empty slice (not an error).
	ListBlobsRecursive(repoPath, ref, path string) ([]TreeEntry, error)

	// ListSubmodulePaths returns the repo-root-relative paths of every gitlink
	// (mode 160000 / submodule) tree entry reachable from the root tree at
	// `ref`. Gitlinks are invisible to ListBlobsRecursive (they are commit
	// pointers, not blobs), so callers that diagnose "committed a nested repo
	// instead of its files" need this dedicated walk (#241).
	ListSubmodulePaths(repoPath, ref string) ([]string, error)
}
