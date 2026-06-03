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
// Scope is populated only when --all is set so single-lock JSON output stays
// backward-compatible.
type outdatedRow struct {
	Scope     string `json:"scope,omitempty"`
	Name      string `json:"name"`
	Registry  string `json:"registry"`
	Branch    string `json:"branch"`
	Local     string `json:"local_commit"`
	Remote    string `json:"remote_commit"`
	LatestTag string `json:"latest_tag,omitempty"`
	State     string `json:"state"`
	Reason    string `json:"reason,omitempty"`
	// Signature is the recorded git-native provenance status (verified |
	// none | invalid). Lets a reader tell "behind + none" (a newer version
	// is available but unsigned) from "behind + verified" at a glance.
	Signature string `json:"signature,omitempty"`
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

var (
	outdatedGlobal bool
	outdatedAll    bool
)

var outdatedCmd = &cobra.Command{
	Use:   "outdated",
	Short: "Show installed skills with newer upstream commits",
	Long: `Per-registry git ls-remote, compared against each lock entry's pinned
commit. No objects are downloaded — use qvr pull to actually update.

Defaults to the project lock; --global reads the user-global lock instead,
and --all unions both (adds a SCOPE column).`,
	RunE: runOutdated,
}

func init() {
	outdatedCmd.Flags().BoolVar(&outdatedGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	outdatedCmd.Flags().BoolVar(&outdatedAll, "all", false,
		"union project and global locks (adds a SCOPE column)")
	rootCmd.AddCommand(outdatedCmd)
}

func runOutdated(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, outdatedGlobal, outdatedAll)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Collect every entry up front so ls-remote runs once per registry
	// across both locks rather than re-fetching per-scope.
	var allEntries []*model.LockEntry
	for _, s := range locks {
		allEntries = append(allEntries, s.Lock.Entries()...)
	}
	if len(allEntries) == 0 {
		printer.Info("No installed skills.")
		return nil
	}

	gc := git.NewGoGitClient()
	remotes := fetchRemotes(cmd.Context(), gc, cfg, allEntries)

	var rows []outdatedRow
	for _, s := range locks {
		for _, e := range s.Lock.Entries() {
			row := computeOutdated(e, remotes[e.Registry])
			if outdatedAll {
				row.Scope = s.Scope
			}
			rows = append(rows, row)
		}
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(rows)
	}
	headers := []string{"SKILL", "BRANCH", "LOCAL", "REMOTE", "STATE", "SIGNED"}
	if outdatedAll {
		headers = append([]string{"SCOPE"}, headers...)
	}
	tbl := make([][]string, 0, len(rows))
	for _, r := range rows {
		remoteCol := shortSHA(r.Remote)
		// Tag-pinned rows show the tag name a caller would move to so the user
		// can see v0.1.1 → v0.2.0 rather than two opaque SHAs. Strip the
		// per-skill namespace prefix for display (#152).
		if r.LatestTag != "" {
			remoteCol = model.VersionPortion(r.LatestTag)
		}
		row := []string{r.Name, model.VersionPortion(r.Branch), shortSHA(r.Local), remoteCol, r.State, signedCol(r.Signature)}
		if outdatedAll {
			row = append([]string{r.Scope}, row...)
		}
		tbl = append(tbl, row)
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

// remoteURLFor picks the upstream URL for a lock entry. v5 carries the
// fetch URL on every non-link entry via entry.Source, so this is just a
// nil/empty guard plus a fallback to the registry config for entries
// (legacy or hand-edited) that ended up without a Source.
func remoteURLFor(e *model.LockEntry, cfg *config.Config) (string, error) {
	if e.IsLink() {
		return "", fmt.Errorf("link install %q has no upstream URL", e.Name)
	}
	if e.Source != "" {
		return e.Source, nil
	}
	if e.Registry != "" {
		if regCfg, ok := cfg.Registries[e.Registry]; ok {
			return regCfg.URL, nil
		}
		return "", fmt.Errorf("registry %q not configured", e.Registry)
	}
	return "", fmt.Errorf("entry %q has no source URL", e.Name)
}

// computeOutdated is the pure comparator: given a lock entry and the remote
// refs we got back for its registry, decide what state the skill is in.
// Pulling this out as a top-level function makes it directly testable without
// any network or filesystem.
func computeOutdated(entry *model.LockEntry, remote remoteResult) outdatedRow {
	row := outdatedRow{
		Name:      entry.Name,
		Registry:  entry.Registry,
		Branch:    entry.Ref,
		Local:     entry.Commit,
		Signature: recordedSigStatus(entry),
	}
	if entry.IsLink() {
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
	remoteHash, matchKind, ok := lookupRemoteRefKind(remote.refs, entry.Ref)
	if !ok {
		row.State = outStateUnreachable
		row.Reason = fmt.Sprintf("ref %q not found on remote", entry.Ref)
		return row
	}
	row.Remote = remoteHash

	// Tag-pinned skills don't move when the pinned tag's commit is stable —
	// what "behind" should mean here is "a newer semver tag exists upstream".
	// Compare pinned tag against the highest-sorted semver tag in the remote
	// refs and, if newer, surface the new tag so `qvr upgrade` has a target.
	if matchKind == "tag" && model.IsSemverTag(entry.Ref) {
		if latestName, latestHash := latestSemverRemoteTag(remote.refs, entry.Name); latestName != "" && latestName != entry.Ref {
			row.LatestTag = latestName
			row.Remote = latestHash
			row.State = outStateBehind
			row.Reason = fmt.Sprintf("newer tag %s available", model.VersionPortion(latestName))
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
// belonging to skillName and returns the name+hash of the highest-sorted one.
// Returns ("", "") when the remote publishes no semver tags for the skill.
// Scoping by skill keeps a multi-skill registry from reporting a sibling's
// newer tag as this skill's upgrade target (issue #152).
func latestSemverRemoteTag(refs *git.RemoteRefInfo, skillName string) (string, string) {
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
		if !model.IsSemverTag(name) || !model.TagBelongsToSkill(name, skillName) {
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
