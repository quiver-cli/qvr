package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/output"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/spf13/cobra"
)

var (
	publishRegistry       string
	publishBranch         string
	publishTag            string
	publishMessage        string
	publishAuthor         string
	publishEmail          string
	publishDryRun         bool
	publishNoCreateBranch bool
	publishNoScan         bool
	publishGlobal         bool
	publishFork           string
	publishMigrate        bool
	publishAllowHeal      bool
	publishAutoCommit     bool
	publishForce          bool
	publishLayout         string
)

var publishCmd = &cobra.Command{
	Use:   "publish [skill|path]",
	Short: "Publish a skill: push edits, cut a release, or fork to a new repo",
	Long: `Publish has two modes.

  Installed-skill mode (` + "`qvr publish <skill>`" + `):
    Pushes the edit copy of an already-installed skill back to its origin.
    Requires ` + "`qvr edit <skill>`" + ` first. Use --tag to cut a release.
    Use --fork <git-url> to retarget the push to a new remote; combine with
    --migrate to flip the lock entry's source so future publishes track
    the fork.

  Greenfield mode (` + "`qvr publish ./path`" + `):
    Clones the target --registry into a temp directory, copies the local
    skill into skills/<name>/, commits, pushes — for adding a new skill
    to a multi-skill registry repo.

The first argument is treated as an installed skill name when it matches a
lock entry; otherwise as a filesystem path.`,
	Args: cobra.ExactArgs(1),
	RunE: runPublish,
}

func init() {
	publishCmd.Flags().StringVar(&publishRegistry, "registry", "", "(path mode) target registry; defaults to default_registry config")
	publishCmd.Flags().StringVar(&publishBranch, "branch", "", "target branch (defaults to entry's Ref / registry default)")
	publishCmd.Flags().StringVar(&publishTag, "tag", "", "annotated tag to create on the new commit (e.g. v1.2.0)")
	publishCmd.Flags().StringVarP(&publishMessage, "message", "m", "", "commit message")
	publishCmd.Flags().StringVar(&publishAuthor, "author", "", "commit author name")
	publishCmd.Flags().StringVar(&publishEmail, "email", "", "commit author email")
	publishCmd.Flags().BoolVar(&publishDryRun, "dry-run", false, "validate and stage without pushing")
	publishCmd.Flags().BoolVar(&publishNoCreateBranch, "no-create-branch", false, "(path mode) refuse to create --branch if it doesn't already exist on origin")
	publishCmd.Flags().BoolVar(&publishNoScan, "no-scan", false, "skip the security scan that normally gates publishes")
	publishCmd.Flags().BoolVar(&publishGlobal, "global", false, "(installed mode) read the user-global lock file instead of the project lock")
	publishCmd.Flags().StringVar(&publishFork, "fork", "", "(installed mode) retarget the publish to a new git URL; pair with --migrate to record `forked-from` provenance in the lockfile")
	publishCmd.Flags().BoolVar(&publishMigrate, "migrate", false, "(installed mode + --fork) rewrite the lock entry so future publishes track the fork URL")
	publishCmd.Flags().BoolVar(&publishAllowHeal, "allow-lockfile-heal", false, "(installed mode) proceed even when qvr.lock.commit doesn't match the edit repo HEAD — overrides the integrity refusal added for #74")
	publishCmd.Flags().BoolVar(&publishAutoCommit, "auto-commit", false, "(installed mode) stage and commit dirty changes in the eject dir before pushing (default refuses dirty WD — issue #83)")
	publishCmd.Flags().BoolVar(&publishForce, "force", false, "overwrite an existing same-name skill in the target registry (issue #72)")
	publishCmd.Flags().StringVar(&publishLayout, "layout", "", "(installed mode) repo layout to publish: \"root\" (single-skill repo) or \"nested\" (multi-skill registry under skills/<name>/). Defaults: root for --fork, nested otherwise (issue #70)")

	// Group flags for --help (issue #109). Order matches the Long
	// description's narrative: what you set every time, then commit
	// metadata, then where the publish goes, then security/integrity
	// overrides, then per-machine scope.
	for flag, group := range map[string]string{
		"message":             "Common",
		"tag":                 "Common",
		"dry-run":             "Common",
		"author":              "Authoring",
		"email":               "Authoring",
		"branch":              "Authoring",
		"registry":            "Routing",
		"fork":                "Routing",
		"migrate":             "Routing",
		"layout":              "Routing",
		"no-scan":             "Trust",
		"allow-lockfile-heal": "Trust",
		"global":              "Scope",
		"force":               "Scope",
		"auto-commit":         "Scope",
		"no-create-branch":    "Scope",
	} {
		markFlagGroup(publishCmd.Flags(), flag, group)
	}
	publishCmd.SetUsageFunc(groupedUsageFunc([]string{"Common", "Authoring", "Routing", "Trust", "Scope"}))

	rootCmd.AddCommand(publishCmd)
}

