package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/astra-sh/qvr/internal/security"
	"github.com/astra-sh/qvr/internal/skill"
	"github.com/astra-sh/qvr/internal/ui"
	"github.com/google/uuid"
)

// uiServer is the read-only HTTP backend for `qvr ui`. Every handler reuses the
// same logic the CLI commands do (loadScopedLocks, buildSkillInfo,
// buildTreeGroups, buildProvenanceView, the store, the scanner) so the dashboard
// can never drift from CLI truth — it's a view layer, not a second source.
//
// The store is opened read-only and may be nil when the audit DB doesn't exist
// yet (audit never enabled); session/overview handlers degrade to empty results
// rather than erroring.
type uiServer struct {
	projectRoot string
	global      bool
	all         bool
	cfg         *config.Config
	store       store.Store // nil when the audit DB is absent
	version     string
}

// buildUIServer bootstraps the server: loads config and opens the audit store
// read-only if its DB file exists. A missing DB is not an error — the dashboard
// still serves skills/tree/provenance/scan. Taking projectRoot as a param (vs.
// os.Getwd internally) lets tests inject a temp project without chdir.
func buildUIServer(ctx context.Context, projectRoot string, global, all bool, version string) (*uiServer, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	s := &uiServer{
		projectRoot: projectRoot,
		global:      global,
		all:         all,
		cfg:         cfg,
		version:     version,
	}
	dbPath := ops.DBPath(cfg)
	if _, statErr := os.Stat(dbPath); statErr == nil {
		st, openErr := store.Open(ctx, store.OpenOptions{Path: dbPath, ReadOnly: true})
		if openErr != nil {
			return nil, fmt.Errorf("open audit store: %w", openErr)
		}
		s.store = st
	}
	return s, nil
}

// Close releases the store handle.
func (s *uiServer) Close() error {
	if s.store != nil {
		return s.store.Close()
	}
	return nil
}

// handler wires the routes. Go 1.22+ method+pattern routing keeps method
// dispatch and path params in the mux instead of hand-rolled switches.
func (s *uiServer) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/registries", s.handleRegistries)
	mux.HandleFunc("GET /api/registries/{name}/skills", s.handleRegistrySkills)
	mux.HandleFunc("GET /api/registries/{name}/skills/{skill}", s.handleRegistrySkill)
	mux.HandleFunc("GET /api/projects", s.handleProjects)
	mux.HandleFunc("GET /api/overview", s.handleOverview)
	mux.HandleFunc("GET /api/sessions", s.handleSessions)
	mux.HandleFunc("GET /api/sessions/{id}", s.handleSession)
	mux.HandleFunc("GET /api/skills", s.handleSkills)
	mux.HandleFunc("GET /api/skills/{name}", s.handleSkill)
	mux.HandleFunc("GET /api/tree", s.handleTree)
	mux.HandleFunc("GET /api/provenance", s.handleProvenance)
	mux.HandleFunc("GET /api/scan", s.handleScanSummary)
	mux.HandleFunc("POST /api/scan/{name}", s.handleScanRun)
	// Catch-all: static SPA assets with index.html fallback for client routes.
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// ---- JSON helpers ----------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// uiScope is the lens a single request is answered through. The server's launch
// flags (projectRoot + --global/--all) are the default, but the dashboard's
// project switcher overrides per request via ?project=/?scope= so one running
// `qvr ui` can browse every project on the machine without relaunching.
type uiScope struct {
	projectRoot string
	global      bool
	all         bool
}

// label names the scope for the overview payload so a reader never misattributes
// global activity to one project.
func (sc uiScope) label() string {
	switch {
	case sc.all:
		return "all"
	case sc.global:
		return "global"
	default:
		return "project"
	}
}

// auditDirs returns the working-directory whitelist that scopes the audit panels
// (sessions/events) for this scope. Project mode pins them to the project root
// so they match the project-scoped skill/gate panels; global/all return nil (no
// scope → every session/event), matching how those widen the lock panels.
func (sc uiScope) auditDirs() []string {
	if sc.global || sc.all {
		return nil
	}
	return []string{sc.projectRoot}
}

// resolveScope reads the per-request scope from ?scope=global|all and
// ?project=<abs path>. A ?project= path MUST be a known project (in
// projects.json, the launch root, or the global home) — the dashboard never
// reads a lock from an arbitrary client-supplied path. With no params it falls
// back to the server's launch flags, preserving the original single-scope CLI
// behavior.
func (s *uiServer) resolveScope(r *http.Request) (uiScope, error) {
	q := r.URL.Query()
	switch q.Get("scope") {
	case "global":
		return uiScope{projectRoot: s.projectRoot, global: true}, nil
	case "all":
		return uiScope{projectRoot: s.projectRoot, all: true}, nil
	}
	if p := q.Get("project"); p != "" {
		abs, err := s.knownProjectRoot(p)
		if err != nil {
			return uiScope{}, err
		}
		return uiScope{projectRoot: abs}, nil
	}
	return uiScope{projectRoot: s.projectRoot, global: s.global, all: s.all}, nil
}

