package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var lsRecursive bool

// lsEntry describes one entry inside a skill directory for `qvr ls`.
type lsEntry struct {
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

var lsCmd = &cobra.Command{
	Use:     "ls <skill>",
	Aliases: []string{"files"},
	Short:   "List files bundled with an installed skill",
	Long: `Resolve the skill via its agent symlink and list the bundled files.
Top-level only by default; pass --recursive to walk the full subtree.`,
	Args: cobra.ExactArgs(1),
	RunE: runLs,
}

func init() {
	lsCmd.Flags().BoolVarP(&lsRecursive, "recursive", "r", false, "list every file recursively")
	rootCmd.AddCommand(lsCmd)
}

func runLs(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	skillDir, err := locateSkillDir(name, projectRoot, orderedTargets())
	if err != nil {
		return err
	}
	entries, err := listSkillEntries(skillDir, lsRecursive)
	if err != nil {
		return fmt.Errorf("list skill files: %w", err)
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"skill": name,
			"path":  skillDir,
			"files": entries,
		})
	}
	for _, e := range entries {
		if e.IsDir {
			fmt.Fprintln(printer.Out, e.Path+"/")
		} else {
			fmt.Fprintln(printer.Out, e.Path)
		}
	}
	return nil
}

// locateSkillDir resolves a skill name to its on-disk directory by walking
// agent target symlinks. Returns the eval-resolved (no-symlink) path so
// callers don't have to think about indirection.
func locateSkillDir(name, projectRoot string, targets []string) (string, error) {
	for _, t := range targets {
		linkPath, err := skill.ResolveTargetPath(t, name, projectRoot, false)
		if err != nil {
			continue
		}
		if _, err := os.Lstat(linkPath); err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(linkPath)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", linkPath, err)
		}
		return resolved, nil
	}
	return "", fmt.Errorf("skill %q not found in any agent target", name)
}

// listSkillEntries walks skillDir and returns sorted entries. Top-level by
// default (one os.ReadDir); recursive mode uses filepath.WalkDir and prunes
// `.git` so worktree metadata never leaks into the output.
func listSkillEntries(skillDir string, recursive bool) ([]lsEntry, error) {
	if !recursive {
		dirEntries, err := os.ReadDir(skillDir)
		if err != nil {
			return nil, err
		}
		out := make([]lsEntry, 0, len(dirEntries))
		for _, d := range dirEntries {
			out = append(out, lsEntry{Path: d.Name(), IsDir: d.IsDir()})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
		return out, nil
	}

	var out []lsEntry
	err := filepath.WalkDir(skillDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == skillDir {
			return nil
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(skillDir, path)
		if err != nil {
			return err
		}
		out = append(out, lsEntry{Path: filepath.ToSlash(rel), IsDir: d.IsDir()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
