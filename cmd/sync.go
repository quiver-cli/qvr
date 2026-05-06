package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	syncOutputPath string
	syncGlobal     bool
)

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Generate AGENTS.md from installed skills",
	Long: `Walk every installed skill's SKILL.md and emit a single AGENTS.md in the
current directory summarising what's available to agents in this project.

Pass --global to read the user-global lock file instead. The output path still
defaults to ./AGENTS.md, so combine with -o when generating from a global lock.`,
	RunE: runSync,
}

func init() {
	syncCmd.Flags().StringVarP(&syncOutputPath, "output-file", "o", "AGENTS.md",
		"path to write the generated file (relative to cwd)")
	syncCmd.Flags().BoolVar(&syncGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	rootCmd.AddCommand(syncCmd)
}

func runSync(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), syncGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	outPath := syncOutputPath
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(projectRoot, outPath)
	}
	entries := lock.Entries()
	if err := writeAgentsMD(outPath, entries); err != nil {
		return err
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{
			"path":   outPath,
			"skills": len(entries),
		})
	}
	printer.Success(fmt.Sprintf("Wrote %s (%d skill(s))", outPath, len(entries)))
	return nil
}

// writeAgentsMD serializes the installed skill list to a single AGENTS.md at
// outPath. Used directly by `qvr sync` and by switch/upgrade/pull to keep the
// file fresh when it already exists (see refreshAgentsMDIfPresent).
func writeAgentsMD(outPath string, entries []*model.LockEntry) error {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })

	var b strings.Builder
	b.WriteString("# Agents\n\n")
	b.WriteString("Skills available to agents in this project. Managed by [Quiver](https://github.com/raks097/quiver).\n\n")
	if len(entries) == 0 {
		b.WriteString("_No skills installed._\n")
	} else {
		b.WriteString("## Skills\n\n")
		for _, e := range entries {
			desc := ""
			if e.Worktree != "" {
				loaded, err := skill.LoadFromPath(filepath.Join(e.Worktree, e.Path))
				if err == nil {
					desc = loaded.Frontmatter.Description
				}
			} else if e.LinkTarget != "" {
				loaded, err := skill.LoadFromPath(e.LinkTarget)
				if err == nil {
					desc = loaded.Frontmatter.Description
				}
			}
			origin := e.Registry
			if e.Source == "link" {
				origin = "link"
			} else if origin == "" {
				origin = "standalone"
			}
			version := e.Branch
			if version == "" {
				version = "-"
			}
			// Descriptions that come from YAML folded/block scalars can carry
			// embedded newlines or trailing whitespace. Collapse to a single
			// space so the _(registry@ref)_ marker always renders on the same
			// line as the description, regardless of source formatting.
			fmt.Fprintf(&b, "- **%s** — %s _(%s@%s)_\n", e.Name, collapseWhitespace(desc), origin, version)
		}
	}

	if err := os.WriteFile(outPath, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", outPath, err)
	}
	return nil
}

// refreshAgentsMDIfPresent regenerates AGENTS.md at projectRoot — but only if
// the file already exists. Callers (install/link/remove/disable/enable/edit/
// switch/upgrade/pull) use this to keep the description cache in sync without
// silently creating an AGENTS.md for users who deliberately haven't opted in
// via `qvr sync`.
func refreshAgentsMDIfPresent(projectRoot string, entries []*model.LockEntry) error {
	outPath := filepath.Join(projectRoot, "AGENTS.md")
	if _, err := os.Stat(outPath); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return nil // any other stat error: treat as "don't touch"
	}
	return writeAgentsMD(outPath, entries)
}

// refreshAgentsMDFromLock re-reads the lock file at projectRoot and refreshes
// AGENTS.md if it already exists. Convenience wrapper for commands that
// mutate the lock via a helper (Installer.Install/Link/Remove) and so don't
// keep a live entries slice in hand.
func refreshAgentsMDFromLock(projectRoot string) {
	lock, err := model.ReadLockFile(filepath.Join(projectRoot, model.LockFileName))
	if err != nil {
		return
	}
	_ = refreshAgentsMDIfPresent(projectRoot, lock.Entries())
}

// collapseWhitespace flattens any run of whitespace (including newlines) into
// a single space and trims the result. Keeps AGENTS.md rows uniform even when
// skill authors use YAML folded (`>`) or block (`|`) scalars for descriptions.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
