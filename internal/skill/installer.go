package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quiver-cli/qvr/internal/canonical"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
	"github.com/quiver-cli/qvr/internal/registry"
)

var (
	ErrSkillNotFound    = errors.New("skill not found in any registry")
	ErrAlreadyInstalled = errors.New("skill is already installed")
	ErrInvalidReference = errors.New("invalid skill reference")
	// ErrAmbiguousRef means the requested skill exists in >1 registry but
	// the @<ref> the user asked for isn't resolvable in any of them.
	// Distinct from ErrSkillNotFound so cmd/add.go can render per-registry
	// version hints instead of a generic "register one" message (issue #101).
	ErrAmbiguousRef = errors.New("ref not found in any registry that provides the skill")
	// ErrInvalidSignature means the resolved ref carried a present-but-bad
	// git signature (BAD signature). The only provenance status that gates an
	// install — absent or unverifiable signatures never block.
	ErrInvalidSignature = errors.New("invalid git signature on resolved ref")
	// ErrSignatureRequired means policy requires a verified git signature but
	// the resolved ref had no verifiable tag or commit signature.
	ErrSignatureRequired = errors.New("verified git signature required")
	// ErrSignedByMismatch means the skill declares metadata.signed_by but the
	// ref's verified signature is by a different identity — the skill was
	// re-signed by someone other than its declared signer. Issue #167.
	ErrSignedByMismatch = errors.New("signed_by does not match the verified signature")
	// ErrSkillAbsentAtRef means the requested ref resolved to a real commit but
	// the skill's subdirectory does not exist in the tree at that commit —
	// almost always because the skill was added to the repo after that commit.
	// Distinct from a bad ref (which fails earlier at worktree creation) so the
	// user gets an actionable "pick a ref where the skill exists" message
	// instead of a leaked internal .staging path and a raw stat failure (#178).
	ErrSkillAbsentAtRef = errors.New("skill not present at the resolved ref")

	// ErrVersionNotAvailable means the requested @ref couldn't be checked out
	// because the registry was cloned latest-only (default branch, no tags/other
	// branches). The remedy is re-adding the registry with --full, so this is
	// distinct from a genuinely nonexistent ref in a full clone.
	ErrVersionNotAvailable = errors.New("version not available in a latest-only registry")
)

// InstallRequest describes a desired install.
type InstallRequest struct {
	Skill       string   // skill name, optionally with @version
	Targets     []string // e.g. []string{"claude", "cursor"}
	Global      bool
	ProjectRoot string
	LockPath    string // optional — DefaultLockPath is used when empty
	Force       bool   // allow overwriting an existing lock entry of the same name
	// Frozen pins installs to the lockfile's recorded state. The skill must
	// already have an entry; its Branch is reused and the computed subtree
	// hash must match the recorded VerificationRecord.SubtreeHash. Drift or
	// missing entries are hard errors.
	Frozen bool
	// PinCommit materializes the worktree at this exact commit SHA instead of
	// re-resolving the ref label. The uv reproducibility contract: `qvr sync`
	// restores the lock's recorded commit even when the ref (e.g. "main") has
	// advanced upstream — only `qvr update` re-resolves. The human ref label
	// (the @<ref> in Skill) is still recorded as entry.Ref. Empty for ordinary
	// `qvr add`, which resolves the ref to today's tip.
	PinCommit string
	// Registry restricts skill resolution to the named registry. Empty
	// means "search every configured registry" (the default `qvr add`
	// behavior). Set by `qvr add --registry <name>` so users can pick
	// a specific source when multiple registries publish a skill of
	// the same name.
	Registry string
	// As overrides the local install name: the lock entry, symlink
	// filename, and `qvr remove`/`qvr list` key all use As instead of
	// the canonical skill name from the registry. Empty means "install
	// under the canonical name" (the default). Set by
	// `qvr add <skill> --as <alias>` so two installs of the same skill
	// at different refs can coexist in one project (A/B testing,
	// pinning an old version while iterating on a new one).
	//
	// The underlying worktree is still keyed by canonical name + SHA,
	// so two aliases pointing at the same canonical commit share one
	// worktree on disk.
	As string
	// RequireSigned refuses installs unless the resolved ref has a verified
	// git tag or commit signature.
	RequireSigned bool
	// TrustedAuthors, when non-empty, refuses installs whose commit author is
	// not in this list.
	TrustedAuthors []string
	// TrustedAuthorsByRegistry applies author pins after the registry is
	// resolved. Used by bare installs that search all registries.
	TrustedAuthorsByRegistry map[string][]string
	// SkillPath is the repo-relative directory of the skill within its
	// registry, when the caller already knows it — the one-step `qvr add
	// <blob-url>` path derives it from the URL. Set together with Registry, it
	// lets resolution read just that one SKILL.md (FindSkillAtPath) instead of
	// indexing the whole registry. Empty for name-based adds. If the pinned
	// path turns out to be stale or root-level, resolution silently falls back
	// to the by-name full-index lookup, so this is a pure speedup, never a
	// correctness change.
	SkillPath string
}

// InstallResult holds the outcome for a single skill install.
//
// Name is the local lock-entry name (the --as alias when set, otherwise the
// canonical name from the registry). Canonical is the canonical name; the
// two are equal in the common no-alias case so existing JSON consumers stay
// stable. Warnings carries non-fatal advisories surfaced during resolution
// — e.g. "the skill name matched 2 registries, picked X" — so the caller
// can render them once per install (issue #101).
type InstallResult struct {
	Name      string   `json:"name"`
	Canonical string   `json:"canonical,omitempty"`
	Registry  string   `json:"registry"`
	Version   string   `json:"version"`
	Worktree  string   `json:"worktree"`
	Targets   []string `json:"targets"`
	Commit    string   `json:"commit"`
	Warnings  []string `json:"warnings,omitempty"`
}