// knownProjectRoot resolves p to an absolute path and confirms it is a project
// Quiver already knows about (the launch root, the global home, or a recorded
// entry in projects.json whose lock still exists). Returns an error for
// anything else.
func (s *uiServer) knownProjectRoot(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("invalid project path: %w", err)
	}
	if abs == s.projectRoot || abs == config.Dir() {
		return abs, nil
	}
	if _, ok := indexedProjectRoots()[abs]; ok {
		return abs, nil
	}
	return "", fmt.Errorf("unknown project: %s", p)
}

// indexedProjectRoots returns the project root directories recorded in
// ~/.quiver/projects.json whose lock file still exists, keyed by absolute root
// with the LastSeen timestamp. The index keys are lock-file paths
// (`<root>/qvr.lock`), so the root is the parent dir. Stale entries (project
// moved/deleted, lock gone) are skipped so the switcher isn't cluttered with
// dead temp dirs.
func indexedProjectRoots() map[string]time.Time {
	roots := map[string]time.Time{}
	pf, err := registry.ReadProjects()
	if err != nil {
		return roots
	}
	for lockPath, rec := range pf.Projects {
		if _, statErr := os.Stat(lockPath); statErr != nil {
			continue
		}
		roots[filepath.Dir(lockPath)] = rec.LastSeen
	}
	return roots
}

// ---- shared lock access ----------------------------------------------------

// entriesForScope returns every lock entry in the given scope, flattened across
// project/global per the resolved flags.
func (s *uiServer) entriesForScope(sc uiScope) ([]*model.LockEntry, error) {
	locks, err := loadScopedLocks(sc.projectRoot, sc.global, sc.all)
	if err != nil {
		return nil, err
	}
	var out []*model.LockEntry
	for _, sl := range locks {
		if sl.Lock == nil {
			continue
		}
		out = append(out, sl.Lock.Entries()...)
	}
	return out, nil
}

// ---- handlers --------------------------------------------------------------

func (s *uiServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"version":       s.version,
		"audit_enabled": s.store != nil,
	})
}

// handleRegistries returns the global registry list — the same data as
// `qvr registry list`, which is shared across all projects (registries live at
// the QUIVER_HOME root, not per project). Manager.List() is local-only (config +
// cached index + FETCH_HEAD mtime), so it never touches the network. Degrades to
// an empty list on error rather than failing the page.
func (s *uiServer) handleRegistries(w http.ResponseWriter, r *http.Request) {
	mgr := newRegistryManager(git.NewGoGitClient())
	regs, err := mgr.List()
	if err != nil || regs == nil {
		regs = []model.RegistryStatus{}
	}
	writeJSON(w, http.StatusOK, regs)
}

// registryVersion is one installable ref (branch or tag) of a registry repo,
// resolved to its commit with a timestamp so the dashboard can render a version
// timeline. Current marks the repo's default branch.
type registryVersion struct {
	Ref     string    `json:"ref"`
	IsTag   bool      `json:"isTag"`
	SHA     string    `json:"sha"`
	Time    time.Time `json:"time"`
	Subject string    `json:"subject,omitempty"`
	Current bool      `json:"current,omitempty"`
}

// registrySkillRow is one skill discovered in a registry, annotated with whether
// (and at what ref/commit) it is installed in the active scope so the UI can
// highlight the in-use version inside the shared version tree.
type registrySkillRow struct {
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Path            string `json:"path,omitempty"`
	Installed       bool   `json:"installed"`
	InstalledRef    string `json:"installedRef,omitempty"`
	InstalledCommit string `json:"installedCommit,omitempty"`
}

// registrySkillsResponse is the payload for the registry detail page: the
// registry's metadata, its full branch/tag version timeline (repo-level, shared
// by every skill in the repo), and the skills it offers with install status.
type registrySkillsResponse struct {
	Registry      string             `json:"registry"`
	URL           string             `json:"url,omitempty"`
	DefaultBranch string             `json:"defaultBranch,omitempty"`
	Versions      []registryVersion  `json:"versions"`
	Skills        []registrySkillRow `json:"skills"`
	Error         string             `json:"error,omitempty"`
}

