package registry

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/quiver-cli/qvr/internal/config"
	"github.com/quiver-cli/qvr/internal/git"
	"github.com/quiver-cli/qvr/internal/model"
)

const defaultBranchFallback = "main"

// DefaultCloneDepth is the history depth `qvr registry add` uses by default — a
// shallow depth-1 clone of just the default branch's latest snapshot, the
// cold-start fast path.
const DefaultCloneDepth = 1

// AddOptions tunes a single registry Add.
type AddOptions struct {
	// Depth bounds clone history: 0 = full history, N>0 = shallow to N commits.
	// Ignored when Full is set (a full clone always takes complete history).
	Depth int

	// Full fetches every branch and tag (so any version can be installed). When
	// false (default), only the remote's default branch is cloned — fast, but a
	// specific tag/older version requires re-cloning with Full.
	Full bool

	// SkipIndex registers the clone WITHOUT building the full skill index. The
	// one-step `qvr add <blob-url>` fast path sets this: the URL pins an exact
	// skill directory, so the install resolves that one SKILL.md directly
	// (FindSkillAtPath) and never needs every SKILL.md in a large registry
	// parsed up front. The index is still built lazily on the next
	// list/search/status. The returned Registry's SkillCount is -1 (not
	// counted) in this mode.
	SkipIndex bool
}

var (
	ErrRegistryNotFound    = errors.New("registry not found")
	ErrRegistryExists      = errors.New("registry already exists")
	ErrInvalidRegistryName = errors.New("invalid registry name")
	ErrInvalidURL          = errors.New("invalid registry URL")
	ErrRemoveFailed        = errors.New("registry removal failed")
	ErrUpdateFailed        = errors.New("registry update failed")
)

// Manager handles registry lifecycle operations.
type Manager struct {
	Git      git.GitClient
	Indexer  *Indexer
	CacheTTL time.Duration
}

// NewManager creates a new Manager with default cache TTL.
func NewManager(gitClient git.GitClient) *Manager {
	return &Manager{
		Git:      gitClient,
		Indexer:  NewIndexer(gitClient),
		CacheTTL: DefaultCacheTTL,
	}
}

// Index returns the skill index for a registry, using the cache when valid.
// Falls through to a fresh build when the cache is missing, stale, or the
// cached commit no longer matches HEAD. A failed cache write is non-fatal —
// we still return the freshly built index so a read-only filesystem doesn't
// break reads. The skipped slice lists candidate skills the indexer could not
// register (missing SKILL.md, parse errors); callers can ignore it.
func (m *Manager) Index(name, repoPath string) ([]SkillIndexEntry, []SkippedSkill, error) {
	return m.IndexWithOptions(name, repoPath, IndexOptions{})
}

// IndexOptions tunes a single call to IndexWithOptions.
type IndexOptions struct {
	// Refresh bypasses the on-disk cache read so the indexer always rebuilds
	// from the bare clone. The fresh result is still written back to the cache
	// so subsequent reads pick it up. Used by `qvr search --refresh` and
	// friends to force a rebuild without going to the network.
	Refresh bool
}

// IndexWithOptions is the variant of Index that accepts caller-supplied
// behaviour overrides. Callers that don't need any overrides should use
// Index — it preserves the original signature for the common path.
func (m *Manager) IndexWithOptions(name, repoPath string, opts IndexOptions) ([]SkillIndexEntry, []SkippedSkill, error) {
	headCommit, _ := m.Git.HeadCommit(repoPath)

	if !opts.Refresh {
		if cached, err := ReadCache(name); err == nil {
			if cached.Commit == headCommit && headCommit != "" && !cached.IsStale(m.CacheTTL) {
				return cached.Skills, cached.Skipped, nil
			}
		}
	}

	skills, skipped, err := m.Indexer.BuildIndex(repoPath)
	if err != nil {
		return nil, skipped, err
	}

	_ = WriteCache(&IndexCache{
		Registry:  name,
		Commit:    headCommit,
		Generated: time.Now().UTC(),
		Skills:    skills,
		Skipped:   skipped,
	})
	return skills, skipped, nil
}

