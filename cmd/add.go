package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/git"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

var (
	addTargets []string
	addGlobal  bool
	addForce   bool
	addFrozen  bool
	addNoScan  bool
)

var addCmd = &cobra.Command{
	Use:   "add <skill>[@<ref>]...",
	Short: "Add one or more skills from registered sources to the project lock",
	Long: `Add a skill (by name) from any registered source to the current project's
lock file. The skill is resolved against every configured registry; pin a
specific branch, tag, or commit with @<ref>.

  qvr add tdd                       # writes ./qvr.lock, symlinks .claude/skills/tdd
  qvr add tdd@v2                    # pin a branch or tag
  qvr add --global diagnose         # ambient lane: appears in every Claude session
  qvr add tdd lint review           # batch add — each must resolve to a registered skill

Need to add a new source first? Use:

  qvr registry add <url>

The lockfile is the only source of truth for what the agent loads. Anything
under .claude/skills/ that isn't in qvr.lock is hidden on the next ` + "`qvr sync`" + `.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runAdd,
}

func init() {
	addCmd.Flags().StringSliceVar(&addTargets, "target", nil,
		"agent target(s) to install into (repeatable). Defaults to default_target (which may itself be comma-separated, e.g. \"claude,cursor\").")
	addCmd.Flags().BoolVar(&addGlobal, "global", false,
		"write to the user-global lock and symlink under ~/.<agent>/skills/ instead of the project")
	addCmd.Flags().BoolVar(&addForce, "force", false,
		"allow replacing an existing lock entry at a different ref")
	addCmd.Flags().BoolVar(&addFrozen, "frozen", false,
		"refuse drift from the recorded subtree hash; the skill must already be in the lock")
	addCmd.Flags().BoolVar(&addNoScan, "no-scan", false,
		"skip the security scan that normally gates installs (override security.scan_on_install)")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	targets := addTargets
	if len(targets) == 0 {
		targets = config.ParseDefaultTargets(cfg.DefaultTarget)
		if len(targets) == 0 {
			return fmt.Errorf("no --target specified and default_target is unset")
		}
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	installer := skill.NewInstaller(newRegistryManager(gc), wt, gc)
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), addGlobal)

	var results []*skill.InstallResult
	var firstErr error
	lockErr := model.WithLock(lockPath, func() error {
		for _, ref := range args {
			result, err := installer.Install(skill.InstallRequest{
				Skill:       ref,
				Targets:     targets,
				Global:      addGlobal,
				ProjectRoot: projectRoot,
				LockPath:    lockPath,
				Force:       addForce,
				Frozen:      addFrozen,
			})
			if err != nil {
				// Skill not found is the headline error — point at `qvr registry add`
				// so the user knows the next step. Everything else falls through with
				// the wrapped error.
				if errors.Is(err, skill.ErrSkillNotFound) {
					err = fmt.Errorf("no registered source contains a skill named %q — register one with `qvr registry add <url>`", ref)
				}
				printer.Error(fmt.Sprintf("add %s: %v", ref, err))
				if firstErr == nil {
					firstErr = err
				}
				continue
			}

			// Security gate. Scan the freshly-installed worktree and roll back
			// the install if findings meet or exceed the configured threshold.
			// Done inside the WithLock window so a blocked install also
			// reverts the lock entry atomically.
			gate, gerr := ScanAndGate(cmd.Context(), skillDirFor(result, lockPath), cfg, scanGateOptions{
				Disabled: addNoScan,
				Action:   "add",
				Subject:  result.Name,
			})
			if gerr != nil {
				printer.Warning(fmt.Sprintf("add %s: scan failed (%v); install kept — rerun `qvr scan %s` to retry", result.Name, gerr, result.Name))
				results = append(results, result)
				continue
			}
			if gate.Blocked {
				removeErr := installer.Remove(result.Name, skill.InstallRequest{
					ProjectRoot: projectRoot,
					Global:      addGlobal,
					LockPath:    lockPath,
				})
				if removeErr != nil {
					printer.Error(fmt.Sprintf("add %s: scan blocked, rollback also failed (%v); run `qvr remove %s --force` to clean up", result.Name, removeErr, result.Name))
				}
				blockErr := &blockedScanError{Subject: result.Name, Threshold: gate.Threshold, Result: gate.Result}
				if firstErr == nil {
					firstErr = blockErr
				}
				continue
			}
			// Persist the (allowed) scan result onto the lock entry so
			// downstream tools can inspect it without re-running the scan.
			// A write failure here is non-fatal — the install itself
			// succeeded and the user can re-record via `qvr scan`.
			if recErr := recordScanResult(lockPath, result.Name, gate); recErr != nil {
				printer.Warning(fmt.Sprintf("add %s: scan recorded only in memory (%v)", result.Name, recErr))
			}
			results = append(results, result)
		}
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	// Record the project so `qvr cache prune` knows this lock is reachable.
	registry.TouchProject(lockPath)

	if !addGlobal {
		refreshAgentsMDFromLock(projectRoot)
	}

	if printer.Format == output.FormatJSON {
		payload := buildAddJSONEnvelope(results, firstErr)
		if jerr := printer.JSON(payload); jerr != nil {
			return jerr
		}
		if firstErr != nil {
			return errJSONHandled
		}
		return nil
	}
	for _, r := range results {
		printer.Success(fmt.Sprintf("Added %s@%s → %v", r.Name, r.Version, r.Targets))
	}
	return firstErr
}

// addJSONEnvelope is the stable shape emitted by `qvr add --output json`. The
// installed array is always present (never null) so consumers can safely call
// `.installed[]` without branching on the empty case. error is populated only
// when at least one input failed to install — matches the {"error": ...}
// contract every other command uses on its failure path (bug #54).
type addJSONEnvelope struct {
	Installed []*skill.InstallResult `json:"installed"`
	Error     string                 `json:"error,omitempty"`
}

func buildAddJSONEnvelope(results []*skill.InstallResult, err error) addJSONEnvelope {
	if results == nil {
		results = []*skill.InstallResult{}
	}
	env := addJSONEnvelope{Installed: results}
	if err != nil {
		env.Error = err.Error()
	}
	return env
}

// skillDirFor returns the absolute path of the SKILL.md-bearing directory
// for an InstallResult. The InstallResult's Worktree is the worktree root;
// for layout-A skills the actual SKILL.md sits at <worktree>/<subpath>, so
// we read the freshly-written lock entry to recover the subpath. Layout-B
// repos (SKILL.md at repo root) have an empty/"." subpath and the worktree
// root itself is the skill dir.
//
// Returns "" only if the entry can't be located — callers treat that as
// "skip the gate" since there's nothing scannable yet.
func skillDirFor(result *skill.InstallResult, lockPath string) string {
	if result == nil || result.Worktree == "" {
		return ""
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return result.Worktree
	}
	entry, err := lock.Get(result.Name)
	if err != nil {
		return result.Worktree
	}
	worktreePath := skill.EntryWorktreePath(entry)
	if entry.Path == "" || entry.Path == "." {
		return worktreePath
	}
	return filepath.Join(worktreePath, entry.Path)
}
