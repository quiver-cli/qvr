package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	addRef     string
	addSubdir  string
	addAs      string
	addTargets []string
	addGlobal  bool
)

var addCmd = &cobra.Command{
	Use:   "add <url>...",
	Short: "Add one or more skills from git URLs — no registry required",
	Long: `Install one or more skills from a remote git repo without registering
the whole repo. Modes:

  Standalone repo (SKILL.md at root):
    qvr add https://github.com/owner/single-skill-repo

  Subdirectory inside a multi-skill repo (auto-detected for github.com URLs):
    qvr add https://github.com/openclaw/skills/blob/main/skills/jchopard69/x-article-editor

  Multiple skills from the same multi-skill repo in one invocation:
    qvr add \
      https://github.com/mattpocock/skills/blob/main/skills/foo/a \
      https://github.com/mattpocock/skills/blob/main/skills/bar/b

  Or pass an opaque clone URL with explicit --subdir / --ref:
    qvr add git@github.com:openclaw/skills.git --ref main \
            --subdir skills/jchopard69/x-article-editor

The subdirectory mode sparse-checks-out only the requested folder, links it
into the agent target dir(s), and writes a normal lock entry — same shape as
a registry install, so qvr remove/upgrade/disable all work afterwards.

When multiple URLs are passed, --ref / --subdir / --as are rejected (each URL
must self-describe its ref and path, e.g. via /blob/<ref>/<path>) so flags
don't silently apply to every skill in the batch.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringVar(&addRef, "ref", "", "branch, tag, or commit (auto-detected from /blob/<ref>/ URLs)")
	addCmd.Flags().StringVar(&addSubdir, "subdir", "", "path inside the repo when not encoded in the URL")
	addCmd.Flags().StringVar(&addAs, "as", "", "override the installed skill name (defaults to the subpath leaf)")
	addCmd.Flags().StringSliceVar(&addTargets, "target", nil,
		"agent target(s) to install into (repeatable). Defaults to default_target (which may itself be comma-separated, e.g. \"claude,cursor\").")
	addCmd.Flags().BoolVar(&addGlobal, "global", false,
		"install into the user-global agent directory and write to the global lock file")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	if len(args) > 1 {
		// Per-URL flags applied to every URL in the batch would be either
		// silently wrong (e.g. --as foo collapsing many skills into one
		// name) or useless (e.g. --subdir applying the same path to
		// different repos). Force the single-URL form when any are set.
		if addRef != "" || addSubdir != "" || addAs != "" {
			return fmt.Errorf("--ref / --subdir / --as require a single URL; remove the flag or run one URL at a time")
		}
	}

	if len(args) == 1 {
		return runAddOne(cmd, args[0])
	}

	var firstErr error
	succeeded := 0
	for i, url := range args {
		if err := runAddOne(cmd, url); err != nil {
			// Continue through the remaining URLs so a single bad link in a
			// large batch doesn't strand the rest. Surface the first failure
			// in the exit code.
			printer.Error(fmt.Sprintf("[%d/%d] %s: %v", i+1, len(args), url, err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		succeeded++
	}
	if printer.Format != output.FormatJSON {
		printer.Info(fmt.Sprintf("%d/%d added", succeeded, len(args)))
	}
	return firstErr
}

func runAddOne(cmd *cobra.Command, url string) error {
	// Try to parse as a subdirectory URL first; fall back to standalone clone
	// for URLs that don't carry subdir info.
	parsed, parseErr := git.ParseSubdirURL(url)
	wantSubdir := parseErr == nil || addSubdir != ""

	if wantSubdir {
		return runAddSubdir(cmd, url, parsed, parseErr)
	}
	return runAddStandalone(cmd, url)
}

// runAddSubdir handles the "single skill from a multi-skill repo" path.
// Resolves repo URL / ref / subpath from either a parsed subdir URL or
// explicit --ref/--subdir flags (or both — flags win), then delegates to
// Installer.InstallFromSubdir.
func runAddSubdir(cmd *cobra.Command, url string, parsed *git.SubdirURL, parseErr error) error {
	repoURL := url
	ref := addRef
	subpath := addSubdir

	if parsed != nil {
		repoURL = parsed.RepoURL
		if ref == "" {
			ref = parsed.Ref
		}
		if subpath == "" {
			subpath = parsed.Subpath
		}
	} else if !errors.Is(parseErr, git.ErrNotSubdirURL) && parseErr != nil {
		return fmt.Errorf("parse url: %w", parseErr)
	}

	if ref == "" {
		return fmt.Errorf("--ref is required (e.g. main, v1.2.3)")
	}
	if subpath == "" {
		return fmt.Errorf("--subdir is required (e.g. skills/foo/bar)")
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	targets := addTargets
	if len(targets) == 0 {
		targets = config.ParseDefaultTargets(cfg.DefaultTarget)
		if len(targets) == 0 {
			return fmt.Errorf("no --target specified and default_target is unset")
		}
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	installer := skill.NewInstaller(registry.NewManager(gc), wt, gc)

	result, err := installer.InstallFromSubdir(cmd.Context(), skill.SubdirInstallRequest{
		RepoURL:     repoURL,
		Ref:         ref,
		Subpath:     subpath,
		As:          addAs,
		Targets:     targets,
		Global:      addGlobal,
		ProjectRoot: projectRoot,
	})
	if err != nil {
		// Surface URL-domain language for the two most common user errors
		// (bad ref, bad subdir). Everything else falls through with the raw
		// wrapped error so we don't accidentally hide unrelated failures.
		switch {
		case errors.Is(err, skill.ErrRefNotFound):
			return fmt.Errorf("add %s: ref %q not found in %s", url, ref, repoURL)
		case errors.Is(err, skill.ErrSubpathMissing):
			return fmt.Errorf("add %s: subdir %q not found in repo at ref %q", url, subpath, ref)
		}
		return fmt.Errorf("add %s: %w", url, err)
	}
	if !addGlobal {
		refreshAgentsMDFromLock(projectRoot)
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}
	printer.Success(fmt.Sprintf("Installed %s@%s from %s → %v", result.Name, result.Version, repoURL, result.Targets))
	return nil
}

func runAddStandalone(cmd *cobra.Command, url string) error {
	mgr := registry.NewManager(git.NewGoGitClient())
	repo, err := mgr.AddStandalone(cmd.Context(), url)
	if err != nil {
		return fmt.Errorf("add standalone repo: %w", err)
	}
	// A standalone repo must have SKILL.md at the root. When it doesn't,
	// this is almost always a multi-skill repo URL passed to the wrong
	// command — keeping the clone around would leak orphan disk state and
	// the green ✓ would mislead the user into thinking the install worked.
	if _, statErr := os.Stat(filepath.Join(repo.Path, "SKILL.md")); statErr != nil {
		_ = os.RemoveAll(repo.Path)
		return fmt.Errorf("no SKILL.md at the repo root — this looks like a multi-skill repo\n"+
			"  install one skill with:   qvr add <url>/blob/<ref>/<subdir>\n"+
			"  or pass --subdir <path> with --ref <branch> to the opaque clone URL\n"+
			"  url: %s", url)
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(repo)
	}
	printer.Success(fmt.Sprintf("Added standalone skill from %s", repo.URL))
	printer.Info(fmt.Sprintf("Cloned to %s", repo.Path))
	return nil
}
