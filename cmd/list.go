package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	listGlobal bool
	listAll    bool
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed skills",
	Long: `List skills recorded in the lock file. Reads the project lock file by
default; pass --global to read the user-global lock at $QUIVER_HOME/` + model.LockFileName + `
(mirrors the same flag on ` + "`qvr add`" + `). Pass --all to union both locks
and render entries with a SCOPE column.`,
	RunE: runList,
}

func init() {
	listCmd.Flags().BoolVar(&listGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	listCmd.Flags().BoolVar(&listAll, "all", false,
		"union project and global locks (adds a SCOPE column)")
	rootCmd.AddCommand(listCmd)
}

// scopedListEntry pairs a lock entry with the scope it came from. The Scope
// field is empty for single-lock invocations (project- or global-only) so the
// JSON output stays backward-compatible; --all populates it. Name and
// Worktree are surfaced explicitly because v5 keeps them off disk (Name is
// the map key; Worktree is derived from registry.WorktreePath).
type scopedListEntry struct {
	Name     string `json:"name"`
	Worktree string `json:"worktree,omitempty"`
	*model.LockEntry
	Scope string `json:"scope,omitempty"`
}

func runList(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, listGlobal, listAll)
	if err != nil {
		return err
	}

	var rows []scopedListEntry
	for _, s := range locks {
		for _, e := range s.Lock.Entries() {
			row := scopedListEntry{
				Name:      e.Name,
				Worktree:  skill.EntryWorktreePath(e),
				LockEntry: e,
			}
			if listAll {
				row.Scope = s.Scope
			}
			rows = append(rows, row)
		}
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(rows)
	}
	if len(rows) == 0 {
		printer.Info("No installed skills.")
		return nil
	}
	headers := []string{"SKILL", "REGISTRY", "VERSION", "TARGETS", "SOURCE", "STATUS", "SIGNED"}
	if listAll {
		headers = append([]string{"SCOPE"}, headers...)
	}
	var tbl [][]string
	for _, r := range rows {
		tbl = append(tbl, listTableRow(r))
	}
	printer.Table(headers, tbl)
	return nil
}

// listTableRow renders one installed-skill row for the `qvr list` text table,
// prepending the SCOPE column under --all.
func listTableRow(r scopedListEntry) []string {
	reg := r.Registry
	if reg == "" {
		reg = "-"
	}
	// SOURCE column precedence (issue #117): mode wins over the raw
	// Source field. A `qvr edit`-ejected skill still has the
	// upstream URL in Source (preserved as SourceUpstream too), so
	// keying the column off Source alone painted ejected entries
	// identical to shared ones — the user couldn't tell at a glance
	// that the row was now eject-mode and writable. Link installs
	// get their own marker for the same reason; only true shared
	// installs surface the upstream URL.
	var source string
	switch {
	case r.LockEntry != nil && r.IsEdit():
		source = "edit"
	case r.LockEntry != nil && r.IsLink():
		source = "local"
	case r.Source != "":
		source = r.Source
	default:
		source = "-"
	}
	status := "enabled"
	if r.Disabled {
		status = "disabled"
	}
	row := []string{
		r.Name, reg, r.Ref, strings.Join(r.Targets, ","), source, status, signedCol(recordedSigStatus(r.LockEntry)),
	}
	if listAll {
		row = append([]string{r.Scope}, row...)
	}
	return row
}
