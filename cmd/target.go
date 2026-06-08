package cmd

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/spf13/cobra"
)

var targetGlobal bool

var targetCmd = &cobra.Command{
	Use:   "target",
	Short: "Manage which coding agents this project installs skills into",
	Long: `Manage the project's agent routing policy — the set of coding agents a bare
'qvr add <skill>' installs into when no --target flag is given.

The policy is stored in qvr.toml ([project].default-targets), so it travels with
the repo: a teammate who clones and runs 'qvr add' or 'qvr sync' lands skills in
exactly the same agent directories, with no machine-local config drift.

  qvr target add codex claude     # default future adds into Codex + Claude
  qvr target list                 # show all supported agents + project defaults
  qvr target remove codex         # stop defaulting into Codex

Target selection is resolved in this strict, mutually-exclusive order:

  1. an explicit 'qvr add --target <name>' flag (overrides everything)
  2. the project's qvr.toml [project].default-targets (set by 'qvr target add')
  3. the machine-local config 'default_target'`,
}

var targetAddCmd = &cobra.Command{
	Use:   "add <name>...",
	Short: "Add agent target(s) to the project's default install set",
	Long: `Add one or more coding agents to the project's default target set in qvr.toml.
Names may be canonical (e.g. "claude") or aliases (e.g. "claude-code"); aliases
are normalised to their canonical name before being written.

  qvr target add codex claude`,
	Args: cobra.MinimumNArgs(1),
	RunE: runTargetAdd,
}

var targetRemoveCmd = &cobra.Command{
	Use:   "remove <name>...",
	Short: "Remove agent target(s) from the project's default install set",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runTargetRemove,
}

var targetListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all supported agents and the project's default targets",
	Args:  cobra.NoArgs,
	RunE:  runTargetList,
}

func init() {
	targetCmd.PersistentFlags().BoolVar(&targetGlobal, "global", false,
		"(unsupported) qvr target operates on the project qvr.toml; global installs are lock-only")
	targetCmd.AddCommand(targetAddCmd, targetRemoveCmd, targetListCmd)
	rootCmd.AddCommand(targetCmd)
}

// canonicalizeTargets resolves every name to its canonical target name,
// rejecting unknown agents with a message that points at `qvr target list`.
func canonicalizeTargets(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		canonical, ok := model.CanonicalTarget(n)
		if !ok {
			return nil, fmt.Errorf("unknown agent target %q — run `qvr target list` to see all supported agents", n)
		}
		out = append(out, canonical)
	}
	return out, nil
}

// targetPaths resolves the project's qvr.toml path plus the qvr.lock path used
// as the WithLock sentinel. target operates on qvr.toml only; --global is
// rejected because default-targets is a project-level routing policy (global
// installs have no project file). The WithLock sentinel stays keyed on the lock
// path so a concurrent `qvr add` (which also writes qvr.toml under that
// sentinel) is serialized against us.
func targetPaths() (projPath, lockPath string, err error) {
	if targetGlobal {
		return "", "", fmt.Errorf("qvr target operates on the project qvr.toml; --global is not supported (global installs are lock-only)")
	}
	projectRoot, err := os.Getwd()
	if err != nil {
		return "", "", fmt.Errorf("resolve cwd: %w", err)
	}
	return model.DefaultProjectPath(projectRoot), model.DefaultLockPath(projectRoot, config.Dir(), false), nil
}

func runTargetAdd(cmd *cobra.Command, args []string) error {
	canonical, err := canonicalizeTargets(args)
	if err != nil {
		return err
	}
	projPath, lockPath, err := targetPaths()
	if err != nil {
		return err
	}

	var added, final []string
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		proj, err := model.ReadProjectFile(projPath)
		if err != nil {
			return fmt.Errorf("read project file: %w", err)
		}
		added = proj.AddDefaultTargets(canonical...)
		final = append([]string(nil), proj.Project.DefaultTargets...)
		if len(added) == 0 {
			return nil // nothing changed — skip the write to keep the diff quiet
		}
		if err := proj.Write(); err != nil {
			return fmt.Errorf("write project file: %w", err)
		}
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{"added": added, "defaultTargets": final})
	}
	if len(added) == 0 {
		printer.Info(fmt.Sprintf("No change — default targets already: %s", strings.Join(final, ", ")))
		return nil
	}
	printer.Success(fmt.Sprintf("Added %s → default targets: %s", strings.Join(added, ", "), strings.Join(final, ", ")))
	printer.Info("Hint: commit qvr.toml so teammates inherit the same agent routing (`git add qvr.toml`)")
	return nil
}

