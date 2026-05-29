package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/output"
	"github.com/raks097/quiver/internal/registry"
	"github.com/raks097/quiver/internal/skill"
	"github.com/spf13/cobra"
)

// doctorCheck records a single health-check result. Type is one of:
//
//	worktree              — the lock entry's worktree dir exists.
//	registry              — the entry's referenced registry is configured and cloned.
//	                        Skipped for Source == "subdir" entries (qvr add installs)
//	                        since those don't live in cfg.Registries by design.
//	symlink               — each target symlink points at the worktree.
//	extra-symlink         — an agent dir holds a symlink not tracked by the lock.
//	orphan-worktree       — a ~/.quiver/worktrees/ entry no lock references.
//	orphan-registry       — a ~/.quiver/registries/ bare clone not in config.
//	orphan-cache          — a cache/index entry for a removed registry.
//	unreferenced-registry — a configured registry with no skills referenced
//	                        by any reachable lock.
//
// Scope labels the lock the entry came from when --all is set ("project" or
// "global"); empty for single-scope runs.
type doctorCheck struct {
	Type    string `json:"type"`
	Scope   string `json:"scope,omitempty"`
	Skill   string `json:"skill,omitempty"`
	Target  string `json:"target,omitempty"`
	Path    string `json:"path,omitempty"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

var (
	doctorGlobal bool
	doctorAll    bool
	doctorStrict bool
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose broken installs, missing worktrees, and stale symlinks",
	Long: `Walk the lock file and verify that every recorded worktree, registry,
and target symlink resolves cleanly. Also surfaces symlinks under agent
directories that have no matching lock entry, plus orphan artifacts left
behind under ~/.quiver/ (worktrees, bare clones, staging dirs, caches).

Defaults to the project lock; --global diagnoses the user-global lock
instead; --all unions both. Exits non-zero on any check failure. Orphan
artifacts and unreferenced-registry checks are informational by default —
pass --strict to fail the exit code on those too.`,
	RunE: runDoctor,
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorGlobal, "global", false,
		"diagnose the user-global lock file instead of the project lock")
	doctorCmd.Flags().BoolVar(&doctorAll, "all", false,
		"diagnose both project and global locks (adds a SCOPE column)")
	doctorCmd.Flags().BoolVar(&doctorStrict, "strict", false,
		"exit non-zero when orphan artifacts are detected (informational by default)")
	rootCmd.AddCommand(doctorCmd)
}

func runDoctor(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}
	locks, err := loadScopedLocks(projectRoot, doctorGlobal, doctorAll)
	if err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	var checks []doctorCheck
	for _, s := range locks {
		scoped := runDoctorChecks(s.Lock, cfg, projectRoot)
		if doctorAll {
			for i := range scoped {
				scoped[i].Scope = s.Scope
			}
		}
		checks = append(checks, scoped...)
	}

	failed := 0
	for _, c := range checks {
		if !c.OK {
			failed++
		}
	}

	// Orphan-artifact scan: walk ~/.quiver/{worktrees,registries,cache/index}
	// and surface entries with no claim from any lock file or config entry.
	// Informational by default; --strict promotes each one to a failure.
	// We pass the first lock as "primary" so scanOrphans still consults the
	// opposite-scope lock; under --all both scopes are already in the lock
	// list so the primary/other distinction collapses to "all entries."
	primary := locks[0]
	orphans := scanOrphans(cfg, primary.Lock, primary.Scope == "global", projectRoot)
	if doctorStrict {
		for i := range orphans {
			orphans[i].OK = false
		}
		failed += len(orphans)
	}
	checks = append(checks, orphans...)

	// unreferenced-registry: a configured registry that no reachable lock
	// (across both scopes) names. Informational; --strict promotes to fail.
	unref := scanUnreferencedRegistries(cfg, locks, projectRoot)
	if doctorStrict {
		for i := range unref {
			unref[i].OK = false
		}
		failed += len(unref)
	}
	checks = append(checks, unref...)

	// When the project lock is empty but the user has skills installed in
	// the global lock, "0/0 checks passed" reads like success and hides the
	// fact that nothing was actually inspected. Surface a clear hint so CI
	// runs and ad-hoc invocations don't silently miss broken global state.
	globalHint := ""
	if !doctorGlobal && !doctorAll && len(primary.Lock.Skills) == 0 {
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
		// The payload already encodes failure via "failed": N. Skip the
		// duplicate {"error": "..."} envelope so stdout stays a single doc.
		if failed > 0 {
			return errJSONHandled
		}
		return nil
	} else {
		for _, c := range checks {
			renderDoctorCheck(c)
		}
		orphanCount, lockFailed := 0, 0
		for _, c := range checks {
			if strings.HasPrefix(c.Type, "orphan-") || c.Type == "unreferenced-registry" {
				orphanCount++
				continue
			}
			if !c.OK {
				lockFailed++
			}
		}
		lockChecks := len(checks) - orphanCount
		switch {
		case len(checks) == 0 && globalHint != "":
			fmt.Fprintf(printer.Out, "\n%s\n", globalHint)
		case len(checks) == 0:
			fmt.Fprintf(printer.Out, "\nno installed skills to check\n")
		default:
			fmt.Fprintf(printer.Out, "\n%d/%d lock-tracked checks passed",
				lockChecks-lockFailed, lockChecks)
			if orphanCount > 0 {
				fmt.Fprintf(printer.Out, ", %d orphan/unreferenced artifact(s) found", orphanCount)
			}
			fmt.Fprintln(printer.Out)
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
// file, config, and project root, it returns the full list of checks. The
// lock's location decides whether symlink targets resolve against project
// agent dirs or user-global ones.
func runDoctorChecks(lock *model.LockFile, cfg *config.Config, projectRoot string) []doctorCheck {
	global := lock.IsGlobal(config.Dir())
	var checks []doctorCheck
	knownLinks := make(map[string]struct{})

	for _, e := range lock.Entries() {
		if e.IsLink() {
			continue
		}
		checks = append(checks, checkWorktree(e))
		// In v5 the lock is self-contained — entry.Source carries the
		// fetch URL on every non-link entry, so `qvr sync` will auto-
		// register any missing registry. Doctor only flags a missing
		// registry when the entry has no Source either (legacy entries
		// or hand-edited locks). Otherwise the registry config is
		// recoverable on the next sync and demanding it here would
		// false-positive on every fresh-clone qvr.lock.
		if e.Registry != "" && e.Source == "" {
			checks = append(checks, checkRegistry(e, cfg))
		}
		for _, t := range e.Targets {
			c, linkPath := checkSymlink(e, t, projectRoot, global)
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
	worktreePath := skill.EntryWorktreePath(e)
	c := doctorCheck{Type: "worktree", Skill: e.Name, Path: worktreePath}
	if worktreePath == "" {
		c.Message = "lock entry has no derivable worktree path"
		return c
	}
	info, err := os.Stat(worktreePath)
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

func checkSymlink(e *model.LockEntry, target, projectRoot string, global bool) (doctorCheck, string) {
	c := doctorCheck{Type: "symlink", Skill: e.Name, Target: target}
	linkPath, err := skill.ResolveTargetPath(target, e.Name, projectRoot, global)
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

// scanOrphans walks the on-disk quiver home and surfaces artifacts that no
// longer correspond to a config entry or a lock entry. We consult both the
// invoking lock (project or global, depending on --global) AND the opposite
// lock so a global install isn't reported as orphaning every project worktree
// or vice versa. Returns informational checks (OK: true); callers can flip
// them to failures under --strict.
func scanOrphans(cfg *config.Config, primary *model.LockFile, primaryIsGlobal bool, projectRoot string) []doctorCheck {
	otherLockPath := model.DefaultLockPath(projectRoot, config.Dir(), !primaryIsGlobal)
	otherLock, _ := model.ReadLockFile(otherLockPath)

	// Index every claim by category so a single pass over each dir is enough.
	claimedWorktrees := map[string]struct{}{}
	claimedRegistries := map[string]struct{}{}
	for _, lock := range []*model.LockFile{primary, otherLock} {
		if lock == nil {
			continue
		}
		for _, e := range lock.Entries() {
			if wt := skill.EntryWorktreePath(e); wt != "" {
				claimedWorktrees[wt] = struct{}{}
			}
			if e.Registry != "" {
				claimedRegistries[e.Registry] = struct{}{}
			}
		}
	}
	for name := range cfg.Registries {
		claimedRegistries[name] = struct{}{}
	}

	var checks []doctorCheck

	// Worktrees orphan: any git worktree under worktrees/ with no lock
	// entry pointing at it. v0.5 layout nests four levels under the root
	// (`<org>/<repo>/<skill>/<sha7>`), while legacy state from earlier
	// versions left flat single-level dirs there. We identify worktrees
	// by the `.git` marker, so both layouts are detected the same way.
	checks = append(checks, scanWorktreeOrphans(
		registry.WorktreesRoot(),
		claimedWorktrees,
	)...)

	// Registry bare clones orphan: dir present under registries/ but no
	// config entry references it. The on-disk layout is two-tier under
	// v0.5 — `<org>/<repo>.git/` for auto-named adds, flat `<name>.git/`
	// for explicit `--name` lanes — so we walk both shapes.
	checks = append(checks, scanRegistryOrphans(
		filepath.Join(config.Dir(), "registries"),
		claimedRegistries,
	)...)

	// Subdir / standalone orphan scans were removed in v4 — both directories
	// were collapsed into registries/, so the registry orphan scan above
	// covers everything.

	// Index cache orphan: cache/index/<name>.json where <name> isn't a configured registry.
	cacheDir := filepath.Join(config.Dir(), "cache", "index")
	if entries, err := os.ReadDir(cacheDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if _, ok := cfg.Registries[name]; ok {
				continue
			}
			checks = append(checks, doctorCheck{
				Type:    "orphan-cache",
				Skill:   name,
				Path:    filepath.Join(cacheDir, e.Name()),
				OK:      true,
				Message: "cached index for removed registry",
			})
		}
	}

	return checks
}

// scanUnreferencedRegistries reports configured registries whose name appears
// in no reachable lock entry. The user can prune these with `qvr registry
// remove <name>` once they confirm nothing local depends on them. Under
// --all we already see both locks via the scopedLock list; in single-scope
// mode we look at the opposite scope too so a global-only-referenced registry
// isn't flagged when the user runs `qvr doctor` from a fresh project.
func scanUnreferencedRegistries(cfg *config.Config, locks []scopedLock, projectRoot string) []doctorCheck {
	if len(cfg.Registries) == 0 {
		return nil
	}
	referenced := map[string]struct{}{}
	collect := func(l *model.LockFile) {
		if l == nil {
			return
		}
		for _, e := range l.Entries() {
			if e.Registry != "" {
				referenced[e.Registry] = struct{}{}
			}
		}
	}
	for _, s := range locks {
		collect(s.Lock)
	}
	// Always pull in the opposite-scope lock so a registry referenced only
	// from the lock we're not inspecting doesn't appear unreferenced. The
	// project lock lives under projectRoot, so the path needs it — empty
	// projectRoot here would resolve to a relative path and silently miss
	// the actual project entries.
	if len(locks) == 1 {
		other := !locks[0].Lock.IsGlobal(config.Dir())
		if otherLock, err := model.ReadLockFile(model.DefaultLockPath(projectRoot, config.Dir(), other)); err == nil {
			collect(otherLock)
		}
	}

	var names []string
	for name := range cfg.Registries {
		if _, ok := referenced[name]; !ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	var checks []doctorCheck
	for _, name := range names {
		checks = append(checks, doctorCheck{
			Type:    "unreferenced-registry",
			Skill:   name,
			Path:    registry.RegistryPath(name),
			OK:      true,
			Message: "configured but no lock entry references it",
		})
	}
	return checks
}

// scanWorktreeOrphans finds every git worktree under the worktrees root
// (identified by a `.git` marker, which `git worktree add` always creates)
// and emits an informational check for any not in the claimed set. We use
// WalkDir + a SkipDir at the worktree boundary so the walker doesn't
// descend into checked-out content — bounded cost regardless of skill
// size. Handles both the v0.5 nested layout (`<org>/<repo>/<skill>/<sha7>`)
// and any flat legacy directories left over from earlier layouts.
func scanWorktreeOrphans(root string, claimed map[string]struct{}) []doctorCheck {
	var out []doctorCheck
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() || path == root {
			return nil //nolint:nilerr // tolerate per-entry stat errors; doctor is best-effort
		}
		if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
			return nil // not a worktree yet, keep descending
		}
		clean := filepath.Clean(path)
		if _, ok := claimed[clean]; !ok {
			out = append(out, doctorCheck{
				Type:    "orphan-worktree",
				Skill:   filepath.Base(path),
				Path:    path,
				OK:      true,
				Message: "not referenced by any lock file",
			})
		}
		return fs.SkipDir // every dir is either a worktree or an intermediate; stop at worktree boundary
	})
	return out
}

