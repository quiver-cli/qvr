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
	if len(parseErrs) > 0 && printer.Format != output.FormatJSON {
		for _, pe := range parseErrs {
			printer.Error(fmt.Sprintf("import %s: %s", manifestPath, pe.Error()))
		}
	}
	if len(entries) == 0 {
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

	defaultTargets := importTargets
	if len(defaultTargets) == 0 {
		defaultTargets = config.ParseDefaultTargets(cfg.DefaultTarget)
		if len(defaultTargets) == 0 {
			return fmt.Errorf("no --target specified and default_target is unset")
		}
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
	var firstErr error
	// Carry every phase-1 failure into the line results so the JSON envelope
	// reports them; the printer already surfaced them in resolveRegistries.
	for _, pr := range phaseResults {
		if pr.Error != "" && firstErr == nil {
			firstErr = errors.New(pr.Error)
		}
	}

	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		for _, entry := range entries {
			lr := importLineResult{
				Line:    entry.Line,
				Skill:   entry.Skill,
				RepoURL: entry.RepoURL,
			}
			pr, ok := urlToAlias[entry.RepoURL]
			if !ok || pr.alias == "" {
				lr.Error = "registry resolution failed (see prior error)"
				lineResults = append(lineResults, lr)
				if firstErr == nil {
					firstErr = errors.New(lr.Error)
				}
				continue
			}
			if pr.added {
				lr.RegistryAdded = pr.alias
			} else {
				lr.RegistryReused = pr.alias
			}

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
				printer.Error(fmt.Sprintf("import %s: %v", entry.Skill, ierr))
				lr.Error = ierr.Error()
				lineResults = append(lineResults, lr)
				if firstErr == nil {
					firstErr = ierr
				}
				continue
			}

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
				lineResults = append(lineResults, lr)
				continue
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
				lineResults = append(lineResults, lr)
				if firstErr == nil {
					firstErr = blockErr
				}
				continue
			}
			if recErr := recordScanResult(lockPath, result.Name, gate); recErr != nil {
				printer.Warning(fmt.Sprintf("import %s: scan recorded only in memory (%v)", result.Name, recErr))
			}

			lr.Install = result
			lineResults = append(lineResults, lr)
			if printer.Format != output.FormatJSON {
				for _, w := range result.Warnings {
					printer.Warning(w)
				}
				if pr.added {
					printer.Success(fmt.Sprintf("Registered registry %s ← %s", pr.alias, entry.RepoURL))
				}
				printer.Success(fmt.Sprintf("Imported %s@%s → %v", result.Name, result.Version, result.Targets))
			}
		}
		// Write-through to qvr.toml for every imported skill — same projection as
		// `qvr add`. Re-read the lock the installs just wrote; the lock is
		// authoritative, so a qvr.toml failure warns rather than failing import.
		if !importGlobal {
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
	if firstErr != nil || len(parseErrs) > 0 || phase1HadError {
		return errTextHandled
	}
	if successCount(lineResults) > 0 {
		if importGlobal {
			printer.Info("Hint: `qvr list --global` shows what's installed in the ambient lane")
		} else {
			printer.Info("Hint: commit qvr.lock so teammates reproduce the same skills (`git add qvr.lock`)")
		}
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
		clean, _, err := git.SanitizeURL(r.url)
		if err != nil {
			printer.Error(fmt.Sprintf("import: %s: %v", r.url, err))
			pr := phaseResolution{Error: err.Error()}
			urlToAlias[r.url] = pr
			results = append(results, pr)
			hadError = true
			continue
		}
		// If the URL is already registered (regardless of the local name the
		// manifest asked for), reuse the existing registration silently.
		// Renaming an existing registry just to honor a manifest's preference
		// would break every other lock entry that already references it.
		if existing, ok := existingByURL[clean]; ok {
			pr := phaseResolution{alias: existing, added: false}
			urlToAlias[r.url] = pr
			results = append(results, pr)
			continue
		}
		// Pick a local name: the manifest's --registry-alias when set,
		// otherwise the standard URL-inferred shape.
		name := strings.TrimSpace(r.alias)
		if name == "" {
			name = registry.InferRegistryName(clean)
			if name == "" {
				msg := fmt.Sprintf("could not infer a registry name from %q — add --registry-alias= to the manifest line", r.url)
				printer.Error("import: " + msg)
				pr := phaseResolution{Error: msg}
				urlToAlias[r.url] = pr
				results = append(results, pr)
				hadError = true
				continue
			}
		}
		// Manifest-supplied aliases might collide with an unrelated existing
		// registry (different URL, same local name). In that case fall through
		// to a URL-inferred name so we never overwrite a user's pinned source.
		if _, taken := cfg.Registries[name]; taken {
			alt := registry.InferRegistryName(clean)
			if alt != "" && alt != name {
				printer.Warning(fmt.Sprintf("import: registry alias %q is taken; using inferred name %q for %s", name, alt, r.url))
				name = alt
			}
		}
		reg, addErr := mgr.Add(ctx, name, clean)
		if addErr != nil {
			if errors.Is(addErr, registry.ErrRegistryExists) {
				// Another goroutine / re-entrant call beat us; fall back to
				// reusing whatever is already there.
				existingByURL[clean] = name
				pr := phaseResolution{alias: name, added: false}
				urlToAlias[r.url] = pr
				results = append(results, pr)
				continue
			}
			printer.Error(fmt.Sprintf("import: add registry %s: %v", r.url, addErr))
			pr := phaseResolution{Error: addErr.Error()}
			urlToAlias[r.url] = pr
			results = append(results, pr)
			hadError = true
			continue
		}
		// Cache the URL→alias mapping for any later entries sharing this URL.
		existingByURL[clean] = reg.Name
		pr := phaseResolution{alias: reg.Name, added: true}
		urlToAlias[r.url] = pr
		results = append(results, pr)
	}
	return urlToAlias, results, hadError
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
