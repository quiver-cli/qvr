package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [name]",
	Short: "Initialize a qvr project (scaffold qvr.toml) in the current directory",
	Long: `Initialize a qvr project by writing a qvr.toml into the current directory.

Mirrors ` + "`uv init`" + `: it scaffolds the declarative front door (qvr.toml) and
nothing else — no skill, no qvr.lock, no git repo. Use ` + "`qvr create`" + ` to author a
skill and ` + "`qvr add`" + ` to install one; the resolved qvr.lock is materialised lazily
by those commands (and by ` + "`qvr sync`" + `).

[name] is the project name (defaults to the current directory's basename).
Project version defaults to "0.1.0".

Target inference:
  If the project already has agent skill directories on disk (e.g. .claude/skills,
  .github/skills, .agents/skills), their targets are auto-populated into
  [project].default-targets. A shared dir like .agents/skills maps to the
  universal "project" target. With no agent dirs present, default-targets is left
  unset and bare ` + "`qvr add`" + ` falls back to the config default_target.

Refuses to overwrite an existing qvr.toml.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runProjectInit,
}

func init() {
	rootCmd.AddCommand(initCmd)
}

func runProjectInit(cmd *cobra.Command, args []string) error {
	_ = cmd
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	projPath := model.DefaultProjectPath(projectRoot)
	if _, serr := os.Stat(projPath); serr == nil {
		return fmt.Errorf("%s already exists — nothing to do (edit it, or use `qvr target add` / `qvr add`)", projPath)
	} else if !os.IsNotExist(serr) {
		return fmt.Errorf("stat qvr.toml: %w", serr)
	}

	name := filepath.Base(projectRoot)
	if len(args) == 1 && args[0] != "" {
		name = args[0]
	}

	targets := model.InferDefaultTargets(projectRoot)
	if err := writeScaffoldedProjectFile(projPath, name, "0.1.0", targets); err != nil {
		return fmt.Errorf("scaffold qvr.toml: %w", err)
	}

	if printer.Format == output.FormatJSON {
		if targets == nil {
			targets = []string{}
		}
		return printer.JSON(map[string]any{
			"name":            name,
			"path":            projPath,
			"version":         "0.1.0",
			"default_targets": targets,
			"inferred":        len(targets) > 0,
			"created":         "qvr.toml",
		})
	}

	printer.Success(fmt.Sprintf("Initialized project %q at %s", name, model.ProjectFileName))
	if len(targets) > 0 {
		printer.Detail(fmt.Sprintf("inferred default targets: %s", strings.Join(targets, ", ")))
	} else {
		printer.Hint("no agent dirs detected — set targets with `qvr target add <agent>`")
	}
	printer.Hint("run `qvr create <skill>` to author a skill, or `qvr add <skill>` to install one")
	return nil
}
