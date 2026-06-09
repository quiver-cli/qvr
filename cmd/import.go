package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/manifest"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	importGlobal  bool
	importForce   bool
	importFrozen  bool
	importNoScan  bool
	importTargets []string
)

var importCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Add registries and install skills from a portable manifest",
	Long: `Read a manifest produced by ` + "`qvr export`" + ` (or hand-written in the
same three-column shape — repo URL, skill name, version) and reproduce the
same skill set locally. For each line, ` + "`qvr import`" + ` registers the
source repo if it isn't already registered, then installs the skill into the
project lock — the same code path ` + "`qvr add`" + ` uses.

  qvr import skills.txt                  # register sources, install each skill
  qvr import --target=claude,cursor skills.txt
  qvr import --frozen skills.lock.txt    # honor --commit pins on each line

URLs already registered under a different local alias are reused silently
rather than registered again. Per-line failures surface as ` + "`✗ import …: <reason>`" + `
on stderr; the command exits non-zero if anything failed but successful
installs are kept.`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

func init() {
	importCmd.Flags().BoolVar(&importGlobal, "global", false,
		"install into the user-global lock instead of the project lock")
	importCmd.Flags().BoolVar(&importForce, "force", false,
		"allow replacing an existing lock entry at a different ref")
	importCmd.Flags().BoolVar(&importFrozen, "frozen", false,
		"refuse drift from the recorded subtree hash on entries that pin a --commit")
	importCmd.Flags().BoolVar(&importNoScan, "no-scan", false,
		"skip the security scan that normally gates installs")
	importCmd.Flags().StringSliceVar(&importTargets, "target", nil,
		"agent target(s) to install into (repeatable). Overrides per-line --target= on each manifest entry.")
	rootCmd.AddCommand(importCmd)
}

// importLineResult is the per-line outcome that feeds both the JSON envelope
// and the text printer. RegistryAdded carries the freshly-added registry
// name (empty when the URL was already registered).
type importLineResult struct {
	Line           int                  `json:"line"`
	Skill          string               `json:"skill"`
	RepoURL        string               `json:"repoUrl"`
	RegistryAdded  string               `json:"registryAdded,omitempty"`
	RegistryReused string               `json:"registryReused,omitempty"`
	Install        *skill.InstallResult `json:"install,omitempty"`
	Error          string               `json:"error,omitempty"`
}

type importEnvelope struct {
	Lines  []importLineResult `json:"lines,omitempty"`
	Errors []string           `json:"parseErrors,omitempty"`
	Error  string             `json:"error,omitempty"`
}

func runImport(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := enforceScanPolicy(cfg, importNoScan); err != nil {
		return err
	}
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	manifestPath := args[0]
	entries, parseErrs, perr := readManifest(manifestPath)
	if perr != nil {
		return perr
	}
	reportManifestParseErrors(manifestPath, parseErrs)
	if len(entries) == 0 {
		return handleEmptyManifest(parseErrs)
	}

	defaultTargets, derr := resolveImportDefaultTargets(cfg)
	if derr != nil {
		return derr
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	gc := git.NewGoGitClient()
	wt := git.NewGoGitWorktree()
	mgr := newRegistryManager(gc)
	installer := skill.NewInstaller(mgr, wt, gc)
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), importGlobal)

	// Phase 1: ensure every URL referenced by the manifest is registered.
	// Done outside the lock window because mgr.Add itself mutates the user
	// config (not the lockfile) and may take a network round-trip — we don't
	// want to hold the lock during a clone. The phase-1 results are passed
	// into phase 2 so the installer's resolution knows which registry name
	// to scope to.
	urlToAlias, phaseResults, phase1HadError := resolveRegistries(ctx, mgr, entries)

	var lineResults []importLineResult
	// Carry every phase-1 failure into the line results so the JSON envelope
	// reports them; the printer already surfaced them in resolveRegistries.
	firstErr := firstPhaseError(phaseResults)

	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		lineResults, firstErr = installImportEntries(ctx, entries, urlToAlias, defaultTargets, installer, cfg, projectRoot, lockPath, firstErr)
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	registry.TouchProject(lockPath)
	if !importGlobal {
		refreshAgentsMDFromLock(projectRoot)
	}

	if printer.Format == output.FormatJSON {
		return emitImportJSON(lineResults, parseErrs, firstErr, phase1HadError)
	}
	if firstErr != nil || len(parseErrs) > 0 || phase1HadError {
		return errTextHandled
	}
	printImportHint(lineResults)
	return nil
}