// Add clones a registry as a bare repo and saves it to config, using the
// default (shallow) clone depth. See AddWithOptions for depth control.
//
// Any embedded credentials in `url` are stripped before the URL is used for
// cloning or persisted to config. The clone itself relies on the user's
// credential helper / SSH agent for auth — we never store tokens on disk.
func (m *Manager) Add(ctx context.Context, name, url string) (*model.Registry, error) {
	return m.AddWithOptions(ctx, name, url, AddOptions{Depth: DefaultCloneDepth, Full: false})
}

// AddWithOptions is Add with caller-supplied clone behaviour (e.g. history
// depth). Add delegates here with the shallow-by-default depth.
func (m *Manager) AddWithOptions(ctx context.Context, name, url string, opts AddOptions) (*model.Registry, error) {
	if err := ValidateRegistryName(name); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidRegistryName, err)
	}
	if url == "" {
		return nil, fmt.Errorf("%w: URL cannot be empty", ErrInvalidURL)
	}

	cleanURL, hadCreds, err := git.SanitizeURL(url)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidURL, err)
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if _, exists := cfg.Registries[name]; exists {
		return nil, fmt.Errorf("%w: %s", ErrRegistryExists, name)
	}

	repoPath := RegistryPath(name)
	// `git clone --mirror` doesn't auto-create the parent directory. With the
	// v0.5 `<org>/<repo>` shape, the parent is the org directory which may
	// not exist yet on first add for that org.
	if err := os.MkdirAll(filepath.Dir(repoPath), 0o755); err != nil {
		return nil, fmt.Errorf("create registry parent dir: %w", err)
	}
	depth := opts.Depth
	if opts.Full {
		depth = 0 // a full clone always takes complete history
	}
	if err := m.Git.BareClone(ctx, cleanURL, repoPath, git.CloneOptions{Depth: depth, AllRefs: opts.Full}); err != nil {
		return nil, fmt.Errorf("clone registry %s: %w", name, err)
	}

	// Build + populate the cache on first clone; non-fatal if it fails. Skipped
	// for the single-skill fast path (SkipIndex), which resolves one known
	// SKILL.md instead of parsing every skill in the repo.
	var skills []SkillIndexEntry
	var skipped []SkippedSkill
	var indexErr error
	if !opts.SkipIndex {
		skills, skipped, indexErr = m.Index(name, repoPath)
	}

	cfg.Registries[name] = config.RegistryConfig{URL: cleanURL}
	if err := config.Save(cfg); err != nil {
		return nil, fmt.Errorf("save config: %w", err)
	}

	defaultBranch, _ := m.Git.DefaultBranch(repoPath)

	reg := &model.Registry{
		Name:                name,
		URL:                 cleanURL,
		Path:                repoPath,
		SkillCount:          len(skills),
		SkippedCount:        len(skipped),
		LastFetched:         time.Now(),
		DefaultBranch:       defaultBranch,
		CredentialsStripped: hadCreds,
	}
	switch {
	case opts.SkipIndex:
		reg.SkillCount = -1 // not counted — index built lazily on first read
	case indexErr != nil:
		reg.SkillCount = 0
	}
	return reg, nil
}

