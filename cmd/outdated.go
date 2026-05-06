package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

// outdatedRow describes one installed skill's freshness against its registry.
type outdatedRow struct {
	Name      string `json:"name"`
	Registry  string `json:"registry"`
	Branch    string `json:"branch"`
	Local     string `json:"local_commit"`
	Remote    string `json:"remote_commit"`
	LatestTag string `json:"latest_tag,omitempty"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
}

const (
	outStateUpToDate    = "up-to-date"
	outStateBehind      = "behind"
	outStateUnreachable = "unreachable"
	outStateLink        = "link"
)

// remoteResult bundles a successful ls-remote read with any error so we can
// keep one entry per registry in the cache.
type remoteResult struct {
	refs *git.RemoteRefInfo
	err  error
}

var outdatedGlobal bool

var outdatedCmd = &cobra.Command{
	Use:   "outdated",
	Short: "Show installed skills with newer upstream commits",
	Long: `Per-registry git ls-remote, compared against each lock entry's pinned
commit. No objects are downloaded — use qvr pull to actually update.`,
	RunE: runOutdated,
}

func init() {
	outdatedCmd.Flags().BoolVar(&outdatedGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	rootCmd.AddCommand(outdatedCmd)
}

func runOutdated(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), outdatedGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	entries := lock.Entries()
	if len(entries) == 0 {
		printer.Info("No installed skills.")
		return nil
	}

	gc := git.NewGoGitClient()
	remotes := fetchRemotes(cmd.Context(), gc, cfg, entries)

	rows := make([]outdatedRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, computeOutdated(e, remotes[e.Registry]))
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(rows)
	}
	headers := []string{"SKILL", "BRANCH", "LOCAL", "REMOTE", "STATE"}
	tbl := make([][]string, 0, len(rows))
	for _, r := range rows {
		remoteCol := shortSHA(r.Remote)
		// Tag-pinned rows show the tag name a caller would move to so the user
		// can see v0.1.1 → v0.2.0 rather than two opaque SHAs.
		if r.LatestTag != "" {
			remoteCol = r.LatestTag
		}
		tbl = append(tbl, []string{r.Name, r.Branch, shortSHA(r.Local), remoteCol, r.State})
	}
	printer.Table(headers, tbl)
	return nil
}

// fetchRemotes calls ls-remote once per registry referenced by the lock. The
// per-registry result (refs or error) is cached so N skills from one registry
// only cost one network round-trip.
//
// The URL we ls-remote depends on Source: "registry" entries look up
// cfg.Registries[Registry]; "subdir" entries (qvr add) carry the URL on the
// lock entry as RepoURL because they intentionally don't appear in config.
func fetchRemotes(ctx context.Context, gc git.GitClient, cfg *config.Config, entries []*model.LockEntry) map[string]remoteResult {
	out := make(map[string]remoteResult)
	for _, e := range entries {
		if e.Source == "link" || e.Registry == "" {
			continue
		}
		if _, ok := out[e.Registry]; ok {
			continue
		}
		url, err := remoteURLFor(e, cfg)
		if err != nil {
			out[e.Registry] = remoteResult{err: err}
			continue
		}
		refs, err := gc.LsRemote(ctx, url)
		out[e.Registry] = remoteResult{refs: refs, err: err}
	}
	return out
}

// remoteURLFor picks the upstream URL for a lock entry. Subdir installs
// (`qvr add`) record the canonical clone URL on the entry; registry installs
// resolve through cfg.Registries.
func remoteURLFor(e *model.LockEntry, cfg *config.Config) (string, error) {
	if e.Source == "subdir" {
		if e.RepoURL == "" {
			return "", fmt.Errorf("subdir install %q has no recorded RepoURL — re-run `qvr add` to refresh the lock entry", e.Name)
		}
		return e.RepoURL, nil
	}
	regCfg, exists := cfg.Registries[e.Registry]
	if !exists {
		return "", fmt.Errorf("registry %q not configured", e.Registry)
	}
	return regCfg.URL, nil
}

// computeOutdated is the pure comparator: given a lock entry and the remote
// refs we got back for its registry, decide what state the skill is in.
// Pulling this out as a top-level function makes it directly testable without
// any network or filesystem.
func computeOutdated(entry *model.LockEntry, remote remoteResult) outdatedRow {
	row := outdatedRow{
		Name:     entry.Name,
		Registry: entry.Registry,
		Branch:   entry.Branch,
		Local:    entry.Commit,
	}
	if entry.Source == "link" {
		row.State = outStateLink
		return row
	}
	if remote.err != nil || remote.refs == nil {
		row.State = outStateUnreachable
		if remote.err != nil {
			row.Reason = remote.err.Error()
		}
		return row
	}
	remoteHash, matchKind, ok := lookupRemoteRefKind(remote.refs, entry.Branch)
	if !ok {
		row.State = outStateUnreachable
		row.Reason = fmt.Sprintf("ref %q not found on remote", entry.Branch)
		return row
	}
	row.Remote = remoteHash

	// Tag-pinned skills don't move when the pinned tag's commit is stable —
	// what "behind" should mean here is "a newer semver tag exists upstream".
	// Compare pinned tag against the highest-sorted semver tag in the remote
	// refs and, if newer, surface the new tag so `qvr upgrade` has a target.
	if matchKind == "tag" && model.IsSemverTag(entry.Branch) {
		if latestName, latestHash := latestSemverRemoteTag(remote.refs); latestName != "" && latestName != entry.Branch {
			row.LatestTag = latestName
			row.Remote = latestHash
			row.State = outStateBehind
			row.Reason = fmt.Sprintf("newer tag %s available", latestName)
			return row
		}
		row.State = outStateUpToDate
		return row
	}

	if remoteHash == entry.Commit {
		row.State = outStateUpToDate
	} else {
		row.State = outStateBehind
	}
	return row
}

// latestSemverRemoteTag scans the remote ref map for refs/tags/<semver> entries
// and returns the name+hash of the highest-sorted one. Returns ("", "") when
// the remote publishes no semver tags.
func latestSemverRemoteTag(refs *git.RemoteRefInfo) (string, string) {
	if refs == nil {
		return "", ""
	}
	var tags []string
	for ref := range refs.Refs {
		if !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		name := strings.TrimPrefix(ref, "refs/tags/")
		if strings.HasSuffix(name, "^{}") {
			// Peeled tag ref — ignore; the non-peeled entry carries the tag name we want.
			continue
		}
		if !model.IsSemverTag(name) {
			continue
		}
		tags = append(tags, name)
	}
	latest := skill.LatestSemverTag(tags)
	if latest == "" {
		return "", ""
	}
	return latest, refs.Refs["refs/tags/"+latest]
}

// lookupRemoteRef resolves a ref name (`main`, `v1.2.0`, …) against the
// ls-remote map. Tries refs/heads/<name> first, then refs/tags/<name>, so
// branch-pinned and tag-pinned installs both work without the caller having
// to know which it is.
func lookupRemoteRef(refs *git.RemoteRefInfo, name string) (string, bool) {
	h, _, ok := lookupRemoteRefKind(refs, name)
	return h, ok
}

// lookupRemoteRefKind is the kind-aware variant: returns "branch" or "tag"
// alongside the hash so callers can treat tag-pinned installs differently.
func lookupRemoteRefKind(refs *git.RemoteRefInfo, name string) (string, string, bool) {
	if name == "" || refs == nil {
		return "", "", false
	}
	if h, ok := refs.Refs["refs/heads/"+name]; ok {
		return h, "branch", true
	}
	if h, ok := refs.Refs["refs/tags/"+name]; ok {
		return h, "tag", true
	}
	return "", "", false
}

func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
