package cmd

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops/store"
)

// The three observability endpoints behind the dashboard's skill report card,
// dead-weight report, and overview rollup. All read-side: they join the
// store's span aggregations (metrics.go) with the scope's lock entries.
//
// Degradation contract: a nil store OR a schema-not-ready DB answers with
// audit_enabled:false and zero usage — never a 500, and never a payload that
// could read as "every skill is dead weight" when usage simply isn't
// measurable yet.

// coreShareThreshold is the "M skills did 90% of verified work" cut for the
// overview headline.
const coreShareThreshold = 0.9

// skillMetricsHeadline is the overview rollup sentence's data.
type skillMetricsHeadline struct {
	Installed           int      `json:"installed"`
	Active              int      `json:"active"`      // installed AND fired in window
	NeverFired          int      `json:"never_fired"` // installed, zero spans ever in window
	VerifiedInvocations int64    `json:"verified_invocations"`
	TotalInvocations    int64    `json:"total_invocations"`
	CoreSkills          []string `json:"core_skills"` // smallest set covering ≥90% of verified work
	CoreShare           float64  `json:"core_share"`
}

// skillMetricsRow is one skill's usage merged with its lock identity. Skills
// present in spans but no longer installed still appear (installed:false) —
// history stays honest.
type skillMetricsRow struct {
	Name          string     `json:"name"`
	Installed     bool       `json:"installed"`
	Registry      string     `json:"registry,omitempty"`
	Ref           string     `json:"ref,omitempty"`
	Commit        string     `json:"commit,omitempty"`
	Disabled      bool       `json:"disabled,omitempty"`
	Gate          string     `json:"gate,omitempty"` // recorded scan decision
	InstalledAt   *time.Time `json:"installedAt,omitempty"`
	Invocations   int64      `json:"invocations"`
	Sessions      int64      `json:"sessions"`
	Verified      int64      `json:"verified"`
	VerifiedShare float64    `json:"verifiedShare"`
	FirstFired    *time.Time `json:"firstFired,omitempty"`
	LastFired     *time.Time `json:"lastFired,omitempty"`
	TokensIn      int64      `json:"tokensIn"`
	TokensOut     int64      `json:"tokensOut"`
	TokenSessions int64      `json:"tokenSessions"`
}

type skillMetricsResponse struct {
	AuditEnabled bool                 `json:"audit_enabled"`
	Scope        string               `json:"scope"`
	Headline     skillMetricsHeadline `json:"headline"`
	Skills       []skillMetricsRow    `json:"skills"`
}

// handleSkillMetrics serves GET /api/metrics/skills?since=&until= — the
// per-skill usage list plus the overview headline.
func (s *uiServer) handleSkillMetrics(w http.ResponseWriter, r *http.Request) {
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
	f := s.metricsFilter(sc, r)
	usage, tokens, ok := s.skillRollups(r.Context(), f)

	resp := skillMetricsResponse{
		AuditEnabled: ok,
		Scope:        sc.label(),
		Skills:       mergeUsageWithLock(entries, usage, tokens),
	}
	resp.Headline = buildSkillMetricsHeadline(resp.Skills)
	writeJSON(w, http.StatusOK, resp)
}

// metricsFilter builds the store filter for a request: scope dirs + optional
// since/until window (same date grammar as the sessions list).
func (s *uiServer) metricsFilter(sc uiScope, r *http.Request) *store.MetricsFilter {
	q := r.URL.Query()
	return &store.MetricsFilter{
		Dirs:  sc.auditDirs(),
		Since: parseDateParam(q.Get("since"), false),
		Until: parseDateParam(q.Get("until"), true),
	}
}

// skillRollups fetches the usage + token aggregations, reporting ok=false when
// usage is unmeasurable (no store, or the spans schema isn't there yet).
func (s *uiServer) skillRollups(ctx context.Context, f *store.MetricsFilter) ([]*store.SkillUsage, map[string]*store.TokenTotals, bool) {
	if s.store == nil {
		return nil, nil, false
	}
	usage, err := s.store.SkillUsageRollup(ctx, f)
	if err != nil {
		return nil, nil, false // schemaNotReady or any read failure → unmeasurable
	}
	tokens, err := s.store.SkillTokenRollup(ctx, f)
	if err != nil {
		tokens = map[string]*store.TokenTotals{}
	}
	return usage, tokens, true
}