// scanRegistryOrphans walks the registries directory under the v0.5
// `<org>/<repo>.git/` layout and emits an informational check for each
// bare clone that no config entry claims. A flat `<name>.git/` at the
// top level (the explicit `--name` lane) is treated identically. Any
// other directory at the org level is left for future schemes — we
// intentionally don't flag it as an orphan unless it contains `.git`
// children, so a hand-placed scratch dir doesn't fail `qvr doctor`.
func scanRegistryOrphans(root string, claimed map[string]struct{}) []doctorCheck {
	topEntries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []doctorCheck
	for _, top := range topEntries {
		topPath := filepath.Join(root, top.Name())
		if !top.IsDir() {
			continue
		}
		// Flat lane: `<name>.git/` directly under registries/.
		if strings.HasSuffix(top.Name(), ".git") {
			name := strings.TrimSuffix(top.Name(), ".git")
			if _, ok := claimed[name]; !ok {
				out = append(out, doctorCheck{
					Type:    "orphan-registry",
					Skill:   name,
					Path:    topPath,
					OK:      true,
					Message: "no matching config entry",
				})
			}
			continue
		}
		// Nested lane: `<org>/<repo>.git/` — recurse one level.
		orgEntries, err := os.ReadDir(topPath)
		if err != nil {
			continue
		}
		for _, child := range orgEntries {
			if !child.IsDir() || !strings.HasSuffix(child.Name(), ".git") {
				continue
			}
			repo := strings.TrimSuffix(child.Name(), ".git")
			name := top.Name() + "/" + repo
			if _, ok := claimed[name]; ok {
				continue
			}
			out = append(out, doctorCheck{
				Type:    "orphan-registry",
				Skill:   name,
				Path:    filepath.Join(topPath, child.Name()),
				OK:      true,
				Message: "no matching config entry",
			})
		}
	}
	return out
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
				Message: "symlink not tracked by " + model.LockFileName,
			})
		}
	}
	return extras
}

func renderDoctorCheck(c doctorCheck) {
	marker := "✗"
	if c.OK {
		marker = "✓"
		// Orphan / unreferenced rows are informational, not "passing" —
		// they need a distinct glyph or users skim past them assuming
		// everything's fine.
		if strings.HasPrefix(c.Type, "orphan-") || c.Type == "unreferenced-registry" {
			marker = "!"
		}
	}
	label := c.Type
	if c.Scope != "" {
		label = fmt.Sprintf("[%s] %s", c.Scope, label)
	}
	if c.Skill != "" {
		label = fmt.Sprintf("%s %s", label, c.Skill)
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
