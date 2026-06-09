package cmd

import (
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var statusGlobal bool

var statusCmd = &cobra.Command{
	Use:   "status [skill]...",
	Short: "Show modification state per installed skill",
	Long: `Report per-skill status: dirty tree, ahead/behind counts versus origin.
Purely local — no network access.`,
	RunE: runStatus,
}

func init() {
	statusCmd.Flags().BoolVar(&statusGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	rootCmd.AddCommand(statusCmd)
}

func runStatus(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), statusGlobal))
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	syncer := skill.NewSyncer(git.NewGoGitWorktree(), git.NewGoGitClient())

	entries := lock.Entries()
	if len(args) > 0 {
		filtered := make([]*model.LockEntry, 0, len(args))
		for _, name := range args {
			e, err := lock.Get(name)
			if err != nil {
				return err
			}
			filtered = append(filtered, e)
		}
		entries = filtered
	}

	if len(entries) == 0 {
		printer.Info("No installed skills.")
		return nil
	}

	results, err := collectStatuses(syncer, entries, projectRoot)
	if err != nil {
		return err
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(results)
	}
	renderStatusTable(results)
	return nil
}

// collectStatuses gathers the local sync status for each entry, short-circuiting
// disabled entries to a synthetic "disabled" row without touching git.
func collectStatuses(syncer *skill.Syncer, entries []*model.LockEntry, projectRoot string) ([]*skill.SyncStatus, error) {
	var results []*skill.SyncStatus
	for _, e := range entries {
		if e.Disabled {
			results = append(results, &skill.SyncStatus{
				Name:    e.Name,
				Branch:  e.Ref,
				Commit:  e.Commit,
				Message: "disabled",
			})
			continue
		}
		s, err := syncer.Status(e, projectRoot)
		if err != nil {
			return nil, fmt.Errorf("status %s: %w", e.Name, err)
		}
		results = append(results, s)
	}
	return results, nil
}

// renderStatusTable prints the per-skill status table, deriving the STATE column
// from the disabled/broken/dirty/message signals.
func renderStatusTable(results []*skill.SyncStatus) {
	headers := []string{"SKILL", "BRANCH", "COMMIT", "STATE", "AHEAD", "BEHIND"}
	var rows [][]string
	for _, s := range results {
		state := "clean"
		switch {
		case s.Message == "disabled":
			state = "disabled"
		case s.Broken:
			state = "broken"
		case s.Dirty:
			state = "modified"
		case s.Message != "":
			state = s.Message
		}
		short := s.Commit
		if len(short) > 7 {
			short = short[:7]
		}
		rows = append(rows, []string{
			s.Name, s.Branch, short, state, fmt.Sprintf("%d", s.Ahead), fmt.Sprintf("%d", s.Behind),
		})
	}
	printer.Table(headers, rows)
}
