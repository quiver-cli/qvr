package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/spf13/cobra"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage skill registries",
	// Reject a typo'd subcommand (`qvr registry ad <url>`) with a non-zero exit
	// instead of silently printing help (issue #169 — the #120 fix missed this
	// parent, and registry's subcommands mutate config). No args still prints help.
	RunE: rejectUnknownSubcommand,
}

var registryAddCmd = &cobra.Command{
	Use:   "add <url>",
	Short: "Register a Git repository as a skill source",
	Long: `Register a Git repository as a source of skills. The repo can hold one
skill or many — the indexer walks it on first clone.

The name is inferred from the URL as <org>/<repo> and the bare clone lives at
~/.quiver/registries/<name>.git/ — so the on-disk path is nested by
construction (e.g. ~/.quiver/registries/anthropics/skills.git/). Override with
--name when two repos share the same inferred name, or when the URL doesn't
carry a usable last segment.

  qvr registry add https://github.com/acme-labs/agent-skills
  qvr registry add git@github.com:org/repo.git --name internal-tools

By default only the remote's default branch is cloned, shallow (latest snapshot,
no history) — so cold start stays fast even when a repo's other branches carry
heavy assets. Every skill is still indexed (skills live on the default branch).
Tags and other branches are NOT fetched, so installing a specific version is not
possible until you re-add with --full.

  qvr registry add <url> --full          # all branches + tags + history (any version installable)
  qvr registry add <url> --depth 50      # 50 commits of the default branch's history

GitHub /tree/<ref>/<path> and /blob/<ref>/<path> web URLs are rejected with
an explanatory error — git can't clone a subdirectory; pass the repo URL.`,
	Args: cobra.ExactArgs(1),
	RunE: runRegistryAdd,
}

var (
	registryAddName  string
	registryAddDepth int
	registryAddFull  bool
)

var registryRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove a registry and its cached data",
	Args:  cobra.ExactArgs(1),
	RunE:  runRegistryRemove,
}

var registryListCmd = &cobra.Command{
	Use:   "list [name...]",
	Short: "List configured registries, or skills within named registries",
	Long: `List all configured registries when called with no arguments.
When one or more registry names are given, list the skills contained in
each of those registries.`,
	Args: cobra.ArbitraryArgs,
	RunE: runRegistryList,
}

var (
	registryUpdateCheck   bool
	registryUpdateVerbose bool
	registryListFull      bool
	registryListRefresh   bool
)

var registryUpdateCmd = &cobra.Command{
	Use:   "update [name]",
	Short: "Fetch latest changes from registries",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runRegistryUpdate,
}

func init() {
	registryAddCmd.Flags().StringVar(&registryAddName, "name", "",
		"override the auto-inferred <org>/<repo> name (full literal override; pass <alias> or <org>/<alias>)")
	registryAddCmd.Flags().IntVar(&registryAddDepth, "depth", registry.DefaultCloneDepth,
		"history depth of the default-branch clone (1 = latest snapshot only). Ignored when --full is set")
	registryAddCmd.Flags().BoolVar(&registryAddFull, "full", false,
		"clone all branches, tags, and full history — needed to install specific tags or older versions")
	registryUpdateCmd.Flags().BoolVar(&registryUpdateCheck, "check", false,
		"check for upstream changes without downloading")
	registryUpdateCmd.Flags().BoolVarP(&registryUpdateVerbose, "verbose", "v", false,
		"print per-skill skip reasons when any skills could not be indexed")
	registryListCmd.Flags().BoolVar(&registryListFull, "full", false,
		"print full descriptions without truncation")
	registryListCmd.Flags().BoolVar(&registryListRefresh, "refresh", false,
		"invalidate cached indexes before listing (local rebuild; no network)")
	registryCmd.AddCommand(registryAddCmd, registryRemoveCmd, registryListCmd, registryUpdateCmd)
	rootCmd.AddCommand(registryCmd)
}