// Installer orchestrates worktree + sparse checkout + symlinks + lock file.
type Installer struct {
	Registry *registry.Manager
	Worktree git.WorktreeManager
	Git      git.GitClient
}

// NewInstaller wires default dependencies.
func NewInstaller(reg *registry.Manager, wt git.WorktreeManager, gc git.GitClient) *Installer {
	return &Installer{Registry: reg, Worktree: wt, Git: gc}
}

// ParseReference splits "name@version" into its two parts. Version may be
// empty, in which case the registry's default branch is used at install time.
func ParseReference(ref string) (name, version string, err error) {
	if ref == "" {
		return "", "", fmt.Errorf("%w: empty reference", ErrInvalidReference)
	}
	parts := strings.SplitN(ref, "@", 2)
	name = strings.TrimSpace(parts[0])
	if name == "" {
		return "", "", fmt.Errorf("%w: empty name", ErrInvalidReference)
	}
	if len(parts) == 2 {
		version = strings.TrimSpace(parts[1])
	}
	return name, version, nil
}

// Install performs the full install flow. It is atomic at the worktree level:
// the worktree is created in a staging path, validated, and only renamed to
// the final path on success. Symlinks and lock file writes happen only after
// the worktree is in place.
func (in *Installer) Install(req InstallRequest) (*InstallResult, error) {
	name, version, err := ParseReference(req.Skill)
	if err != nil {
		return nil, err
	}
	// Whether the user explicitly pinned a version (`skill@ref`). Drives the
	// "latest-only registry can't reach this version → use --full" diagnostic:
	// a missing ref is only "go fetch all versions" when the user asked for one.
	explicitVersion := version != ""
	if len(req.Targets) == 0 {
		return nil, fmt.Errorf("at least one --target is required")
	}
	for _, t := range req.Targets {
		if _, ok := model.Targets[t]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, t)
		}
	}

	// --frozen lock peek: the lockfile is authoritative for a frozen
	// install, so use it to pre-fill request fields the user didn't
	// supply. Two effects:
	//   1. Alias support (#102): when the user runs `qvr add --frozen
	//      <alias>` and the lock records <alias> as an alias entry
	//      (entry.Canonical != ""), swap the registry lookup to the
	//      canonical name and preserve the alias via req.As. Without
	//      this the lookup treats the alias as a registry skill name and
	//      fails ErrSkillNotFound, even though the lock is self-describing.
	//      RestoreAll already does this swap explicitly when iterating
	//      entries; here we handle the caller-supplied-name path.
	//   2. Registry scoping (#105): pre-fill req.Registry from
	//      entry.Registry so resolveSkill is scoped to the source that
	//      was pinned at install time. Without this the resolver walks
	//      every configured registry and may emit a stale ambiguity
	//      warning even though the lockfile already chose.
	if req.Frozen {
		lp := req.LockPath
		if lp == "" {
			lp = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
		}
		// --frozen is lock-authoritative, so a missing/unreadable lock is a
		// hard error BEFORE we resolve anything. Checked here (not just at the
		// drift gate below) so the user gets the contract string "requires a
		// readable lock file" regardless of whether the skill name happens to
		// resolve in a registry. ReadLockFile returns an empty lock — not an
		// error — for a non-existent file (that's the expected pre-first-install
		// state), so we stat explicitly to tell "no lock at all" (this error)
		// apart from "lock exists but lacks the entry" (the "skill not present"
		// error at the drift gate). AC-FROZEN-2 / #132.
		if _, statErr := os.Stat(lp); statErr != nil {
			return nil, fmt.Errorf("--frozen requires a readable lock file: %w", statErr)
		}
		existingLock, lerr := model.ReadLockFile(lp)
		if lerr != nil {
			return nil, fmt.Errorf("--frozen requires a readable lock file: %w", lerr)
		}
		if existing, gerr := existingLock.Get(name); gerr == nil {
			if req.As == "" && existing.Canonical != "" {
				req.As = name
				name = existing.Canonical
			}
			if req.Registry == "" && existing.Registry != "" {
				req.Registry = existing.Registry
			}
		}
	}

	// localName is what we record in the lock and use for symlink
	// filenames; canonical `name` still drives registry lookup and the
	// worktree path so aliases at the same SHA share one worktree.
	localName := name
	if req.As != "" {
		if !nameRegex.MatchString(req.As) || strings.Contains(req.As, "--") {
			return nil, fmt.Errorf("invalid --as value %q: must be 1-64 chars, lowercase alphanumeric + hyphens, no leading/trailing or consecutive hyphens", req.As)
		}
		localName = req.As
	}

	loc, ambiguityWarning, err := in.resolveSkill(name, version, req.Registry, req.SkillPath)
	if err != nil {
		return nil, err
	}
	if version == "" {
		version = resolveDefaultRef(loc, in.Git)
	} else if !req.Frozen {
		// Map an explicit per-skill version to its namespaced tag when the
		// registry namespaces versions per skill (#152): `qvr add alpha@v0.1.0`
		// transparently resolves to the tag `alpha/v0.1.0` if that's how it was
		// published. Falls through unchanged for branches, SHAs, and bare
		// single-skill tags.
		if eff, ok := resolveSkillRef(in.Git, loc.RepoPath, loc.Entry.Name, version); ok {
			version = eff
		}
	}

	// --frozen pins to the lockfile: the entry must exist and its recorded
	// Branch/SubtreeHash become the install target. Captured here so the
	// drift check at the end can re-read the same recorded values.
	var frozenRef *model.LockEntry
	if req.Frozen {
		lp := req.LockPath
		if lp == "" {
			lp = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
		}
		existingLock, lerr := model.ReadLockFile(lp)
		if lerr != nil {
			return nil, fmt.Errorf("--frozen requires a readable lock file: %w", lerr)
		}
		existing, gerr := existingLock.Get(localName)
		if gerr != nil {
			return nil, fmt.Errorf("--frozen: skill %q not present in lock file", localName)
		}
		if existing.Ref != "" {
			version = existing.Ref
		}
		frozenRef = existing
	}

	// Conflict check: silently swapping the lock entry to a different ref
	// would contradict the "switching refs is a symlink repoint, not a
	// re-install" contract. Refuse with a hint that covers all three
	// recovery paths. Issue #111: the old hint led with `qvr switch
	// <name> <ref>`, which only works when the source is the same as
	// the requested one — for a same-alias-different-registry conflict
	// it silently kept the wrong source. The new hint leads with
	// remove+add (always correct), keeps --force as the in-place
	// overwrite, and qualifies `qvr switch` as "same-source-only".
	// Idempotent when the existing ref matches. Uses localName so
	// `--as <alias>` installs only conflict with prior installs of the
	// same alias, not the canonical name — the whole point of --as is
	// coexistence.
	if !req.Force {
		lp := req.LockPath
		if lp == "" {
			lp = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
		}
		if existingLock, lerr := model.ReadLockFile(lp); lerr == nil {
			if existing, gerr := existingLock.Get(localName); gerr == nil && existing.Ref != "" && existing.Ref != version {
				return nil, fmt.Errorf("%s already installed at %s (from %s); pass --force to overwrite, or `qvr remove %s --force && qvr add %s@%s` to reinstall (if the source is changing too — `qvr switch` only moves the ref within the same source)",
					localName, existing.Ref, existing.Source, localName, localName, version)
			}
		}
	}

	// Resolve the ref → full SHA against the bare clone so the worktree path
	// is SHA-keyed, not ref-keyed. Two projects pinning the same commit then
	// share one worktree even when they wrote different ref labels (one pinned
	// "main", the other "abc123"). Falls back to a degraded path using the ref
	// label when resolution fails — the install still succeeds and the lock
	// entry's Worktree field is still self-consistent; only the cross-project
	// share-by-SHA optimization is lost.
	resolvedSHA, sherr := in.Git.ResolveRef(loc.RepoPath, version)
	if sherr != nil || resolvedSHA == "" {
		resolvedSHA = version
	}

	// checkoutRef is what the worktree actually checks out. Normally the ref
	// label; under PinCommit (qvr sync restore) it's the lock's recorded
	// commit, so a restore is reproducible even when the ref advanced
	// upstream. entry.Ref stays the human label either way.
	checkoutRef := version
	if req.PinCommit != "" {
		checkoutRef = req.PinCommit
		resolvedSHA = req.PinCommit
	}

	// Staging path → final path. Worktree creation can fail mid-way (e.g., bad
	// ref), and we don't want a half-populated directory masquerading as an
	// installed skill. Stage in a sibling dir and rename at the end.
	finalPath := registry.WorktreePath(loc.RegistryName, name, registry.ShortSHA(resolvedSHA))
	stagingPath := finalPath + ".staging"
	_ = os.RemoveAll(stagingPath) // clear any stale staging from a prior crash

	// Decide the content scope. Normally taken from the freshly-resolved index
	// entry, but a reproducible restore (`qvr sync` via PinCommit) materializes
	// the LOCKED commit while the index reflects HEAD — so honor the scope
	// recorded in the lock at original install time, which can't drift if
	// upstream later changed the sibling layout. See model.SkillScopePaths.
	rootCoexists := loc.Entry.RootCoexists
	if req.PinCommit != "" {
		lp := req.LockPath
		if lp == "" {
			lp = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
		}
		if el, lerr := model.ReadLockFile(lp); lerr == nil {
			if prev, gerr := el.Get(localName); gerr == nil {
				rootCoexists = prev.RootCoexists
			}
		}
	}

	if _, err := os.Stat(finalPath); err == nil {
		// Worktree already exists — reuse it. This makes `qvr install` idempotent
		// across multiple agent targets (install once, add cursor target, rerun
		// install).
	} else {
		if err := in.Worktree.Add(loc.RepoPath, stagingPath, checkoutRef); err != nil {
			_ = os.RemoveAll(stagingPath)
			// A latest-only registry (default-branch clone) has no tags or other
			// branches, so an explicitly-pinned version simply isn't on disk.
			// Point the user at --full instead of dumping a raw git checkout
			// error. Only when the user actually pinned a version and the
			// registry isn't a full clone — a missing ref in a full clone is a
			// genuine "no such ref".
			if explicitVersion && !git.IsFullClone(loc.RepoPath) {
				return nil, fmt.Errorf("%w: %q not found in registry %q — it was cloned latest-only (default branch). Re-add with all versions: `qvr registry add %s --full`, then retry",
					ErrVersionNotAvailable, version, loc.RegistryName, loc.RegistryURL)
			}
			return nil, fmt.Errorf("create worktree: %w", err)
		}
		// Scope the checkout to the skill's own content so what we install
		// matches what the scan gate inspected. A root skill that coexists with
		// sibling skills is narrowed to SKILL.md + recognized content dirs; a
		// non-root skill to its subtree; a lone root skill keeps the whole repo.
		if scope := model.SkillScopePaths(loc.Entry.Path, rootCoexists); rootCoexists {
			if err := in.Worktree.SetSparseCheckoutPatterns(stagingPath, scope); err != nil {
				_ = os.RemoveAll(stagingPath)
				return nil, fmt.Errorf("sparse checkout: %w", err)
			}
		} else if err := in.Worktree.SetSparseCheckout(stagingPath, []string{loc.Entry.Path}); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, fmt.Errorf("sparse checkout: %w", err)
		}
		// The ref resolved (worktree creation succeeded), but if the skill's own
		// SKILL.md isn't in the checked-out tree the skill simply doesn't exist
		// at this commit — typically because it was added to the repo later.
		// Catch that here with a user-facing message rather than letting the
		// loader surface a raw `stat skill dir` failure over the internal
		// `.staging` path (#178).
		if _, statErr := os.Stat(filepath.Join(stagingPath, loc.Entry.Path, "SKILL.md")); errors.Is(statErr, os.ErrNotExist) {
			_ = os.RemoveAll(stagingPath)
			return nil, fmt.Errorf("%w: skill %q (%s) does not exist at %s (%s) — the skill was likely added to the repo after that commit; run `qvr version list %s` to find a ref where it exists",
				ErrSkillAbsentAtRef, name, loc.Entry.Path, version, registry.ShortSHA(resolvedSHA), name)
		}
		if err := validateStagedSkill(stagingPath, loc.Entry.Path, name); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, err
		}
		if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
			_ = os.RemoveAll(stagingPath)
			return nil, fmt.Errorf("create worktrees dir: %w", err)
		}
		if err := os.Rename(stagingPath, finalPath); err != nil {
			// Race: another process may have created finalPath between our
			// initial Stat and the Rename. If finalPath now exists, drop our
			// staged copy and reuse the winning one.
			if _, statErr := os.Stat(finalPath); statErr == nil {
				_ = os.RemoveAll(stagingPath)
			} else {
				_ = os.RemoveAll(stagingPath)
				return nil, fmt.Errorf("finalize worktree: %w", err)
			}
		}
	}

	skillDir := filepath.Join(finalPath, loc.Entry.Path)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("skill path missing after checkout: %w", err)
	}

	// Optional git-native provenance (the v1 trust surface). Verify a signed
	// tag/commit if one is present. An invalid present-but-bad signature blocks
	// the install before any symlink or lock side effects, because it signals
	// tampering. When policy requires signatures, absent or unverifiable
	// signatures also block. Computed once here and reused for the lock entry.
	// Scoped to the skill's own subtree (loc.Entry.Path) so signature status
	// reflects the commit that wrote the skill, not the branch tip (#173).
	provenance := CheckGitProvenance(loc.RepoPath, version, resolvedSHA, loc.Entry.Path)
	if provenance != nil && provenance.SignatureStatus == model.SignatureStatusInvalid {
		return nil, fmt.Errorf("%w: %s@%s carries an invalid git signature on %q — refusing to install",
			ErrInvalidSignature, name, registry.ShortSHA(resolvedSHA), version)
	}
	if req.RequireSigned && (provenance == nil || provenance.SignatureStatus != model.SignatureStatusVerified) {
		return nil, fmt.Errorf("%w: %s@%s is unsigned; disable security.require_signed or install a signed tag/commit",
			ErrSignatureRequired, name, registry.ShortSHA(resolvedSHA))
	}
	// signed_by declaration: a skill can name (in metadata.signed_by) the
	// identity that must have signed its ref. When a verified signature is
	// present its signer must match the declaration — a mismatch means the ref
	// was re-signed by someone other than the declared signer and blocks the
	// install. An absent/unverifiable signature leaves the declaration
	// unverifiable; require_signed (above) gates that case when policy demands
	// it, so here we act only on a present-but-wrong signer. Matching reuses the
	// email-aware author matcher so `signed_by: alice@example.com` matches a
	// signer reported as `Alice <alice@example.com>`. Issue #167.
	if signedBy := DeclaredSignedBy(skillDir); signedBy != "" &&
		provenance != nil && provenance.SignatureStatus == model.SignatureStatusVerified &&
		!AuthorMatchesPin(provenance.Signer, signedBy) {
		return nil, fmt.Errorf("%w: %s@%s declares signed_by %q but its verified signature is by %q",
			ErrSignedByMismatch, name, registry.ShortSHA(resolvedSHA), signedBy, provenance.Signer)
	}
	commitAuthor := CommitAuthor(loc.RepoPath, resolvedSHA, loc.Entry.Path)
	trustedAuthors := req.TrustedAuthors
	if len(trustedAuthors) == 0 && req.TrustedAuthorsByRegistry != nil {
		trustedAuthors = req.TrustedAuthorsByRegistry[loc.RegistryName]
	}
	if len(trustedAuthors) > 0 && !AuthorAllowed(commitAuthor, trustedAuthors) {
		return nil, fmt.Errorf("untrusted commit author: %s@%s was authored by %q, not one of %s",
			name, registry.ShortSHA(resolvedSHA), commitAuthor, strings.Join(trustedAuthors, ", "))
	}

	// Freeze the verified bytes: write-protect the installed skill subtree so
	// what an agent reads through the symlink stays identical to what was
	// scanned and verified. This is a shared consume install — modifying it
	// requires `qvr edit`, which ejects a writable project-local copy. The
	// shared worktree is keyed by SHA and shared across projects; freezing it
	// also prevents one project's edit from silently mutating another's.
	setSubtreeReadOnly(skillDir)

	// The agent-facing symlink points at the skill content. For a root-layout
	// skill (the whole worktree IS the skill) that content root carries the
	// live .git/, so we expose a sanitized view (under .git/, mirroring the
	// content minus .git) instead — an agent reading the skill never sees repo
	// internals (issue #154). Subdir skills already point at a clean subtree.
	linkTarget := skillDir
	if IsRootLayoutPath(loc.Entry.Path) {
		view, verr := buildAgentViewAt(finalPath)
		if verr != nil {
			return nil, fmt.Errorf("agent view: %w", verr)
		}
		linkTarget = view
	}

	// Create symlinks for every target. If any fails, roll back previously
	// created symlinks for this install to leave the filesystem consistent.
	var created []string
	for _, t := range req.Targets {
		linkPath, err := ResolveTargetPath(t, localName, req.ProjectRoot, req.Global)
		if err != nil {
			rollbackLinks(created)
			return nil, err
		}
		if err := CreateSymlink(linkPath, linkTarget); err != nil {
			rollbackLinks(created)
			return nil, fmt.Errorf("symlink %s: %w", t, err)
		}
		created = append(created, linkPath)
	}

	commit, _ := in.resolveCommit(finalPath)

	// Update lock file last — if it fails, everything else is still usable and
	// a subsequent install will reconcile state.
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	targets := req.Targets
	var priorVerification *model.VerificationRecord
	if existing, err := lock.Get(localName); err == nil {
		targets = mergeTargets(existing.Targets, req.Targets)
		// Preserve the prior Verification when the install is a no-op
		// (same commit). Without this, a re-add wipes the scan attestation
		// before the gate rewrites it — even when the new SHA matches, the
		// intermediate "no verification" lockfile state churns concurrent
		// readers, and any code path that doesn't re-run the gate would
		// permanently lose the prior signal. Issue #77.
		if existing.Commit == commit && existing.Verification != nil {
			priorVerification = existing.Verification
		}
	}
	var subtreeHash, treeOID string
	if id, hashErr := ComputeEntryIdentity(finalPath, loc.Entry.Path, rootCoexists); hashErr == nil {
		subtreeHash = id.SubtreeHash
		treeOID = id.TreeSHA
	}
	// else: hashing failure shouldn't block the install — we still want the
	// worktree and symlinks on disk so the user can use the skill. Leave
	// SubtreeHash/TreeOID empty; doctor/verify will flag the missing seal.

	// Merge the freshly-computed provenance into the verification record,
	// preserving any prior signal (e.g. a scan attestation) carried over from
	// a same-commit no-op re-add.
	verification := priorVerification
	if provenance != nil {
		if verification == nil {
			verification = &model.VerificationRecord{}
		}
		verification.Provenance = provenance
	}

	entry := &model.LockEntry{
		Name:          localName,
		Registry:      loc.RegistryName,
		Source:        loc.RegistryURL,
		Path:          loc.Entry.Path,
		RootCoexists:  rootCoexists,
		Ref:           version,
		Commit:        commit,
		InstallCommit: commit,
		CommitAuthor:  commitAuthor,
		SubtreeHash:   subtreeHash,
		TreeOID:       treeOID,
		Targets:       targets,
		Verification:  verification,
	}
	// Record the canonical (registry-side) skill name when the user
	// installed under an alias, so `qvr list` / `qvr upgrade` can map
	// the local lock key back to the registry skill it points at.
	if req.As != "" {
		entry.Canonical = name
	}

	// --frozen drift check: the just-installed worktree must hash to the
	// same SubtreeHash recorded in the prior lockfile entry. Mismatch
	// usually means the registry was force-pushed or the recorded entry
	// itself was tampered with — refuse the install rather than silently
	// rewriting history.
	if req.Frozen && frozenRef != nil && frozenRef.SubtreeHash != "" {
		if entry.SubtreeHash != frozenRef.SubtreeHash {
			return nil, fmt.Errorf("--frozen: subtree hash drift for %s (expected %s, got %s)",
				localName, frozenRef.SubtreeHash, entry.SubtreeHash)
		}
	}

	lock.Put(entry)
	if err := lock.Write(); err != nil {
		return nil, fmt.Errorf("write lock file: %w", err)
	}

	result := &InstallResult{
		Name:     localName,
		Registry: loc.RegistryName,
		Version:  version,
		Worktree: finalPath,
		Targets:  targets,
		Commit:   commit,
	}
	if req.As != "" {
		result.Canonical = name
	}
	if ambiguityWarning != "" {
		result.Warnings = append(result.Warnings, ambiguityWarning)
	}
	return result, nil
}