// handleRegistrySkills powers the registry detail page: every skill the registry
// offers plus the repo's branch/tag version timeline. Skills come from the
// cache-aware index (no network); versions come from the bare clone's refs
// resolved to commit + time. Install status is taken from the active scope's
// lock so the in-use ref can be highlighted in the version tree. Registries are
// global, but install status is scoped — so this endpoint still honors ?scope/
// ?project for the installed/current markers.
func (s *uiServer) handleRegistrySkills(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if _, ok := s.cfg.Registries[name]; !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("registry %q not found", name))
		return
	}
	resp := registrySkillsResponse{
		Registry: name,
		URL:      s.cfg.Registries[name].URL,
		Versions: []registryVersion{},
		Skills:   []registrySkillRow{},
	}

	repoPath := registry.RegistryPath(name)
	gc := git.NewGoGitClient()
	defaultBranch, _ := gc.DefaultBranch(repoPath)
	resp.DefaultBranch = defaultBranch

	// Repo-level version timeline: branches + tags resolved to commit + time.
	if vers, err := gc.RefVersions(repoPath); err == nil {
		for _, v := range vers {
			resp.Versions = append(resp.Versions, registryVersion{
				Ref:     v.Name,
				IsTag:   v.IsTag,
				SHA:     v.Hash,
				Time:    v.Time,
				Subject: v.Subject,
				Current: v.Name == defaultBranch,
			})
		}
	}

	// Installed skills in the active scope, keyed by name, so each offered skill
	// can show its in-use ref/commit. Best-effort: a scope error just omits the
	// install annotations rather than failing the (global) registry page.
	installed := map[string]*model.LockEntry{}
	if sc, err := s.resolveScope(r); err == nil {
		if entries, err := s.entriesForScope(sc); err == nil {
			for _, e := range entries {
				if e.Registry == name {
					installed[e.Name] = e
				}
			}
		}
	}

	mgr := newRegistryManager(gc)
	listed, err := mgr.ListSkills([]string{name})
	if err != nil || len(listed) == 0 {
		if err != nil {
			resp.Error = err.Error()
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	rs := listed[0]
	if rs.Error != "" {
		resp.Error = rs.Error
	}
	for _, sk := range rs.Skills {
		row := registrySkillRow{
			Name:        sk.Name,
			Description: sk.Description,
			Path:        sk.Path,
		}
		if e, ok := installed[sk.Name]; ok {
			row.Installed = true
			row.InstalledRef = e.Ref
			row.InstalledCommit = e.Commit
		}
		resp.Skills = append(resp.Skills, row)
	}
	writeJSON(w, http.StatusOK, resp)
}

// registrySkillDetail powers the registry-scope skill page: a single skill the
// registry offers, browsed (and possibly not installed) at a chosen ref. It
// carries enough to render the same workbench the installed view shows — the
// skill's file structure (listed straight from the bare clone, no checkout) and
// the repo's version timeline — plus install status so the page can mark the
// in-use version. Metadata stays light (name + description from the index): a
// full SKILL.md parse isn't needed to browse, and a real scan only runs at
// install, so the page derives a file-type inventory client-side instead.
type registrySkillDetail struct {
	Registry        string            `json:"registry"`
	Name            string            `json:"name"`
	Description     string            `json:"description,omitempty"`
	Path            string            `json:"path,omitempty"`
	Ref             string            `json:"ref,omitempty"`
	Commit          string            `json:"commit,omitempty"`
	Files           []string          `json:"files"`
	Installed       bool              `json:"installed"`
	InstalledRef    string            `json:"installedRef,omitempty"`
	InstalledCommit string            `json:"installedCommit,omitempty"`
	Versions        []registryVersion `json:"versions"`
	Error           string            `json:"error,omitempty"`
}

// handleRegistrySkill powers the registry-scope skill detail page. Unlike
// handleSkill (which needs an installed worktree on disk), this reads the skill
// straight out of the bare clone at a ref: file list via ListBlobsRecursive,
// version timeline via RefVersions. Install status comes from the active scope's
// lock so the in-use version can still be highlighted even though the page is
// reached from the (global) registry browser.
func (s *uiServer) handleRegistrySkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	skillName := r.PathValue("skill")
	if _, ok := s.cfg.Registries[name]; !ok {
		writeErr(w, http.StatusNotFound, fmt.Errorf("registry %q not found", name))
		return
	}

	repoPath := registry.RegistryPath(name)
	gc := git.NewGoGitClient()
	defaultBranch, _ := gc.DefaultBranch(repoPath)

	ref := r.URL.Query().Get("ref")
	if ref == "" {
		ref = defaultBranch
	}

	resp := registrySkillDetail{
		Registry: name,
		Name:     skillName,
		Ref:      ref,
		Files:    []string{},
		Versions: []registryVersion{},
	}

	// Repo-level version timeline: branches + tags resolved to commit + time.
	if vers, err := gc.RefVersions(repoPath); err == nil {
		for _, v := range vers {
			resp.Versions = append(resp.Versions, registryVersion{
				Ref:     v.Name,
				IsTag:   v.IsTag,
				SHA:     v.Hash,
				Time:    v.Time,
				Subject: v.Subject,
				Current: v.Name == defaultBranch,
			})
		}
	}

	// Install status in the active scope, so the in-use ref/commit can be marked.
	if sc, err := s.resolveScope(r); err == nil {
		if entries, err := s.entriesForScope(sc); err == nil {
			for _, e := range entries {
				if e.Registry == name && e.Name == skillName {
					resp.Installed = true
					resp.InstalledRef = e.Ref
					resp.InstalledCommit = e.Commit
					break
				}
			}
		}
	}

	// Resolve the skill's subpath from the cache-aware index (no network), then
	// list its files from the bare clone at the chosen ref.
	mgr := newRegistryManager(gc)
	listed, err := mgr.ListSkills([]string{name})
	if err != nil || len(listed) == 0 {
		if err != nil {
			resp.Error = err.Error()
		} else {
			resp.Error = fmt.Sprintf("skill %q not found in registry %q", skillName, name)
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	rs := listed[0]
	if rs.Error != "" {
		resp.Error = rs.Error
		writeJSON(w, http.StatusOK, resp)
		return
	}

	var skillPath string
	found := false
	for _, sk := range rs.Skills {
		if sk.Name == skillName {
			resp.Description = sk.Description
			resp.Path = sk.Path
			skillPath = sk.Path
			found = true
			break
		}
	}
	if !found {
		resp.Error = fmt.Sprintf("skill %q not found in registry %q", skillName, name)
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// A root-layout skill (Path ".") lives at the repo root: list the whole tree.
	treePath := skillPath
	if treePath == "." {
		treePath = ""
	}
	blobs, err := gc.ListBlobsRecursive(repoPath, ref, treePath)
	if err != nil {
		resp.Error = fmt.Sprintf("list files at %s: %v", ref, err)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	prefix := treePath
	if prefix != "" {
		prefix += "/"
	}
	for _, b := range blobs {
		rel := strings.TrimPrefix(b.Path, prefix)
		resp.Files = append(resp.Files, rel)
	}
	sort.Strings(resp.Files)
	writeJSON(w, http.StatusOK, resp)
}

// projectSummary is one row in the dashboard's project switcher. Projects come
// from Quiver's existing on-disk index (~/.quiver/projects.json, maintained by
// every project-lock mutation), plus a synthetic Global entry and the launch
// project. Counts are cheap: a lock read for skills, indexed COUNTs for traces.
type projectSummary struct {
	Path     string    `json:"path"`  // abs project root; "" for Global
	Name     string    `json:"name"`  // base name, or "Global"
	Scope    string    `json:"scope"` // "project" | "global"
	LockPath string    `json:"lockPath,omitempty"`
	HasLock  bool      `json:"hasLock"`
	Current  bool      `json:"current"` // the directory `qvr ui` was launched from
	Skills   int       `json:"skills"`
	Sessions int64     `json:"sessions"`
	Events   int64     `json:"events"`
	LastSeen time.Time `json:"lastSeen,omitempty"`
}

func (s *uiServer) handleProjects(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Collect candidate project roots: the recorded index (live locks only) ∪
	// the launch root.
	roots := indexedProjectRoots()
	if _, ok := roots[s.projectRoot]; !ok {
		roots[s.projectRoot] = time.Time{} // launch project, even if never `qvr add`ed
	}

	out := make([]projectSummary, 0, len(roots)+1)

	// Global pseudo-project: the user-global lock + machine-wide trace totals.
	globalLock := filepath.Join(config.Dir(), model.LockFileName)
	gs := projectSummary{
		Name:     "Global",
		Scope:    "global",
		LockPath: globalLock,
		Skills:   countLockEntries(globalLock),
	}
	gs.HasLock = gs.Skills > 0 || lockExists(globalLock)
	if s.store != nil {
		if n, err := s.store.CountRawSessions(ctx, nil, ""); err == nil {
			gs.Sessions = n
		}
		if n, err := s.store.CountRawTraces(ctx, nil, ""); err == nil {
			gs.Events = n
		}
	}
	out = append(out, gs)

	for root, lastSeen := range roots {
		lockPath := filepath.Join(root, model.LockFileName)
		ps := projectSummary{
			Path:     root,
			Name:     filepath.Base(root),
			Scope:    "project",
			LockPath: lockPath,
			Current:  root == s.projectRoot,
			HasLock:  lockExists(lockPath),
			Skills:   countLockEntries(lockPath),
			LastSeen: lastSeen,
		}
		if s.store != nil {
			if n, err := s.store.CountRawSessions(ctx, []string{root}, ""); err == nil {
				ps.Sessions = n
			}
			if n, err := s.store.CountRawTraces(ctx, []string{root}, ""); err == nil {
				ps.Events = n
			}
		}
		out = append(out, ps)
	}

	// Global first, then most-recently-seen projects; the launch project sorts
	// to the top of its group so "where am I" is always visible.
	sort.SliceStable(out[1:], func(i, j int) bool {
		a, b := out[1+i], out[1+j]
		if a.Current != b.Current {
			return a.Current
		}
		return a.LastSeen.After(b.LastSeen)
	})
	writeJSON(w, http.StatusOK, out)
}

// lockExists reports whether a lock file is present at path.
func lockExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// countLockEntries returns the number of installed skills in the lock at path,
// or 0 when the lock is absent or unreadable.
func countLockEntries(path string) int {
	lock, err := model.ReadLockFile(path)
	if err != nil || lock == nil {
		return 0
	}
	return len(lock.Entries())
}

// overviewResponse is the dashboard home payload: headline counts, a scan-gate
// rollup, and the most recent sessions.
type overviewResponse struct {
	AuditEnabled bool `json:"audit_enabled"`
	// Scope and ProjectRoot describe the lens the whole payload is taken
	// through — including the sessions/events counts below — so the UI can
	// label "this project" vs "global" instead of presenting project-scoped
	// gate data and global audit data as if they share scope (issue #141).
	Scope          string              `json:"scope"`
	ProjectRoot    string              `json:"project_root,omitempty"`
	Sessions       int64               `json:"sessions"`
	Events         int64               `json:"events"`
	Skills         int                 `json:"skills"`
	Registries     int                 `json:"registries"`
	GateAllowed    int                 `json:"gate_allowed"`
	GateBlocked    int                 `json:"gate_blocked"`
	GateUnscanned  int                 `json:"gate_unscanned"`
	RecentSessions []*store.RawSession `json:"recent_sessions"`
}

func (s *uiServer) handleOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp := overviewResponse{
		AuditEnabled:   s.store != nil,
		Scope:          sc.label(),
		RecentSessions: []*store.RawSession{},
	}
	if resp.Scope == "project" {
		resp.ProjectRoot = sc.projectRoot
	}

	if s.store != nil {
		// Scope sessions/traces to the same lens as the skill/gate panels so
		// switching project/global rescopes every count, not just half the screen.
		dirs := sc.auditDirs()
		if n, err := s.store.CountRawSessions(ctx, dirs, ""); err == nil {
			resp.Sessions = n
		}
		if n, err := s.store.CountRawTraces(ctx, dirs, ""); err == nil {
			resp.Events = n
		}
		if recent, err := s.store.ListRawSessions(ctx, &store.RawSessionFilter{Dirs: dirs, SkillsOnly: true, Limit: 5}); err == nil && recent != nil {
			s.populateTitles(ctx, recent)
			s.populateSessionSkills(ctx, recent)
			resp.RecentSessions = recent
		}
	}

	entries, err := s.entriesForScope(sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resp.Skills = len(entries)
	regs := map[string]struct{}{}
	for _, e := range entries {
		if e.Registry != "" {
			regs[e.Registry] = struct{}{}
		}
		switch buildProvenanceView(e).ScanDecision {
		case "allowed":
			resp.GateAllowed++
		case "blocked":
			resp.GateBlocked++
		default:
			resp.GateUnscanned++
		}
	}
	resp.Registries = len(regs)
	writeJSON(w, http.StatusOK, resp)
}

func (s *uiServer) handleSessions(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeJSON(w, http.StatusOK, []*store.RawSession{})
		return
	}
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	q := r.URL.Query()
	limit := 100
	if v := q.Get("limit"); v != "" {
		if n, err := parsePositiveInt(v); err == nil {
			limit = n
		}
	}
	filter := &store.RawSessionFilter{
		Dirs:  sc.auditDirs(),
		Agent: q.Get("agent"),
		Skill: q.Get("skill"),
		// The dashboard only surfaces skill-attributed sessions; skill-less ones
		// that escaped capture's retention gate (in-flight, no deriver, ingested)
		// stay in the DB but never show in the UI.
		SkillsOnly: true,
		Limit:      limit,
	}
	// Date filters accept a calendar day (YYYY-MM-DD from the UI's date inputs)
	// or a full RFC3339 instant. `until` is inclusive: a bare day extends to its
	// end so a session anywhere on that day still matches.
	if since := parseDateParam(q.Get("since"), false); since != nil {
		filter.Since = since
	}
	if until := parseDateParam(q.Get("until"), true); until != nil {
		filter.Until = until
	}

	sessions, err := s.store.ListRawSessions(r.Context(), filter)
	if err != nil {
		if schemaNotReady(err) {
			writeJSON(w, http.StatusOK, []*store.RawSession{})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if sessions == nil {
		sessions = []*store.RawSession{}
	}
	s.populateTitles(r.Context(), sessions)
	s.populateSessionSkills(r.Context(), sessions)
	writeJSON(w, http.StatusOK, sessions)
}

// parseDateParam parses a session date filter: a calendar day (YYYY-MM-DD) or a
// full RFC3339 instant, in UTC. When endOfDay is set a bare day is pushed to its
// last instant so an inclusive `until` covers the whole day. Returns nil for an
// empty or unparseable value (the filter is then simply not applied).
func parseDateParam(s string, endOfDay bool) *time.Time {
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		u := t.UTC()
		return &u
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		u := t.UTC()
		if endOfDay {
			u = u.Add(24*time.Hour - time.Nanosecond)
		}
		return &u
	}
	return nil
}

// populateSessionSkills fills each session's Skills with the distinct skills it
// used, looked up from its derived spans in one batched query. Best-effort: a
// lookup error leaves Skills empty rather than failing the list.
func (s *uiServer) populateSessionSkills(ctx context.Context, sessions []*store.RawSession) {
	if s.store == nil || len(sessions) == 0 {
		return
	}
	ids := make([]string, 0, len(sessions))
	for _, sess := range sessions {
		ids = append(ids, sess.SessionID.String())
	}
	bySession, err := s.store.SkillsForSessions(ctx, ids)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if skills := bySession[sess.SessionID.String()]; len(skills) > 0 {
			sess.Skills = skills
		}
	}
}

// titlePromptRows is how many leading transcript rows we read per session to
// derive its title. The first user prompt is essentially always within the
// first few rows, so a small window keeps the sessions list cheap (the deriver
// runs over just these rows, not the whole session).
const titlePromptRows = 20

// titleMaxLen clips derived session titles so a long first prompt stays a tidy
// single-line table cell.
const titleMaxLen = 100

// schemaNotReady reports whether err is the "raw-trace tables don't exist yet"
// condition — a DB that predates v0.10.0 and hasn't had a write-mode capture
// apply migration 0002 (the read-only UI open can't create tables). The
// dashboard treats this as "no audit data captured yet" and shows the empty
// state with the `qvr audit enable` hint, rather than surfacing a raw SQL error.
func schemaNotReady(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "no such table: raw_traces") ||
		strings.Contains(msg, "no such table: spans")
}

// populateTitles fills each session's Title with its first user prompt, derived
// from a small leading window of its transcript rows. Best-effort: a session we
// can't derive (no deriver for its agent, no transcript, query error) keeps an
// empty Title and the UI falls back to a placeholder. The store is required;
// callers only invoke this when s.store != nil.
func (s *uiServer) populateTitles(ctx context.Context, sessions []*store.RawSession) {
	if s.store == nil {
		return
	}
	for _, sess := range sessions {
		if sess == nil || sess.TranscriptLines == 0 {
			continue
		}
		id := sess.SessionID
		rows, err := s.store.QueryRawTraces(ctx, &store.RawTraceFilter{
			SessionID: &id,
			Sources:   []string{ops.RawSourceTranscript},
			Limit:     titlePromptRows,
		})
		if err != nil || len(rows) == 0 {
			continue
		}
		sess.Title = derive.FirstPrompt(rows, titleMaxLen)
	}
}

// sessionDetail bundles both representations of a session so the UI can toggle
// between them: the derived span timeline (the processed view) and the verbatim
// raw rows (the lossless source). Session carries the summary + derived title so
// the detail header names the session by its first prompt, not by its agent.
type sessionDetail struct {
	Session *store.RawSession `json:"session"`
	Spans   []*store.SpanRow  `json:"spans"`
	Traces  []rawTraceView    `json:"traces"`
}

// rawTraceView is one raw row rendered for the dashboard. Raw holds the verbatim
// native bytes as inline JSON when they parse (the common case — transcript
// lines and hook payloads are JSON), or as a quoted string when they don't, so
// the UI can pretty-print without a second decode step. RawText carries the
// undecoded string as a copy/export fallback.
type rawTraceView struct {
	Seq        int             `json:"seq"`
	Source     string          `json:"source"`
	HookType   string          `json:"hook_type,omitempty"`
	SourcePath string          `json:"source_path,omitempty"`
	CapturedAt time.Time       `json:"captured_at"`
	Raw        json.RawMessage `json:"raw"`
}

func (s *uiServer) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.store == nil {
		writeErr(w, http.StatusNotFound, fmt.Errorf("audit not enabled"))
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid session id"))
		return
	}
	ctx := r.Context()

	// Pull the full set of raw rows for this session once: they drive the raw
	// view, the derived title, and the summary, so we read them a single time.
	rawRows, err := s.store.QueryRawTraces(ctx, &store.RawTraceFilter{
		SessionID: &id,
		Limit:     100000,
	})
	if err != nil {
		if schemaNotReady(err) {
			writeJSON(w, http.StatusOK, sessionDetail{
				Session: &store.RawSession{SessionID: id},
				Spans:   []*store.SpanRow{},
				Traces:  []rawTraceView{},
			})
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	spans, err := s.store.QuerySpans(ctx, &store.SpanFilter{SessionID: &id})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if spans == nil {
		spans = []*store.SpanRow{}
	}

	detail := sessionDetail{
		Session: summarizeRawSession(id, rawRows),
		Spans:   spans,
		Traces:  toRawTraceViews(rawRows),
	}
	writeJSON(w, http.StatusOK, detail)
}

// summarizeRawSession builds the session summary the detail header renders from
// the session's own raw rows — the same fields ListRawSessions computes in SQL,
// recomputed here in Go so the detail page never needs a second scan over every
// session. Includes the derived title.
func summarizeRawSession(id uuid.UUID, rows []*ops.RawTrace) *store.RawSession {
	sess := &store.RawSession{SessionID: id, TotalRows: int64(len(rows))}
	for i, r := range rows {
		if r == nil {
			continue
		}
		if r.AgentName != "" {
			sess.AgentName = r.AgentName
		}
		if r.AgentSessionID != "" {
			sess.AgentSessionID = r.AgentSessionID
		}
		if r.WorkingDirectory != "" {
			sess.WorkingDirectory = r.WorkingDirectory
		}
		switch r.Source {
		case ops.RawSourceTranscript:
			sess.TranscriptLines++
		case ops.RawSourceHookPayload:
			sess.HookPayloads++
		}
		if i == 0 || r.CapturedAt.Before(sess.StartedAt) {
			sess.StartedAt = r.CapturedAt
		}
		if r.CapturedAt.After(sess.LastAt) {
			sess.LastAt = r.CapturedAt
		}
	}
	sess.Title = derive.FirstPrompt(rows, titleMaxLen)
	return sess
}

// toRawTraceViews renders raw rows for the dashboard's raw toggle, decoding each
// row's bytes to inline JSON when valid (so the UI pretty-prints directly).
func toRawTraceViews(rows []*ops.RawTrace) []rawTraceView {
	out := make([]rawTraceView, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		out = append(out, rawTraceView{
			Seq:        r.Seq,
			Source:     r.Source,
			HookType:   r.HookType,
			SourcePath: r.SourcePath,
			CapturedAt: r.CapturedAt,
			Raw:        rawAsJSON(r.Raw),
		})
	}
	return out
}

// rawAsJSON returns b verbatim if it is valid JSON, otherwise b quoted as a JSON
// string. Either way the result is a legal json.RawMessage, so the row always
// serializes cleanly even when a transcript line isn't JSON.
func rawAsJSON(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	if json.Valid(b) {
		return json.RawMessage(b)
	}
	quoted, err := json.Marshal(string(b))
	if err != nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(quoted)
}

func (s *uiServer) handleSkills(w http.ResponseWriter, r *http.Request) {
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	locks, err := loadScopedLocks(sc.projectRoot, sc.global, sc.all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rows := []scopedListEntry{}
	for _, sl := range locks {
		if sl.Lock == nil {
			continue
		}
		for _, e := range sl.Lock.Entries() {
			row := scopedListEntry{
				Name:      e.Name,
				Worktree:  skill.EntryWorktreePath(e),
				LockEntry: e,
			}
			if sc.all {
				row.Scope = sl.Scope
			}
			rows = append(rows, row)
		}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *uiServer) handleSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	locks, err := loadScopedLocks(sc.projectRoot, sc.global, sc.all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	entry, scope, err := findEntryAcrossLocks(name, locks)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	info, err := buildSkillInfo(entry, sc.projectRoot, scope.Scope == "global")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *uiServer) handleTree(w http.ResponseWriter, r *http.Request) {
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	locks, err := loadScopedLocks(sc.projectRoot, sc.global, sc.all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	groups := buildTreeGroups(locks, sc.all)
	if groups == nil {
		groups = []treeGroup{}
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *uiServer) handleProvenance(w http.ResponseWriter, r *http.Request) {
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	entries, err := s.entriesForScope(sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	views := make([]*provenanceView, 0, len(entries))
	for _, e := range entries {
		views = append(views, buildProvenanceView(e))
	}
	writeJSON(w, http.StatusOK, views)
}

// scanSummaryRow is the recorded (install-time) scan-gate decision per skill —
// instant, no live scan. The Scan page renders this and offers a live re-scan.
type scanSummaryRow struct {
	Name           string `json:"name"`
	Registry       string `json:"registry,omitempty"`
	Decision       string `json:"decision,omitempty"`
	ScannerVersion string `json:"scannerVersion,omitempty"`
	Mode           string `json:"mode,omitempty"`
}

func (s *uiServer) handleScanSummary(w http.ResponseWriter, r *http.Request) {
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	entries, err := s.entriesForScope(sc)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rows := make([]scanSummaryRow, 0, len(entries))
	for _, e := range entries {
		v := buildProvenanceView(e)
		mode := ""
		switch {
		case e.IsEdit():
			mode = "edit"
		case e.IsLink():
			mode = "link"
		}
		rows = append(rows, scanSummaryRow{
			Name:           e.Name,
			Registry:       e.Registry,
			Decision:       v.ScanDecision,
			ScannerVersion: v.ScannerVersion,
			Mode:           mode,
		})
	}
	writeJSON(w, http.StatusOK, rows)
}

// scanRunGate is the live re-scan's gate verdict, computed under the SAME
// policy that produced the recorded gate (cfg.Security.BlockSeverity) and
// reported on the SAME 5-rung severity scale the lock uses
// (critical/high/medium/low/info). This lets the Scan page compare the
// recorded verdict to the live one 1:1 and surface drift — the recorded
// `allowed` silently becoming a live `blocked` is exactly what a read-only
// audit dashboard exists to show (issue #140).
type scanRunGate struct {
	Decision  string               `json:"decision"`  // allowed | blocked
	Threshold string               `json:"threshold"` // block_severity policy applied
	Counts    model.SeverityCounts `json:"counts"`    // lock-scale, comparable to verification.scan.counts
}

// scanRunResponse embeds the raw ScanResult (path/skill/findings/summary on the
// scanner's native 4-rung scale, for the detail view) and adds the gate verdict
// on the recorded scale so both representations are available to the UI.
type scanRunResponse struct {
	*security.ScanResult
	Gate scanRunGate `json:"gate"`
}

// handleScanRun runs the scanner live against an installed skill's bytes and
// returns the full ScanResult plus a gate verdict computed under the recorded
// gate's policy (issue #140). This is the on-demand path; the recorded gate
// decision still comes from handleScanSummary.
func (s *uiServer) handleScanRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	locks, err := loadScopedLocks(sc.projectRoot, sc.global, sc.all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	entry, _, err := findEntryAcrossLocks(name, locks)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	dir := skill.EffectiveTarget(entry, sc.projectRoot)
	if dir == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("skill %q has no scannable directory (link install?)", name))
		return
	}
	loaded, err := skill.LoadFromPath(dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("load skill: %w", err))
		return
	}
	scanner := security.New()
	if p := security.LLMProviderFromEnv(); p != nil {
		scanner = scanner.WithLLMProvider(p)
	}
	// Bound the live scan so a wedged check can't hang the request.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	result, err := scanner.Scan(ctx, loaded, dir)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("scan: %w", err))
		return
	}
	result.Lint = lintReportFor(loaded)

	// Apply the same threshold the install-time gate recorded into the lock
	// (block_severity, default critical) so "live verdict" is the recorded
	// verdict recomputed against the bytes on disk *now* — directly comparable.
	threshold, perr := security.ParseSeverity(blockSeverityOrDefault(s.cfg))
	if perr != nil {
		threshold = security.SeverityCritical
	}
	decision := "allowed"
	if exceedsThreshold(result, threshold) {
		decision = "blocked"
	}
	writeJSON(w, http.StatusOK, scanRunResponse{
		ScanResult: result,
		Gate: scanRunGate{
			Decision:  decision,
			Threshold: string(threshold),
			Counts:    severityCountsFromSummary(result.Summary),
		},
	})
}

// ---- static SPA serving ----------------------------------------------------

func (s *uiServer) handleStatic(w http.ResponseWriter, r *http.Request) {
	// Unmatched /api/* routes are real 404s, not SPA paths — keep them JSON so
	// a typo in a fetch surfaces as an API error, not an HTML page.
	if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no such endpoint: %s", r.URL.Path))
		return
	}
	if !ui.HasIndex() {
		serveStub(w)
		return
	}
	dist := ui.Dist()
	name := path.Clean("/" + r.URL.Path)[1:] // strip leading slash, clean traversal
	if name == "" {
		name = "index.html"
	}
	// Missing files fall back to index.html so client-side routes (e.g.
	// /sessions/<id>) resolve to the SPA instead of 404.
	if st, err := fs.Stat(dist, name); err != nil || st.IsDir() {
		name = "index.html"
	}
	http.ServeFileFS(w, r, dist, name)
}

// serveStub is shown when the React bundle hasn't been built. It tells the user
// how to build it rather than rendering a blank page.
func serveStub(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!doctype html>
<html><head><meta charset="utf-8"><title>Quiver UI</title>
<style>body{font:16px/1.5 system-ui,sans-serif;max-width:42rem;margin:4rem auto;padding:0 1rem;color:#1f2937}
code{background:#f3f4f6;padding:.15rem .4rem;border-radius:.25rem}</style></head>
<body><h1>Quiver dashboard not built</h1>
<p>The API is live, but the React bundle hasn't been compiled into this binary yet.</p>
<p>Build it with:</p>
<pre><code>make ui &amp;&amp; make build</code></pre>
<p>Then re-run <code>qvr ui</code>. The JSON API is already available under <code>/api/</code>.</p>
</body></html>`))
}

// ---- small utils -----------------------------------------------------------

func parsePositiveInt(s string) (int, error) {
	const maxN = 100000
	if s == "" {
		return 0, fmt.Errorf("must be positive")
	}
	n := 0
	for _, c := range s {
		// Validate every character — never break early, or a trailing
		// non-digit (e.g. "100000abc") would slip through unvalidated.
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		if n < maxN {
			n = n*10 + int(c-'0')
			if n > maxN {
				n = maxN // clamp; keep scanning to validate the rest
			}
		}
	}
	if n <= 0 {
		return 0, fmt.Errorf("must be positive")
	}
	return n, nil
}