// Deepen converts an existing latest-only (shallow, single-branch) registry
// clone into a full clone in place — all branches and tags, so any version
// becomes installable — without a remove + re-add. This backs the `--full`
// re-add path (#184): `qvr registry add <url> --full` on a registry that's
// already configured deepens it here instead of erroring. The bare clone
// directory and config entry are unchanged; only the on-disk refspec and
// fetched history grow. Idempotent: a clone that's already full is just
// fetched (an update), not rebuilt. Accepts a bare leaf name (e.g. `skills`
// for `acme/skills`).
func (m *Manager) Deepen(ctx context.Context, name string) (*model.Registry, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	name, err = ResolveName(cfg, name)
	if err != nil {
		return nil, err
	}
	regCfg := cfg.Registries[name]
	repoPath := RegistryPath(name)

	if err := m.Git.DeepenToFull(ctx, repoPath); err != nil {
		return nil, fmt.Errorf("deepen registry %s: %w", name, err)
	}

	// The deepen brought new branches/tags (and possibly skills that only
	// existed off the default branch) into view — rebuild the index so the
	// reported count and subsequent lookups reflect them.
	_ = Invalidate(name)
	skills, skipped, indexErr := m.Index(name, repoPath)
	defaultBranch, _ := m.Git.DefaultBranch(repoPath)

	reg := &model.Registry{
		Name:          name,
		URL:           regCfg.URL,
		Path:          repoPath,
		SkillCount:    len(skills),
		SkippedCount:  len(skipped),
		LastFetched:   time.Now(),
		DefaultBranch: defaultBranch,
	}
	if indexErr != nil {
		reg.SkillCount = 0
	}
	return reg, nil
}

// Remove deletes a registry: config entry first (recoverable), then bare clone.
func (m *Manager) Remove(name string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	// Accept a bare leaf (e.g. `skills` -> `acme/skills`). ResolveName
	// returns ErrRegistryNotFound for an unknown name (same sentinel callers
	// already check) or an ambiguity error when a leaf matches several.
	name, err = ResolveName(cfg, name)
	if err != nil {
		return err
	}

	// Remove from config first — re-adding is easy, recovering deleted files is not
	delete(cfg.Registries, name)
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	repoPath := RegistryPath(name)
	if err := os.RemoveAll(repoPath); err != nil {
		return fmt.Errorf("%w: remove clone: %v", ErrRemoveFailed, err)
	}

	// If this was the last registry under its org dir (v0.5 `<org>/<repo>`
	// shape), drop the empty parent so `registries/` doesn't accumulate
	// hollow org directories. os.Remove only succeeds on empty dirs, so
	// other registries under the same org keep the parent alive. Bounded
	// to a couple of levels in case future schemes nest deeper.
	parent := filepath.Dir(repoPath)
	registriesRoot := filepath.Join(config.Dir(), "registries")
	for parent != registriesRoot && parent != filepath.Dir(parent) {
		if err := os.Remove(parent); err != nil {
			break // not empty, or vanished — either way we're done
		}
		parent = filepath.Dir(parent)
	}

	// Drop the cache entry last — if config save succeeded, a stale cache
	// file is harmless (next Index call rebuilds) but we clean up anyway.
	_ = Invalidate(name)

	return nil
}