// resolveSkill picks the SkillLocation for a (name, version, registry) tuple
// and returns a non-fatal ambiguity warning when the caller didn't scope to
// a single registry and the name resolves to >1 source.
//
// When registryName is set, this is a single-registry FindSkillIn — the
// scoped error flows through unchanged.
//
// Otherwise we collect every registry that exposes `name`:
//
//  0. zero matches → ErrSkillNotFound
//  1. one match    → use it, no warning
//     N. multiple:
//     - if version == "": pick the first (alphabetical) and warn so the
//     user knows the resolution wasn't unique and can re-pin with
//     --registry. Closes the silent-pick half of issue #101.
//     - if version != "": try every candidate via ResolveRef; pick the
//     first one whose bare clone actually contains the ref. If none
//     do, return ErrAmbiguousRef with per-registry version summaries
//     so the user sees who has what instead of the misleading
//     "create worktree: reference not found" from the old single-pick
//     path. Closes the wrong-pick-then-error half of issue #101.
func (in *Installer) resolveSkill(name, version, registryName, skillPath string) (*registry.SkillLocation, string, error) {
	// Fast path: the caller pinned an exact skill directory (e.g. from a
	// `qvr add <blob-url>` spec) under a single registry. Resolve just that
	// SKILL.md instead of indexing the entire registry. A miss (stale path,
	// root-level skill, name↔dir mismatch) falls through to the by-name lookup
	// below, so this is a pure speedup and never changes what resolves.
	if skillPath != "" && registryName != "" {
		if loc, err := in.Registry.FindSkillAtPath(registryName, skillPath); err == nil {
			return loc, "", nil
		}
	}
	if registryName != "" {
		loc, err := in.Registry.FindSkillIn(name, registryName)
		if err != nil {
			return nil, "", err
		}
		return loc, "", nil
	}
	locs, err := in.Registry.FindAllSkillLocations(name)
	if err != nil {
		return nil, "", err
	}
	switch len(locs) {
	case 0:
		return nil, "", fmt.Errorf("%w: %s", ErrSkillNotFound, name)
	case 1:
		return locs[0], "", nil
	}

	regNames := make([]string, len(locs))
	for i, l := range locs {
		regNames[i] = l.RegistryName
	}

	if version == "" {
		picked := locs[0]
		warning := fmt.Sprintf("%s resolves to %d registries (%s) — picked %s (alphabetical). Pass --registry %s to silence this, or --registry <name> to pick another.",
			name, len(locs), strings.Join(regNames, ", "), picked.RegistryName, picked.RegistryName)
		return picked, warning, nil
	}

	// Collect every registry whose bare clone contains the requested ref
	// rather than short-circuiting on the first match. Mirrors the bare-name
	// ambiguity shape: 1 → silent pick, 2+ → warn + alphabetical pick.
	// Before this, `qvr add skill@v1` with two registries both holding v1
	// silently picked alphabetical with no warning (issue #106).
	var matched []*registry.SkillLocation
	for _, l := range locs {
		// Match via resolveSkillRef so a per-skill-namespaced version
		// (`alpha@v0.1.0` → tag `alpha/v0.1.0`) counts as present (#152).
		if _, ok := resolveSkillRef(in.Git, l.RepoPath, l.Entry.Name, version); ok {
			matched = append(matched, l)
		}
	}
	switch len(matched) {
	case 0:
		var lines []string
		for _, l := range locs {
			lines = append(lines, fmt.Sprintf("  - %s: %s", l.RegistryName, summarizeVersions(l)))
		}
		return nil, "", fmt.Errorf("%w: ref %q not found in any registry that provides %q:\n%s\nPass --registry <name> to scope",
			ErrAmbiguousRef, version, name, strings.Join(lines, "\n"))
	case 1:
		return matched[0], "", nil
	}

	matchedNames := make([]string, len(matched))
	for i, l := range matched {
		matchedNames[i] = l.RegistryName
	}
	picked := matched[0]
	warning := fmt.Sprintf("%s@%s resolves in %d registries (%s) — picked %s (alphabetical). Pass --registry %s to silence this, or --registry <name> to pick another.",
		name, version, len(matched), strings.Join(matchedNames, ", "), picked.RegistryName, picked.RegistryName)
	return picked, warning, nil
}