func runTargetRemove(cmd *cobra.Command, args []string) error {
	// Removal accepts aliases too, normalising them so e.g. `remove claude-code`
	// drops the `claude` entry. Unknown names are still rejected up front.
	canonical, err := canonicalizeTargets(args)
	if err != nil {
		return err
	}
	projPath, lockPath, err := targetPaths()
	if err != nil {
		return err
	}

	var removed, final []string
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		proj, err := model.ReadProjectFile(projPath)
		if err != nil {
			return fmt.Errorf("read project file: %w", err)
		}
		removed = proj.RemoveDefaultTargets(canonical...)
		final = append([]string(nil), proj.Project.DefaultTargets...)
		if len(removed) == 0 {
			return nil
		}
		if err := proj.Write(); err != nil {
			return fmt.Errorf("write project file: %w", err)
		}
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	if printer.Format == output.FormatJSON {
		return printer.JSON(map[string]any{"removed": removed, "defaultTargets": final})
	}
	if len(removed) == 0 {
		printer.Info("No change — none of those targets were in the default set")
		return nil
	}
	printer.Success(fmt.Sprintf("Removed %s → default targets: %s", strings.Join(removed, ", "), defaultsOrNone(final)))
	return nil
}

func runTargetList(cmd *cobra.Command, args []string) error {
	projPath, _, err := targetPaths()
	if err != nil {
		return err
	}
	proj, err := model.ReadProjectFile(projPath)
	if err != nil {
		return fmt.Errorf("read project file: %w", err)
	}
	defaults := make(map[string]struct{}, len(proj.Project.DefaultTargets))
	for _, n := range proj.Project.DefaultTargets {
		defaults[n] = struct{}{}
	}

	if printer.Format == output.FormatJSON {
		type targetJSON struct {
			Name      string   `json:"name"`
			Display   string   `json:"display,omitempty"`
			LocalDir  string   `json:"local_dir"`
			GlobalDir string   `json:"global_dir"`
			Aliases   []string `json:"aliases,omitempty"`
			Default   bool     `json:"default"`
		}
		out := make([]targetJSON, 0, len(model.Targets))
		for _, name := range model.TargetNames() {
			t := model.Targets[name]
			_, isDefault := defaults[name]
			out = append(out, targetJSON{
				Name: t.Name, Display: t.Display, LocalDir: t.LocalDir,
				GlobalDir: t.GlobalDir, Aliases: t.Aliases, Default: isDefault,
			})
		}
		return printer.JSON(map[string]any{
			"defaultTargets": proj.Project.DefaultTargets,
			"targets":        out,
		})
	}

	rows := make([][]string, 0, len(model.Targets))
	for _, name := range model.TargetNames() {
		t := model.Targets[name]
		marker := ""
		if _, ok := defaults[name]; ok {
			marker = "✓"
		}
		rows = append(rows, []string{marker, t.Name, t.Display, t.LocalDir, t.GlobalDir, strings.Join(t.Aliases, ", ")})
	}
	printer.Table([]string{"DEFAULT", "NAME", "AGENT", "PROJECT DIR", "GLOBAL DIR", "ALIASES"}, rows)
	if len(proj.Project.DefaultTargets) > 0 {
		sorted := append([]string(nil), proj.Project.DefaultTargets...)
		sort.Strings(sorted)
		printer.Info(fmt.Sprintf("Project default targets: %s", strings.Join(sorted, ", ")))
	} else {
		printer.Info("No project default targets set — `qvr add` falls back to config default_target. Set them with `qvr target add <name>...`")
	}
	return nil
}

// defaultsOrNone renders a target list for humans, or "(none)" when empty.
func defaultsOrNone(targets []string) string {
	if len(targets) == 0 {
		return "(none)"
	}
	return strings.Join(targets, ", ")
}
