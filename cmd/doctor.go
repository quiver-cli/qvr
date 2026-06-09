package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
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

	checks := runScopedDoctorChecks(locks, cfg, projectRoot)

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
	checks = appendInformationalChecks(checks, orphans, &failed)

	// unreferenced-registry: a configured registry that no reachable lock
	// (across both scopes) names. Informational; --strict promotes to fail.
	unref := scanUnreferencedRegistries(cfg, locks, projectRoot)
	checks = appendInformationalChecks(checks, unref, &failed)

	// qvr.toml ⟷ qvr.lock consistency (project scope only — qvr.toml is
	// project-level). Informational unless --strict.
	if !doctorGlobal {
		if projectLock := projectScopeLock(locks); projectLock != nil {
			drift := scanProjectFileDrift(projectLock, model.DefaultProjectPath(projectRoot))
			checks = appendInformationalChecks(checks, drift, &failed)
		}
	}

	globalHint := emptyProjectGlobalHint(primary, projectRoot)

	if printer.Format == output.FormatJSON {
		return emitDoctorJSON(checks, failed, globalHint)
	}
	renderDoctorSummary(checks, globalHint)
	if failed > 0 {
		return fmt.Errorf("%s failed", output.Plural(failed, "check"))
	}
	return nil
}

// runScopedDoctorChecks runs the per-lock health checks across every scoped lock,
// tagging each result with its scope when --all unions both lanes.
func runScopedDoctorChecks(locks []scopedLock, cfg *config.Config, projectRoot string) []doctorCheck {
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
	return checks
}

// emptyProjectGlobalHint returns a hint when the project lock is empty but the
// user has skills installed globally — otherwise "0/0 checks passed" reads like
// success and hides un-inspected global state. Empty when not applicable.
func emptyProjectGlobalHint(primary scopedLock, projectRoot string) string {
	if doctorGlobal || doctorAll || len(primary.Lock.Skills) != 0 {
		return ""
	}
	globalLockPath := model.DefaultLockPath(projectRoot, config.Dir(), true)
	if globalLock, gerr := model.ReadLockFile(globalLockPath); gerr == nil && len(globalLock.Skills) > 0 {
		return fmt.Sprintf("project lock is empty; %s installed globally — run `qvr doctor --global` to diagnose them", output.Plural(len(globalLock.Skills), "skill"))
	}
	return ""
}

// appendInformationalChecks appends an informational scan's results to checks,
// promoting each to a failure (OK=false, bumping *failed) under --strict — the
// shared shape of doctor's orphan / unreferenced / drift augmentations.
func appendInformationalChecks(checks, scanned []doctorCheck, failed *int) []doctorCheck {
	if doctorStrict {
		for i := range scanned {
			scanned[i].OK = false
		}
		*failed += len(scanned)
	}
	return append(checks, scanned...)
}

// projectScopeLock returns the project-scope lock from the scoped list (the one
// that isn't the global lock), or nil when only the global lock is present.
func projectScopeLock(locks []scopedLock) *model.LockFile {
	for _, s := range locks {
		if !s.Lock.IsGlobal(config.Dir()) {
			return s.Lock
		}
	}
	return nil
}

// emitDoctorJSON writes the doctor result envelope. The payload already encodes
// failure via "failed": N, so a non-zero failed count returns errJSONHandled
// rather than a duplicate {"error": …} envelope, keeping stdout a single doc.
func emitDoctorJSON(checks []doctorCheck, failed int, globalHint string) error {
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
	if failed > 0 {
		return errJSONHandled
	}
	return nil
}