// summarizeVersions renders a compact "tags: vA..vZ; branches: main, dev"
// hint for a SkillLocation, used in ErrAmbiguousRef messages. Empty lists
// are dropped so a tag-only registry doesn't carry an empty "branches:"
// segment.
func summarizeVersions(loc *registry.SkillLocation) string {
	var parts []string
	if tags := loc.Entry.Versions.Tags; len(tags) > 0 {
		parts = append(parts, "tags: "+strings.Join(tagsForSummary(tags), ", "))
	}
	if branches := loc.Entry.Versions.Branches; len(branches) > 0 {
		parts = append(parts, "branches: "+strings.Join(branches, ", "))
	}
	if len(parts) == 0 {
		return "no published refs"
	}
	return strings.Join(parts, "; ")
}

// tagsForSummary returns the up-to-five most relevant tag labels for an
// error message. We don't sort — registries publish their own ordering and
// re-sorting by semver here would be a dependency-heavy distraction in an
// error path. Truncation just keeps the line readable.
func tagsForSummary(tags []string) []string {
	const max = 5
	if len(tags) <= max {
		return tags
	}
	out := append([]string{}, tags[:max]...)
	out = append(out, fmt.Sprintf("…(+%d more)", len(tags)-max))
	return out
}

// Remove tears down a skill: remove symlinks, worktree, and lock entry.
//
// Ordering invariant (issue #93): the filesystem teardown happens FIRST.
// Only if every required FS step succeeds do we drop the lock entry. A
// failure mid-teardown returns an error WITHOUT mutating the lock so the
// user has a recovery path (re-run with `--force`, fix the underlying FS
// issue, then retry) rather than an orphan eject dir + missing lock entry.
//
// Mode:edit handling: the canonical install path is a real directory
// holding the user's edits, not a symlink. RemoveSymlink would refuse it
// (`not a symlink`). With req.Force, the eject dir is rm -rf'd; without
// Force, the caller (cmd/remove.go) refuses upstream, so this code path
// shouldn't run on an unforced mode:edit entry — defensive check kept
// here too.
func (in *Installer) Remove(name string, req InstallRequest) error {
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return fmt.Errorf("read lock file: %w", err)
	}
	entry, err := lock.Get(name)
	if err != nil {
		return err
	}

	entryGlobal := lock.IsGlobal(quiverHome())
	canonicalEditAbs := ""
	if entry.IsEdit() {
		if !req.Force {
			return fmt.Errorf("remove %s: skill is in edit mode — pass --force to delete the eject dir at %s", name, entry.EditPath)
		}
		canonicalEditAbs = entry.EditPath
		if canonicalEditAbs != "" && !filepath.IsAbs(canonicalEditAbs) {
			canonicalEditAbs = filepath.Join(req.ProjectRoot, canonicalEditAbs)
		}
	}

	// Pass 1: drop target symlinks (and the canonical edit dir, when in
	// edit mode). Bail without touching the lock if any step fails so the
	// user can recover rather than be left with an orphan lock entry.
	for _, t := range entry.Targets {
		linkPath, err := ResolveTargetPath(t, name, req.ProjectRoot, entryGlobal)
		if err != nil {
			return fmt.Errorf("remove %s: resolve target %s: %w", name, t, err)
		}
		// For mode:edit canonical target: rm -rf the eject dir. Siblings
		// are symlinks pointing at canonical and use RemoveSymlink.
		if entry.IsEdit() && canonicalEditAbs != "" {
			canonicalAbs, _ := filepath.Abs(canonicalEditAbs)
			absLink, _ := filepath.Abs(linkPath)
			if canonicalAbs == absLink {
				if err := os.RemoveAll(linkPath); err != nil {
					return fmt.Errorf("remove %s: rm eject dir %s: %w", name, linkPath, err)
				}
				continue
			}
		}
		if err := RemoveSymlink(linkPath); err != nil && !errors.Is(err, ErrSymlinkNotFound) {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}

	// Pass 2: drop the shared worktree for non-edit, non-link entries.
	// Mode:edit entries never had a shared worktree to clean; link
	// installs point at user-owned dirs we don't touch.
	if !entry.IsLink() && !entry.IsEdit() {
		worktreePath := EntryWorktreePath(entry)
		if worktreePath != "" {
			if err := in.Worktree.Remove(worktreePath); err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
				return fmt.Errorf("remove %s: drop worktree: %w", name, err)
			}
		}
	}

	// Only now drop the lock entry. Symmetric with Install, which writes
	// the lock last.
	if err := lock.Remove(name); err != nil && !errors.Is(err, model.ErrLockSkillMissing) {
		return fmt.Errorf("remove %s: drop lock entry: %w", name, err)
	}
	if err := lock.Write(); err != nil {
		return fmt.Errorf("remove %s: write lock: %w", name, err)
	}
	return nil
}