// reportManifestParseErrors surfaces each manifest parse error to the printer in
// text mode (JSON mode folds them into the envelope instead).
func reportManifestParseErrors(manifestPath string, parseErrs []manifest.ParseError) {
	if len(parseErrs) > 0 && printer.Format != output.FormatJSON {
		for _, pe := range parseErrs {
			printer.Error(fmt.Sprintf("import %s: %s", manifestPath, pe.Error()))
		}
	}
}

// installImportEntries runs the under-lock install loop for every manifest entry
// and projects the results back into qvr.toml. It returns the per-line results
// and the first failure encountered (seeded with priorErr, typically a phase-1
// registry error). Runs inside WithLock so installs and the qvr.toml write-back
// share one lock window.
func installImportEntries(ctx context.Context, entries []manifest.Entry, urlToAlias map[string]phaseResolution, defaultTargets []string, installer *skill.Installer, cfg *config.Config, projectRoot, lockPath string, priorErr error) ([]importLineResult, error) {
	var lineResults []importLineResult
	firstErr := priorErr
	for _, entry := range entries {
		lr, lerr := importOneEntry(ctx, entry, urlToAlias, defaultTargets, installer, cfg, projectRoot, lockPath)
		lineResults = append(lineResults, lr)
		if lerr != nil && firstErr == nil {
			firstErr = lerr
		}
	}
	// Write-through to qvr.toml for every imported skill — same projection as
	// `qvr add`. Re-read the lock the installs just wrote; the lock is
	// authoritative, so a qvr.toml failure warns rather than failing import.
	if !importGlobal {
		writeImportedProjectFile(projectRoot, lockPath, lineResults)
	}
	return lineResults, firstErr
}

// printImportHint emits the post-import next-step hint when at least one skill
// imported cleanly, steering project vs global installs to the right verb.
func printImportHint(lineResults []importLineResult) {
	if successCount(lineResults) > 0 {
		if importGlobal {
			printer.Info("Hint: `qvr list --global` shows what's installed in the ambient lane")
		} else {
			printer.Info("Hint: commit qvr.lock so teammates reproduce the same skills (`git add qvr.lock`)")
		}
	}
}

// resolveImportDefaultTargets picks the importer-level default target set: the
// --target flag when given, otherwise the machine-local default_target. Returns
// an error when neither is set.
func resolveImportDefaultTargets(cfg *config.Config) ([]string, error) {
	defaultTargets := importTargets
	if len(defaultTargets) == 0 {
		defaultTargets = config.ParseDefaultTargets(cfg.DefaultTarget)
		if len(defaultTargets) == 0 {
			return nil, fmt.Errorf("no --target specified and default_target is unset")
		}
	}
	return defaultTargets, nil
}

// firstPhaseError returns the first phase-1 registry-resolution failure as an
// error (nil when every URL resolved), seeding runImport's firstErr so the
// JSON envelope and exit code reflect registration failures.
func firstPhaseError(phaseResults []phaseResolution) error {
	for _, pr := range phaseResults {
		if pr.Error != "" {
			return errors.New(pr.Error)
		}
	}
	return nil
}

// handleEmptyManifest renders the no-entries outcome for both JSON and text
// modes: parse errors flip the exit non-zero (errJSONHandled / errTextHandled),
// while a genuinely empty manifest is a clean no-op.
func handleEmptyManifest(parseErrs []manifest.ParseError) error {
	if printer.Format == output.FormatJSON {
		env := importEnvelope{}
		for _, pe := range parseErrs {
			env.Errors = append(env.Errors, pe.Error())
		}
		if len(parseErrs) > 0 {
			env.Error = "manifest has no parseable entries"
		}
		if jerr := printer.JSON(env); jerr != nil {
			return jerr
		}
		if len(parseErrs) > 0 {
			return errJSONHandled
		}
		return nil
	}
	if len(parseErrs) > 0 {
		return errTextHandled
	}
	printer.Info("manifest is empty; nothing to import")
	return nil
}