// runPublish dispatches between installed-skill mode and greenfield-path
// mode. The first argument is treated as an installed skill name when the
// (project or global) lock contains an entry by that name; otherwise as a
// filesystem path.
func runPublish(cmd *cobra.Command, args []string) error {
	arg := args[0]
	projectRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolve cwd: %w", err)
	}

	// Installed-skill mode takes precedence if the arg matches a lock entry.
	// We probe the lock without grabbing the WithLock window; the real publish
	// re-reads under the lock if it needs to mutate.
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), publishGlobal)
	if lock, lerr := model.ReadLockFile(lockPath); lerr == nil {
		if _, gerr := lock.Get(arg); gerr == nil {
			return runPublishInstalled(cmd, arg, projectRoot, lockPath)
		}
	}

	// --fork only applies to installed-skill mode; greenfield publishes
	// always go through a registry. Surface the misuse early.
	if publishFork != "" {
		return ErrPublishForkNeedsInstall
	}
	if publishMigrate {
		return errors.New("--migrate requires --fork and an installed skill")
	}
	return runPublishGreenfield(cmd, arg)
}

// ErrPublishForkNeedsInstall is surfaced when --fork is passed but the
// arg doesn't match a lock entry (and so we'd otherwise fall through to
// greenfield mode, which can't accept --fork).
var ErrPublishForkNeedsInstall = errors.New("--fork requires an installed skill name; pass the skill name (not a path) and run `qvr edit <skill>` first")

// autoRegisterForkAsRegistry registers a fork URL as a local configured
// registry so the post-publish auto-uneject can route through the
// standard installer (which only knows how to resolve via configured
// registries). Derives the name with registry.InferRegistryName so the
// fork lands at `~/.quiver/registries/<org>/<repo>.git/` — the same
// shape `qvr registry add` would have produced.
//
// Returns the registry name on success, "" on every refusable failure
// (with a warning printed). Failures intentionally never propagate as
// errors: the publish itself succeeded; the user just doesn't get the
// auto-uneject convenience. Refusal cases:
//
//   - URL doesn't yield a usable name (e.g. a path-only "./fork" URL)
//   - derived name already maps to a different URL — silently reusing
//     would attach the local install to an unrelated registry
//   - cfg.Load or mgr.Add fail (network / fs)
//
// Issue #108.
func autoRegisterForkAsRegistry(ctx context.Context, forkURL string, e *model.LockEntry) string {
	name := registry.InferRegistryName(forkURL)
	if name == "" {
		printer.Warning(fmt.Sprintf("publish %s: auto un-eject skipped — could not infer a registry name from %q (run `qvr registry add <name> %s` + `qvr add %s@<tag> --registry <name>` to finish manually)",
			e.Name, forkURL, forkURL, e.Name))
		return ""
	}
	cfg, err := config.Load()
	if err != nil {
		printer.Warning(fmt.Sprintf("publish %s: auto un-eject skipped — config load failed (%v)", e.Name, err))
		return ""
	}
	if existing, exists := cfg.Registries[name]; exists {
		if existing.URL != forkURL {
			printer.Warning(fmt.Sprintf("publish %s: auto un-eject skipped — derived registry name %q already maps to %s (not %s); pass a different --fork name or `qvr registry add` manually",
				e.Name, name, existing.URL, forkURL))
			return ""
		}
		e.Registry = name
		return name
	}
	mgr := newRegistryManager(git.NewGoGitClient())
	if _, addErr := mgr.Add(ctx, name, forkURL); addErr != nil {
		if errors.Is(addErr, registry.ErrRegistryExists) {
			// Race or stale read; treat as success and route through.
			e.Registry = name
			return name
		}
		printer.Warning(fmt.Sprintf("publish %s: auto un-eject skipped — could not auto-register %q at %s (%v)",
			e.Name, name, forkURL, addErr))
		return ""
	}
	e.Registry = name
	return name
}