func runRegistryAdd(cmd *cobra.Command, args []string) error {
	repoURL := args[0]

	if err := rejectWebURL(repoURL); err != nil {
		return err
	}

	// Sanitize before naming — we don't want an embedded token surfacing in
	// the inferred slug (and the Manager will reject any name that contains
	// credentials-shaped characters anyway).
	cleanURL, _, err := git.SanitizeURL(repoURL)
	if err != nil {
		return fmt.Errorf("parse url: %w", err)
	}

	name := strings.TrimSpace(registryAddName)
	if name == "" {
		name = registry.InferRegistryName(cleanURL)
		if name == "" {
			return fmt.Errorf("could not infer a registry name from %q — pass --name <alias>", repoURL)
		}
	}

	cfg, cerr := config.Load()
	if cerr != nil {
		return fmt.Errorf("load config: %w", cerr)
	}

	gc := git.NewGoGitClient()
	mgr := newRegistryManager(gc)

	// --full pulls all branches/tags/history; otherwise the fast default clones
	// just the default branch at --depth (negative normalised to full history of
	// that one branch).
	full := registryAddFull
	depth := registryAddDepth
	if depth < 0 {
		depth = 0
	}

	// Re-adding an already-configured registry: with --full, deepen the existing
	// latest-only clone in place (fetch all branches + tags into the same bare
	// clone) instead of erroring. This is the recovery the install-time `--full`
	// hint promises — previously it dead-ended because `registry add --full`
	// refused an existing registry and there was no in-place deepen, forcing an
	// unmentioned `registry remove` first (#184). Without --full it stays a
	// conflict, now signposting BOTH escape hatches.
	if _, exists := cfg.Registries[name]; exists {
		if !full {
			return fmt.Errorf("registry %q already exists — pass --name <alias> to add it under a different name, or --full to deepen it to all branches and tags", name)
		}
		sp := printer.Spinner()
		sp.Start(fmt.Sprintf("Deepening %s to full (all branches, tags, history) …", name))
		reg, derr := mgr.Deepen(cmd.Context(), name)
		sp.Stop()
		if derr != nil {
			return fmt.Errorf("deepen registry: %w", derr)
		}
		if printer.Format == output.FormatJSON {
			return printer.JSON(reg)
		}
		msg := fmt.Sprintf("Deepened registry %q (%s) to full — %d skills; all branches and tags are now installable",
			reg.Name, reg.URL, reg.SkillCount)
		if reg.SkippedCount > 0 {
			msg += fmt.Sprintf(" (%d skipped — run `qvr registry update %s --verbose` for reasons)",
				reg.SkippedCount, reg.Name)
		}
		printer.Success(msg)
		return nil
	}

	// Clone + first index happen inside Add and can take seconds on a large
	// or remote repo. Animate a spinner so the terminal never looks frozen
	// (no-op in JSON mode / non-TTY — see Printer.Spinner).
	sp := printer.Spinner()
	if full {
		sp.Start(fmt.Sprintf("Cloning %s (full — all branches, tags, history) …", name))
	} else {
		sp.Start(fmt.Sprintf("Cloning %s (latest, default branch only) …", name))
	}
	reg, err := mgr.AddWithOptions(cmd.Context(), name, repoURL, registry.AddOptions{Depth: depth, Full: full})
	sp.Stop()
	if err != nil {
		// Manager.Add surfaces ErrRegistryExists when the name is taken.
		// Reformat with a `--name` hint so the user knows the override path.
		if errors.Is(err, registry.ErrRegistryExists) {
			return fmt.Errorf("registry %q already exists — pass --name <alias> to use a different name", name)
		}
		return fmt.Errorf("add registry: %w", err)
	}

	// No scan here. `qvr registry add` only clones the repo and builds the
	// skill index (the "skill tree") — it neither installs nor executes any
	// skill, so there is nothing to gate yet. The security scan runs at
	// `qvr add <skill>`, where the selected skill is materialised and the
	// install gate decides whether it may be linked into an agent dir.

	if printer.Format == output.FormatJSON {
		return printer.JSON(reg)
	}
	if reg.CredentialsStripped {
		printer.Warning("URL contained embedded credentials; stored sanitised URL. " +
			"Configure a credential helper (e.g. `gh auth login` or osxkeychain) for auth.")
	}
	msg := fmt.Sprintf("Added registry %q (%s) with %d skills", reg.Name, reg.URL, reg.SkillCount)
	if reg.SkippedCount > 0 {
		msg += fmt.Sprintf(" (%d skipped — run `qvr registry update %s --verbose` for reasons)",
			reg.SkippedCount, reg.Name)
	}
	printer.Success(msg)
	printer.Info(registryTrustSummary(reg, cfg, fetchRegistryOwnerSignals(cmd.Context(), reg, cfg)))
	// Proactive diagnostic: a default (latest-only) clone can't install tags or
	// older versions. Tell the user how to get them rather than letting a later
	// `qvr add skill@v1` fail mysteriously.
	if !full {
		printer.Info(fmt.Sprintf("Fetched the default branch only (fast). To install specific tags or older versions, re-add with: qvr registry add %s --full", reg.URL))
	}
	return nil
}