// importOneEntry installs a single manifest entry under the lock: resolve the
// pre-registered registry alias, install the skill, run the scan gate (rolling
// back a blocked install like `qvr add`), and record the result. It returns the
// per-line outcome plus the error (if any) the caller should surface as the
// command's first failure; a scan-failure-but-install-kept yields a nil error.
func importOneEntry(ctx context.Context, entry manifest.Entry, urlToAlias map[string]phaseResolution, defaultTargets []string, installer *skill.Installer, cfg *config.Config, projectRoot, lockPath string) (importLineResult, error) {
	lr := importLineResult{
		Line:    entry.Line,
		Skill:   entry.Skill,
		RepoURL: entry.RepoURL,
	}
	pr, ok := urlToAlias[entry.RepoURL]
	if !ok || pr.alias == "" {
		lr.Error = "registry resolution failed (see prior error)"
		return lr, errors.New(lr.Error)
	}
	if pr.added {
		lr.RegistryAdded = pr.alias
	} else {
		lr.RegistryReused = pr.alias
	}

	result, ierr := installImportSkill(entry, pr, defaultTargets, installer, cfg, projectRoot, lockPath)
	if ierr != nil {
		printer.Error(fmt.Sprintf("import %s: %v", entry.Skill, ierr))
		lr.Error = ierr.Error()
		return lr, ierr
	}

	printSuccess, blockErr := gateImportedSkill(ctx, result, installer, cfg, projectRoot, lockPath, &lr)
	if blockErr != nil {
		return lr, blockErr
	}
	if !printSuccess {
		// Scan failed but install kept — gateImportedSkill already warned and
		// set lr.Install; skip the normal success rendering.
		return lr, nil
	}

	if printer.Format != output.FormatJSON {
		for _, w := range result.Warnings {
			printer.Warning(w)
		}
		if pr.added {
			printer.Success(fmt.Sprintf("Registered registry %s ← %s", pr.alias, entry.RepoURL))
		}
		printer.Success(fmt.Sprintf("Imported %s@%s → %v", result.Name, result.Version, result.Targets))
	}
	return lr, nil
}

// installImportSkill synthesises the "name@ref" skill reference and target set
// for one manifest entry and runs the installer. A not-found error is rewritten
// into a clearer "registry does not publish skill" message; the caller renders
// it. The returned result is nil on error.
func installImportSkill(entry manifest.Entry, pr phaseResolution, defaultTargets []string, installer *skill.Installer, cfg *config.Config, projectRoot, lockPath string) (*skill.InstallResult, error) {
	// Build the skill reference. The installer accepts "name@ref"
	// strings; we synthesise it from the manifest's version (or
	// commit, when --frozen and the entry carries a --commit pin).
	ref := entry.Version
	if importFrozen && entry.Commit != "" {
		ref = entry.Commit
	}
	skillRef := entry.Skill
	if ref != "" {
		skillRef = entry.Skill + "@" + ref
	}

	// Per-line --target= overrides the importer-level default;
	// CLI --target=… overrides both. The CLI override is already
	// folded into defaultTargets above.
	targets := defaultTargets
	if len(importTargets) == 0 && len(entry.Targets) > 0 {
		targets = entry.Targets
	}

	result, ierr := installer.Install(skill.InstallRequest{
		Skill:                    skillRef,
		Targets:                  targets,
		Global:                   importGlobal,
		ProjectRoot:              projectRoot,
		LockPath:                 lockPath,
		Force:                    importForce,
		Frozen:                   importFrozen && entry.Commit != "",
		Registry:                 pr.alias,
		As:                       entry.Alias,
		RequireSigned:            cfg.Security.RequireSigned,
		TrustedAuthors:           trustedAuthorsForRegistry(cfg, pr.alias),
		TrustedAuthorsByRegistry: trustedAuthorsByRegistry(cfg),
	})
	if ierr != nil {
		if errors.Is(ierr, skill.ErrSkillNotFound) {
			ierr = fmt.Errorf("registry %s does not publish skill %q", pr.alias, entry.Skill)
		}
		return nil, ierr
	}
	return result, nil
}