// Link creates symlinks from a local skill directory into agent dirs. No
// worktree, no git, no lock-file bookkeeping unless the caller asked for it.
// This powers `qvr link` for local skill development.
func (in *Installer) Link(localPath string, req InstallRequest) (*InstallResult, error) {
	for _, t := range req.Targets {
		if _, ok := model.Targets[t]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrUnknownTarget, t)
		}
	}
	abs, err := filepath.Abs(localPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}
	loaded, err := LoadFromPath(abs)
	if err != nil {
		return nil, err
	}
	// `qvr link` must respect the same spec rule `qvr validate` enforces: the
	// frontmatter name has to match the directory it lives in. Letting a
	// mismatch through creates a symlink that subsequent validate/doctor runs
	// immediately flag — silent drift we'd rather catch at link time.
	if result := Validate(loaded); !result.Valid {
		var lines []string
		for _, e := range result.Errors {
			lines = append(lines, e.Error())
		}
		return nil, fmt.Errorf("skill validation failed:\n  %s", strings.Join(lines, "\n  "))
	}
	name := loaded.Frontmatter.Name
	if name == "" {
		name = loaded.Name
	}

	// Conflict check: refuse to silently replace an existing entry of the
	// same name with a different on-disk target. Idempotent when the path
	// matches; --force needed to switch paths.
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	if existing, err := lock.Get(name); err == nil && !req.Force {
		// In v5, `source` carries the absolute path for link installs (and a
		// git URL for remote installs). A link install collides with an
		// existing entry only when the existing entry is *also* a link
		// pointing at the same path; otherwise refuse so we don't silently
		// shadow a remote-installed skill with a local symlink.
		if !existing.IsLink() || existing.Source != abs {
			sourceLabel := existing.Source
			if sourceLabel == "" {
				sourceLabel = "registry"
			}
			return nil, fmt.Errorf("skill %q already installed from %s; pass --force to relink",
				name, sourceLabel)
		}
	}

	var created []string
	for _, t := range req.Targets {
		linkPath, err := ResolveTargetPath(t, name, req.ProjectRoot, req.Global)
		if err != nil {
			rollbackLinks(created)
			return nil, err
		}
		if err := CreateSymlink(linkPath, abs); err != nil {
			rollbackLinks(created)
			return nil, fmt.Errorf("symlink %s: %w", t, err)
		}
		created = append(created, linkPath)
	}
	// Compute a subtree hash for the linked dir so drift detection still
	// works against the live source. Best-effort: a hashing failure leaves
	// the field empty and doctor/verify will flag it.
	linkSubtreeHash, _ := canonical.HashSubtreeFromDisk(abs)
	lock.Put(&model.LockEntry{
		Name:        name,
		Source:      abs,
		Ref:         "local",
		Mode:        model.ModeLink,
		SubtreeHash: linkSubtreeHash,
		Targets:     req.Targets,
		InstalledAt: time.Now().UTC(),
	})
	if err := lock.Write(); err != nil {
		return nil, fmt.Errorf("write lock file: %w", err)
	}
	return &InstallResult{
		Name:     name,
		Version:  "link",
		Worktree: abs,
		Targets:  req.Targets,
	}, nil
}