type registryOwnerSignals struct {
	AccountAge   string
	LastActivity string
	Followers    string
	PublicRepos  string
}

func registryTrustSummary(reg *model.Registry, cfg *config.Config, signals registryOwnerSignals) string {
	name := ""
	if reg != nil {
		name = reg.Name
	}
	owner := name
	if i := strings.Index(name, "/"); i > 0 {
		owner = name[:i]
	}
	if owner == "" {
		owner = "unknown"
	}
	skills := "unknown"
	if reg != nil {
		skills = fmt.Sprintf("%d", reg.SkillCount)
	}
	if signals.AccountAge == "" {
		signals.AccountAge = "unknown"
	}
	if signals.LastActivity == "" {
		signals.LastActivity = "unknown"
	}
	if signals.Followers == "" {
		signals.Followers = "unknown"
	}
	if signals.PublicRepos == "" {
		signals.PublicRepos = "unknown"
	}
	scans := "enabled"
	signatures := "optional"
	if cfg != nil {
		switch {
		case cfg.Security.RequireScan:
			scans = "required"
		case !cfg.Security.ScanOnInstall:
			scans = "disabled"
		}
		if cfg.Security.RequireSigned {
			signatures = "required"
		}
	}
	return fmt.Sprintf("Trust: owner %s; account age %s; last activity %s; followers %s; public repos %s; skills %s; scans %s; signatures %s",
		owner, signals.AccountAge, signals.LastActivity, signals.Followers, signals.PublicRepos, skills, scans, signatures)
}