// gateImportedSkill runs the scan gate over a freshly imported skill, rolling
// back a blocked install (like `qvr add`) and recording the scan result. It
// sets lr.Install whenever the install is kept and returns printSuccess=true
// only on a clean pass (the caller then renders the success lines). A scan that
// errored keeps the install but returns printSuccess=false; a blocked scan rolls
// the install back and returns a non-nil blockedScanError.
func gateImportedSkill(ctx context.Context, result *skill.InstallResult, installer *skill.Installer, cfg *config.Config, projectRoot, lockPath string, lr *importLineResult) (printSuccess bool, err error) {
	// Reuse the standard scan gate so import is symmetric with `qvr add`
	// for security defaults. Blocked installs are rolled back the same
	// way add does.
	gate, gerr := ScanAndGate(ctx, skillDirFor(result, lockPath), cfg, scanGateOptions{
		Disabled: importNoScan,
		Action:   "import",
		Subject:  result.Name,
		Quiet:    true,
	})
	if gerr != nil {
		printer.Warning(fmt.Sprintf("import %s: scan failed (%v); install kept — rerun `qvr scan %s` to retry", result.Name, gerr, result.Name))
		lr.Install = result
		return false, nil
	}
	if gate.Blocked {
		removeErr := installer.Remove(result.Name, skill.InstallRequest{
			ProjectRoot: projectRoot,
			Global:      importGlobal,
			LockPath:    lockPath,
		})
		if removeErr != nil {
			printer.Error(fmt.Sprintf("import %s: scan blocked, rollback also failed (%v); run `qvr remove %s --force` to clean up", result.Name, removeErr, result.Name))
		}
		blockErr := &blockedScanError{Subject: result.Name, Threshold: gate.Threshold, Result: gate.Result}
		lr.Error = blockErr.Error()
		return false, blockErr
	}
	if recErr := recordScanResult(lockPath, result.Name, gate); recErr != nil {
		printer.Warning(fmt.Sprintf("import %s: scan recorded only in memory (%v)", result.Name, recErr))
	}
	lr.Install = result
	return true, nil
}

// writeImportedProjectFile projects every imported skill back into qvr.toml,
// re-reading the lock the installs just wrote. The lock is authoritative, so a
// qvr.toml failure warns rather than failing import.
func writeImportedProjectFile(projectRoot, lockPath string, lineResults []importLineResult) {
	var installed []*skill.InstallResult
	for _, lr := range lineResults {
		if lr.Install != nil {
			installed = append(installed, lr.Install)
		}
	}
	if lock, lerr := model.ReadLockFile(lockPath); lerr == nil {
		if perr := syncProjectFileFromLock(model.DefaultProjectPath(projectRoot), lock, installed); perr != nil {
			printer.Warning(fmt.Sprintf("imported into qvr.lock but failed to update qvr.toml (%v); run `qvr sync` to reconcile", perr))
		}
	}
}

// emitImportJSON renders the import envelope and applies the exit-code contract:
// a per-line failure or any phase-1 registry error flips the exit non-zero via
// errJSONHandled so the stream stays a single JSON document.
func emitImportJSON(lineResults []importLineResult, parseErrs []manifest.ParseError, firstErr error, phase1HadError bool) error {
	env := importEnvelope{Lines: lineResults}
	for _, pe := range parseErrs {
		env.Errors = append(env.Errors, pe.Error())
	}
	if firstErr != nil {
		env.Error = firstErr.Error()
	}
	if jerr := printer.JSON(env); jerr != nil {
		return jerr
	}
	if firstErr != nil || phase1HadError {
		return errJSONHandled
	}
	return nil
}

// readManifest opens path, parses, and surfaces parse errors. Returns the
// entries (possibly partial) and the parse-error list. Read failures are
// returned as the third value and stop the command.
func readManifest(path string) ([]manifest.Entry, []manifest.ParseError, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open manifest: %w", err)
	}
	defer f.Close()
	entries, perrs, perr := manifest.Parse(f)
	if perr != nil {
		return entries, perrs, perr
	}
	return entries, perrs, nil
}

// phaseResolution records the outcome of resolving one URL to a registry alias.
type phaseResolution struct {
	alias string
	added bool
	Error string
}