// List returns all configured registries with their status.
func (m *Manager) List() ([]model.RegistryStatus, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	var result []model.RegistryStatus
	for name, regCfg := range cfg.Registries {
		repoPath := RegistryPath(name)
		status := model.RegistryStatus{
			Registry: model.Registry{
				Name: name,
				URL:  regCfg.URL,
				Path: repoPath,
			},
		}

		// Get skill count via the cache-aware index.
		if skills, skipped, err := m.Index(name, repoPath); err == nil {
			status.SkillCount = len(skills)
			status.SkippedCount = len(skipped)
			status.Skipped = skipped
		}

		// Get last fetched time from FETCH_HEAD mtime
		fetchHeadPath := filepath.Join(repoPath, "FETCH_HEAD")
		if info, err := os.Stat(fetchHeadPath); err == nil {
			status.LastFetched = info.ModTime()
		}

		if branch, err := m.Git.DefaultBranch(repoPath); err == nil {
			status.DefaultBranch = branch
		}

		result = append(result, status)
	}

	// Sort by name so output is deterministic across runs. Go map iteration
	// is randomized; without this `qvr registry list` produces a different
	// order on every invocation, and scripts piping the output to `head`,
	// `awk`, or `diff` get nondeterministic answers (issue #76).
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

// Update fetches new refs for a registry (or all if name is empty).
func (m *Manager) Update(ctx context.Context, name string) ([]model.RegistryStatus, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	name, err = resolveForBatch(cfg, name)
	if err != nil {
		return nil, err
	}
	names := registryNames(cfg, name)

	var results []model.RegistryStatus
	for _, n := range names {
		regCfg, exists := cfg.Registries[n]
		if !exists {
			results = append(results, model.RegistryStatus{
				Registry: model.Registry{Name: n},
				Error:    fmt.Sprintf("registry %q not found", n),
			})
			continue
		}

		repoPath := RegistryPath(n)
		status := model.RegistryStatus{
			Registry: model.Registry{
				Name: n,
				URL:  regCfg.URL,
				Path: repoPath,
			},
		}

		if err := m.Git.Fetch(ctx, repoPath); err != nil {
			status.Error = fmt.Sprintf("fetch failed: %v", err)
			results = append(results, status)
			continue
		}

		// A fetch may have moved HEAD, so drop the cache and rebuild via
		// Index() — that re-populates the cache with the fresh commit hash.
		_ = Invalidate(n)
		if skills, skipped, err := m.Index(n, repoPath); err == nil {
			status.SkillCount = len(skills)
			status.SkippedCount = len(skipped)
			status.Skipped = skipped
		}

		status.LastFetched = time.Now()
		results = append(results, status)
	}

	return results, nil
}

// Check performs a dry-run check for upstream changes.
func (m *Manager) Check(ctx context.Context, name string) ([]model.RegistryStatus, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	name, err = resolveForBatch(cfg, name)
	if err != nil {
		return nil, err
	}
	names := registryNames(cfg, name)

	var results []model.RegistryStatus
	for _, n := range names {
		regCfg, exists := cfg.Registries[n]
		if !exists {
			continue
		}

		repoPath := RegistryPath(n)
		status := model.RegistryStatus{
			Registry: model.Registry{
				Name: n,
				URL:  regCfg.URL,
				Path: repoPath,
			},
		}

		localHead, err := m.Git.HeadCommit(repoPath)
		if err != nil {
			status.Error = fmt.Sprintf("read local HEAD: %v", err)
			results = append(results, status)
			continue
		}

		remoteRefs, err := m.Git.LsRemote(ctx, regCfg.URL)
		if err != nil {
			status.Error = fmt.Sprintf("ls-remote: %v", err)
			results = append(results, status)
			continue
		}

		defaultBranch, _ := m.Git.DefaultBranch(repoPath)
		if defaultBranch == "" {
			defaultBranch = defaultBranchFallback
		}
		remoteRef := "refs/heads/" + defaultBranch
		if remoteHash, ok := remoteRefs.Refs[remoteRef]; ok {
			status.HasUpstreamChanges = remoteHash != localHead
		}

		results = append(results, status)
	}

	return results, nil
}

// SearchWithFilter is the filter-aware variant used by `qvr search --tag` and
// `qvr search --author`. It walks every configured registry and applies the
// filter per entry, merging results by score then name.
func (m *Manager) SearchWithFilter(filter SearchFilter) ([]SearchResult, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	var all []SearchResult
	for name := range cfg.Registries {
		repoPath := RegistryPath(name)
		entries, _, err := m.Index(name, repoPath)
		if err != nil {
			continue
		}
		hits := Search(filter, entries)
		for i := range hits {
			hits[i].Registry = name
		}
		all = append(all, hits...)
	}

	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Score != all[j].Score {
			return all[i].Score > all[j].Score
		}
		return all[i].Name < all[j].Name
	})
	return all, nil
}

// RegistrySkills holds the skills in a single registry, or an error if the
// registry is unknown or its index could not be built.
type RegistrySkills struct {
	Name   string            `json:"name"`
	Skills []SkillIndexEntry `json:"skills,omitempty"`
	Error  string            `json:"error,omitempty"`
}

