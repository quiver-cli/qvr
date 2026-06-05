package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/output"
	"github.com/quiver-cli/qvr/internal/registry"
	"github.com/quiver-cli/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	addTargets  []string
	addGlobal   bool
	addForce    bool
	addFrozen   bool
	addNoScan   bool
	addRegistry string
	addAs       string
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

One-step install — no prior 'qvr registry add' needed. Point at a skill inside a
repo and qvr auto-registers the source, then installs the skill:

  qvr add github.com/org/repo/tdd        # register org/repo, install tdd
  qvr add github.com/org/repo/tdd@v2     # …pinned to a ref
  qvr add github.com/org/repo            # single-skill repo: installs the lone skill

Or register a source explicitly first, then add by name:

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
	addCmd.Flags().StringVar(&addRegistry, "registry", "",
		"scope resolution to a single registry (defaults to all configured); use to disambiguate same-named skills")
	addCmd.Flags().StringVar(&addAs, "as", "",
		"install under a different local name (lock entry + symlink filename). Lets two versions of the same skill coexist in one project for A/B testing. Single skill only.")
	rootCmd.AddCommand(addCmd)
}

func runAdd(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := enforceScanPolicy(cfg, addNoScan); err != nil {
		return err
	}
	// --as renames a single lock entry; with multiple positional args it
	// would silently apply to only one and skip the rest, which would be
	// the kind of "looks like it worked" footgun the rest of `qvr add`
	// guards against. Refuse rather than guess.
	if addAs != "" && len(args) != 1 {
		return fmt.Errorf("--as can only be used with a single skill argument (got %d)", len(args))
	}
	// --as "" reaches the installer as an empty string indistinguishable
	// from "flag not passed", so the installer silently installs under the
	// canonical name. From the user's perspective they explicitly asked
	// for an alias and got none — a footgun for `qvr add foo --as "$x"`
	// when $x is empty. Detect the explicit empty here and route through
	// the same invalid-name error that other malformed --as values produce.
	// Issue #103.
	if cmd.Flags().Changed("as") && addAs == "" {
		err := fmt.Errorf("invalid --as value %q: must be 1-64 chars, lowercase alphanumeric + hyphens, no leading/trailing or consecutive hyphens", addAs)
		// Issue #121: route through the same printer/envelope path the
		// rest of add uses. Pre-fix `--as ""` returned the bare error,
		// so text mode rendered `Error: …` (Execute's default envelope)
		// while every other add failure rendered `✗ add …: …`. JSON mode
		// emitted `{"error": "..."}` here vs the legacy
		// `{"installed": [], "error": "..."}` elsewhere — two distinct
		// shapes from the same command.
		if printer.Format == output.FormatJSON {
			payload := buildAddJSONEnvelope(nil, err)
			if jerr := printer.JSON(payload); jerr != nil {
				return jerr
			}
			return errJSONHandled
		}
		printer.Error(fmt.Sprintf("add: %v", err))
		return errTextHandled
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
	mgr := newRegistryManager(gc)
	installer := skill.NewInstaller(mgr, wt, gc)
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), addGlobal)

	// One-step install: an arg shaped like a clone URL with a skill path
	// (e.g. github.com/org/repo/skill) auto-registers its registry here, before
	// the lock window, then flows through as a plain name scoped to that source.
	// Plain skill names pass straight through with the global --registry scope.
	items, perr := resolveAddItems(cmd.Context(), mgr, args)
	if perr != nil {
		return perr
	}

	var results []*skill.InstallResult
	var firstErr error
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		for _, item := range items {
			ref := item.skillRef
			result, err := installer.Install(skill.InstallRequest{
				Skill:                    ref,
				Targets:                  targets,
				Global:                   addGlobal,
				ProjectRoot:              projectRoot,
				LockPath:                 lockPath,
				Force:                    addForce,
				Frozen:                   addFrozen,
				Registry:                 item.registry,
				As:                       addAs,
				RequireSigned:            cfg.Security.RequireSigned,
				TrustedAuthors:           trustedAuthorsForRegistry(cfg, item.registry),
				TrustedAuthorsByRegistry: trustedAuthorsByRegistry(cfg),
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
				// Quiet: collapse benign-finding noise to a one-line banner.
				// Blocked installs still get the full detail.
				Quiet: true,
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
			// Issue #66: print the success marker inside the loop so
			// per-skill output (scan warnings, then ✓ Added) reads in
			// order. Previously every ✓ printed in a trailing loop
			// after all failures, making partial-failure batches look
			// like total failures on a CI scroll-by.
			if printer.Format != output.FormatJSON {
				// Surface installer-side advisories (e.g. multi-registry
				// ambiguity pick) before the ✓ so the user sees the
				// caveat associated with the install it qualifies
				// (issue #101).
				for _, w := range result.Warnings {
					printer.Warning(w)
				}
				printer.Success(fmt.Sprintf("Added %s@%s → %v", result.Name, result.Version, result.Targets))
			}
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
	if firstErr != nil {
		// Per-skill `✗ add <name>: <reason>` lines already surfaced
		// every failure (see line ~105). Returning firstErr would make
		// Cobra's Execute() print `Error: <first reason>` a second
		// time, which CI logs and chats read as "the whole batch
		// failed" even when successes ran (issue #66). Sentinel
		// preserves the exit-1 contract without the duplicate.
		return errTextHandled
	}
	// Next-step hint, init.go-style. Only when at least one skill landed;
	// otherwise a no-op rerun stays quiet. Project installs get the
	// "commit your lockfile" nudge because reproducibility is the
	// whole point of qvr.lock; global installs get the inspection hint.
	if len(results) > 0 {
		if addGlobal {
			printer.Info("Hint: `qvr list --global` shows what's installed in the ambient lane")
		} else {
			printer.Info("Hint: commit qvr.lock so teammates reproduce the same skills (`git add qvr.lock`)")
		}
	}
	return nil
}

// addJSONEnvelope is the stable shape emitted by `qvr add --output json`.
//
// Issue #121: pre-fix the envelope always emitted `installed: []` even on a
// total-failure run, while every other command (read, list, edit, …) emitted
// just `{"error": "..."}`. A consumer walking the CLI couldn't write one error
// handler — it had to branch on the per-command shape. Add now follows the
// universal rule: the `installed` array is only present when at least one
// install attempt produced a result (success or partial-success); pure-error
// runs emit `{"error": "..."}` like the rest of the CLI.
type addJSONEnvelope struct {
	Installed []*skill.InstallResult `json:"installed,omitempty"`
	Error     string                 `json:"error,omitempty"`
}

func buildAddJSONEnvelope(results []*skill.InstallResult, err error) addJSONEnvelope {
	env := addJSONEnvelope{Installed: results}
	if err != nil {
		env.Error = err.Error()
	}
	return env
}

// addItem is one normalized install target: the skill ref to hand the
// installer (`name` or `name@ref`) plus the registry scope it resolves under
// ("" = every configured registry, the historical default).
type addItem struct {
	skillRef string
	registry string
}

// resolveAddItems turns the raw `qvr add` positional args into install targets.
// A plain skill name (optionally `@ref`) passes through scoped to the global
// --registry flag. An arg shaped like a remote clone path
// (`[scheme://]host/org/repo[/skill][@ref]`, or scp-style `git@host:org/repo/...`)
// is the one-step install path: its registry is auto-registered (a no-op if
// already configured) and the arg becomes the skill name scoped to that source.
// Registration happens here — outside the project-lock window — because it's a
// registry-level side effect, not a lock mutation.
func resolveAddItems(ctx context.Context, mgr *registry.Manager, args []string) ([]addItem, error) {
	items := make([]addItem, 0, len(args))
	for _, arg := range args {
		cloneURL, skillName, ref, ok := parseRemoteSkillSpec(arg)
		if !ok {
			items = append(items, addItem{skillRef: arg, registry: addRegistry})
			continue
		}
		regName, err := ensureRegistryFor(ctx, mgr, cloneURL)
		if err != nil {
			return nil, err
		}
		if skillName == "" {
			// Bare repo (`host/org/repo`): install the lone skill, or refuse
			// and name the choices when the repo ships several.
			skillName, err = soleSkillName(mgr, regName, arg)
			if err != nil {
				return nil, err
			}
		}
		skillRef := skillName
		if ref != "" {
			skillRef = skillName + "@" + ref
		}
		items = append(items, addItem{skillRef: skillRef, registry: regName})
	}
	return items, nil
}

// ensureRegistryFor registers the registry for cloneURL if it isn't already
// configured, returning its inferred `<org>/<repo>` name. An already-registered
// source is reused as-is (no re-clone). The per-skill install scan still runs
// downstream, so we skip the registry-wide scan pass `qvr registry add` does.
func ensureRegistryFor(ctx context.Context, mgr *registry.Manager, cloneURL string) (string, error) {
	name := registry.InferRegistryName(cloneURL)
	if name == "" {
		return "", fmt.Errorf("could not infer a registry name from %q", cloneURL)
	}
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("load config: %w", err)
	}
	if _, ok := cfg.Registries[name]; ok {
		return name, nil
	}
	reg, err := mgr.Add(ctx, name, cloneURL)
	if err != nil {
		return "", fmt.Errorf("auto-register %s: %w", cloneURL, err)
	}
	if printer.Format != output.FormatJSON {
		printer.Info(fmt.Sprintf("Registered %s as %q (%d skills)", reg.URL, reg.Name, reg.SkillCount))
	}
	return name, nil
}

// soleSkillName returns the single skill in a freshly-registered registry, or
// an error listing the candidates when the repo exposes more than one (so a
// bare `host/org/repo` spec stays unambiguous).
func soleSkillName(mgr *registry.Manager, regName, spec string) (string, error) {
	skills, _, err := mgr.Index(regName, registry.RegistryPath(regName))
	if err != nil {
		return "", fmt.Errorf("index %s: %w", regName, err)
	}
	switch len(skills) {
	case 1:
		return skills[0].Name, nil
	case 0:
		return "", fmt.Errorf("registry %s has no installable skills", regName)
	default:
		names := make([]string, len(skills))
		for i, s := range skills {
			names[i] = s.Name
		}
		sort.Strings(names)
		return "", fmt.Errorf("%s exposes %d skills (%s); name one, e.g. `qvr add %s/%s`",
			regName, len(skills), strings.Join(names, ", "), strings.TrimRight(spec, "/"), names[0])
	}
}

// parseRemoteSkillSpec recognizes a one-step install spec and splits it into a
// clone URL for the org/repo, the skill name (the path segment past org/repo,
// empty when the spec points at the bare repo), and an optional `@ref`. It
// accepts `[scheme://]host/org/repo[/skill...][@ref]` and scp-style
// `git@host:org/repo[/skill...][@ref]`. ok=false means the arg is a plain skill
// name (no host/path shape) and should flow through normal registry resolution.
//
// Detection is deliberately conservative: an arg with no `/` is always a plain
// name, and the first path component must look like a host (contain a `.`), so
// a stray `foo/bar` never silently triggers a network clone.
func parseRemoteSkillSpec(arg string) (cloneURL, skillName, ref string, ok bool) {
	raw := strings.TrimSpace(arg)
	if raw == "" || !strings.Contains(raw, "/") {
		return "", "", "", false
	}

	// Peel a trailing @ref only when the '@' sits after the last '/', so the
	// user@ in a scp-style git@host:... authority isn't mistaken for a ref.
	if at := strings.LastIndex(raw, "@"); at > strings.LastIndex(raw, "/") {
		ref = raw[at+1:]
		raw = raw[:at]
	}

	scheme := "https"
	host := ""
	rest := raw
	switch {
	case strings.Contains(rest, "://"):
		i := strings.Index(rest, "://")
		scheme = rest[:i]
		rest = rest[i+3:]
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", "", "", false
		}
		authority := rest[:slash]
		if a := strings.LastIndex(authority, "@"); a >= 0 {
			authority = authority[a+1:] // drop user@
		}
		host, rest = authority, rest[slash+1:]
	case strings.Contains(rest, ":") && strings.Index(rest, "@") < strings.Index(rest, ":") && strings.Contains(rest, "@"):
		// scp-style git@host:org/repo/...
		at := strings.Index(rest, "@")
		colon := strings.Index(rest, ":")
		host, rest = rest[at+1:colon], rest[colon+1:]
		scheme = "scp"
	default:
		slash := strings.Index(rest, "/")
		if slash < 0 {
			return "", "", "", false
		}
		host, rest = rest[:slash], rest[slash+1:]
	}

	// The host must look like one — guards a bare `org/repo/skill` (no host)
	// from triggering a clone of a nonexistent remote.
	if !strings.Contains(host, ".") {
		return "", "", "", false
	}

	var segs []string
	for _, p := range strings.Split(strings.Trim(rest, "/"), "/") {
		if p != "" {
			segs = append(segs, p)
		}
	}
	if len(segs) < 2 {
		return "", "", "", false // need at least org/repo
	}
	org := segs[0]
	repo := strings.TrimSuffix(segs[1], ".git")
	if org == "" || repo == "" {
		return "", "", "", false
	}
	if len(segs) > 2 {
		skillName = segs[len(segs)-1] // deepest segment names the skill
	}
	if scheme == "scp" {
		cloneURL = fmt.Sprintf("git@%s:%s/%s.git", host, org, repo)
	} else {
		cloneURL = fmt.Sprintf("%s://%s/%s/%s.git", scheme, host, org, repo)
	}
	return cloneURL, skillName, ref, true
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
