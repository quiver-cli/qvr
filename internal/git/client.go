package git

import "context"

// RefInfo holds information about a git reference.
type RefInfo struct {
	Name  string
	Hash  string
	IsTag bool
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

// GitClient provides git operations over bare and working repositories.
type GitClient interface {
	// BareClone clones a repository as a bare repo to the given path.
	BareClone(ctx context.Context, url, path string) error

	// Clone performs a full clone to the given path.
	Clone(ctx context.Context, url, path string) error

	// SubdirClone produces a partial, sparse-checkout clone of url at dest,
	// materializing only the files under subpath at ref. Use for ad-hoc
	// "install one skill from a multi-skill repo" — never downloads blobs
	// outside the subpath, and stays small even on large source repos.
	SubdirClone(ctx context.Context, url, ref, subpath, dest string) error

	// Fetch fetches all refs from the remote in a bare repository.
	Fetch(ctx context.Context, repoPath string) error

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
}