func runPublishInstalled(cmd *cobra.Command, name, projectRoot, lockPath string) error {
	if publishMigrate && publishFork == "" {
		return errors.New("--migrate requires --fork <git-url>")
	}

	var (
		result              *skill.PublishInstalledResult
		entry               *model.LockEntry
		autoUnejected       bool // tag pushed + relocked + relinked at the new tag
		autoUnejectNeedsAdd bool // eject torn down, but re-install failed; user must `qvr add` to finish
	)
	lockErr := model.WithLock(config.Dir(), lockPath, func() error {
		lock, err := model.ReadLockFile(lockPath)
		if err != nil {
			return fmt.Errorf("read lock: %w", err)
		}
		e, err := lock.Get(name)
		if err != nil {
			return err
		}
		if e.IsLink() {
			return fmt.Errorf("cannot publish %q: it is a link install — edit the source path and push with raw git", name)
		}
		if !e.IsEdit() {
			return fmt.Errorf("publish %s: skill is not ejected. Run `qvr edit %s` first to make it editable", name, name)
		}

		// Integrity pre-check: refuse to publish when the lockfile's recorded
		// commit doesn't match the edit repo's HEAD (issue #74). Publish is the
		// place where lockfile drift becomes a permanent artifact on the
		// registry — silently healing the SHA destroys the audit trail. Allow
		// override via --allow-lockfile-heal so users with intentional resets
		// can proceed explicitly.
		editDir := skill.EffectiveTarget(e, projectRoot)
		if editDir != "" {
			head, herr := skill.ResolveEntryHeadCommit(e, projectRoot)
			if herr == nil && head != "" && e.Commit != "" && head != e.Commit {
				// Differentiate "user committed legitimately on top of e.Commit"
				// from "lockfile commit is a fabrication". If head is a
				// descendant of e.Commit, this is the #99 case — silently
				// heal so the user doesn't have to flag every local commit.
				// Otherwise it's the #74 case (tampered or orphaned) and we
				// refuse without an explicit --allow-lockfile-heal.
				ancestor, aerr := skill.EntryCommitIsAncestorOfHead(e, projectRoot)
				switch {
				case aerr == nil && ancestor:
					// Legitimate local commit advance — heal silently.
					e.Commit = head
				case publishAllowHeal:
					printer.Warning(fmt.Sprintf("publish %s: healing lockfile commit %s → %s (--allow-lockfile-heal)", name, e.Commit, head))
					// Persist the heal NOW so the next publish doesn't see drift
					// even when this publish ends up nothing-to-publish (issue #96).
					e.Commit = head
				default:
					return fmt.Errorf("publish %s: lockfile commit %s does not match edit repo HEAD %s — refuse to publish without --allow-lockfile-heal (issue #74)",
						name, e.Commit, head)
				}
			}
		}

		// Pre-flight scan gate against the edit dir — mirror the path-mode
		// publisher's behavior so blocked publishes never touch the upstream.
		var publishGate *scanGateResult
		cfg, cerr := config.Load()
		if cerr == nil && editDir != "" {
			if err := enforceScanPolicy(cfg, publishNoScan); err != nil {
				return err
			}
			gate, gerr := ScanAndGate(cmd.Context(), editDir, cfg, scanGateOptions{
				Disabled: publishNoScan,
				Action:   "publish",
				Subject:  name,
			})
			if gerr != nil {
				printer.Warning(fmt.Sprintf("publish: scan failed (%v); proceeding — rerun `qvr scan %s` to retry", gerr, name))
			} else if gate.Blocked {
				return fmt.Errorf("publish: scan blocked (max severity %s ≥ threshold %s); upstream not touched — see findings above or pass --no-scan to override (issue #74)",
					gate.Result.Summary.MaxSeverity(), gate.Threshold)
			} else {
				publishGate = gate
			}
		}

		p := skill.NewPublisher(git.NewGoGitClient())
		// Serialize all publishes (greenfield and installed) on a single
		// user-machine sentinel so two concurrent publishes can't race the
		// remote registry's atomic ref check (issue #88).
		var r *skill.PublishInstalledResult
		err = model.WithPublishLock(config.Dir(), func() error {
			ri, ierr := p.PublishInstalled(cmd.Context(), skill.PublishInstalledRequest{
				Entry:       e,
				ProjectRoot: projectRoot,
				ForkURL:     publishFork,
				Migrate:     publishMigrate,
				Tag:         publishTag,
				Branch:      publishBranch,
				Message:     publishMessage,
				Author:      publishAuthor,
				Email:       publishEmail,
				DryRun:      publishDryRun,
				AutoCommit:  publishAutoCommit,
				Layout:      publishLayout,
			})
			r = ri
			return ierr
		})
		if err != nil {
			return err
		}
		// PublishInstalled mutated the entry on success — persist. Reflect
		// the just-run scan gate onto the entry's verification block so the
		// recorded scan describes the NEW commit, not a stale one carried
		// from the previous publish (issue #71). When the gate was skipped
		// via --no-scan, applyScanToEntry installs the sentinel from
		// toScanRef so the lock distinguishes "scanned and clean" from
		// "scan was skipped".
		if !publishDryRun {
			applyScanToEntry(e, publishGate)
			lock.Put(e)
			if err := lock.Write(); err != nil {
				return fmt.Errorf("write lock: %w", err)
			}

			// Auto un-eject: after a tagged publish, flip the lockfile
			// out of edit mode and re-symlink the agent targets at the
			// new tag's shared worktree — the same end state as
			//   qvr remove --force <skill> && qvr add <skill>@<tag>
			// without making the maintainer run them. The eject dir is
			// removed; any committed-but-unpublished work beyond <tag>
			// is gone, matching the cargo/npm convention that publish
			// ends the editing session.
			//
			// Skipped for:
			//   - dry-run / nothing-to-publish (no new state to switch to)
			//   - HEAD-only push (no tag means the user is iterating)
			//   - --fork without --migrate (the local entry still tracks
			//     the original registry — re-resolving against the old
			//     registry's tags would silently pick the wrong source)
			//   - any other state where Registry can't be resolved
			//     (auto-register attempted for --fork --migrate; see
			//     autoRegisterForkAsRegistry — issue #108)
			if r != nil && !r.NothingToPublish && publishTag != "" {
				targetsCopy := append([]string{}, e.Targets...)
				registryName := e.Registry
				if registryName == "" && publishFork != "" && publishMigrate {
					// --fork --migrate --tag graduation: PublishInstalled
					// just cleared e.Registry and pointed e.Source at the
					// fork URL. Auto-register the fork as a local
					// registry so the standard install path can resolve
					// it, then proceed through the same Remove + Install
					// rails as a same-registry graduation. Issue #108.
					if newName := autoRegisterForkAsRegistry(cmd.Context(), publishFork, e); newName != "" {
						registryName = newName
						// Persist the entry's freshly-set Registry now so
						// a mid-flight failure leaves the lock pointing
						// at a registry the world knows about, not "".
						lock.Put(e)
						if perr := lock.Write(); perr != nil {
							printer.Warning(fmt.Sprintf("publish %s: persist auto-added registry failed (%v)", name, perr))
						}
					}
				}
				if registryName != "" {
					gcc := git.NewGoGitClient()
					wt := git.NewGoGitWorktree()
					mgr := newRegistryManager(gcc)
					installer := skill.NewInstaller(mgr, wt, gcc)

					// Aliased installs need to re-install with the canonical
					// name + As=alias because the registry index lists the
					// canonical, not the local alias. Pre-#113 the auto-
					// uneject used `name + "@" + publishTag` directly, which
					// silently failed `FindSkillIn` for aliased entries. The
					// non-aliased path is a no-op (canonical == name, As ==
					// ""). Mirrors the same pattern in cmd/switch.go and
					// cmd/upgrade.go.
					canonicalSkill := name
					aliasFlag := ""
					if e.Canonical != "" {
						canonicalSkill = e.Canonical
						aliasFlag = name
					}

					// Refresh the bare clone so FindSkillIn sees the
					// just-pushed tag; without this the new tag isn't
					// in the local index yet and Install would resolve
					// against stale tags.
					if _, uerr := mgr.Update(cmd.Context(), registryName); uerr != nil {
						printer.Warning(fmt.Sprintf("publish %s: auto un-eject skipped — refresh %s failed (%v)", name, registryName, uerr))
					} else if rerr := installer.Remove(name, skill.InstallRequest{
						ProjectRoot: projectRoot,
						Global:      publishGlobal,
						LockPath:    lockPath,
						Force:       true,
					}); rerr != nil {
						printer.Warning(fmt.Sprintf("publish %s: auto un-eject skipped — remove failed (%v)", name, rerr))
					} else if _, ierr := installer.Install(skill.InstallRequest{
						Skill:       canonicalSkill + "@" + publishTag,
						Targets:     targetsCopy,
						Global:      publishGlobal,
						ProjectRoot: projectRoot,
						LockPath:    lockPath,
						Force:       true,
						Registry:    registryName,
						As:          aliasFlag,
					}); ierr != nil {
						// Remove already tore down the eject dir + lock
						// entry. The user needs `qvr add` (not the full
						// two-step) to recover.
						autoUnejectNeedsAdd = true
						printer.Warning(fmt.Sprintf("publish %s: tag pushed and eject torn down, but re-install at %s failed (%v)", name, publishTag, ierr))
					} else {
						autoUnejected = true
					}
				}
			}
		}
		result = r
		entry = e
		return nil
	})
	if lockErr != nil {
		return lockErr
	}

	// Write-through: a successful publish auto-un-ejects the skill back to shared
	// mode (with --fork --migrate, on the new fork's registry), so it re-gains a
	// qvr.toml coordinate. Reconcile qvr.toml from the final lock — the back-fill
	// records the (possibly migrated) coordinate at the published tag. Dry-run
	// and --global publishes don't touch qvr.toml.
	if !publishDryRun && !publishGlobal {
		if lock, lerr := model.ReadLockFile(lockPath); lerr == nil {
			if perr := syncProjectFileFromLock(model.DefaultProjectPath(projectRoot), lock, nil); perr != nil {
				printer.Warning(fmt.Sprintf("published, but failed to update qvr.toml (%v); run `qvr sync` to reconcile", perr))
			}
		}
	}

	registry.TouchProject(lockPath)

	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}
	if result.DryRun {
		tagSuffix := ""
		if result.Tag != "" {
			tagSuffix = fmt.Sprintf(" (tag %s)", result.Tag)
		}
		printer.Info(fmt.Sprintf("Dry run OK: %s would be published to %s@%s%s", result.Skill, result.Remote, result.Branch, tagSuffix))
		return nil
	}
	// Nothing-to-publish (issue #84): when the eject dir was clean and the
	// caller didn't ask for a tag, surface as "Nothing to publish" instead
	// of a misleading "Published" with the same SHA as before. Exit 0 so
	// pipelines that ran `qvr publish` defensively don't break.
	if result.NothingToPublish {
		printer.Info(fmt.Sprintf("Nothing to publish: %s already matches %s@%s", result.Skill, result.Remote, result.Branch))
		return nil
	}
	shortCommit := result.Commit
	if len(shortCommit) >= 7 {
		shortCommit = shortCommit[:7]
	} else if shortCommit == "" {
		shortCommit = "<unknown>"
	}
	msg := fmt.Sprintf("Published %s to %s@%s (%s)", result.Skill, result.Remote, result.Branch, shortCommit)
	if result.Tag != "" {
		msg += fmt.Sprintf(", tagged %s", result.Tag)
	}
	if result.Migrated {
		msg += " — lock entry now tracks the fork"
		// Three states for the trailing context:
		//   - autoUnejected:        success line is enough (followed by
		//                           "Switched ... back to consume mode").
		//   - autoUnejectNeedsAdd:  Remove ran, Install failed — the
		//                           warning printed above already spells
		//                           out "eject torn down, re-install
		//                           failed". Saying "auto-uneject did
		//                           not run" here would contradict it
		//                           (issue #113).
		//   - neither (registry "", autoRegister refused, etc.):
		//                           keep the legacy explanation.
		if !autoUnejected && !autoUnejectNeedsAdd {
			msg += " (Registry field cleared; auto-uneject did not run)"
		}
		_ = entry // suppress unused — kept for future hook points
	}
	if result.Layout != "" {
		msg += fmt.Sprintf(" [layout=%s]", result.Layout)
	}
	printer.Success(msg)

	// Auto un-eject status. After a tagged install-mode publish, the
	// maintainer round-trip flips the lockfile out of edit mode and
	// re-symlinks at the new tag automatically. Cover the four states:
	//
	//   - autoUnejected: full success, the entry is back in consume mode.
	//   - autoUnejectNeedsAdd: Remove tore down state but Install failed;
	//     the user has no entry on disk and needs to add to recover.
	//   - publishFork or empty registry: auto un-eject was skipped by
	//     design; print the manual recovery hint.
	//   - otherwise (tag set, normal publish): never reached unless the
	//     auto-un-eject Update or Remove step bailed mid-flight; surface
	//     the same manual recovery hint as the conservative fallback.
	switch {
	case autoUnejected:
		printer.Info(fmt.Sprintf("Switched %s back to consume mode at %s", result.Skill, result.Tag))
	case autoUnejectNeedsAdd:
		printer.Info(fmt.Sprintf("Hint: run `qvr add %s@%s` to finish switching back to consume mode",
			result.Skill, result.Tag))
	case result.Tag != "" && !result.DryRun && !result.NothingToPublish && publishFork == "":
		printer.Info(fmt.Sprintf(
			"Hint: run `qvr remove --force %s && qvr add %s@%s` to switch back to consume mode at the new tag",
			result.Skill, result.Skill, result.Tag))
		// The --fork --migrate --tag case is handled by
		// autoRegisterForkAsRegistry's bespoke warning when auto-uneject
		// fails (and Switched … above when it succeeds). No generic
		// trailing hint — `qvr add %s@%s` doesn't work for a fork URL
		// that isn't a configured registry yet.
	}
	return nil
}