// resolveRegistries walks the manifest once, batching unique URLs so we don't
// hit the network multiple times for repeated repos. Returns:
//   - urlToAlias: the registry alias each URL resolved to (empty when failed).
//   - results: one record per unique URL (for the JSON envelope).
//   - hadError: true when any URL failed to register.
func resolveRegistries(ctx context.Context, mgr *registry.Manager, entries []manifest.Entry) (map[string]phaseResolution, []phaseResolution, bool) {
	// Build the set of (URL, requested-alias) pairs, in first-seen order so the
	// resulting registrations are deterministic.
	type req struct {
		url   string
		alias string
	}
	seen := map[string]bool{}
	var ordered []req
	for _, e := range entries {
		if seen[e.RepoURL] {
			continue
		}
		seen[e.RepoURL] = true
		ordered = append(ordered, req{url: e.RepoURL, alias: e.RegistryAlias})
	}

	cfg, _ := config.Load()
	existingByURL := map[string]string{}
	if cfg != nil {
		for name, rc := range cfg.Registries {
			clean, _, err := git.SanitizeURL(rc.URL)
			if err == nil {
				existingByURL[clean] = name
			} else {
				existingByURL[rc.URL] = name
			}
		}
	}

	urlToAlias := map[string]phaseResolution{}
	var results []phaseResolution
	hadError := false
	for _, r := range ordered {
		pr := resolveOneRegistry(ctx, mgr, cfg, existingByURL, r.url, r.alias)
		urlToAlias[r.url] = pr
		results = append(results, pr)
		if pr.Error != "" {
			hadError = true
		}
	}
	return urlToAlias, results, hadError
}

// resolveOneRegistry resolves a single manifest URL to a registry alias,
// reusing an already-registered URL, inferring or honoring a requested alias,
// and registering via mgr.Add otherwise. existingByURL is updated in place so
// later entries sharing the URL reuse the registration. A failure is reported
// to the printer and returned as a phaseResolution carrying its Error.
func resolveOneRegistry(ctx context.Context, mgr *registry.Manager, cfg *config.Config, existingByURL map[string]string, url, requestedAlias string) phaseResolution {
	clean, _, err := git.SanitizeURL(url)
	if err != nil {
		printer.Error(fmt.Sprintf("import: %s: %v", url, err))
		return phaseResolution{Error: err.Error()}
	}
	// If the URL is already registered (regardless of the local name the
	// manifest asked for), reuse the existing registration silently.
	// Renaming an existing registry just to honor a manifest's preference
	// would break every other lock entry that already references it.
	if existing, ok := existingByURL[clean]; ok {
		return phaseResolution{alias: existing, added: false}
	}
	// Pick a local name: the manifest's --registry-alias when set,
	// otherwise the standard URL-inferred shape.
	name := strings.TrimSpace(requestedAlias)
	if name == "" {
		name = registry.InferRegistryName(clean)
		if name == "" {
			msg := fmt.Sprintf("could not infer a registry name from %q — add --registry-alias= to the manifest line", url)
			printer.Error("import: " + msg)
			return phaseResolution{Error: msg}
		}
	}
	// Manifest-supplied aliases might collide with an unrelated existing
	// registry (different URL, same local name). In that case fall through
	// to a URL-inferred name so we never overwrite a user's pinned source.
	if _, taken := cfg.Registries[name]; taken {
		alt := registry.InferRegistryName(clean)
		if alt != "" && alt != name {
			printer.Warning(fmt.Sprintf("import: registry alias %q is taken; using inferred name %q for %s", name, alt, url))
			name = alt
		}
	}
	reg, addErr := mgr.Add(ctx, name, clean)
	if addErr != nil {
		if errors.Is(addErr, registry.ErrRegistryExists) {
			// Another goroutine / re-entrant call beat us; fall back to
			// reusing whatever is already there.
			existingByURL[clean] = name
			return phaseResolution{alias: name, added: false}
		}
		printer.Error(fmt.Sprintf("import: add registry %s: %v", url, addErr))
		return phaseResolution{Error: addErr.Error()}
	}
	// Cache the URL→alias mapping for any later entries sharing this URL.
	existingByURL[clean] = reg.Name
	return phaseResolution{alias: reg.Name, added: true}
}

func successCount(lines []importLineResult) int {
	n := 0
	for _, l := range lines {
		if l.Install != nil && l.Error == "" {
			n++
		}
	}
	return n
}
