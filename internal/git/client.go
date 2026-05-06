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

	// HeadCommit returns the commit hash of HEAD.
	HeadCommit(repoPath string) (string, error)

	// DefaultBranch returns the name of the default branch.
	DefaultBranch(repoPath string) (string, error)

	// ReadBlob reads a file from the git object store at a given ref and path.
	ReadBlob(repoPath, ref, filePath string) ([]byte, error)

	// ListTree lists entries in a tree at a given ref and path.
	ListTree(repoPath, ref, path string) ([]TreeEntry, error)
}