func runPublishGreenfield(cmd *cobra.Command, path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}

	// Security gate. Scan the local skill BEFORE we touch the registry so a
	// blocked publish never leaves a partially-staged clone behind. Runs in
	// dry-run too — the gate is part of publishability, not push-side.
	cfg, cerr := config.Load()
	if cerr == nil {
		if err := enforceScanPolicy(cfg, publishNoScan); err != nil {
			return err
		}
		scanPath, _, derr := resolveSkillDir(path)
		if derr != nil || scanPath == "" {
			scanPath = path
		}
		gate, gerr := ScanAndGate(cmd.Context(), scanPath, cfg, scanGateOptions{
			Disabled: publishNoScan,
			Action:   "publish",
			Subject:  scanPath,
		})
		if gerr != nil {
			printer.Warning(fmt.Sprintf("publish: scan failed (%v); proceeding — rerun `qvr scan %s` to retry", gerr, scanPath))
		} else if gate.Blocked {
			return fmt.Errorf("publish: scan blocked (max severity %s ≥ threshold %s); upstream not touched — see findings above or pass --no-scan to override",
				gate.Result.Summary.MaxSeverity(), gate.Threshold)
		}
	}

	p := skill.NewPublisher(git.NewGoGitClient())
	var result *skill.PublishResult
	// Serialize all publishes on the user-machine sentinel so two
	// concurrent `qvr publish ./path --registry r` calls don't both run
	// the full clone+commit dance only to discover at git push time that
	// one of them lost (issue #88).
	if err := model.WithPublishLock(config.Dir(), func() error {
		r, perr := p.Publish(cmd.Context(), skill.PublishRequest{
			LocalPath:      path,
			Registry:       publishRegistry,
			Branch:         publishBranch,
			Tag:            publishTag,
			Message:        publishMessage,
			Author:         publishAuthor,
			AuthorEmail:    publishEmail,
			DryRun:         publishDryRun,
			NoCreateBranch: publishNoCreateBranch,
			Force:          publishForce,
		})
		result = r
		return perr
	}); err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	if printer.Format == output.FormatJSON {
		return printer.JSON(result)
	}
	if result.DryRun {
		tagSuffix := ""
		if result.Tag != "" {
			tagSuffix = fmt.Sprintf(" (tag %s)", result.Tag)
		}
		printer.Info(fmt.Sprintf("Dry run OK: %s would be published to %s@%s%s", result.Skill, result.Registry, result.Branch, tagSuffix))
		return nil
	}
	shortCommit := result.Commit
	if len(shortCommit) >= 7 {
		shortCommit = shortCommit[:7]
	} else if shortCommit == "" {
		shortCommit = "<unknown>"
	}
	msg := fmt.Sprintf("Published %s to %s@%s (%s)", result.Skill, result.Registry, result.Branch, shortCommit)
	if result.Tag != "" {
		msg += fmt.Sprintf(", tagged %s", result.Tag)
	}
	printer.Success(msg)
	return nil
}