// ListSkills returns the skills for each named registry, in input order.
// A missing or broken registry surfaces as a per-entry error rather than a
// fatal failure, so callers can render partial results. Skills within each
// registry are sorted by name.
func (m *Manager) ListSkills(names []string) ([]RegistrySkills, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	out := make([]RegistrySkills, 0, len(names))
	for _, name := range names {
		// Accept a bare leaf (e.g. `skills` -> `acme/skills`). Resolution
		// failures stay per-entry so a bad name in a multi-name list doesn't
		// abort the rest; the resolved full name is what we display.
		resolved, rerr := ResolveName(cfg, name)
		if rerr != nil {
			r := RegistrySkills{Name: name}
			if errors.Is(rerr, ErrRegistryNotFound) {
				r.Error = "registry not found"
			} else {
				r.Error = rerr.Error()
			}
			out = append(out, r)
			continue
		}
		r := RegistrySkills{Name: resolved}
		skills, _, err := m.Index(resolved, RegistryPath(resolved))
		if err != nil {
			r.Error = err.Error()
			out = append(out, r)
			continue
		}
		sort.SliceStable(skills, func(i, j int) bool {
			return skills[i].Name < skills[j].Name
		})
		r.Skills = skills
		out = append(out, r)
	}
	return out, nil
}

// SkillLocation holds a found skill's index entry and registry context.
type SkillLocation struct {
	Entry         SkillIndexEntry
	RegistryName  string
	RegistryURL   string // canonical clone URL — recorded as supply-chain provenance
	RepoPath      string
	DefaultBranch string
}

// FindSkill searches all registries for a skill by name and returns the first
// match in alphabetical-by-registry-name order. Same-named skills in multiple
// registries surface as whichever registry sorts first — callers that need
// to detect the ambiguity should use FindAllSkillLocations instead.
func (m *Manager) FindSkill(skillName string) (*SkillLocation, error) {
	return m.FindSkillIn(skillName, "")
}

// FindSkillIn searches for a skill by name, restricted to the named
// registry when registryName is non-empty. Empty registryName searches
// every configured registry and returns the first match in alphabetical
// order (see FindSkill). Used by `qvr add --registry <name>` to
// disambiguate same-named skills across registries.
func (m *Manager) FindSkillIn(skillName, registryName string) (*SkillLocation, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if registryName != "" {
		// Accept a bare leaf (e.g. `skills` for `acme/skills`) so
		// `qvr add --registry skills` doesn't force the org segment.
		resolved, rerr := ResolveName(cfg, registryName)
		if rerr != nil {
			if errors.Is(rerr, ErrRegistryNotFound) {
				return nil, fmt.Errorf("registry %q is not configured — run `qvr registry list`", registryName)
			}
			return nil, rerr
		}
		regCfg := cfg.Registries[resolved]
		if loc := m.findSkillInRegistry(skillName, resolved, regCfg.URL); loc != nil {
			return loc, nil
		}
		return nil, fmt.Errorf("skill %q not found in registry %q", skillName, resolved)
	}

	for _, regName := range registryNames(cfg, "") {
		if loc := m.findSkillInRegistry(skillName, regName, cfg.Registries[regName].URL); loc != nil {
			return loc, nil
		}
	}
	return nil, fmt.Errorf("skill %q not found in any registry", skillName)
}

