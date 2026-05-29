package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/raks097/quiver/internal/config"
	"github.com/raks097/quiver/internal/model"
	"github.com/raks097/quiver/internal/skill"
)

// resolveSkillArg is the shared name-or-path resolver behind
// `qvr scan <arg>` and `qvr validate <arg>` (issue #64). Without it, the
// two adjacent commands behaved inconsistently — scan resolved installed
// names through the lock, validate only accepted filesystem paths and
// surfaced a raw `stat: no such file or directory` for the same input.
//
// Lookup precedence:
//  1. arg looks like a path (`.`, `..`, contains `/`, or `./`/`../`
//     prefix) → resolveSkillDir does the filesystem walk.
//  2. when global is true (the explicit --global signal) → consult the
//     global lock first and never fall back to a CWD-relative stat. A
//     miss becomes a typed user-facing error.
//  3. arg is a directory in cwd → prefer the filesystem reading; this
//     keeps the `cd to/skill-dir && qvr scan demo` muscle memory intact.
//  4. otherwise → look up arg as a skill name in the appropriate lock
//     and return EffectiveTarget(entry); falls back to resolveSkillDir
//     on a miss so an unrecognised bare name still gets a tidy "not
//     found" error that names both attempts.
//
// The previous resolveScanTarget / resolveByLock pair lives here too,
// now parameterised on `global` instead of reading the scan command's
// package-level flag, so any cobra command can call into this helper.
func resolveSkillArg(arg string, global bool) (string, []string, error) {
	if looksLikePath(arg) {
		return resolveSkillDir(arg)
	}
	if global {
		return resolveByLockScoped(arg, true /*requireHit*/, true /*global*/)
	}
	if _, err := os.Stat(arg); err == nil {
		// Bare arg happens to be a directory in cwd — prefer the
		// filesystem reading.
		return resolveSkillDir(arg)
	}
	return resolveByLockScoped(arg, false /*requireHit*/, false /*global*/)
}

// resolveByLockScoped loads the (project or global) lock file and
// resolves arg to the on-disk SKILL.md directory via skill.EffectiveTarget.
//
// When requireHit is true (the --global path), a missing entry becomes a
// typed user-facing error and the function never falls back to filesystem
// resolution. When false (the default path), the caller wants either-or
// semantics and we hand off to resolveSkillDir on a miss.
func resolveByLockScoped(arg string, requireHit, global bool) (string, []string, error) {
	projectRoot, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("resolve cwd: %w", err)
	}
	lockPath := model.DefaultLockPath(projectRoot, config.Dir(), global)
	lock, lockErr := model.ReadLockFile(lockPath)
	if lockErr != nil {
		if requireHit {
			return "", nil, fmt.Errorf("read global lock %s: %w", lockPath, lockErr)
		}
		return resolveSkillDir(arg)
	}
	entry, getErr := lock.Get(arg)
	if getErr != nil {
		if requireHit {
			scope := "project"
			if global {
				scope = "global"
			}
			return "", nil, fmt.Errorf("no installed skill %q in %s lock — run `qvr list --global` to see installed names", arg, scope)
		}
		return resolveSkillDir(arg)
	}
	target := skill.EffectiveTarget(entry, projectRoot)
	if target == "" {
		return "", nil, fmt.Errorf("lock entry %q has no resolvable target — try `qvr sync` to rebuild it", arg)
	}
	// EffectiveTarget already points at the SKILL.md-bearing dir for
	// nested layouts. Confirm SKILL.md is there before handing off.
	if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err == nil {
		return target, nil, nil
	}
	// Fall through to the directory-walk resolver as a last resort —
	// covers the legacy case where the lock predates the Path field.
	return resolveSkillDir(target)
}

func looksLikePath(s string) bool {
	return s == "." || s == ".." || strings.ContainsAny(s, "/\\") || strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")
}
