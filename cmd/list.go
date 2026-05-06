package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/spf13/cobra"
)

var listGlobal bool

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed skills",
	Long: `List skills recorded in the lock file. Reads the project lock file by
default; pass --global to read the user-global lock at $QUIVER_HOME/qvr.lock.json
(mirrors the same flag on ` + "`qvr install`" + `).`,
	RunE: runList,
}

func init() {
	listCmd.Flags().BoolVar(&listGlobal, "global", false,
		"read the user-global lock file instead of the project lock")
	rootCmd.AddCommand(listCmd)
}

func runList(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), listGlobal)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	entries := lock.Entries()
	if printer.Format == output.FormatJSON {
		return printer.JSON(entries)
	}
	if len(entries) == 0 {
		printer.Info("No installed skills.")
		return nil
	}
	headers := []string{"SKILL", "REGISTRY", "VERSION", "TARGETS", "SOURCE", "STATUS"}
	var rows [][]string
	for _, e := range entries {
		reg := e.Registry
		if reg == "" {
			reg = "-"
		}
		source := e.Source
		if source == "" {
			source = "registry"
		}
		status := "enabled"
		if e.Disabled {
			status = "disabled"
		}
		rows = append(rows, []string{
			e.Name, reg, e.Branch, strings.Join(e.Targets, ","), source, status,
		})
	}
	printer.Table(headers, rows)
	return nil
}
