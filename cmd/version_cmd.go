package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print qvr's build version, or manage skill versions",
	Long: `Print qvr's build version and provenance.

With no arguments this reports the binary's own version, commit, and build date
(like ` + "`go version`" + `). The ` + "`list`" + ` subcommand instead lists the
available versions (branches and tags) of an installed skill.`,
	// No subcommand → print the binary's own provenance. A stray positional is
	// a typo'd subcommand (e.g. `qvr version lst`) and must fail loudly rather
	// than silently printing the binary version (mirrors rejectUnknownSubcommand).
	RunE: runVersion,
}

func runVersion(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{
			"version": version,
			"commit":  commit,
			"date":    date,
		})
	}
	// Line 1 is machine-friendly ("qvr <version>") so installers and the
	// Makefile `verify` target can `awk '{print $2}'` it; provenance follows.
	fmt.Fprintf(cmd.OutOrStdout(), "qvr %s\n  commit: %s\n  built:  %s\n", version, commit, date)
	return nil
}

var versionListRefresh bool

var versionListCmd = &cobra.Command{
	Use:   "list <skill>",
	Short: "List available versions (branches and tags) for a skill",
	Args:  cobra.ExactArgs(1),
	RunE:  runVersionList,
}

func init() {
	versionListCmd.Flags().BoolVar(&versionListRefresh, "refresh", false,
		"invalidate cached indexes before listing (local rebuild; no network)")
	versionCmd.AddCommand(versionListCmd)
	rootCmd.AddCommand(versionCmd)
}

func runVersionList(cmd *cobra.Command, args []string) error {
	if versionListRefresh {
		refreshAllIndexes()
	}
	skillName := args[0]
	mgr := newRegistryManager(git.NewGoGitClient())

	loc, err := mgr.FindSkill(skillName)
	if err != nil {
		return err
	}

	branches, err := mgr.Git.ListBranches(loc.RepoPath)
	if err != nil {
		return fmt.Errorf("list branches: %w", err)
	}
	tags, err := mgr.Git.ListTags(loc.RepoPath)
	if err != nil {
		return fmt.Errorf("list tags: %w", err)
	}

	// Current ref comes from the lock file when the skill is installed here.
	// A missing lock (e.g. running `version list` in an unrelated dir) just
	// means nothing is marked current — not an error.
	current := currentInstalledRef(skillName)

	vl := &model.VersionList{
		SkillName:     skillName,
		Registry:      loc.RegistryName,
		DefaultBranch: loc.DefaultBranch,
		Current:       current,
	}

	for _, b := range branches {
		vl.Branches = append(vl.Branches, model.Version{
			Ref:       b.Name,
			Kind:      model.VersionKindBranch,
			Commit:    b.Hash,
			IsCurrent: current != "" && b.Name == current,
			IsDefault: b.Name == loc.DefaultBranch,
		})
	}
	for _, tag := range tags {
		// In a multi-skill registry, only this skill's tags (its namespaced
		// "<skill>/<v>" plus any bare legacy tags) belong here — not siblings'
		// versions (issue #152).
		if !model.TagBelongsToSkill(tag.Name, skillName) {
			continue
		}
		vl.Tags = append(vl.Tags, model.Version{
			Ref:       tag.Name,
			Kind:      model.VersionKindTag,
			Commit:    tag.Hash,
			IsSemver:  model.IsSemverTag(tag.Name),
			IsCurrent: current != "" && tag.Name == current,
		})
	}

	model.SortVersions(vl, loc.DefaultBranch)

	if printer.Format == output.FormatJSON {
		return printer.JSON(vl)
	}

	printer.Info(fmt.Sprintf("Versions for %s (registry: %s):\n", skillName, loc.RegistryName))
	if len(vl.Tags) > 0 {
		printer.Info("Tags:")
		for _, tag := range vl.Tags {
			marker := "  "
			if tag.IsCurrent {
				marker = "* "
			}
			// Show the bare version (v0.1.0), not the namespaced ref
			// (alpha/v0.1.0) — the prefix is just registry bookkeeping (#152).
			fmt.Fprintf(cmd.OutOrStdout(), "  %s%s\t%s\n", marker, model.VersionPortion(tag.Ref), shortHash(tag.Commit))
		}
	}
	if len(vl.Branches) > 0 {
		printer.Info("Branches:")
		for _, b := range vl.Branches {
			marker := "  "
			if b.IsCurrent {
				marker = "* "
			}
			suffix := ""
			if b.IsDefault {
				suffix = "\t(default)"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  %s%s\t%s%s\n", marker, b.Ref, shortHash(b.Commit), suffix)
		}
	}
	return nil
}

// currentInstalledRef looks up the skill in the project's lock file and
// returns the ref it's currently checked out at, or "" when the skill isn't
// installed here (or no lock file exists yet). Any I/O error is treated as
// "not installed" — `version list` stays useful even outside a project dir.
func currentInstalledRef(skillName string) string {
	projectRoot, err := os.Getwd()
	if err != nil {
		return ""
	}
	lock, err := model.ReadLockFile(filepath.Join(projectRoot, model.LockFileName))
	if err != nil {
		return ""
	}
	entry, err := lock.Get(skillName)
	if err != nil {
		if errors.Is(err, model.ErrLockSkillMissing) {
			return ""
		}
		return ""
	}
	return entry.Ref
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