func fetchRegistryOwnerSignals(ctx context.Context, reg *model.Registry, cfg *config.Config) registryOwnerSignals {
	if ctx == nil {
		ctx = context.Background()
	}
	owner := githubOwner(reg)
	if owner == "" {
		return registryOwnerSignals{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/users/"+owner, nil)
	if err != nil {
		return registryOwnerSignals{}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if cfg != nil && cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.GithubToken)
	}
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	resp, err := client.Do(req)
	if err != nil {
		return registryOwnerSignals{}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return registryOwnerSignals{}
	}
	var body struct {
		CreatedAt   time.Time `json:"created_at"`
		UpdatedAt   time.Time `json:"updated_at"`
		Followers   int       `json:"followers"`
		PublicRepos int       `json:"public_repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return registryOwnerSignals{}
	}
	out := registryOwnerSignals{
		Followers:   fmt.Sprintf("%d", body.Followers),
		PublicRepos: fmt.Sprintf("%d", body.PublicRepos),
	}
	if !body.CreatedAt.IsZero() {
		out.AccountAge = body.CreatedAt.Format("2006-01-02")
	}
	if !body.UpdatedAt.IsZero() {
		out.LastActivity = body.UpdatedAt.Format("2006-01-02")
	}
	return out
}

func githubOwner(reg *model.Registry) string {
	if reg == nil {
		return ""
	}
	if u, err := url.Parse(reg.URL); err == nil && strings.EqualFold(u.Host, "github.com") {
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		if len(parts) >= 2 && parts[0] != "" {
			return parts[0]
		}
	}
	if strings.HasPrefix(reg.URL, "git@github.com:") {
		rest := strings.TrimPrefix(reg.URL, "git@github.com:")
		parts := strings.Split(strings.Trim(rest, "/"), "/")
		if len(parts) >= 2 && parts[0] != "" {
			return parts[0]
		}
	}
	if i := strings.Index(reg.Name, "/"); i > 0 {
		return reg.Name[:i]
	}
	return ""
}

func runRegistryRemove(cmd *cobra.Command, args []string) error {
	// Resolve a bare leaf (e.g. `skills` -> `acme/skills`) up front so the
	// confirmation/echo reports the actual registry removed, not the shorthand.
	name := args[0]
	if cfg, cerr := config.Load(); cerr == nil {
		if resolved, rerr := registry.ResolveName(cfg, name); rerr == nil {
			name = resolved
		}
	}

	mgr := newRegistryManager(git.NewGoGitClient())
	if err := mgr.Remove(name); err != nil {
		return fmt.Errorf("remove registry: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{"removed": name})
	}
	printer.Success(fmt.Sprintf("Removed registry %q", name))
	return nil
}

func runRegistryList(cmd *cobra.Command, args []string) error {
	if registryListRefresh {
		refreshAllIndexes()
	}
	if len(args) > 0 {
		return runRegistrySkillsList(args)
	}

	mgr := newRegistryManager(git.NewGoGitClient())
	registries, err := mgr.List()
	if err != nil {
		return fmt.Errorf("list registries: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(registries)
	}

	if len(registries) == 0 {
		printer.Info("No registries configured. Run 'qvr registry add <url>' to register one.")
		return nil
	}

	// Only render the SKIPPED column when at least one registry has malformed
	// skills — the common case stays uncluttered.
	anySkipped := false
	for _, r := range registries {
		if r.SkippedCount > 0 {
			anySkipped = true
			break
		}
	}

	headers := []string{"NAME", "URL", "SKILLS", "LAST FETCHED"}
	if anySkipped {
		headers = []string{"NAME", "URL", "SKILLS", "SKIPPED", "LAST FETCHED"}
	}
	var rows [][]string
	for _, r := range registries {
		fetched := "never"
		if !r.LastFetched.IsZero() {
			fetched = time.Since(r.LastFetched).Truncate(time.Second).String() + " ago"
		}
		row := []string{r.Name, r.URL, fmt.Sprintf("%d", r.SkillCount)}
		if anySkipped {
			row = append(row, fmt.Sprintf("%d", r.SkippedCount))
		}
		row = append(row, fetched)
		rows = append(rows, row)
	}
	printer.Table(headers, rows)
	if anySkipped {
		printer.Info("Some skills could not be indexed. Run `qvr registry update <name> --verbose` for reasons.")
	}
	return nil
}

func runRegistryUpdate(cmd *cobra.Command, args []string) error {
	mgr := newRegistryManager(git.NewGoGitClient())
	name := ""
	if len(args) > 0 {
		name = args[0]
	}

	if registryUpdateCheck {
		results, err := mgr.Check(cmd.Context(), name)
		if err != nil {
			return fmt.Errorf("check registries: %w", err)
		}
		failed := 0
		for _, r := range results {
			if r.Error != "" {
				failed++
			}
		}
		if printer.Format == output.FormatJSON {
			if jerr := printer.JSON(results); jerr != nil {
				return jerr
			}
			if failed > 0 {
				return errJSONHandled
			}
			return nil
		}
		for _, r := range results {
			if r.Error != "" {
				printer.Error(fmt.Sprintf("%s: %s", r.Name, r.Error))
			} else if r.HasUpstreamChanges {
				printer.Info(fmt.Sprintf("%s: upstream changes available", r.Name))
			} else {
				printer.Info(fmt.Sprintf("%s: up to date", r.Name))
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d registr(y/ies) failed to check", failed)
		}
		return nil
	}

	results, err := mgr.Update(cmd.Context(), name)
	if err != nil {
		return fmt.Errorf("update registries: %w", err)
	}

	failed := 0
	for _, r := range results {
		if r.Error != "" {
			failed++
		}
	}

	if printer.Format == output.FormatJSON {
		if jerr := printer.JSON(results); jerr != nil {
			return jerr
		}
		if failed > 0 {
			return errJSONHandled
		}
		return nil
	}
	for _, r := range results {
		if r.Error != "" {
			printer.Error(fmt.Sprintf("%s: %s", r.Name, r.Error))
			continue
		}
		msg := fmt.Sprintf("%s: updated (%d skills", r.Name, r.SkillCount)
		if r.SkippedCount > 0 {
			msg += fmt.Sprintf(", %d skipped", r.SkippedCount)
		}
		msg += ")"
		printer.Success(msg)
		if registryUpdateVerbose && len(r.Skipped) > 0 {
			for _, s := range r.Skipped {
				printer.Warning(fmt.Sprintf("  skipped %s (%s): %s", s.Name, s.Path, s.Reason))
			}
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d registr(y/ies) failed to update", failed)
	}
	return nil
}

// rejectWebURL rejects GitHub/GitLab/Bitbucket web-browse URLs like
// /tree/<ref>/<subdir> and /blob/<ref>/<file> before they reach `git clone`,
// where the resulting error ("repository not found") obscures the real cause.
// /blob/ URLs are the install shape for single skills — point users at qvr add.
func rejectWebURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil
	}
	host := strings.ToLower(u.Host)
	switch host {
	case "github.com", "gitlab.com", "bitbucket.org", "www.github.com":
	default:
		return nil
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 3 {
		return nil
	}
	switch parts[2] {
	case "tree", "blob":
		return fmt.Errorf("that's a web URL, not a clone URL\n"+
			"  git can't clone a subdirectory — register the whole repo and add the skill by name:\n"+
			"    qvr registry add %s://%s/%s/%s.git\n"+
			"    qvr add <skill>",
			u.Scheme, u.Host, parts[0], parts[1])
	}
	return nil
}

func runRegistrySkillsList(names []string) error {
	mgr := newRegistryManager(git.NewGoGitClient())
	results, err := mgr.ListSkills(names)
	if err != nil {
		return fmt.Errorf("list skills: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(results)
	}

	showRegCol := len(names) > 1
	headers := []string{"SKILL", "DESCRIPTION"}
	if showRegCol {
		headers = []string{"REGISTRY", "SKILL", "DESCRIPTION"}
	}

	var rows [][]string
	errored := 0
	for _, r := range results {
		if r.Error != "" {
			printer.Error(fmt.Sprintf("%s: %s", r.Name, r.Error))
			errored++
			continue
		}
		if len(r.Skills) == 0 {
			printer.Info(fmt.Sprintf("%s: no skills", r.Name))
			continue
		}
		for _, s := range r.Skills {
			desc := output.TruncDesc(s.Description, registryListFull)
			if showRegCol {
				rows = append(rows, []string{r.Name, s.Name, desc})
			} else {
				rows = append(rows, []string{s.Name, desc})
			}
		}
	}

	if len(rows) > 0 {
		printer.Table(headers, rows)
	}
	if errored == len(results) {
		return fmt.Errorf("no valid registries provided")
	}
	return nil
}
