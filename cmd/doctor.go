package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

// doctorCheck records a single health-check result. Type is one of:
//
//	worktree       — the lock entry's worktree dir exists.
//	registry       — the entry's referenced registry is configured and cloned.
//	                 Skipped for Source == "subdir" entries (qvr add installs)
//	                 since those don't live in cfg.Registries by design.
//	symlink        — each target symlink points at the worktree.
//	extra-symlink  — an agent dir holds a symlink not tracked by the lock.
type doctorCheck struct {
	Type    string `json:"type"`
	Skill   string `json:"skill,omitempty"`
	Target  string `json:"target,omitempty"`
	Path    string `json:"path,omitempty"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

var doctorGlobal bool

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose broken installs, missing worktrees, and stale symlinks",
	Long: `Walk the lock file and verify that every recorded worktree, registry,
and target symlink resolves cleanly. Also surfaces symlinks under agent
directories that have no matching lock entry. Exits non-zero on any failure
so it slots into CI.`,
	RunE: runDoctor,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorGlobal, "global", false,
		"diagnose the user-global lock file instead of the project lock")
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), doctorGlobal)
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	checks := runDoctorChecks(lock, cfg, projectRoot)

	failed := 0
	for _, c := range checks {
		if !c.OK {
			failed++
		}
	}

	// When the project lock is empty but the user has skills installed in
	// the global lock, "0/0 checks passed" reads like success and hides the
	// fact that nothing was actually inspected. Surface a clear hint so CI
	// runs and ad-hoc invocations don't silently miss broken global state.
	globalHint := ""
	if !doctorGlobal && len(lock.Skills) == 0 {
		globalLockPath := model.DefaultLockPath(projectRoot, config.Dir(), true)
		if globalLock, gerr := model.ReadLockFile(globalLockPath); gerr == nil && len(globalLock.Skills) > 0 {
			globalHint = fmt.Sprintf("project lock is empty; %d skill(s) installed globally — run `qvr doctor --global` to diagnose them", len(globalLock.Skills))
		}
	}

	if printer.Format == output.FormatJSON {
		out := map[string]any{
			"checks": checks,
			"failed": failed,
			"total":  len(checks),
		}
		if globalHint != "" {
			out["hint"] = globalHint
		}
		if jsonErr := printer.JSON(out); jsonErr != nil {
			return jsonErr
		}
	} else {
		for _, c := range checks {
			renderDoctorCheck(c)
		}
		switch {
		case len(checks) == 0 && globalHint != "":
			fmt.Fprintf(printer.Out, "\n%s\n", globalHint)
		case len(checks) == 0:
			fmt.Fprintf(printer.Out, "\nno installed skills to check\n")
		default:
			fmt.Fprintf(printer.Out, "\n%d/%d checks passed\n", len(checks)-failed, len(checks))
			if globalHint != "" {
				fmt.Fprintf(printer.Out, "%s\n", globalHint)
			}
		}
	}

	if failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

// runDoctorChecks is the side-effect-free heart of `qvr doctor` — given a lock
// file, config, and project root, it returns the full list of checks.
func runDoctorChecks(lock *model.LockFile, cfg *config.Config, projectRoot string) []doctorCheck {
	var checks []doctorCheck
	knownLinks := make(map[string]struct{})

	for _, e := range lock.Entries() {
		if e.Source == "link" {
			continue
		}
		checks = append(checks, checkWorktree(e))
		// Subdir installs (`qvr add <url>`) deliberately don't appear in
		// cfg.Registries — the bare clone is owned by the lock entry and
		// kept under registry.SubdirRoot(). Verifying that the worktree
		// path exists (above) is sufficient; demanding a config entry
		// would flag every `qvr add` install as broken.
		if e.Registry != "" && e.Source != "subdir" {
			checks = append(checks, checkRegistry(e, cfg))
		}
		for _, t := range e.Targets {
			c, linkPath := checkSymlink(e, t, projectRoot)
			checks = append(checks, c)
			// Even disabled entries claim their symlink path so a disabled
			// skill's old symlink — if some other process re-creates one —
			// isn't surfaced as an "extra".
			if linkPath != "" {
				knownLinks[linkPath] = struct{}{}
			}
		}
	}

	checks = append(checks, scanExtraSymlinks(projectRoot, knownLinks)...)

	sort.SliceStable(checks, func(i, j int) bool {
		if checks[i].Skill != checks[j].Skill {
			return checks[i].Skill < checks[j].Skill
		}
		return checks[i].Type < checks[j].Type
	})
	return checks
}

func checkWorktree(e *model.LockEntry) doctorCheck {
	c := doctorCheck{Type: "worktree", Skill: e.Name, Path: e.Worktree}
	if e.Worktree == "" {
		c.Message = "lock entry has no worktree path"
		return c
	}
	info, err := os.Stat(e.Worktree)
	if err != nil {
		c.Message = err.Error()
		return c
	}
	if !info.IsDir() {
		c.Message = "worktree path is not a directory"
		return c
	}
	c.OK = true
	return c
}

func checkRegistry(e *model.LockEntry, cfg *config.Config) doctorCheck {
	c := doctorCheck{Type: "registry", Skill: e.Name, Target: e.Registry}
	if _, ok := cfg.Registries[e.Registry]; !ok {
		c.Message = "registry not in config"
		return c
	}
	repoPath := registry.RegistryPath(e.Registry)
	c.Path = repoPath
	if _, err := os.Stat(repoPath); err != nil {
		c.Message = "bare clone missing: " + err.Error()
		return c
	}
	c.OK = true
	return c
}

func checkSymlink(e *model.LockEntry, target, projectRoot string) (doctorCheck, string) {
	c := doctorCheck{Type: "symlink", Skill: e.Name, Target: target}
	linkPath, err := skill.ResolveTargetPath(target, e.Name, projectRoot, e.Global)
	if err != nil {
		c.Message = err.Error()
		return c, ""
	}
	c.Path = linkPath
	if e.Disabled {
		c.OK = true
		c.Message = "disabled (no symlink expected)"
		return c, linkPath
	}
	expected := skill.EffectiveTarget(e)
	if expected == "" {
		c.Message = "no worktree to verify against"
		return c, linkPath
	}
	if err := skill.VerifyTarget(linkPath, expected); err != nil {
		c.Message = err.Error()
		return c, linkPath
	}
	c.OK = true
	return c, linkPath
}

// scanExtraSymlinks looks under each known agent target dir for symlinks that
// don't appear in the lock file. These are usually leftovers from a manual
// `rm` of a lock entry or a previous tool — not catastrophic, but noisy.
func scanExtraSymlinks(projectRoot string, knownLinks map[string]struct{}) []doctorCheck {
	var extras []doctorCheck
	for tname, t := range model.Targets {
		dir := filepath.Join(projectRoot, t.LocalDir)
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			full := filepath.Join(dir, entry.Name())
			if _, ok := knownLinks[full]; ok {
				continue
			}
			li, err := os.Lstat(full)
			if err != nil || li.Mode()&os.ModeSymlink == 0 {
				continue
			}
			extras = append(extras, doctorCheck{
				Type:    "extra-symlink",
				Skill:   entry.Name(),
				Target:  tname,
				Path:    full,
				Message: "symlink not tracked by qvr.lock.json",
			})
		}
	}
	return extras
}

func renderDoctorCheck(c doctorCheck) {
	marker := "✗"
	if c.OK {
		marker = "✓"
	}
	label := c.Type
	if c.Skill != "" {
		label = fmt.Sprintf("%s %s", c.Type, c.Skill)
	}
	if c.Target != "" {
		label += " [" + c.Target + "]"
	}
	if c.Message != "" {
		fmt.Fprintf(printer.Out, "%s %-32s %s\n", marker, label, c.Message)
		return
	}
	fmt.Fprintf(printer.Out, "%s %s\n", marker, label)
}