// mergeUsageWithLock joins span usage with lock entries by skill name: every
// installed skill gets a row (zero usage if never fired), and skills that
// fired historically but are no longer installed keep a row marked
// installed:false. Rows sort by verified work, then invocations, then name.
func mergeUsageWithLock(entries []*model.LockEntry, usage []*store.SkillUsage, tokens map[string]*store.TokenTotals) []skillMetricsRow {
	byName := map[string]*skillMetricsRow{}
	rowFor := func(name string) *skillMetricsRow {
		if row, ok := byName[name]; ok {
			return row
		}
		row := &skillMetricsRow{Name: name}
		byName[name] = row
		return row
	}
	for _, e := range entries {
		row := rowFor(e.Name)
		row.Installed = true
		row.Registry = e.Registry
		row.Ref = e.Ref
		row.Commit = e.Commit
		row.Disabled = e.Disabled
		row.Gate = buildProvenanceView(e).ScanDecision
		if !e.InstalledAt.IsZero() {
			t := e.InstalledAt
			row.InstalledAt = &t
		}
	}
	for _, u := range usage {
		row := rowFor(u.Skill)
		row.Invocations = u.Invocations
		row.Sessions = u.Sessions
		row.Verified = u.Verified
		if u.Invocations > 0 {
			row.VerifiedShare = float64(u.Verified) / float64(u.Invocations)
		}
		row.FirstFired = msToTimePtr(u.FirstFiredMs)
		row.LastFired = msToTimePtr(u.LastFiredMs)
		if t := tokens[u.Skill]; t != nil {
			row.TokensIn = t.InputTokens
			row.TokensOut = t.OutputTokens
			row.TokenSessions = t.Sessions
		}
	}
	out := make([]skillMetricsRow, 0, len(byName))
	for _, row := range byName {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Verified != out[j].Verified {
			return out[i].Verified > out[j].Verified
		}
		if out[i].Invocations != out[j].Invocations {
			return out[i].Invocations > out[j].Invocations
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// buildSkillMetricsHeadline computes the "N installed / M did 90% of verified
// work / K never fired" rollup from the merged rows (already sorted by
// verified work desc, which the core-set accumulation relies on).
func buildSkillMetricsHeadline(rows []skillMetricsRow) skillMetricsHeadline {
	h := skillMetricsHeadline{CoreSkills: []string{}}
	for _, row := range rows {
		h.TotalInvocations += row.Invocations
		h.VerifiedInvocations += row.Verified
		if !row.Installed {
			continue
		}
		h.Installed++
		if row.Invocations > 0 {
			h.Active++
		} else {
			h.NeverFired++
		}
	}
	if h.VerifiedInvocations == 0 {
		return h
	}
	var acc int64
	for _, row := range rows {
		if row.Verified == 0 {
			break // sorted desc — nothing verified beyond this point
		}
		h.CoreSkills = append(h.CoreSkills, row.Name)
		acc += row.Verified
		if float64(acc) >= coreShareThreshold*float64(h.VerifiedInvocations) {
			break
		}
	}
	h.CoreShare = float64(acc) / float64(h.VerifiedInvocations)
	return h
}

func msToTimePtr(ms int64) *time.Time {
	if ms <= 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

// ---- report card -------------------------------------------------------------

// skillReportEntry is the lock identity slice the report card header renders.
type skillReportEntry struct {
	Registry    string     `json:"registry,omitempty"`
	Source      string     `json:"source,omitempty"`
	Ref         string     `json:"ref,omitempty"`
	Commit      string     `json:"commit,omitempty"`
	SubtreeHash string     `json:"subtreeHash,omitempty"`
	Mode        string     `json:"mode,omitempty"`
	Targets     []string   `json:"targets,omitempty"`
	Gate        string     `json:"gate,omitempty"`
	InstalledAt *time.Time `json:"installedAt,omitempty"`
	Disabled    bool       `json:"disabled,omitempty"`
}

type skillReportTotals struct {
	Invocations int64      `json:"invocations"`
	Sessions    int64      `json:"sessions"`
	Verified    int64      `json:"verified"`
	Unverified  int64      `json:"unverified"`
	FirstFired  *time.Time `json:"firstFired,omitempty"`
	LastFired   *time.Time `json:"lastFired,omitempty"`
}

type skillReportAgent struct {
	Agent       string     `json:"agent"`
	Invocations int64      `json:"invocations"`
	Verified    int64      `json:"verified"`
	Sessions    int64      `json:"sessions"`
	LastFired   *time.Time `json:"lastFired,omitempty"`
}

type skillReportSeriesPoint struct {
	Day         string `json:"day"`
	Agent       string `json:"agent"`
	Invocations int64  `json:"invocations"`
	Verified    int64  `json:"verified"`
}

type skillReportTokens struct {
	Sessions int64 `json:"sessions"`
	Input    int64 `json:"input"`
	Output   int64 `json:"output"`
}

// skillReportVersion is one (ref, commit) the skill fired as — the lineage
// row. Current marks the lock's pinned commit.
type skillReportVersion struct {
	Ref         string     `json:"ref,omitempty"`
	Commit      string     `json:"commit,omitempty"`
	Invocations int64      `json:"invocations"`
	Sessions    int64      `json:"sessions"`
	Verified    int64      `json:"verified"`
	FirstFired  *time.Time `json:"firstFired,omitempty"`
	LastFired   *time.Time `json:"lastFired,omitempty"`
	TokensIn    int64      `json:"tokensIn"`
	TokensOut   int64      `json:"tokensOut"`
	Current     bool       `json:"current,omitempty"`
}

type skillReportResponse struct {
	AuditEnabled   bool                     `json:"audit_enabled"`
	Name           string                   `json:"name"`
	Installed      bool                     `json:"installed"`
	Entry          *skillReportEntry        `json:"entry,omitempty"`
	Totals         skillReportTotals        `json:"totals"`
	Agents         []skillReportAgent       `json:"agents"`
	Series         []skillReportSeriesPoint `json:"series"`
	Tokens         skillReportTokens        `json:"tokens"`
	Versions       []skillReportVersion     `json:"versions"`
	RecentSessions []*store.RawSession      `json:"recentSessions"`
}

// handleSkillReport serves GET /api/metrics/skills/{name} — the report card.
// 404 only when the skill is neither installed in scope nor present in spans.
func (s *uiServer) handleSkillReport(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
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
	resp := skillReportResponse{
		Name:           name,
		Agents:         []skillReportAgent{},
		Series:         []skillReportSeriesPoint{},
		Versions:       []skillReportVersion{},
		RecentSessions: []*store.RawSession{},
	}
	var entry *model.LockEntry
	for _, e := range entries {
		if e.Name == name {
			entry = e
			break
		}
	}
	if entry != nil {
		resp.Installed = true
		resp.Entry = reportEntryFor(entry)
	}

	f := s.metricsFilter(sc, r)
	f.Skill = name
	resp.AuditEnabled = s.fillSkillReportMetrics(r.Context(), &resp, f, entry)

	if !resp.Installed && resp.Totals.Invocations == 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("skill %q not installed and never fired in scope", name))
		return
	}
	s.fillReportRecentSessions(r.Context(), &resp, f)
	writeJSON(w, http.StatusOK, resp)
}

// reportEntryFor maps a lock entry onto the report card's identity slice.
func reportEntryFor(e *model.LockEntry) *skillReportEntry {
	out := &skillReportEntry{
		Registry:    e.Registry,
		Source:      e.Source,
		Ref:         e.Ref,
		Commit:      e.Commit,
		SubtreeHash: e.SubtreeHash,
		Mode:        e.Mode,
		Targets:     e.Targets,
		Gate:        buildProvenanceView(e).ScanDecision,
		Disabled:    e.Disabled,
	}
	if !e.InstalledAt.IsZero() {
		t := e.InstalledAt
		out.InstalledAt = &t
	}
	return out
}

// fillSkillReportMetrics populates totals/agents/series/tokens/versions from
// the store aggregations. Returns false when usage is unmeasurable (no store
// or schema not ready) — the payload then carries zero usage honestly.
func (s *uiServer) fillSkillReportMetrics(ctx context.Context, resp *skillReportResponse, f *store.MetricsFilter, entry *model.LockEntry) bool {
	if s.store == nil {
		return false
	}
	usage, err := s.store.SkillUsageRollup(ctx, f)
	if err != nil {
		return false
	}
	if len(usage) > 0 {
		u := usage[0]
		resp.Totals = skillReportTotals{
			Invocations: u.Invocations,
			Sessions:    u.Sessions,
			Verified:    u.Verified,
			Unverified:  u.Invocations - u.Verified,
			FirstFired:  msToTimePtr(u.FirstFiredMs),
			LastFired:   msToTimePtr(u.LastFiredMs),
		}
	}
	if agents, err := s.store.SkillAgentRollup(ctx, f); err == nil {
		for _, a := range agents {
			resp.Agents = append(resp.Agents, skillReportAgent{
				Agent:       a.Agent,
				Invocations: a.Invocations,
				Verified:    a.Verified,
				Sessions:    a.Sessions,
				LastFired:   msToTimePtr(a.LastFiredMs),
			})
		}
	}
	if series, err := s.store.SkillInvocationSeries(ctx, f); err == nil {
		for _, p := range series {
			resp.Series = append(resp.Series, skillReportSeriesPoint{
				Day: p.Day, Agent: p.Agent, Invocations: p.Invocations, Verified: p.Verified,
			})
		}
	}
	if tokens, err := s.store.SkillTokenRollup(ctx, f); err == nil {
		if t := tokens[f.Skill]; t != nil {
			resp.Tokens = skillReportTokens{Sessions: t.Sessions, Input: t.InputTokens, Output: t.OutputTokens}
		}
	}
	if versions, err := s.store.SkillVersionRollup(ctx, f); err == nil {
		for _, v := range versions {
			resp.Versions = append(resp.Versions, skillReportVersion{
				Ref:         v.Ref,
				Commit:      v.Commit,
				Invocations: v.Invocations,
				Sessions:    v.Sessions,
				Verified:    v.Verified,
				FirstFired:  msToTimePtr(v.FirstFiredMs),
				LastFired:   msToTimePtr(v.LastFiredMs),
				TokensIn:    v.InputTokens,
				TokensOut:   v.OutputTokens,
				Current:     entry != nil && commitsMatch(entry.Commit, v.Commit),
			})
		}
	}
	return true
}

// commitsMatch compares two commit identifiers tolerating short-vs-full SHA.
func commitsMatch(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	return strings.HasPrefix(b, a)
}

// fillReportRecentSessions attaches the skill's most recent sessions, titled.
func (s *uiServer) fillReportRecentSessions(ctx context.Context, resp *skillReportResponse, f *store.MetricsFilter) {
	if s.store == nil {
		return
	}
	sessions, err := s.store.ListRawSessions(ctx, &store.RawSessionFilter{
		Skill: f.Skill,
		Dirs:  f.Dirs,
		Limit: 8,
	})
	if err != nil || sessions == nil {
		return
	}
	s.populateTitles(ctx, sessions)
	s.populateSessionSkills(ctx, sessions)
	resp.RecentSessions = sessions
}

// ---- dead weight ---------------------------------------------------------------

// deadweightRow is one installed-but-never-fired skill. The prune command
// string is built client-side; this ships data only.
type deadweightRow struct {
	Name        string     `json:"name"`
	Registry    string     `json:"registry,omitempty"`
	Ref         string     `json:"ref,omitempty"`
	Scope       string     `json:"scope,omitempty"` // set in --all view only
	InstalledAt *time.Time `json:"installedAt,omitempty"`
	AgeDays     int        `json:"ageDays"`
	Disabled    bool       `json:"disabled,omitempty"`
}

type deadweightResponse struct {
	AuditEnabled bool            `json:"audit_enabled"`
	Scope        string          `json:"scope"`
	Rows         []deadweightRow `json:"rows"`
}

// handleDeadweight serves GET /api/metrics/deadweight — lock entries with zero
// SKILL spans ever (no window: "never fired" means never). When usage is
// unmeasurable the response says audit_enabled:false with no rows, so the UI
// can point at `qvr audit enable` instead of suggesting a mass prune.
func (s *uiServer) handleDeadweight(w http.ResponseWriter, r *http.Request) {
	sc, err := s.resolveScope(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp := deadweightResponse{Scope: sc.label(), Rows: []deadweightRow{}}
	usage, _, ok := s.skillRollups(r.Context(), &store.MetricsFilter{Dirs: sc.auditDirs()})
	resp.AuditEnabled = ok
	if !ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	fired := make(map[string]struct{}, len(usage))
	for _, u := range usage {
		fired[u.Skill] = struct{}{}
	}

	locks, err := loadScopedLocks(sc.projectRoot, sc.global, sc.all)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	now := time.Now().UTC()
	for _, sl := range locks {
		if sl.Lock == nil {
			continue
		}
		for _, e := range sl.Lock.Entries() {
			if _, ok := fired[e.Name]; ok {
				continue
			}
			row := deadweightRow{
				Name:     e.Name,
				Registry: e.Registry,
				Ref:      e.Ref,
				Disabled: e.Disabled,
			}
			if sc.all {
				row.Scope = sl.Scope
			}
			if !e.InstalledAt.IsZero() {
				t := e.InstalledAt
				row.InstalledAt = &t
				row.AgeDays = int(now.Sub(e.InstalledAt).Hours() / 24)
			}
			resp.Rows = append(resp.Rows, row)
		}
	}
	sort.Slice(resp.Rows, func(i, j int) bool {
		if resp.Rows[i].AgeDays != resp.Rows[j].AgeDays {
			return resp.Rows[i].AgeDays > resp.Rows[j].AgeDays
		}
		return resp.Rows[i].Name < resp.Rows[j].Name
	})
	writeJSON(w, http.StatusOK, resp)
}
