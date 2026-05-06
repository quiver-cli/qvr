package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Manage skill versions",
}

var versionListCmd = &cobra.Command{
	Use:   "list <skill>",
	Short: "List available versions (branches and tags) for a skill",
	Args:  cobra.ExactArgs(1),
	RunE:  runVersionList,
}

func init() {
	versionCmd.AddCommand(versionListCmd)
	rootCmd.AddCommand(versionCmd)
}

func runVersionList(cmd *cobra.Command, args []string) error {
	skillName := args[0]
	mgr := registry.NewManager(git.NewGoGitClient())

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
			fmt.Fprintf(cmd.OutOrStdout(), "  %s%s\t%s\n", marker, tag.Ref, shortHash(tag.Commit))
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
	return entry.Branch
}

func shortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}
