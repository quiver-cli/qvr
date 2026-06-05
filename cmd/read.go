package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var readFile string

var readCmd = &cobra.Command{
	Use:   "read <skill>",
	Short: "Read a skill's contents through its symlink (hot path)",
	Long: `Follow the symlink for a skill and emit a file (SKILL.md by default).
Performs no git operations and no lock-file parsing — it reads directly from
the first agent target that has the skill symlinked.`,
	Args: cobra.ExactArgs(1),
	RunE: runRead,
}

func init() {
	readCmd.Flags().StringVar(&readFile, "file", "SKILL.md", "file within the skill to read")
	rootCmd.AddCommand(readCmd)
}

func runRead(cmd *cobra.Command, args []string) error {
	name := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	data, sawSkill, fileErr := readSkillFile(name, readFile, projectRoot, orderedTargets())
	if data != nil {
		return emit(data)
	}
	if sawSkill && fileErr != nil {
		return fileErr
	}
	return fmt.Errorf("skill %q not found in any agent target", name)
}

// readSkillFile walks the agent targets looking for skillName's symlink.
// Returns:
//   - data when a symlink resolves and the requested file reads cleanly.
//   - sawSkill=true when at least one target had a live symlink, even if the
//     file inside it was missing — lets the caller surface "file not found"
//     instead of the misleading "skill not found".
//   - fileErr describing the last in-skill read failure (path resolution,
//     traversal attempt, or os.ReadFile error).
func readSkillFile(skillName, file, projectRoot string, targets []string) ([]byte, bool, error) {
	var sawSkill bool
	var lastFileErr error
	for _, t := range targets {
		linkPath, err := skill.ResolveTargetPath(t, skillName, projectRoot, false)
		if err != nil {
			continue
		}
		linkPathAbs, err := filepath.Abs(filepath.Clean(linkPath))
		if err != nil {
			continue
		}
		if _, err := os.Lstat(linkPathAbs); err != nil {
			continue
		}
		sawSkill = true
		filePath := filepath.Join(linkPathAbs, file)
		filePathAbs, err := filepath.Abs(filepath.Clean(filePath))
		if err != nil {
			lastFileErr = fmt.Errorf("resolve file path: %w", err)
			continue
		}
		rel, err := filepath.Rel(linkPathAbs, filePathAbs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
			lastFileErr = fmt.Errorf("file %q escapes skill %q", file, skillName)
			continue
		}
		data, err := os.ReadFile(filePathAbs)
		if err == nil {
			return data, true, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			lastFileErr = fmt.Errorf("file %q not found in skill %q (looked in %s)", file, skillName, linkPathAbs)
			continue
		}
		lastFileErr = fmt.Errorf("read %s: %w", filePathAbs, err)
	}
	return nil, sawSkill, lastFileErr
}

// orderedTargets returns the target search order: configured defaults first
// (in the order the user listed them), then the remaining built-in targets
// in their canonical order. Honours comma-separated default_target values
// like "claude,cursor".
func orderedTargets() []string {
	all := []string{"claude", "cursor", "copilot", "codex", "windsurf", "project"}
	cfg, err := config.Load()
	if err != nil {
		return all
	}
	preferred := config.ParseDefaultTargets(cfg.DefaultTarget)
	if len(preferred) == 0 {
		return all
	}
	seen := make(map[string]struct{}, len(preferred))
	out := make([]string, 0, len(all))
	for _, p := range preferred {
		if _, dup := seen[p]; dup {
			continue
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, t := range all {
		if _, used := seen[t]; used {
			continue
		}
		out = append(out, t)
	}
	return out
}

func emit(data []byte) error {
	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]string{"content": string(data)})
	}
	_, err := os.Stdout.Write(data)
	return err
}
