package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/astra-sh/qvr/internal/canonical"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/registry"
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
	// hash must match the recorded LockEntry.SubtreeHash. Drift or
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

// Installer orchestrates materialization + symlinks + lock file. Consume
// installs are materialized worktree-free directly from bare git objects (see
// internal/skill/materialize.go); the Worktree manager is retained only for the
// edit/publish branch flows and for removing legacy worktree dirs.
type Installer struct {
	Registry *registry.Manager
	Worktree git.WorktreeManager
	Git      git.GitClient
	// Blob is the optional reflink/content-store seam for materialization
	// (#205). nil means plain stream copy.
	Blob BlobMaterializer

	// resolveMu guards the command-scoped resolution memos below. An Installer
	// is created fresh per command (see cmd/*.go), so these caches live exactly
	// one invocation — during which a bare repo's HEAD/refs are stable, making
	// (repoPath, ref) → SHA an invariant safe to memoize. The memos collapse the
	// dominant `qvr add --all` cost: N skills from one registry at one ref all
	// resolve to the SAME commit, but resolveInstall would otherwise re-open the
	// bare repo (gogit.PlainOpen) and re-resolve it 2×N times (#whole-repo perf).
	resolveMu sync.Mutex
	refSHA    map[string]string       // key: repoPath\x00ref → resolved SHA
	planMemo  map[string]*installPlan // key: planMemoKey(req) → resolved plan (non-frozen)
}