// FindSkillAtPath resolves a single skill at a known repo-relative directory in
// the named registry WITHOUT building the registry's full index. It backs the
// one-step `qvr add <blob-url>` fast path: the URL pins an exact skill
// directory, so we read just that one SKILL.md instead of parsing every skill
// in a large registry. registryName may be a bare leaf (e.g. `skills` for
// `acme/skills`). Returns an error when the directory holds no installable
// skill — callers fall back to the by-name full-index lookup, so a stale or
// root-level path still resolves, just without the speedup.
func (m *Manager) FindSkillAtPath(registryName, skillDir string) (*SkillLocation, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	resolved, rerr := ResolveName(cfg, registryName)
	if rerr != nil {
		if errors.Is(rerr, ErrRegistryNotFound) {
			return nil, fmt.Errorf("registry %q is not configured — run `qvr registry list`", registryName)
		}
		return nil, rerr
	}

	repoPath := RegistryPath(resolved)
	entry, err := m.Indexer.BuildEntry(repoPath, skillDir)
	if err != nil {
		return nil, fmt.Errorf("no skill at %q in registry %q: %w", skillDir, resolved, err)
	}

	defaultBranch, _ := m.Git.DefaultBranch(repoPath)
	if defaultBranch == "" {
		defaultBranch = defaultBranchFallback
	}
	return &SkillLocation{
		Entry:         entry,
		RegistryName:  resolved,
		RegistryURL:   cfg.Registries[resolved].URL,
		RepoPath:      repoPath,
		DefaultBranch: defaultBranch,
	}, nil
}

// FindSkillForSource resolves a skill scoped to a lock entry's recorded origin
// so version/tag discovery follows the entry's CURRENT registry — e.g. a fork it
// was migrated to via `qvr publish --fork --migrate` — instead of the first
// registry that alphabetically shares the skill name. Resolution order:
//
//  1. registryName (the entry's Registry) when set — the stable display name.
//  2. sourceURL (the entry's Source) when set — matched against configured
//     registries by canonical URL, so a migrated fork resolves to its own clone
//     even though `--migrate` clears Registry (#85). This is what keeps
//     `switch --latest` and `version list` consistent with `outdated`,
//     `provenance`, and explicit `switch <ref>` (#183).
//  3. a name-only search (FindSkill) when the entry carries no origin hint, or
//     when its source matches no configured registry — preserving behaviour for
//     never-migrated skills and for running outside a project (no lock entry).
func (m *Manager) FindSkillForSource(skillName, registryName, sourceURL string) (*SkillLocation, error) {
	if registryName != "" {
		return m.FindSkillIn(skillName, registryName)
	}
	if sourceURL != "" {
		cfg, err := config.Load()
		if err != nil {
			return nil, fmt.Errorf("load config: %w", err)
		}
		if regName := registryNameForURL(cfg, sourceURL); regName != "" {
			return m.FindSkillIn(skillName, regName)
		}
	}
	return m.FindSkill(skillName)
}

// registryNameForURL returns the configured registry whose clone URL refers to
// the same repo as want, or "" when none match. Comparison is canonicalised
// (credentials stripped, lowercased, trailing "/" and ".git" ignored) so a lock
// entry's Source resolves to its registry despite cosmetic URL differences.
func registryNameForURL(cfg *config.Config, want string) string {
	target := canonicalRepoURL(want)
	if target == "" {
		return ""
	}
	for name, rc := range cfg.Registries {
		if canonicalRepoURL(rc.URL) == target {
			return name
		}
	}
	return ""
}

// canonicalRepoURL reduces a clone URL to a comparison key: credentials
// stripped, lowercased, trailing slash and ".git" suffix removed. Best-effort —
// a URL that won't sanitize (e.g. a local path used in tests) falls back to a
// trimmed/lowercased form so matching still has a chance.
func canonicalRepoURL(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if clean, _, err := git.SanitizeURL(s); err == nil && clean != "" {
		s = clean
	}
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	return strings.ToLower(s)
}

// FindAllSkillLocations returns every registry that exposes a skill of the
// given name, in alphabetical-by-registry-name order. The empty-slice +
// nil-error case means "no registry has this skill"; callers can rely on
// len(locs) for the ambiguity check that drives `qvr add`'s pick-one warning
// and ref-aware fallback. Index-build failures for a single registry are
// silently skipped, matching FindSkill's behavior.
func (m *Manager) FindAllSkillLocations(skillName string) ([]*SkillLocation, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	var out []*SkillLocation
	for _, regName := range registryNames(cfg, "") {
		if loc := m.findSkillInRegistry(skillName, regName, cfg.Registries[regName].URL); loc != nil {
			out = append(out, loc)
		}
	}
	return out, nil
}