func (in *Installer) resolveCommit(worktreePath string) (string, error) {
	if in.Git == nil {
		return "", nil
	}
	return in.Git.HeadCommit(worktreePath)
}

// validateStagedSkill loads the skill at the expected path inside the staged
// worktree and runs the standard validator. Refuses installs that would produce
// a symlink to a non-conformant skill.
//
// expectedName is the skill's canonical name from the registry index. The
// loader sets Skill.Name from the on-disk directory's basename, which for a
// layout-B repo (SKILL.md at the root, skillRelPath == ".") is the staging
// directory itself (e.g. `<reg>--<skill>--<ref>.staging`) — definitely not
// what the user wrote in `name:`. Overriding to expectedName before validation
// keeps the name↔dir match meaningful for layout A while letting layout B
// pass without leaking internal `.staging` paths to the user (bug #50).
func validateStagedSkill(stagingPath, skillRelPath, expectedName string) error {
	skillDir := filepath.Join(stagingPath, skillRelPath)
	loaded, err := LoadFromPath(skillDir)
	if err != nil {
		return fmt.Errorf("load staged skill: %w", err)
	}
	if expectedName != "" {
		loaded.Name = expectedName
	}
	if result := Validate(loaded); !result.Valid {
		var lines []string
		for _, e := range result.Errors {
			lines = append(lines, e.Error())
		}
		return fmt.Errorf("skill validation failed:\n  %s", strings.Join(lines, "\n  "))
	}
	return nil
}

