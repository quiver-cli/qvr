package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/spf13/cobra"
)

var registryCmd = &cobra.Command{
	Use:   "registry",
	Short: "Manage skill registries",
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

GitHub /tree/<ref>/<path> and /blob/<ref>/<path> web URLs are rejected with
an explanatory error — git can't clone a subdirectory; pass the repo URL.`,
	Args: cobra.ExactArgs(1),
	RunE: runRegistryAdd,
}

var registryAddName string

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
	registryAddNoScan     bool
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
	registryAddCmd.Flags().BoolVar(&registryAddNoScan, "no-scan", false,
		"skip the per-skill security scan that normally gates registry adds (override security.scan_on_install)")
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

	reg, err := mgr.Add(cmd.Context(), name, repoURL)
	if err != nil {
		// Manager.Add surfaces ErrRegistryExists when the name is taken.
		// Reformat with a `--name` hint so the user knows the override path.
		if errors.Is(err, registry.ErrRegistryExists) {
			return fmt.Errorf("registry %q already exists — pass --name <alias> to use a different name", name)
		}
		return fmt.Errorf("add registry: %w", err)
	}

	// Registry-source scan. Materialise each indexed skill into a throwaway
	// worktree and scan it so users see risky entries early. This is advisory:
	// registering a source does not install or execute any skill, and blocking
	// the entire registry because one large repo has fixtures/vendor code is too
	// coarse. The hard gate still runs when a specific skill is installed.
	var flaggedByScan []string
	if gateAvailable(cfg, registryAddNoScan) {
		skills, _, ixerr := mgr.Index(reg.Name, registry.RegistryPath(reg.Name))
		if ixerr == nil && len(skills) > 0 {
			blockedSkills, gateErr := scanRegistrySkillsBeforeAdd(cmd.Context(), reg, skills, cfg)
			if gateErr != nil {
				printer.Warning(fmt.Sprintf("registry %s: scan pass failed (%v); registry kept — rerun `qvr scan <skill>` per-skill to retry", reg.Name, gateErr))
			} else if len(blockedSkills) > 0 {
				flaggedByScan = blockedSkills
				printer.Warning(fmt.Sprintf("registry %s: scan flagged %d skill(s) at/above the install block threshold (%s); registry kept — `qvr add` will re-scan and gate the selected skill",
					reg.Name, len(blockedSkills), strings.Join(blockedSkills, ", ")))
			}
		}
	}

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
	if len(flaggedByScan) > 0 {
		msg += fmt.Sprintf(" (%d scan-flagged)", len(flaggedByScan))
	}
	printer.Success(msg)
	return nil
}

// scanRegistrySkillsBeforeAdd materialises each indexed skill into a throwaway
// worktree (sparse-checked-out to just that skill's subpath) and runs the
// standard scan gate. Returns the list of skill names that were blocked
// (each already had its findings surfaced via ScanAndGate's renderer).
//
// The materialisation cost is proportional to the count of skills × the
// skill subtree size, not the whole repo — sparse checkout keeps each scan
// dir bounded to one skill. Slow registries (~30 skills) still finish in
// under a couple of minutes; users see incremental progress via the
// per-finding banner.
func scanRegistrySkillsBeforeAdd(ctx context.Context, reg *model.Registry, skills []registry.SkillIndexEntry, cfg *config.Config) ([]string, error) {
	worktreeMgr := git.NewGoGitWorktree()
	barePath := registry.RegistryPath(reg.Name)

	tmpRoot, err := os.MkdirTemp("", "qvr-registry-scan-*")
	if err != nil {
		return nil, fmt.Errorf("create scan workspace: %w", err)
	}
	defer os.RemoveAll(tmpRoot)

	ref := reg.DefaultBranch
	if ref == "" {
		ref = "main"
	}

	var blocked []string
	for _, s := range skills {
		// Each skill scans in its own subdir so a per-skill failure doesn't
		// abort the rest — we collect all blocks and report the full list.
		stage := filepath.Join(tmpRoot, sanitizeForFs(s.Name))
		if err := worktreeMgr.Add(barePath, stage, ref); err != nil {
			printer.Warning(fmt.Sprintf("scan %s: could not materialise (%v); skipping gate", s.Name, err))
			continue
		}
		// Scope the scan to just this skill's content so a multi-purpose repo's
		// app code / test fixtures never gate a skill that doesn't ship them.
		//   - non-root skill → its own subtree (scan dir is that subtree)
		//   - lone root skill → the whole repo (no narrowing; it IS the skill)
		//   - root skill with siblings → SKILL.md + recognized content dirs only
		scopePaths := registry.SkillScopePaths(s)
		skillDir := stage
		switch {
		case len(scopePaths) == 1 && scopePaths[0] == s.Path && s.Path != "" && s.Path != ".":
			if err := worktreeMgr.SetSparseCheckout(stage, scopePaths); err != nil {
				printer.Warning(fmt.Sprintf("scan %s: sparse-checkout failed (%v); scanning full clone", s.Name, err))
			}
			skillDir = filepath.Join(stage, s.Path)
		case len(scopePaths) > 0:
			// root-with-siblings: explicit content patterns, scan from repo root
			if err := worktreeMgr.SetSparseCheckoutPatterns(stage, scopePaths); err != nil {
				printer.Warning(fmt.Sprintf("scan %s: sparse-checkout failed (%v); scanning full clone", s.Name, err))
			}
		default:
			// lone root skill: scan the full clone (matches what gets installed)
		}
		gate, gerr := ScanAndGate(ctx, skillDir, cfg, scanGateOptions{
			Action:            "registry add",
			Subject:           s.Name,
			WarnOnly:          true,
			Quiet:             true,
			QuietHint:         fmt.Sprintf("Run `qvr add %s` to re-scan and apply the install gate for this skill.", s.Name),
			ReportOnlyBlocked: true,
		})
		if gerr != nil {
			printer.Warning(fmt.Sprintf("scan %s: %v; skipping gate", s.Name, gerr))
			continue
		}
		if gate.Blocked {
			blocked = append(blocked, s.Name)
		}
	}
	return blocked, nil
}

// sanitizeForFs produces a safe directory segment from a skill name, for the
// per-skill scan workspace. The skill name is already lowercase-alphanumeric
// + hyphens per spec, but we belt-and-brace strip anything funky just in
// case the indexer accepted a borderline case.
func sanitizeForFs(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if out == "" {
		out = "skill"
	}
	return out
}

func runRegistryRemove(cmd *cobra.Command, args []string) error {
	mgr := newRegistryManager(git.NewGoGitClient())
	if err := mgr.Remove(args[0]); err != nil {
		return fmt.Errorf("remove registry: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{"removed": args[0]})
	}
	printer.Success(fmt.Sprintf("Removed registry %q", args[0]))
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