// renderDoctorSummary prints the text-mode check rows followed by the
// lock-tracked pass tally, the orphan/unreferenced artifact count, and the
// empty-lock / global-hint variants.
func renderDoctorSummary(checks []doctorCheck, globalHint string) {
	for _, c := range checks {
		renderDoctorCheck(c)
	}
	orphanCount, lockFailed := 0, 0
	for _, c := range checks {
		if strings.HasPrefix(c.Type, "orphan-") || c.Type == "unreferenced-registry" || c.Type == "project-file-drift" {
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
		fmt.Fprintf(printer.Out, "\nNo installed skills to check\n")
	default:
		fmt.Fprintf(printer.Out, "\n%d/%d lock-tracked checks passed",
			lockChecks-lockFailed, lockChecks)
		if orphanCount > 0 {
			fmt.Fprintf(printer.Out, ", %s found", output.Plural(orphanCount, "orphan/unreferenced artifact"))
		}
		fmt.Fprintln(printer.Out)
		if globalHint != "" {
			fmt.Fprintf(printer.Out, "%s\n", globalHint)
		}
	}
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
		checks = append(checks, checkCommitIntegrity(e, projectRoot))
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

	// Walk the agent dirs that match the lock's scope: project locks
	// own the project-relative dirs (.claude/skills/, .cursor/rules/,
	// etc.); the global lock owns the user-home variants
	// (~/.claude/skills/, …). Pre-#60 this was always projectRoot,
	// so a `qvr doctor --global` from inside a project flagged every
	// project-scope symlink as an extra orphan against the global
	// lock — see issue #60.
	checks = append(checks, scanExtraSymlinks(projectRoot, knownLinks, global)...)

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
	// Edit-mode entries (`qvr edit`, `qvr create`) live at EditPath, not
	// in the shared worktree cache. The shared lane is intentionally
	// absent for these, so the worktree check would always fire as
	// red even though the install is healthy. Skip and pass — the
	// edit dir's integrity is covered by `ejected` / `commit-integrity`.
	// Issue #117. Mirror the way commit-integrity already short-circuits
	// when there's nothing to check.
	if e.IsEdit() {
		c.OK = true
		c.Message = "edit-mode entry; shared worktree skipped"
		return c
	}
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

// checkCommitIntegrity verifies entry.Commit hasn't been hand-edited to a
// SHA that doesn't exist in the repo (issue #73). A descendant relationship
// (HEAD descends from entry.Commit, the normal "local commits pending publish"
// state — issue #99) is fine and silent; the only thing surfaced as a failure
// is "entry.Commit isn't reachable from HEAD at all".
func checkCommitIntegrity(e *model.LockEntry, projectRoot string) doctorCheck {
	c := doctorCheck{Type: "commit-integrity", Skill: e.Name}
	if e.Commit == "" {
		c.OK = true
		c.Message = "no commit recorded; skipped"
		return c
	}
	head, herr := skill.ResolveEntryHeadCommit(e, projectRoot)
	if herr != nil || head == "" {
		// Can't read HEAD — leave silent (worktree check above already
		// flags the missing repo case).
		c.OK = true
		return c
	}
	if head == e.Commit {
		c.OK = true
		return c
	}
	if ancestor, _ := skill.EntryCommitIsAncestorOfHead(e, projectRoot); ancestor {
		// Normal "user committed locally, lockfile hasn't caught up" —
		// not tamper, no signal.
		c.OK = true
		return c
	}
	c.Message = fmt.Sprintf("lockfile commit %s is not reachable from worktree HEAD %s — tampered or detached (issue #73)",
		e.Commit, head)
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
	// A consumed root-layout skill's symlink points at the sanitized agent
	// view, not the worktree root (issue #154) — verify against that so doctor
	// doesn't flag the intended target as drift.
	expected := skill.AgentLinkTarget(e, projectRoot)
	if expected == "" {
		c.Message = "no worktree to verify against"
		return c, linkPath
	}
	// For mode:edit entries the canonical target dir IS a real directory
	// (the edit dir), not a symlink. Doctor previously ran VerifyTarget on
	// it and reported `✗ symlink ... is not a symlink` for every ejected
	// skill (issue #81). Detect this case and check the directory's
	// integrity instead — the install path resolves to the canonical when
	// linkPath equals expected, which is the case for the canonical
	// target. Sibling targets remain symlinks pointing at canonical and
	// continue to use VerifyTarget.
	if e.IsEdit() && linkPath == expected {
		c.Type = "ejected"
		if err := skill.VerifyDirContainsSkill(linkPath); err != nil {
			c.Message = err.Error()
			return c, linkPath
		}
		c.OK = true
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

// scanProjectFileDrift compares qvr.toml (declarative intent) against the
// project lock (resolved state) and reports divergence: skills declared in
// qvr.toml the lock lacks, portable lock entries missing from qvr.toml, and ref
// mismatches. Write-through on every mutating command keeps the two in step, so
// drift means a hand-edit or a git merge — informational by default, promoted by
// --strict. Skipped entirely when the project hasn't adopted qvr.toml.
func scanProjectFileDrift(lock *model.LockFile, projPath string) []doctorCheck {
	if _, err := os.Stat(projPath); os.IsNotExist(err) {
		return nil // project hasn't adopted qvr.toml — nothing to reconcile
	}
	proj, err := model.ReadProjectFile(projPath)
	if err != nil {
		return []doctorCheck{{Type: "project-file-drift", Path: projPath, OK: true, Message: "qvr.toml unreadable: " + err.Error()}}
	}

	lockByCoord := make(map[string]*model.LockEntry)
	var lockCoords []string
	for _, e := range lock.Entries() {
		if c := model.SkillCoordinate(e); c != "" {
			lockByCoord[c] = e
			lockCoords = append(lockCoords, c)
		}
	}
	sort.Strings(lockCoords)

	var checks []doctorCheck
	for _, coord := range proj.SkillCoordinates() {
		ref := proj.SkillRef(coord)
		e, ok := lockByCoord[coord]
		if !ok {
			checks = append(checks, doctorCheck{
				Type: "project-file-drift", Skill: coord, OK: true,
				Message: fmt.Sprintf("declared in qvr.toml (@%s) but not in qvr.lock — run `qvr sync`", ref),
			})
			continue
		}
		if e.Ref != ref {
			checks = append(checks, doctorCheck{
				Type: "project-file-drift", Skill: coord, OK: true,
				Message: fmt.Sprintf("qvr.toml @%s vs qvr.lock @%s — run `qvr lock --from-toml` (apply qvr.toml) or `qvr sync` (keep the lock)", ref, e.Ref),
			})
		}
	}
	for _, coord := range lockCoords {
		if !proj.HasSkill(coord) {
			checks = append(checks, doctorCheck{
				Type: "project-file-drift", Skill: coord, OK: true,
				Message: "in qvr.lock but not qvr.toml — run `qvr sync` to record it",
			})
		}
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
			// Render the path relative to the worktrees root
			// (`<registry>/<skill>/<sha7>`) so distinct orphans don't
			// collapse to the same display label when two skills happen
			// to share a commit prefix — the bug #62 case for monorepo
			// registries like dspy-skills where every skill rides one
			// master SHA. Falls back to the leaf if filepath.Rel fails.
			label, rerr := filepath.Rel(root, path)
			if rerr != nil || label == "" {
				label = filepath.Base(path)
			}
			out = append(out, doctorCheck{
				Type:    "orphan-worktree",
				Skill:   label,
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
		if before, ok := strings.CutSuffix(top.Name(), ".git"); ok {
			name := before
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

// agentDirForScope picks the agent dir scanExtraSymlinks should walk for a
// given target and lock scope. Project-scope locks own the project-relative
// dirs (.claude/skills/<name>); the global lock owns the user-home variants
// (~/.claude/skills/<name>). Returning "" means skip this target — the
// scanner treats unresolvable home expansions as best-effort.
func agentDirForScope(t model.Target, projectRoot string, global bool) (string, error) {
	if !global {
		return filepath.Join(projectRoot, t.LocalDir), nil
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", herr
	}
	g := t.GlobalDir
	if strings.HasPrefix(g, "~/") {
		g = filepath.Join(home, g[2:])
	} else if g == "~" {
		g = home
	}
	return g, nil
}

// scanExtraSymlinks looks under each known agent target dir for symlinks that
// don't appear in the lock file. These are usually leftovers from a manual
// `rm` of a lock entry or a previous tool — not catastrophic, but noisy.
//
// global=true walks the user-home agent dirs (~/.claude/skills, etc.) so
// `qvr doctor --global` compares the global lock against the global
// symlinks instead of the surrounding project's dirs — pre-#60 the
// unconditional projectRoot walk made every project-scope symlink look
// like an orphan against the global lock and broke `qvr doctor --global`
// in CI.
func scanExtraSymlinks(projectRoot string, knownLinks map[string]struct{}, global bool) []doctorCheck {
	var extras []doctorCheck
	// Same managed-prefix policy `qvr sync` uses (issue #68). A symlink
	// whose target lives inside the qvr-managed scope (the worktrees
	// cache or the project vendor dir) but isn't named in the lock is a
	// real `extra-symlink` failure — sync would prune it. A symlink
	// whose target is some other tool's territory (claudeskills.io,
	// MCP-managed dirs, hand-managed symlinks under ~/.agents/...) is
	// not ours to touch; doctor surfaces it informationally (`!` glyph)
	// so it doesn't permanently red-exit CI for users with a mixed
	// agent-dir setup.
	managedPrefixes := skill.ManagedRoots(projectRoot)
	// Targets can share a skills dir (the AGENTS.md `.agents/skills` convention,
	// or `.claude/skills` shared by claude and xcode-claude). Walk each unique
	// dir once so a stray symlink isn't reported under every target that maps to
	// it.
	scanned := make(map[string]struct{}, len(model.Targets))
	// Iterate in canonical (sorted) name order so that when several targets
	// share a skills dir (e.g. claude and xcode-claude both map .claude/skills),
	// the orphan is consistently attributed to the alphabetically-first target
	// rather than to whichever map iteration happened to land first.
	for _, tname := range model.TargetNames() {
		t := model.Targets[tname]
		dir, derr := agentDirForScope(t, projectRoot, global)
		if derr != nil || dir == "" {
			continue
		}
		if _, dup := scanned[filepath.Clean(dir)]; dup {
			continue
		}
		scanned[filepath.Clean(dir)] = struct{}{}
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
			// Follow the link and decide which bucket it belongs in.
			// Readlink failure → treat as extra (the conservative
			// default: if we can't tell where it points, surface it).
			resolved, rerr := os.Readlink(full)
			if rerr == nil {
				if !filepath.IsAbs(resolved) {
					resolved = filepath.Join(filepath.Dir(full), resolved)
				}
				if !skill.IsManaged(resolved, managedPrefixes) {
					extras = append(extras, doctorCheck{
						Type:    "orphan-external-symlink",
						Skill:   entry.Name(),
						Target:  tname,
						Path:    full,
						OK:      true, // informational — not a failure
						Message: "target outside qvr scope (" + resolved + ") — left untouched",
					})
					continue
				}
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
	// Standalone status markers (not tabwriter cells), so colour is safe:
	// green pass, red fail, yellow informational.
	style := printer.StyleOut()
	marker := style.Red("✗")
	if c.OK {
		marker = style.Green("✓")
		// Orphan / unreferenced rows are informational, not "passing" —
		// they need a distinct glyph or users skim past them assuming
		// everything's fine.
		if strings.HasPrefix(c.Type, "orphan-") || c.Type == "unreferenced-registry" || c.Type == "project-file-drift" {
			marker = style.Yellow("!")
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