// NewInstaller wires default dependencies, including the reflink-backed blob
// store so materialization is O(metadata) where the filesystem supports
// copy-on-write and a plain copy elsewhere (#205).
func NewInstaller(reg *registry.Manager, wt git.WorktreeManager, gc git.GitClient) *Installer {
	return &Installer{Registry: reg, Worktree: wt, Git: gc, Blob: NewCachedBlobMaterializer()}
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
//
// Install is the single-skill entry point: it reads the project lock, runs the
// install against it, and writes the lock once. Batch callers (e.g. `qvr add
// --all`) should instead read the lock once and call InstallInto per skill,
// writing the shared lock a single time — that turns N full lock read+write
// cycles into one, the dominant warm-cache cost on multi-skill installs.
func (in *Installer) Install(req InstallRequest) (*InstallResult, error) {
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	result, err := in.InstallInto(req, lock)
	if err != nil {
		return nil, err
	}
	if err := lock.Write(); err != nil {
		return nil, fmt.Errorf("write lock file: %w", err)
	}
	return result, nil
}

// InstallInto runs the full install flow against an already-loaded, in-memory
// lock instead of reading and writing the lockfile itself. The caller owns the
// lock's lifecycle: it must hold the project file lock (model.WithLock), pass a
// lock loaded via model.ReadLockFile, and call lock.Write() once after all
// installs in the batch complete. Mutations land via lock.Put on the shared
// in-memory map, so later skills in the same batch observe earlier ones (the
// same-commit scan/provenance preservation and alias handling rely on this).
// On any error the lock is left unwritten; the caller decides whether to write
// the partial batch or discard it.
func (in *Installer) InstallInto(req InstallRequest, lock *model.LockFile) (*InstallResult, error) {
	plan, err := in.resolveInstallMemo(&req)
	if err != nil {
		return nil, err
	}
	name := plan.name
	localName := plan.localName
	version := plan.version
	loc := plan.loc
	resolvedSHA := plan.resolvedSHA
	finalPath := plan.finalPath
	ambiguityWarning := plan.ambiguityWarning

	materializedCommit, err := in.materializeIfNeeded(plan)
	if err != nil {
		return nil, err
	}

	skillDir := filepath.Join(finalPath, loc.Entry.Path)
	if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err != nil {
		return nil, fmt.Errorf("skill path missing after checkout: %w", err)
	}

	// Optional git-native provenance (the v1 trust surface) plus the skill's
	// author. Verify a signed tag/commit if one is present. An invalid
	// present-but-bad signature blocks the install before any symlink or lock
	// side effects, because it signals tampering. When policy requires
	// signatures, absent or unverifiable signatures also block. Scoped to the
	// skill's own subtree (loc.Entry.Path) so signature status reflects the
	// commit that wrote the skill, not the branch tip (#173).
	//
	// Both are memoized in the global ~/.quiver cache (keyed by ref+commit+path)
	// since computing them spawns several `git` processes whose results are
	// invariant for an immutable commit — the dominant warm-cache install cost.
	// Security-gated installs recompute fresh: require_signed needs a current
	// verified status, and a skill that declares signed_by needs a current
	// signer. The author and the "invalid signature" tampering signal are
	// content-determined and stable, so they're safe to serve from cache.
	provenance, commitAuthor, err := in.gateInstallTrust(req, plan, skillDir)
	if err != nil {
		return nil, err
	}

	// Freeze the verified bytes: write-protect the installed skill subtree so
	// what an agent reads through the symlink stays identical to what was
	// scanned and verified. This is a shared consume install — modifying it
	// requires `qvr edit`, which ejects a writable project-local copy. The
	// shared worktree is keyed by SHA and shared across projects; freezing it
	// also prevents one project's edit from silently mutating another's.
	//
	// Skip the recursive chmod walk when the subtree is already frozen — a
	// content dir shared with (or reused from) a prior install is read-only, and
	// re-walking it on every warm reuse is pure overhead on a multi-skill add. A
	// single stat of the just-verified SKILL.md stands in for the whole tree;
	// any partial-thaw edge case is caught by doctor/verify (freezing is
	// hardening, not load-bearing — see immutable.go).
	if !subtreeFrozen(skillDir) {
		setSubtreeReadOnly(skillDir)
	}

	if err := in.linkInstallTargets(req, plan, skillDir); err != nil {
		return nil, err
	}

	commit := in.resolveInstallCommit(materializedCommit, resolvedSHA, version, finalPath)

	targets, entry, err := in.buildLockEntry(req, plan, lock, commit, commitAuthor, provenance)
	if err != nil {
		return nil, err
	}
	lock.Put(entry)

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

// resolveInstallCommit picks the commit SHA to record. The materialized commit
// is the SHA the materializer resolved — no HEAD to read on a worktree-free dir.
// On the reuse path (finalPath already on disk) it falls back to resolvedSHA,
// and for a reused legacy worktree whose resolvedSHA degraded to a ref label it
// reads the dir HEAD.
func (in *Installer) resolveInstallCommit(materializedCommit, resolvedSHA, version, finalPath string) string {
	commit := materializedCommit
	if commit == "" {
		commit = resolvedSHA
		if HasGitDir(finalPath) && (commit == "" || commit == version) {
			if c, cerr := in.resolveCommit(finalPath); cerr == nil && c != "" {
				commit = c
			}
		}
	}
	return commit
}

// materializeIfNeeded reuses an existing content dir at plan.finalPath or
// materializes the skill subtree DIRECTLY from the bare repo's git objects (no
// `git worktree`, no git subprocess) into the staging dir and atomically renames
// it into place. Returns the materialized commit SHA ("" on the reuse path).
//
// When finalPath already exists it is reused — this makes `qvr install`
// idempotent across multiple agent targets (install once, add cursor target,
// rerun install), and lets two projects on the same SHA share one materialized
// dir. Both legacy `.git`-bearing worktrees and new worktree-free content dirs
// are valid here; downstream identity is computed from the bare repo at the
// commit, not from this dir.
func (in *Installer) materializeIfNeeded(plan *installPlan) (string, error) {
	loc := plan.loc
	finalPath := plan.finalPath
	stagingPath := plan.stagingPath
	if _, err := os.Stat(finalPath); err == nil {
		return "", nil
	}
	// Materialize the skill subtree DIRECTLY from the bare repo's git
	// objects — no `git worktree`, no git subprocess. The bytes + modes
	// written are chosen so the disk hash agrees with the bare-commit hash
	// (see internal/skill/materialize.go). Scope mirrors the prior sparse
	// patterns: a root skill coexisting with siblings gets SKILL.md +
	// recognized content dirs; a non-root skill its subtree; a lone root
	// the whole repo.
	//
	// Clear any stale staging dir from a prior crash before writing. This
	// lives here (not in resolveInstall) so resolveInstall stays a pure
	// function safe to memoize across the pre-pass and the serial loop.
	_ = os.RemoveAll(stagingPath)
	mat := &Materializer{Blob: in.Blob}
	sha, err := mat.MaterializeSubtree(loc.RepoPath, plan.resolvedSHA, loc.Entry.Path, plan.rootCoexists, stagingPath)
	if err != nil {
		_ = os.RemoveAll(stagingPath)
		// The skill's subtree isn't present at this commit — typically it
		// was added to the repo after that commit. Surface the actionable
		// "pick a ref where the skill exists" message (#178).
		if errors.Is(err, ErrSubtreeAbsent) {
			return "", fmt.Errorf("%w: skill %q (%s) does not exist at %s (%s) — the skill was likely added to the repo after that commit; run `qvr version list %s` to find a ref where it exists",
				ErrSkillAbsentAtRef, plan.name, loc.Entry.Path, plan.version, registry.ShortSHA(plan.resolvedSHA), plan.name)
		}
		// A latest-only registry (default-branch clone) has no tags or other
		// branches, so an explicitly-pinned version simply isn't on disk.
		// Point the user at --full instead of dumping a raw error. Only when
		// the user actually pinned a version and the registry isn't a full
		// clone — a missing ref in a full clone is a genuine "no such ref".
		if plan.explicitVersion && !git.IsFullClone(loc.RepoPath) {
			return "", fmt.Errorf("%w: %q not found in registry %q — it was cloned latest-only (default branch). Re-add with all versions: `qvr registry add %s --full`, then retry",
				ErrVersionNotAvailable, plan.version, loc.RegistryName, loc.RegistryURL)
		}
		return "", fmt.Errorf("materialize skill: %w", err)
	}
	// The subtree materialized, but if the skill's own SKILL.md isn't in the
	// written tree the skill simply doesn't exist at this commit. Catch that
	// with a user-facing message rather than a raw `stat skill dir` failure
	// over the internal `.staging` path (#178).
	if _, statErr := os.Stat(filepath.Join(stagingPath, loc.Entry.Path, "SKILL.md")); errors.Is(statErr, os.ErrNotExist) {
		_ = os.RemoveAll(stagingPath)
		return "", fmt.Errorf("%w: skill %q (%s) does not exist at %s (%s) — the skill was likely added to the repo after that commit; run `qvr version list %s` to find a ref where it exists",
			ErrSkillAbsentAtRef, plan.name, loc.Entry.Path, plan.version, registry.ShortSHA(plan.resolvedSHA), plan.name)
	}
	if err := lintStagedSkill(stagingPath, loc.Entry.Path); err != nil {
		_ = os.RemoveAll(stagingPath)
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		_ = os.RemoveAll(stagingPath)
		return "", fmt.Errorf("create worktrees dir: %w", err)
	}
	if err := os.Rename(stagingPath, finalPath); err != nil {
		// Race: another process may have created finalPath between our
		// initial Stat and the Rename. If finalPath now exists, drop our
		// staged copy and reuse the winning one.
		if _, statErr := os.Stat(finalPath); statErr == nil {
			_ = os.RemoveAll(stagingPath)
		} else {
			_ = os.RemoveAll(stagingPath)
			return "", fmt.Errorf("finalize worktree: %w", err)
		}
	}
	return sha, nil
}

// gateInstallTrust resolves git-native provenance plus the commit author for the
// skill at skillDir and enforces the v1 trust surface: an invalid signature, a
// require_signed policy with no verified signature, a signed_by declaration whose
// verified signer mismatches, and an untrusted commit author each block the
// install before any symlink or lock side effects. Returns the provenance record
// (may be nil) and the commit author on success.
func (in *Installer) gateInstallTrust(req InstallRequest, plan *installPlan, skillDir string) (*model.ProvenanceRef, string, error) {
	loc := plan.loc
	name := plan.name
	version := plan.version
	resolvedSHA := plan.resolvedSHA
	signedBy := DeclaredSignedBy(skillDir)
	freshProvenance := req.RequireSigned || signedBy != ""
	provenance, commitAuthor := in.resolveProvenanceAndAuthor(loc.RepoPath, version, resolvedSHA, loc.Entry.Path, freshProvenance)
	if provenance != nil && provenance.SignatureStatus == model.SignatureStatusInvalid {
		return nil, "", fmt.Errorf("%w: %s@%s carries an invalid git signature on %q — refusing to install",
			ErrInvalidSignature, name, registry.ShortSHA(resolvedSHA), version)
	}
	if req.RequireSigned && (provenance == nil || provenance.SignatureStatus != model.SignatureStatusVerified) {
		return nil, "", fmt.Errorf("%w: %s@%s is unsigned; disable security.require_signed or install a signed tag/commit",
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
	if signedBy != "" &&
		provenance != nil && provenance.SignatureStatus == model.SignatureStatusVerified &&
		!AuthorMatchesPin(provenance.Signer, signedBy) {
		return nil, "", fmt.Errorf("%w: %s@%s declares signed_by %q but its verified signature is by %q",
			ErrSignedByMismatch, name, registry.ShortSHA(resolvedSHA), signedBy, provenance.Signer)
	}
	trustedAuthors := req.TrustedAuthors
	if len(trustedAuthors) == 0 && req.TrustedAuthorsByRegistry != nil {
		trustedAuthors = req.TrustedAuthorsByRegistry[loc.RegistryName]
	}
	if len(trustedAuthors) > 0 && !AuthorAllowed(commitAuthor, trustedAuthors) {
		return nil, "", fmt.Errorf("untrusted commit author: %s@%s was authored by %q, not one of %s",
			name, registry.ShortSHA(resolvedSHA), commitAuthor, strings.Join(trustedAuthors, ", "))
	}
	return provenance, commitAuthor, nil
}

// linkInstallTargets resolves the agent-facing link target (a clean subtree, or
// a sanitized agent view for a legacy root-layout worktree with a live .git/ —
// issue #154) and creates a symlink for every requested target, rolling back any
// links created so far if one fails.
func (in *Installer) linkInstallTargets(req InstallRequest, plan *installPlan, skillDir string) error {
	loc := plan.loc
	finalPath := plan.finalPath
	localName := plan.localName

	// The agent-facing symlink points at the skill content. A worktree-free
	// content dir has no .git/, so a root-layout skill can be linked directly —
	// the dir is already clean. Only a LEGACY worktree dir (still on disk from a
	// prior qvr version) carries a live .git/ at its root, so for those we keep
	// exposing the sanitized view (issue #154). Subdir skills always point at a
	// clean subtree.
	linkTarget := skillDir
	if IsRootLayoutPath(loc.Entry.Path) && HasGitDir(finalPath) {
		view, verr := buildAgentViewAt(finalPath)
		if verr != nil {
			return fmt.Errorf("agent view: %w", verr)
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
			return err
		}
		if err := CreateSymlink(linkPath, linkTarget); err != nil {
			rollbackLinks(created)
			return fmt.Errorf("symlink %s: %w", t, err)
		}
		created = append(created, linkPath)
	}
	return nil
}

// buildLockEntry assembles the in-memory lock entry for the install: it merges
// targets with any existing entry (preserving prior scan/provenance signals on
// a same-commit no-op — issue #77), resolves the subtree identity, folds in
// the freshly-computed provenance, and enforces the --frozen drift check.
// Returns the merged targets and the entry to Put.
func (in *Installer) buildLockEntry(req InstallRequest, plan *installPlan, lock *model.LockFile, commit, commitAuthor string, provenance *model.ProvenanceRef) ([]string, *model.LockEntry, error) {
	loc := plan.loc
	localName := plan.localName
	version := plan.version
	rootCoexists := plan.rootCoexists
	frozenRef := plan.frozenRef
	name := plan.name

	// Update the in-memory lock last — if a later step fails, everything else is
	// still usable and a subsequent install will reconcile state. The caller
	// persists the lock once after the batch (see InstallInto's contract).
	targets := req.Targets
	var priorScan *model.ScanRef
	var priorProvenance *model.ProvenanceRef
	if existing, err := lock.Get(localName); err == nil {
		targets = mergeTargets(existing.Targets, req.Targets)
		// Preserve the prior scan/provenance signals when the install is a
		// no-op (same commit). Without this, a re-add wipes the scan
		// attestation before the gate rewrites it — even when the new SHA
		// matches, the intermediate "no signal" lockfile state churns
		// concurrent readers, and any code path that doesn't re-run the
		// gate would permanently lose the prior signal. Issue #77.
		if existing.Commit == commit {
			priorScan = existing.Scan
			priorProvenance = existing.Provenance
		}
	}
	// Resolve the subtree identity. The canonical hash of an immutable git
	// (commit, subtree, scope) is globally invariant, so consult the global
	// ~/.quiver identity cache first: a fresh project installing a skill at an
	// already-materialized commit then reuses the recorded hash instead of
	// re-walking and re-SHA-256ing every blob from the bare repo — the cost that
	// dominated the warm-cache (hot) install path. A miss computes from the bare
	// repo and populates the cache. --frozen always recomputes: its drift gate
	// below must compare a freshly computed hash against the lock to catch an
	// upstream force-push, so it must not trust a memo.
	// Hashing from the bare repo — not the materialized dir — works whether
	// finalPath is a worktree-free content dir or a legacy worktree, and is
	// byte-identical to the disk hash that `qvr lock verify` recomputes. The
	// batch pre-pass warms this same cache concurrently, so on a multi-skill add
	// this is usually a hit. --frozen recomputes fresh (useCache=false): its
	// drift gate below must compare against the lock to catch an upstream
	// force-push, so it must not trust a memo. A hashing failure leaves
	// SubtreeHash/TreeOID empty — the install still lands; doctor/verify flags
	// the missing seal.
	subtreeHash, treeOID := subtreeIdentity(loc.RepoPath, commit, loc.Entry.Path, rootCoexists, !req.Frozen)

	entry := &model.LockEntry{
		Name:          localName,
		Registry:      loc.RegistryName,
		Source:        loc.RegistryURL,
		Path:          loc.Entry.Path,
		RootCoexists:  rootCoexists,
		Ref:           version,
		Commit:        commit,
		InstallCommit: commit,
		SubtreeHash:   subtreeHash,
		TreeOID:       treeOID,
		Targets:       targets,
		Scan:          priorScan,
		Provenance:    mergeProvenance(priorProvenance, provenance, commitAuthor),
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
			return nil, nil, fmt.Errorf("--frozen: subtree hash drift for %s (expected %s, got %s)",
				localName, frozenRef.SubtreeHash, entry.SubtreeHash)
		}
	}

	return targets, entry, nil
}

// mergeProvenance folds the freshly-computed signature check and commit
// author into the provenance carried over from a same-commit no-op re-add.
// Fresh signals win; lineage markers (upstream, forkedFrom) survive from the
// prior block. Returns nil when there is nothing to record so the block
// stays omitted from the lock.
func mergeProvenance(prior, fresh *model.ProvenanceRef, commitAuthor string) *model.ProvenanceRef {
	var merged model.ProvenanceRef
	if prior != nil {
		merged = *prior
	}
	if fresh != nil {
		merged.Tag = fresh.Tag
		merged.SignatureStatus = fresh.SignatureStatus
		merged.Signer = fresh.Signer
	}
	merged.CommitAuthor = commitAuthor
	if merged.IsEmpty() {
		return nil
	}
	return &merged
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

	return in.resolveAmbiguousByRef(name, version, locs)
}

// resolveAmbiguousByRef disambiguates a multi-registry name pinned to a version
// by collecting every registry whose bare clone contains the requested ref
// rather than short-circuiting on the first match. Mirrors the bare-name
// ambiguity shape: 0 → ErrAmbiguousRef with per-registry version summaries,
// 1 → silent pick, 2+ → warn + alphabetical pick. Before this, `qvr add
// skill@v1` with two registries both holding v1 silently picked alphabetical
// with no warning (issue #106).
func (in *Installer) resolveAmbiguousByRef(name, version string, locs []*registry.SkillLocation) (*registry.SkillLocation, string, error) {
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
	if err := in.RemoveFrom(name, req, lock); err != nil {
		return err
	}
	if err := lock.Write(); err != nil {
		return fmt.Errorf("remove %s: write lock: %w", name, err)
	}
	return nil
}

// RemoveFrom is Remove against an already-loaded, in-memory lock. The caller
// owns the lock's lifecycle (hold model.WithLock, write once). It performs the
// same filesystem-first teardown and drops the in-memory lock entry without
// persisting — used by the batch add path to roll back a scan-blocked install
// without an extra lock read+write cycle.
func (in *Installer) RemoveFrom(name string, req InstallRequest, lock *model.LockFile) error {
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
	if err := removeTargetLinks(name, entry, req, entryGlobal, canonicalEditAbs); err != nil {
		return err
	}

	// Pass 2: drop the shared worktree for non-edit, non-link entries.
	if err := in.removeSharedWorktree(name, entry, req, lock); err != nil {
		return err
	}

	// Only now drop the lock entry. Symmetric with Install, which mutates
	// the lock last. The caller persists it.
	if err := lock.Remove(name); err != nil && !errors.Is(err, model.ErrLockSkillMissing) {
		return fmt.Errorf("remove %s: drop lock entry: %w", name, err)
	}
	return nil
}

// removeTargetLinks drops every target symlink for the entry, and for a
// mode:edit canonical target rm -rf's the eject dir (siblings are symlinks
// pointing at canonical and use RemoveSymlink). Bails on the first failure so
// the caller can leave the lock untouched for recovery.
func removeTargetLinks(name string, entry *model.LockEntry, req InstallRequest, entryGlobal bool, canonicalEditAbs string) error {
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
	return nil
}

// removeSharedWorktree drops the shared worktree for non-edit, non-link entries —
// but ONLY if no one else still references it. Worktrees are global and
// content-keyed (`<registry>/<skill>/<sha>`), so the same skill@sha installed in
// another project (or twice in this one via `--as`) shares one on-disk worktree.
// Deleting it on a single project's `qvr remove` / `lock --from-toml` teardown
// would break every sibling that still points at it (data loss #232). Mode:edit
// entries never had a shared worktree to clean; link installs point at
// user-owned dirs we don't touch.
func (in *Installer) removeSharedWorktree(name string, entry *model.LockEntry, req InstallRequest, lock *model.LockFile) error {
	if entry.IsLink() || entry.IsEdit() {
		return nil
	}
	worktreePath := EntryWorktreePath(entry)
	if worktreePath != "" && !worktreeStillReferenced(lock, name, worktreePath, req) {
		if err := in.Worktree.Remove(worktreePath); err != nil && !errors.Is(err, git.ErrWorktreeNotFound) {
			return fmt.Errorf("remove %s: drop worktree: %w", name, err)
		}
	}
	return nil
}

// worktreeStillReferenced reports whether the SHA-keyed worktree at worktreePath
// is still needed by anyone other than the entry being removed — a sibling entry
// in the SAME (in-memory) lock (e.g. an `--as` alias of the same skill@sha), or
// any OTHER live project / the global lock (via projects.json). When true, the
// teardown must keep the worktree and drop only this project's symlinks + lock
// entry (data loss #232).
func worktreeStillReferenced(lock *model.LockFile, removingName, worktreePath string, req InstallRequest) bool {
	// Siblings in the current lock that resolve to the same worktree.
	for _, e := range lock.Entries() {
		if e.Name == removingName {
			continue
		}
		if EntryWorktreePath(e) == worktreePath {
			return true
		}
	}
	// Other projects / the global lock. Exclude the lock currently being mutated:
	// its in-memory state (checked above) is authoritative, and its on-disk copy
	// is stale until the caller writes it.
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	return registry.WorktreeReferencedExcept(worktreePath, lockPath)
}

// InstallLocal installs a skill from a local directory as an immutable copy.
// Unlike a registry install (materialized from git objects) it copies the
// folder verbatim into a hash-keyed worktree under the reserved LocalRegistry
// namespace, freezes it read-only, and symlinks agent dirs at the copy — so
// the bytes an agent reads are a stable snapshot, not the user's live folder.
// To get a mutable working copy, `qvr edit` ejects it like any other install.
// This powers `qvr add --local <path>` for local skill development.
func (in *Installer) InstallLocal(localPath string, req InstallRequest) (*InstallResult, error) {
	for _, t := range req.Targets {
		if _, ok := model.LookupTarget(t); !ok {
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
	// Spec lint (agentskills.io conformance, e.g. name↔directory match) is
	// advisory and does not block a local install — surface it via `qvr lint`
	// or `qvr scan`. A skill that fails to load was already rejected above.
	name := loaded.Frontmatter.Name
	if name == "" {
		name = loaded.Name
	}

	// Content-address the copy by its subtree hash so the same folder maps to a
	// stable worktree dir (idempotent re-adds) and an edited folder lands in a
	// fresh one. A hashing failure is fatal here — the hash keys the worktree
	// path, so we can't proceed without it.
	hash, err := canonical.HashSubtreeFromDisk(abs)
	if err != nil {
		return nil, fmt.Errorf("hash local skill: %w", err)
	}
	// Key the worktree off the hex digest, not the full "sha256:<hex>" string:
	// ShortSHA truncates to 7 chars, so the "sha256:" prefix would collapse
	// every version to the same dir. The hex key keeps each distinct content
	// (and each edit of the source folder) in its own content-addressed dir.
	// It also feeds WorktreePathForEntry via Commit/InstallCommit, so the
	// symlink, reconciler, and edit paths all resolve to the same place.
	commitKey := strings.TrimPrefix(hash, "sha256:")
	finalPath := registry.WorktreePath(model.LocalRegistry, name, registry.ShortSHA(commitKey))

	// Conflict check: refuse to silently replace an existing entry of the same
	// name pointing at a different source. Idempotent when the source matches;
	// --force needed to re-point. Mirrors the remote-install conflict rule.
	lockPath := req.LockPath
	if lockPath == "" {
		lockPath = model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
	}
	lock, err := model.ReadLockFile(lockPath)
	if err != nil {
		return nil, fmt.Errorf("read lock file: %w", err)
	}
	if err := checkLocalConflict(lock, name, abs, req.Force); err != nil {
		return nil, err
	}

	if err := materializeLocalCopy(abs, finalPath); err != nil {
		return nil, err
	}
	// Freeze: same immutability contract as a shared install (see immutable.go)
	// — files read-only, directories writable so `rm -rf`/`qvr cache clean` and
	// other ordinary teardown keep working. Tampering is caught by
	// `qvr lock verify` and healed by `qvr sync` (re-copy from Source).
	setSubtreeReadOnly(finalPath)

	var created []string
	for _, t := range req.Targets {
		linkPath, err := ResolveTargetPath(t, name, req.ProjectRoot, req.Global)
		if err != nil {
			rollbackLinks(created)
			return nil, err
		}
		if err := CreateSymlink(linkPath, finalPath); err != nil {
			rollbackLinks(created)
			return nil, fmt.Errorf("symlink %s: %w", t, err)
		}
		created = append(created, linkPath)
	}

	lock.Put(&model.LockEntry{
		Name:          name,
		Registry:      model.LocalRegistry,
		Source:        abs,
		Ref:           "local",
		Mode:          model.ModeLocal,
		Commit:        commitKey,
		InstallCommit: commitKey,
		SubtreeHash:   hash,
		Targets:       req.Targets,
		InstalledAt:   time.Now().UTC(),
	})
	if err := lock.Write(); err != nil {
		rollbackLinks(created)
		return nil, fmt.Errorf("write lock file: %w", err)
	}
	return &InstallResult{
		Name:     name,
		Registry: model.LocalRegistry,
		Version:  "local",
		Worktree: finalPath,
		Commit:   commitKey,
		Targets:  req.Targets,
	}, nil
}

// checkLocalConflict refuses to silently replace an existing entry of the same
// name pointing at a different source. Idempotent when the source matches;
// --force needed to re-point. Mirrors the remote-install conflict rule.
func checkLocalConflict(lock *model.LockFile, name, abs string, force bool) error {
	if existing, err := lock.Get(name); err == nil && !force {
		if !existing.IsLocal() || existing.Source != abs {
			sourceLabel := existing.Source
			if sourceLabel == "" {
				sourceLabel = "registry"
			}
			return fmt.Errorf("skill %q already installed from %s; pass --force to replace",
				name, sourceLabel)
		}
	}
	return nil
}

// materializeLocalCopy copies the local skill folder into the immutable worktree
// at finalPath if it isn't already on disk. It stages in a sibling dir and
// atomically renames so a crashed copy never leaves a half-written worktree
// behind. copyDir skips .git/ and preserves exec bits.
func materializeLocalCopy(abs, finalPath string) error {
	if _, statErr := os.Stat(finalPath); statErr == nil {
		return nil
	}
	stagingPath := fmt.Sprintf("%s.staging.%d", finalPath, os.Getpid())
	_ = os.RemoveAll(stagingPath)
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o755); err != nil {
		return fmt.Errorf("create worktrees dir: %w", err)
	}
	if err := copyDir(abs, stagingPath); err != nil {
		_ = os.RemoveAll(stagingPath)
		return fmt.Errorf("copy local skill: %w", err)
	}
	if err := os.Rename(stagingPath, finalPath); err != nil {
		// Lost the race to a concurrent install of the same content: the
		// winning dir is byte-identical (same hash), so drop our staged copy.
		if _, e := os.Stat(finalPath); e == nil {
			_ = os.RemoveAll(stagingPath)
		} else {
			_ = os.RemoveAll(stagingPath)
			return fmt.Errorf("finalize local worktree: %w", err)
		}
	}
	return nil
}

func (in *Installer) resolveCommit(worktreePath string) (string, error) {
	if in.Git == nil {
		return "", nil
	}
	return in.Git.HeadCommit(worktreePath)
}

// lintStagedSkill loads the skill at the expected path inside the staged
// worktree to confirm it parses before we link it in. Spec lint
// (agentskills.io conformance) is advisory — it is surfaced via `qvr scan` and
// `qvr lint`, never blocks the install here. Only a skill whose SKILL.md fails
// to load (unparseable frontmatter) is rejected, since that can't be linked or
// read at all.
func lintStagedSkill(stagingPath, skillRelPath string) error {
	skillDir := filepath.Join(stagingPath, skillRelPath)
	if _, err := LoadFromPath(skillDir); err != nil {
		return fmt.Errorf("load staged skill: %w", err)
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

// installPlan is the resolved, side-effect-light result of resolveInstall:
// everything needed to materialize and record an install, with no worktree
// created or lock written yet. Shared by Install and the parallel
// PrematerializeBatch pre-pass so resolution logic lives in exactly one place.
type installPlan struct {
	name             string // canonical registry-side skill name
	localName        string // lock key + symlink filename (the --as alias when set)
	version          string // resolved human ref label
	explicitVersion  bool
	loc              *registry.SkillLocation
	resolvedSHA      string
	finalPath        string
	stagingPath      string
	rootCoexists     bool
	frozenRef        *model.LockEntry
	ambiguityWarning string
}

// resolveRefCached resolves a ref to a full commit SHA against the bare repo,
// memoized for the Installer's (command) lifetime. The first call for a given
// (repoPath, ref) does the real gogit.PlainOpen + resolve; the rest — every
// other skill in a `qvr add --all` that shares the registry and ref — return the
// cached SHA without re-opening the repo. Only successes are cached; an error is
// returned uncached so a transient failure can be retried. Mirrors the contract
// of git.GitClient.ResolveRef (degraded callers fall back to the ref label).
func (in *Installer) resolveRefCached(repoPath, ref string) (string, error) {
	key := repoPath + "\x00" + ref
	in.resolveMu.Lock()
	if sha, ok := in.refSHA[key]; ok {
		in.resolveMu.Unlock()
		return sha, nil
	}
	in.resolveMu.Unlock()

	sha, err := in.Git.ResolveRef(repoPath, ref)
	if err != nil {
		return "", err
	}
	in.resolveMu.Lock()
	if in.refSHA == nil {
		in.refSHA = make(map[string]string)
	}
	in.refSHA[key] = sha
	in.resolveMu.Unlock()
	return sha, nil
}

// planMemoKey is the resolution-determining fingerprint of a request: every
// field resolveInstall reads to produce its installPlan. Frozen is excluded —
// frozen requests bypass the memo entirely (they read the lock as authoritative
// and mutate req). RequireSigned / trusted-author fields are NOT here because
// they gate the install AFTER resolution and don't change the plan.
func planMemoKey(req *InstallRequest) string {
	return strings.Join([]string{
		req.Skill, req.Registry, req.SkillPath, req.As,
		req.LockPath, req.ProjectRoot, req.PinCommit,
		boolFlag(req.Global), boolFlag(req.Force),
	}, "\x00")
}

func boolFlag(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// resolveInstallMemo is resolveInstall with a command-scoped result memo. In
// `qvr add --all` the same skill is resolved twice — once in the PrematerializeBatch
// pre-pass and once in the serial InstallInto loop — and resolveInstall re-opens
// the bare repo (gogit.PlainOpen for the lock-conflict check + ResolveRef) each
// time. The memo lets the serial loop reuse the pre-pass's plan, halving the
// resolution work. resolveInstall is a pure function of its inputs (the only
// stale-staging side effect was moved to the materialize site), so a cached plan
// is always a valid substitute. A plan that errored (e.g. a --force conflict in
// the pre-pass) is never cached, so the serial loop re-resolves and surfaces the
// real error. Frozen requests bypass the memo (resolution is lock-authoritative
// and mutates req).
func (in *Installer) resolveInstallMemo(req *InstallRequest) (*installPlan, error) {
	if req.Frozen {
		return in.resolveInstall(req)
	}
	key := planMemoKey(req)
	in.resolveMu.Lock()
	if p, ok := in.planMemo[key]; ok {
		in.resolveMu.Unlock()
		return p, nil
	}
	in.resolveMu.Unlock()

	plan, err := in.resolveInstall(req)
	if err != nil {
		return nil, err
	}
	in.resolveMu.Lock()
	if in.planMemo == nil {
		in.planMemo = make(map[string]*installPlan)
	}
	in.planMemo[key] = plan
	in.resolveMu.Unlock()
	return plan, nil
}

// resolveInstall performs the resolution phase of an install: parse the
// reference, apply --frozen/--as, locate the skill in a registry, resolve the
// ref to a commit SHA, and compute the SHA-keyed content dir and scope. It
// reads the lock (frozen peek, conflict check) but writes nothing and creates
// no worktree, so it is safe to call from both Install and the materialization
// pre-pass. It may mutate req in place (req.As / req.Registry) when a --frozen
// lock entry supplies them.
func (in *Installer) resolveInstall(req *InstallRequest) (*installPlan, error) {
	name, version, err := ParseReference(req.Skill)
	if err != nil {
		return nil, err
	}
	// Whether the user explicitly pinned a version (`skill@ref`). Drives the
	// "latest-only registry can't reach this version → use --full" diagnostic:
	// a missing ref is only "go fetch all versions" when the user asked for one.
	explicitVersion := version != ""
	if err := validateInstallTargets(req.Targets); err != nil {
		return nil, err
	}

	if req.Frozen {
		var ferr error
		if name, ferr = in.prefillFromFrozenLock(req, name); ferr != nil {
			return nil, ferr
		}
	}

	localName, err := resolveLocalName(req, name)
	if err != nil {
		return nil, err
	}

	loc, ambiguityWarning, err := in.resolveSkill(name, version, req.Registry, req.SkillPath)
	if err != nil {
		return nil, err
	}
	version = in.resolveInstallVersion(req, loc, version)

	// --frozen pins to the lockfile: the entry must exist and its recorded
	// Branch/SubtreeHash become the install target. Captured here so the
	// drift check at the end can re-read the same recorded values.
	var frozenRef *model.LockEntry
	if req.Frozen {
		var ferr error
		if frozenRef, version, ferr = in.pinToFrozenLock(req, localName, version); ferr != nil {
			return nil, ferr
		}
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
		if err := in.checkRefConflict(req, localName, version); err != nil {
			return nil, err
		}
	}

	// Resolve the ref → full SHA against the bare clone so the worktree path
	// is SHA-keyed, not ref-keyed. Two projects pinning the same commit then
	// share one worktree even when they wrote different ref labels (one pinned
	// "main", the other "abc123"). Falls back to a degraded path using the ref
	// label when resolution fails — the install still succeeds and the lock
	// entry's Worktree field is still self-consistent; only the cross-project
	// share-by-SHA optimization is lost.
	resolvedSHA, sherr := in.resolveRefCached(loc.RepoPath, version)
	if sherr != nil || resolvedSHA == "" {
		resolvedSHA = version
	}

	// Under PinCommit (qvr sync restore) materialize the lock's recorded commit
	// instead of today's ref tip, so a restore is reproducible even when the ref
	// advanced upstream. entry.Ref stays the human label either way.
	if req.PinCommit != "" {
		resolvedSHA = req.PinCommit
	}

	// Staging path → final path. Worktree creation can fail mid-way (e.g., bad
	// ref), and we don't want a half-populated directory masquerading as an
	// installed skill. Stage in a sibling dir and rename at the end.
	finalPath := registry.WorktreePath(loc.RegistryName, name, registry.ShortSHA(resolvedSHA))
	stagingPath := finalPath + ".staging"
	// NOTE: the stale-staging cleanup (os.RemoveAll) lives at the materialize
	// site in InstallInto, not here, so resolveInstall stays side-effect-free and
	// memoizable (resolveInstallMemo). resolveInstall must remain a pure function
	// of its inputs for the pre-pass and the serial loop to share one result.

	rootCoexists := in.resolveRootCoexists(req, loc, localName)
	return &installPlan{
		name:             name,
		localName:        localName,
		version:          version,
		explicitVersion:  explicitVersion,
		loc:              loc,
		resolvedSHA:      resolvedSHA,
		finalPath:        finalPath,
		stagingPath:      stagingPath,
		rootCoexists:     rootCoexists,
		frozenRef:        frozenRef,
		ambiguityWarning: ambiguityWarning,
	}, nil
}

// resolveLockPath returns req.LockPath, falling back to the scope-appropriate
// default lock path when the caller left it empty.
func resolveLockPath(req *InstallRequest) string {
	if req.LockPath != "" {
		return req.LockPath
	}
	return model.DefaultLockPath(req.ProjectRoot, quiverHome(), req.Global)
}

// resolveLocalName returns the lock key + symlink filename for the install:
// the canonical `name`, or the validated `--as` alias when set. Canonical
// `name` still drives registry lookup and the worktree path so aliases at the
// same SHA share one worktree.
func resolveLocalName(req *InstallRequest, name string) (string, error) {
	if req.As != "" {
		if !nameRegex.MatchString(req.As) || strings.Contains(req.As, "--") {
			return "", fmt.Errorf("invalid --as value %q: must be 1-64 chars, lowercase alphanumeric + hyphens, no leading/trailing or consecutive hyphens", req.As)
		}
		return req.As, nil
	}
	return name, nil
}

// resolveInstallVersion resolves the human ref label: an empty version becomes
// the skill's default ref; an explicit (non-frozen) version is mapped to its
// namespaced per-skill tag when the registry publishes versions that way (#152):
// `qvr add alpha@v0.1.0` transparently resolves to the tag `alpha/v0.1.0`.
// Falls through unchanged for branches, SHAs, and bare single-skill tags.
func (in *Installer) resolveInstallVersion(req *InstallRequest, loc *registry.SkillLocation, version string) string {
	if version == "" {
		return resolveDefaultRef(loc, in.Git)
	}
	if !req.Frozen {
		if eff, ok := resolveSkillRef(in.Git, loc.RepoPath, loc.Entry.Name, version); ok {
			version = eff
		}
	}
	return version
}

// validateInstallTargets requires at least one target and rejects any unknown one.
func validateInstallTargets(targets []string) error {
	if len(targets) == 0 {
		return fmt.Errorf("at least one --target is required")
	}
	for _, t := range targets {
		if _, ok := model.LookupTarget(t); !ok {
			return fmt.Errorf("%w: %s", ErrUnknownTarget, t)
		}
	}
	return nil
}

// prefillFromFrozenLock uses the lockfile (authoritative for a frozen install)
// to pre-fill request fields the user didn't supply, returning the possibly
// canonicalised lookup name. Two effects:
//  1. Alias support (#102): when the user runs `qvr add --frozen <alias>` and
//     the lock records <alias> as an alias entry (entry.Canonical != ""), swap
//     the registry lookup to the canonical name and preserve the alias via
//     req.As. Without this the lookup treats the alias as a registry skill name
//     and fails ErrSkillNotFound, even though the lock is self-describing.
//     RestoreAll already does this swap explicitly when iterating entries;
//     here we handle the caller-supplied-name path.
//  2. Registry scoping (#105): pre-fill req.Registry from entry.Registry so
//     resolveSkill is scoped to the source that was pinned at install time.
//     Without this the resolver walks every configured registry and may emit a
//     stale ambiguity warning even though the lockfile already chose.
func (in *Installer) prefillFromFrozenLock(req *InstallRequest, name string) (string, error) {
	lp := resolveLockPath(req)
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
		return name, fmt.Errorf("--frozen requires a readable lock file: %w", statErr)
	}
	existingLock, lerr := model.ReadLockFile(lp)
	if lerr != nil {
		return name, fmt.Errorf("--frozen requires a readable lock file: %w", lerr)
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
	return name, nil
}

// pinToFrozenLock pins the install to the lockfile: the entry must exist and its
// recorded Ref becomes the install version. Returns the captured frozen entry
// (so the drift check can re-read the same recorded values) and the version.
func (in *Installer) pinToFrozenLock(req *InstallRequest, localName, version string) (*model.LockEntry, string, error) {
	lp := resolveLockPath(req)
	existingLock, lerr := model.ReadLockFile(lp)
	if lerr != nil {
		return nil, version, fmt.Errorf("--frozen requires a readable lock file: %w", lerr)
	}
	existing, gerr := existingLock.Get(localName)
	if gerr != nil {
		return nil, version, fmt.Errorf("--frozen: skill %q not present in lock file", localName)
	}
	if existing.Ref != "" {
		version = existing.Ref
	}
	return existing, version, nil
}

// checkRefConflict refuses an install that would silently swap the lock entry to
// a different ref — that contradicts the "switching refs is a symlink repoint,
// not a re-install" contract. Idempotent when the existing ref matches. Issue
// #111: the hint leads with remove+add (always correct), keeps --force as the
// in-place overwrite, and qualifies `qvr switch` as same-source-only. Uses
// localName so `--as <alias>` installs only conflict with prior installs of the
// same alias, not the canonical name — the whole point of --as is coexistence.
func (in *Installer) checkRefConflict(req *InstallRequest, localName, version string) error {
	lp := resolveLockPath(req)
	if existingLock, lerr := model.ReadLockFile(lp); lerr == nil {
		if existing, gerr := existingLock.Get(localName); gerr == nil && existing.Ref != "" && existing.Ref != version {
			return fmt.Errorf("%s already installed at %s (from %s); pass --force to overwrite, or `qvr remove %s --force && qvr add %s@%s` to reinstall (if the source is changing too — `qvr switch` only moves the ref within the same source)",
				localName, existing.Ref, existing.Source, localName, localName, version)
		}
	}
	return nil
}

// resolveRootCoexists decides the content scope. Normally taken from the
// freshly-resolved index entry, but a reproducible restore (`qvr sync` via
// PinCommit) materializes the LOCKED commit while the index reflects HEAD — so
// honor the scope recorded in the lock at original install time, which can't
// drift if upstream later changed the sibling layout. See model.SkillScopePaths.
func (in *Installer) resolveRootCoexists(req *InstallRequest, loc *registry.SkillLocation, localName string) bool {
	rootCoexists := loc.Entry.RootCoexists
	if req.PinCommit != "" {
		lp := resolveLockPath(req)
		if el, lerr := model.ReadLockFile(lp); lerr == nil {
			if prev, gerr := el.Get(localName); gerr == nil {
				rootCoexists = prev.RootCoexists
			}
		}
	}
	return rootCoexists
}