// findSkillInRegistry returns the SkillLocation for skillName in the named
// registry, or nil if the index can't be built or the skill isn't present.
// Shared body for FindSkillIn and FindAllSkillLocations so the lookup,
// default-branch fallback, and SkillLocation shape stay in one place.
func (m *Manager) findSkillInRegistry(skillName, regName, regURL string) *SkillLocation {
	repoPath := RegistryPath(regName)
	entries, _, err := m.Index(regName, repoPath)
	if err != nil {
		return nil
	}
	for _, entry := range entries {
		if entry.Name != skillName {
			continue
		}
		defaultBranch, _ := m.Git.DefaultBranch(repoPath)
		if defaultBranch == "" {
			defaultBranch = defaultBranchFallback
		}
		return &SkillLocation{
			Entry:         entry,
			RegistryName:  regName,
			RegistryURL:   regURL,
			RepoPath:      repoPath,
			DefaultBranch: defaultBranch,
		}
	}
	return nil
}

// ResolveName maps a possibly-abbreviated registry name to the full
// `<org>/<repo>` name recorded in config. An exact match always wins. Failing
// that, the input is treated as a leaf — the `<repo>` half of an `<org>/<repo>`
// name — and matched against every configured registry:
//
//   - exactly one leaf match resolves to that registry's full name,
//   - no match returns ErrRegistryNotFound (wrapped, with the input name),
//   - more than one match returns an ambiguity error naming the candidates.
//
// This is what lets `qvr registry update skills`, `qvr registry list skills`,
// and `qvr add --registry skills` work when only `acme/skills` is
// configured, without forcing the user to type the org segment. The empty
// string resolves to itself (callers treat "" as "all registries").
func ResolveName(cfg *config.Config, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	if _, ok := cfg.Registries[name]; ok {
		return name, nil
	}
	var matches []string
	for full := range cfg.Registries {
		if registryLeaf(full) == name {
			matches = append(matches, full)
		}
	}
	sort.Strings(matches)
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("%w: %s", ErrRegistryNotFound, name)
	default:
		return "", fmt.Errorf("registry name %q is ambiguous — it matches %s; use the full <org>/<repo> name",
			name, strings.Join(matches, ", "))
	}
}

// resolveForBatch is the leniency wrapper Update/Check use on their optional
// single-name argument. An empty name (operate on all) passes through. A leaf
// that resolves to exactly one registry is expanded to its full name. A leaf
// that matches nothing is returned unchanged so the per-name loop still reports
// it as the registry's own "not found" result (preserving the JSON shape and
// per-entry error). Only an ambiguous leaf is fatal — guessing one of several
// registries would silently fetch/check the wrong source.
func resolveForBatch(cfg *config.Config, name string) (string, error) {
	if name == "" {
		return "", nil
	}
	resolved, err := ResolveName(cfg, name)
	if err != nil {
		if errors.Is(err, ErrRegistryNotFound) {
			return name, nil // let the loop surface the not-found per entry
		}
		return "", err // ambiguous — fail loudly
	}
	return resolved, nil
}

// registryLeaf returns the last `/`-separated segment of a registry name —
// the `<repo>` half of `<org>/<repo>`, or the whole name when it's flat.
func registryLeaf(name string) string {
	if i := strings.LastIndex(name, "/"); i >= 0 {
		return name[i+1:]
	}
	return name
}

func registryNames(cfg *config.Config, name string) []string {
	if name != "" {
		return []string{name}
	}
	names := make([]string, 0, len(cfg.Registries))
	for n := range cfg.Registries {
		names = append(names, n)
	}
	// Sort so callers (Update, Check) iterate in a deterministic order — Go
	// map iteration is randomized and downstream output is otherwise
	// nondeterministic between runs (issue #76).
	sort.Strings(names)
	return names
}