func rollbackLinks(paths []string) {
	for _, p := range paths {
		_ = RemoveSymlink(p)
	}
}

func mergeTargets(existing, add []string) []string {
	set := make(map[string]struct{})
	for _, t := range existing {
		set[t] = struct{}{}
	}
	for _, t := range add {
		set[t] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// resolveDefaultRef picks the latest semver tag whose commit still holds the
// cached skill path; if no semver tag qualifies, it falls back to the
// registry's default branch. Non-semver tags are ignored so "bare install"
// rewards tag-using registries without surprising users with arbitrary
// moving labels like `latest` or `stable`.
//
// gc is consulted to confirm the candidate tag's commit actually contains the
// indexed skill path (issue #100): a fork registry will commonly have older
// tags pointing at commits where the skill didn't exist yet (or lived at a
// different layout), and silently checking those out would produce an empty
// sparse worktree that fails validation with "load staged skill: no such
// file or directory". Tag-existence is checked via ReadBlob on `<path>/SKILL.md`,
// so the same call already costs one tree walk for the path we'd sparse-check
// out anyway. A nil gc skips the validation (callers that just want "which
// label" without I/O — currently none in the install path, but kept ergonomic
// for callers like `qvr outdated` that may want the unfiltered answer).
func resolveDefaultRef(loc *registry.SkillLocation, gc git.GitClient) string {
	if tag := latestValidSemverTag(loc, gc); tag != "" {
		return tag
	}
	return loc.DefaultBranch
}

// resolveSkillRef returns the git ref to check out for a requested
// (skillName, version) in repoPath, transparently mapping a per-skill version
// to its namespaced tag (issue #152). It prefers the namespaced tag
// "<skill>/<version>" when that ref exists (the multi-skill case), else the
// bare ref — a branch, a commit SHA, or a legacy single-skill tag. Returns
// ("", false) when version is empty or neither form resolves.
func resolveSkillRef(gc git.GitClient, repoPath, skillName, version string) (string, bool) {
	if version == "" || gc == nil {
		return "", false
	}
	if skillName != "" {
		ns := skillName + model.SkillTagSep + version
		if _, err := gc.ResolveRef(repoPath, ns); err == nil {
			return ns, true
		}
	}
	if _, err := gc.ResolveRef(repoPath, version); err == nil {
		return version, true
	}
	return "", false
}

// LatestSemverTag returns the highest-sorted semver tag from the given list,
// or "" when none qualify. Reuses model.SortVersions so precedence matches
// `qvr version list`.
//
// Path-agnostic: doesn't verify the tag's commit contains a specific skill —
// use latestValidSemverTag for that. Callers that want "what is the marketing
// version" (qvr outdated, qvr upgrade prompts) should keep using this.
func LatestSemverTag(tags []string) string {
	vl := &model.VersionList{}
	for _, t := range tags {
		if model.IsSemverTag(t) {
			vl.Tags = append(vl.Tags, model.Version{Ref: t, IsSemver: true})
		}
	}
	if len(vl.Tags) == 0 {
		return ""
	}
	model.SortVersions(vl, "")
	return vl.Tags[0].Ref
}

// latestValidSemverTag walks loc.Entry.Versions.Tags newest-first and returns
// the first semver tag whose commit still contains loc.Entry.Path (i.e., the
// skill exists at that tag). Returns "" when no semver tag qualifies — either
// none are semver, or every semver tag predates the skill being added to the
// repo.
//
// When gc is nil, falls back to LatestSemverTag (no I/O, no validation) so
// the function stays usable in tests / callers that explicitly want the
// unchecked behaviour.
func latestValidSemverTag(loc *registry.SkillLocation, gc git.GitClient) string {
	if gc == nil {
		return LatestSemverTag(loc.Entry.Versions.Tags)
	}
	vl := &model.VersionList{}
	for _, t := range loc.Entry.Versions.Tags {
		if model.IsSemverTag(t) {
			vl.Tags = append(vl.Tags, model.Version{Ref: t, IsSemver: true})
		}
	}
	if len(vl.Tags) == 0 {
		return ""
	}
	model.SortVersions(vl, "")
	for _, v := range vl.Tags {
		if tagContainsSkillPath(gc, loc.RepoPath, v.Ref, loc.Entry.Path) {
			return v.Ref
		}
	}
	return ""
}

// tagContainsSkillPath reports whether the tree at `ref` in the bare repo at
// repoPath contains an SKILL.md under `path`. For root-layout entries (path
// is "" or "."), SKILL.md is looked up directly at the root. Errors from
// ReadBlob — missing blob, missing path, unknown ref — all collapse to
// false: any failure to confirm means we shouldn't trust the tag.
func tagContainsSkillPath(gc git.GitClient, repoPath, ref, path string) bool {
	target := "SKILL.md"
	if path != "" && path != "." {
		target = path + "/SKILL.md"
	}
	_, err := gc.ReadBlob(repoPath, ref, target)
	return err == nil
}

// quiverHome resolves the QUIVER_HOME override or falls back to ~/.quiver.
// Duplicated from config.Dir() to keep this package import-light in tests.
func quiverHome() string {
	if env := os.Getenv("QUIVER_HOME"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".quiver"
	}
	return filepath.Join(home, ".quiver")
}
