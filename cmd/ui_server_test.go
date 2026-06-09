package cmd

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/git"
	"github.com/astra-sh/qvr/internal/model"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/derive"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/astra-sh/qvr/internal/registry"
	"github.com/google/uuid"

	_ "modernc.org/sqlite"
)

// seedUIEnv wires a temp QUIVER_HOME + project lock + a seeded audit store and
// returns the project root plus the seeded session ID. Mirrors the lock-writing
// pattern in tree_test.go and the store seeding in the ops/store tests.
func seedUIEnv(t *testing.T, withStore bool) (projectRoot string, sessionID uuid.UUID) {
	t.Helper()
	t.Setenv("QUIVER_HOME", t.TempDir())
	projectRoot = t.TempDir()

	// Project lock: one normal remote skill + one edit-mode skill pointed at a
	// real testdata fixture so POST /api/scan/<name> has bytes to scan.
	validSkill, err := filepath.Abs(filepath.Join("..", "testdata", "valid-skill"))
	if err != nil {
		t.Fatalf("abs testdata: %v", err)
	}
	lock := model.NewLockFile(filepath.Join(projectRoot, model.LockFileName))
	lock.Put(&model.LockEntry{
		Name: "code-reviewer", Registry: "acme", Source: "git@x:acme.git",
		Ref: "v1.2.0", Commit: "a1b2c3d4567", Targets: []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	lock.Put(&model.LockEntry{
		Name: "valid-skill", Registry: "acme", Source: "git@x:acme.git",
		Ref: "v1", Mode: model.ModeEdit, EditPath: validSkill,
		Targets:     []string{"claude"},
		InstalledAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	})
	if err := lock.Write(); err != nil {
		t.Fatalf("write lock: %v", err)
	}

	if !withStore {
		return projectRoot, uuid.Nil
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	sessionID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	// Skill-bearing so the UI's SkillsOnly filter surfaces it. Uses a different
	// skill than addSkillSession's "code-review" so the per-skill filter tests
	// stay unambiguous.
	seedRawSession(t, st, sessionID, projectRoot, "claude-code", "code-reviewer")
	if err := st.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}
	return projectRoot, sessionID
}

// seedRawSession writes a couple of verbatim transcript rows for a session and
// persists its derived spans — the raw-only equivalent of seeding an event.
// Any skills passed are attached as extra SKILL-attributed spans so the session
// counts as skill-bearing (the UI's SkillsOnly surfacing filter keeps it); with
// none, the session is skill-less and the dashboard hides it.
func seedRawSession(t *testing.T, st store.Store, sessionID uuid.UUID, workingDir, agent string, skills ...string) {
	t.Helper()
	ctx := context.Background()
	rows := []*ops.RawTrace{
		{
			AgentName: agent, SessionID: sessionID, Source: ops.RawSourceTranscript,
			WorkingDirectory: workingDir, CapturedAt: time.Now().UTC(),
			Raw: []byte(`{"type":"user","timestamp":"2026-06-02T00:00:00.000Z","message":{"role":"user","content":"hi"}}`),
		},
		{
			AgentName: agent, SessionID: sessionID, Source: ops.RawSourceTranscript,
			WorkingDirectory: workingDir, CapturedAt: time.Now().UTC(),
			Raw: []byte(`{"type":"assistant","timestamp":"2026-06-02T00:00:01.000Z","message":{"role":"assistant","model":"claude-opus-4-8","usage":{"input_tokens":5,"output_tokens":2},"content":[{"type":"text","text":"ok"}]}}`),
		},
	}
	if err := st.AppendRawTraces(ctx, rows, nil); err != nil {
		t.Fatalf("seed raw traces: %v", err)
	}
	spans, _ := derive.DeriveSession(rows)
	srows := make([]*store.SpanRow, 0, len(spans)+len(skills))
	for _, sp := range spans {
		attrs, _ := json.Marshal(sp.Attributes)
		srows = append(srows, &store.SpanRow{
			SpanID: sp.SpanID, TraceID: sp.TraceID, ParentSpanID: sp.ParentSpanID,
			SessionID: sessionID, AgentName: agent, Kind: sp.Kind, Name: sp.Name,
			StartMs: sp.StartMs, EndMs: sp.EndMs, Attributes: string(attrs),
			DeriverVersion: derive.Version,
		})
	}
	for i, sk := range skills {
		attrs, _ := json.Marshal(map[string]any{"skill.name": sk})
		srows = append(srows, &store.SpanRow{
			SpanID: fmt.Sprintf("skillspan-%s-%d", sessionID, i), TraceID: "tr",
			SessionID: sessionID, AgentName: agent, Kind: "SKILL",
			Name: "execute_tool", StartMs: 1, EndMs: 2,
			Attributes: string(attrs), DeriverVersion: derive.Version,
		})
	}
	if err := st.ReplaceSessionSpans(ctx, sessionID, srows); err != nil {
		t.Fatalf("seed spans: %v", err)
	}
}

// newUITestServer builds a uiServer over the seeded env and returns its handler.
func newUITestServer(t *testing.T, projectRoot string) http.Handler {
	t.Helper()
	srv, err := buildUIServer(context.Background(), projectRoot, false, false, "test")
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.handler()
}

// do issues a request against the handler and returns the recorder.
func do(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestUI_Health(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/health")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		OK           bool `json:"ok"`
		AuditEnabled bool `json:"audit_enabled"`
	}
	mustJSON(t, rec, &body)
	if !body.OK || !body.AuditEnabled {
		t.Errorf("health = %+v, want ok+audit_enabled", body)
	}
}

func TestUI_Overview(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/overview")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var ov overviewResponse
	mustJSON(t, rec, &ov)
	if !ov.AuditEnabled {
		t.Errorf("audit_enabled = false, want true")
	}
	if ov.Skills != 2 {
		t.Errorf("skills = %d, want 2", ov.Skills)
	}
	if ov.Sessions < 1 {
		t.Errorf("sessions = %d, want >=1", ov.Sessions)
	}
	if len(ov.RecentSessions) < 1 {
		t.Errorf("recent_sessions empty, want >=1")
	}
	if ov.Registries != 1 {
		t.Errorf("registries = %d, want 1", ov.Registries)
	}
}

func TestUI_Sessions(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var sessions []*store.RawSession
	mustJSON(t, rec, &sessions)
	if len(sessions) < 1 {
		t.Fatalf("sessions empty, want >=1")
	}
	if sessions[0].AgentName != "claude-code" {
		t.Errorf("agent = %q, want claude-code", sessions[0].AgentName)
	}
	// The session is named by its first prompt (seeded as "hi"), not its agent.
	if sessions[0].Title != "hi" {
		t.Errorf("title = %q, want %q (first prompt)", sessions[0].Title, "hi")
	}
}

func TestUI_SessionDetail(t *testing.T) {
	root, id := seedUIEnv(t, true)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/sessions/"+id.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var detail sessionDetail
	mustJSON(t, rec, &detail)
	if detail.Session == nil || detail.Session.SessionID != id {
		t.Errorf("session id mismatch: got %v want %v", detail.Session, id)
	}
	// Raw rows back the dashboard's raw-trace toggle.
	if len(detail.Traces) < 1 {
		t.Errorf("traces empty, want >=1 raw rows")
	}
	// The derived spans back the dashboard's session timeline.
	if len(detail.Spans) < 1 {
		t.Fatalf("spans empty, want >=1")
	}

	// Bad UUID -> 400; unknown UUID -> 200 with an empty span list.
	if rec := do(t, h, http.MethodGet, "/api/sessions/not-a-uuid"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id status = %d, want 400", rec.Code)
	}
	rec = do(t, h, http.MethodGet, "/api/sessions/"+uuid.New().String())
	if rec.Code != http.StatusOK {
		t.Errorf("unknown id status = %d, want 200 (empty spans)", rec.Code)
	}
	var empty sessionDetail
	mustJSON(t, rec, &empty)
	if len(empty.Spans) != 0 {
		t.Errorf("unknown session should have 0 spans; got %d", len(empty.Spans))
	}
}

func TestUI_Skills(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/skills")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var rows []scopedListEntry
	mustJSON(t, rec, &rows)
	if len(rows) != 2 {
		t.Fatalf("skills = %d, want 2", len(rows))
	}
	if !hasSkill(rows, "code-reviewer") {
		t.Errorf("code-reviewer not in %v", rows)
	}
}

func TestUI_SkillDetail(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/skills/code-reviewer")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var info skillInfo
	mustJSON(t, rec, &info)
	if info.Name != "code-reviewer" {
		t.Errorf("name = %q, want code-reviewer", info.Name)
	}

	if rec := do(t, h, http.MethodGet, "/api/skills/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown skill status = %d, want 404", rec.Code)
	}
}

func TestUI_Tree(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/tree")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var groups []treeGroup
	mustJSON(t, rec, &groups)
	if len(groups) < 1 {
		t.Errorf("tree empty, want >=1 group")
	}
}

func TestUI_Provenance(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/provenance")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var views []*provenanceView
	mustJSON(t, rec, &views)
	if len(views) != 2 {
		t.Errorf("provenance views = %d, want 2", len(views))
	}
}

func TestUI_ScanSummary(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/scan")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var rows []scanSummaryRow
	mustJSON(t, rec, &rows)
	if len(rows) != 2 {
		t.Errorf("scan rows = %d, want 2", len(rows))
	}
}

func TestUI_ScanRun(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodPost, "/api/scan/valid-skill")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var result struct {
		Skill    string `json:"skill"`
		Findings []any  `json:"findings"`
	}
	mustJSON(t, rec, &result)
	if result.Skill == "" {
		t.Errorf("scan result has empty skill name")
	}

	// Unknown skill -> 404.
	if rec := do(t, h, http.MethodPost, "/api/scan/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown scan target status = %d, want 404", rec.Code)
	}
}

func TestUI_StaticStubAndAPI404(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)

	// No real bundle built in test -> stub HTML served for SPA paths.
	rec := do(t, h, http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("/ content-type = %q, want text/html", ct)
	}

	// Unmatched /api/* stays JSON 404, not HTML.
	rec = do(t, h, http.MethodGet, "/api/bogus")
	if rec.Code != http.StatusNotFound {
		t.Errorf("/api/bogus status = %d, want 404", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("/api/bogus content-type = %q, want application/json", ct)
	}
}

func TestUI_DBAbsentDegrades(t *testing.T) {
	root, _ := seedUIEnv(t, false) // no store seeded -> no skillops.db
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/health")
	var health struct {
		AuditEnabled bool `json:"audit_enabled"`
	}
	mustJSON(t, rec, &health)
	if health.AuditEnabled {
		t.Errorf("audit_enabled = true, want false (no DB)")
	}

	// Sessions returns empty array, not 500.
	rec = do(t, h, http.MethodGet, "/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("sessions status = %d, want 200", rec.Code)
	}
	var sessions []*store.RawSession
	mustJSON(t, rec, &sessions)
	if len(sessions) != 0 {
		t.Errorf("sessions = %d, want 0", len(sessions))
	}

	// Skills still work without the audit DB.
	rec = do(t, h, http.MethodGet, "/api/skills")
	if rec.Code != http.StatusOK {
		t.Errorf("skills status = %d, want 200", rec.Code)
	}
}

// newUITestServerScoped builds a uiServer with explicit --global/--all flags so
// scope-sensitive tests (issue #141) can exercise both lenses over one store.
func newUITestServerScoped(t *testing.T, projectRoot string, global, all bool) http.Handler {
	t.Helper()
	srv, err := buildUIServer(context.Background(), projectRoot, global, all, "test")
	if err != nil {
		t.Fatalf("build server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv.handler()
}

// dbPathForTest resolves the seeded audit DB path within the test's QUIVER_HOME.
func dbPathForTest(t *testing.T) string {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return ops.DBPath(cfg)
}

// addSession seeds one raw session pinned to workingDir, for scope tests.
func addSession(t *testing.T, workingDir, agentSessionID string) {
	t.Helper()
	st, err := store.Open(context.Background(), store.OpenOptions{Path: dbPathForTest(t)})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	seedRawSession(t, st, uuid.NewSHA1(uuid.NameSpaceOID, []byte(agentSessionID)), workingDir, "claude-code")
}

// insertRawSession writes a raw_traces row with a non-UUID session id, for the
// malformed-id resilience test (the session list must skip it, not blank out).
func insertRawSession(t *testing.T, id string) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPathForTest(t))
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(
		`INSERT INTO raw_traces(id, agent_name, session_id, source, seq, captured_at, raw)
		 VALUES(?,?,?,?,?,?,?)`,
		uuid.New().String(), "legacy", id, "transcript", 0, time.Now().UTC(), []byte("{}"),
	); err != nil {
		t.Fatalf("insert raw session: %v", err)
	}
}

// TestUI_ScanRunGate asserts the live re-scan returns a gate verdict computed
// under the recorded gate's policy, so recorded vs live compare 1:1 (issue #140).
func TestUI_ScanRunGate(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodPost, "/api/scan/valid-skill")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Skill string `json:"skill"`
		Gate  struct {
			Decision  string               `json:"decision"`
			Threshold string               `json:"threshold"`
			Counts    model.SeverityCounts `json:"counts"`
		} `json:"gate"`
	}
	mustJSON(t, rec, &resp)
	if resp.Gate.Decision != "allowed" && resp.Gate.Decision != "blocked" {
		t.Errorf("gate.decision = %q, want allowed|blocked", resp.Gate.Decision)
	}
	if resp.Gate.Threshold == "" {
		t.Errorf("gate.threshold empty, want the recorded block_severity policy")
	}
	// The clean fixture must not block under the default critical threshold.
	if resp.Gate.Decision != "allowed" {
		t.Errorf("gate.decision = %q for clean fixture, want allowed", resp.Gate.Decision)
	}
}

// TestUI_AuditScope asserts sessions/events rescope with --global the same way
// skills do, instead of always reading the whole DB (issue #141).
func TestUI_AuditScope(t *testing.T) {
	root, _ := seedUIEnv(t, true)         // one session pinned to the project root
	addSession(t, t.TempDir(), "other-1") // one session in an unrelated dir

	var proj overviewResponse
	mustJSON(t, do(t, newUITestServerScoped(t, root, false, false), http.MethodGet, "/api/overview"), &proj)
	if proj.Scope != "project" {
		t.Errorf("project scope = %q, want project", proj.Scope)
	}
	if proj.Sessions != 1 {
		t.Errorf("project sessions = %d, want 1 (in-scope only)", proj.Sessions)
	}

	var glob overviewResponse
	mustJSON(t, do(t, newUITestServerScoped(t, root, true, false), http.MethodGet, "/api/overview"), &glob)
	if glob.Scope != "global" {
		t.Errorf("global scope = %q, want global", glob.Scope)
	}
	if glob.Sessions != 2 {
		t.Errorf("global sessions = %d, want 2 (all dirs)", glob.Sessions)
	}

	// The Sessions list endpoint must rescope identically to the overview count.
	var projList []*store.RawSession
	mustJSON(t, do(t, newUITestServerScoped(t, root, false, false), http.MethodGet, "/api/sessions"), &projList)
	if len(projList) != 1 {
		t.Errorf("project sessions list = %d, want 1", len(projList))
	}
}

// TestUI_SessionsMalformedID asserts one non-UUID row doesn't blank the whole
// list — the good rows still come back (issue #142).
func TestUI_SessionsMalformedID(t *testing.T) {
	root, _ := seedUIEnv(t, true) // one good (UUID) session in scope
	insertRawSession(t, "not-a-uuid")

	h := newUITestServerScoped(t, root, true, false) // global so dir scope doesn't hide rows
	rec := do(t, h, http.MethodGet, "/api/sessions")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (one bad row must not fail the list); body: %s",
			rec.Code, rec.Body.String())
	}
	var sessions []*store.RawSession
	mustJSON(t, rec, &sessions)
	// The malformed row is skipped; the good one survives.
	if len(sessions) != 1 {
		t.Errorf("sessions = %d, want 1 (good row kept, bad row skipped)", len(sessions))
	}
}

// addSkillSession seeds a codex session in workingDir that used the code-review
// skill (a skill-attributed span), for the Sessions filter test.
func addSkillSession(t *testing.T, workingDir string) {
	t.Helper()
	st, err := store.Open(context.Background(), store.OpenOptions{Path: dbPathForTest(t)})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()
	ctx := context.Background()
	id := uuid.New()
	if err := st.AppendRawTraces(ctx, []*ops.RawTrace{{
		AgentName: "codex", SessionID: id, Source: ops.RawSourceTranscript,
		WorkingDirectory: workingDir, CapturedAt: time.Now().UTC(),
		Raw: []byte(`{"type":"event_msg","payload":{"type":"user_message","message":"review"}}`),
	}}, nil); err != nil {
		t.Fatalf("seed raw: %v", err)
	}
	attrs, _ := json.Marshal(map[string]any{"skill.name": "code-review"})
	if err := st.ReplaceSessionSpans(ctx, id, []*store.SpanRow{{
		SpanID: "skillspan", TraceID: "tr", SessionID: id, AgentName: "codex",
		Kind: "SKILL", Name: "execute_tool exec_command", StartMs: 1, EndMs: 2,
		Attributes: string(attrs), DeriverVersion: derive.Version,
	}}); err != nil {
		t.Fatalf("seed span: %v", err)
	}
}

// TestUI_SessionsFilters exercises the harness/skill filters and the
// skills-per-session payload over the HTTP layer.
func TestUI_SessionsFilters(t *testing.T) {
	root, _ := seedUIEnv(t, true) // one claude session ("hi") that used code-reviewer
	addSkillSession(t, root)      // one codex session that used code-review
	h := newUITestServer(t, root)

	var all []*store.RawSession
	mustJSON(t, do(t, h, http.MethodGet, "/api/sessions"), &all)
	if len(all) != 2 {
		t.Fatalf("unfiltered sessions = %d, want 2", len(all))
	}

	// Harness filter narrows to the codex session.
	var codexOnly []*store.RawSession
	mustJSON(t, do(t, h, http.MethodGet, "/api/sessions?agent=codex"), &codexOnly)
	if len(codexOnly) != 1 || codexOnly[0].AgentName != "codex" {
		t.Errorf("agent filter = %+v, want one codex session", codexOnly)
	}

	// Skill filter narrows to the skill-using session, with skills populated.
	var skillOnly []*store.RawSession
	mustJSON(t, do(t, h, http.MethodGet, "/api/sessions?skill=code-review"), &skillOnly)
	if len(skillOnly) != 1 {
		t.Fatalf("skill filter = %d sessions, want 1", len(skillOnly))
	}
	if len(skillOnly[0].Skills) != 1 || skillOnly[0].Skills[0] != "code-review" {
		t.Errorf("session.skills = %v, want [code-review]", skillOnly[0].Skills)
	}

	// A skill nobody used returns nothing.
	var none []*store.RawSession
	mustJSON(t, do(t, h, http.MethodGet, "/api/sessions?skill=nope"), &none)
	if len(none) != 0 {
		t.Errorf("unknown skill filter = %d, want 0", len(none))
	}
}

// TestUI_SessionsExcludeSkilless asserts the dashboard surfaces only
// skill-bearing sessions: a skill-less session that lingers in the DB (e.g. it
// never ended, so capture's retention gate hasn't pruned it) is hidden, while a
// multi-skill session shows every skill it used.
func TestUI_SessionsExcludeSkilless(t *testing.T) {
	root, _ := seedUIEnv(t, true) // one skill-bearing claude session (code-reviewer)

	st, err := store.Open(context.Background(), store.OpenOptions{Path: dbPathForTest(t)})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	// A skill-less session in scope — present in the DB, must not surface.
	seedRawSession(t, st, uuid.New(), root, "claude-code")
	// A multi-skill session in scope — surfaces, tagged with both skills.
	multiID := uuid.New()
	seedRawSession(t, st, multiID, root, "claude-code", "alpha-skill", "beta-skill")
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	h := newUITestServer(t, root)
	var sessions []*store.RawSession
	mustJSON(t, do(t, h, http.MethodGet, "/api/sessions"), &sessions)

	// Exactly the two skill-bearing sessions (seedUIEnv's + the multi-skill one);
	// the skill-less one is excluded.
	if len(sessions) != 2 {
		t.Fatalf("sessions = %d, want 2 (skill-less hidden)", len(sessions))
	}
	var multi *store.RawSession
	for _, s := range sessions {
		if s.SessionID == multiID {
			multi = s
		}
	}
	if multi == nil {
		t.Fatalf("multi-skill session %s missing from list", multiID)
	}
	if len(multi.Skills) != 2 {
		t.Errorf("multi.Skills = %v, want both alpha-skill and beta-skill", multi.Skills)
	}
	for _, want := range []string{"alpha-skill", "beta-skill"} {
		found := false
		for _, got := range multi.Skills {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Errorf("multi.Skills %v missing %q", multi.Skills, want)
		}
	}
}

// TestUI_Registries asserts the global registry list is served from config,
// independent of project scope.
func TestUI_Registries(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	// Seed one registry into the temp config.
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	cfg.Registries = map[string]config.RegistryConfig{"acme": {URL: "git@x:acme.git"}}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}

	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/registries")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var regs []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	mustJSON(t, rec, &regs)
	if len(regs) != 1 || regs[0].Name != "acme" {
		t.Errorf("registries = %+v, want one named acme", regs)
	}
}

// TestUI_Projects asserts the switcher data lists a Global entry plus the launch
// project (even though it was never `qvr add`ed into projects.json), with the
// right skill/session counts.
func TestUI_Projects(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/projects")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var projects []projectSummary
	mustJSON(t, rec, &projects)
	if len(projects) < 2 {
		t.Fatalf("projects = %d, want >=2 (Global + launch)", len(projects))
	}
	if projects[0].Scope != "global" {
		t.Errorf("projects[0].scope = %q, want global (pinned first)", projects[0].Scope)
	}
	var launch *projectSummary
	for i := range projects {
		if projects[i].Path == root {
			launch = &projects[i]
		}
	}
	if launch == nil {
		t.Fatalf("launch project %q not in %+v", root, projects)
	}
	if !launch.Current || !launch.HasLock {
		t.Errorf("launch project = %+v, want current+hasLock", *launch)
	}
	if launch.Skills != 2 {
		t.Errorf("launch skills = %d, want 2", launch.Skills)
	}
	if launch.Sessions != 1 {
		t.Errorf("launch sessions = %d, want 1 (scoped to working dir)", launch.Sessions)
	}
}

// TestUI_ProjectScopeParam asserts ?project= scopes to a known project and
// rejects an unknown path (the dashboard never reads a lock from an arbitrary
// client-supplied path).
func TestUI_ProjectScopeParam(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)

	// Known project (the launch root) → its 2 skills.
	rec := do(t, h, http.MethodGet, "/api/skills?project="+url.QueryEscape(root))
	if rec.Code != http.StatusOK {
		t.Fatalf("known project status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var rows []scopedListEntry
	mustJSON(t, rec, &rows)
	if len(rows) != 2 {
		t.Errorf("scoped skills = %d, want 2", len(rows))
	}

	// Unknown project → 400, no lock read.
	if rec := do(t, h, http.MethodGet, "/api/skills?project=/nope/not/a/project"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown project status = %d, want 400", rec.Code)
	}
}

// ---- helpers ----

func mustJSON(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode json: %v (body: %s)", err, rec.Body.String())
	}
}

func hasSkill(rows []scopedListEntry, name string) bool {
	for _, r := range rows {
		if r.Name == name {
			return true
		}
	}
	return false
}

// seedBareRegistry builds a one-skill remote repo (a SKILL.md plus a nested
// script) and bare-clones it into the registry path under name, so handlers
// that read straight from the bare clone (file listing, ref versions) have real
// content to walk. Returns the registry name. Assumes QUIVER_HOME is already set
// (seedUIEnv does this).
func seedBareRegistry(t *testing.T, name string) {
	t.Helper()
	remote := filepath.Join(t.TempDir(), "remote")
	skillDir := filepath.Join(remote, "skills", "demo")
	if err := os.MkdirAll(filepath.Join(skillDir, "scripts"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := "---\nname: demo\ndescription: a demo skill\n---\n# demo\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o755); err != nil {
		t.Fatalf("write run.sh: %v", err)
	}
	gitCmd(t, remote, "init", "-q", "-b", "main")
	gitCmd(t, remote, "add", "-A")
	gitCmd(t, remote, "-c", "user.email=t@t.t", "-c", "user.name=t", "commit", "-q", "-m", "init")

	bare := registry.RegistryPath(name)
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatalf("mkdir bare parent: %v", err)
	}
	if err := git.NewGoGitClient().BareClone(context.Background(), remote, bare, git.CloneOptions{AllRefs: true}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Registries == nil {
		cfg.Registries = map[string]config.RegistryConfig{}
	}
	cfg.Registries[name] = config.RegistryConfig{URL: remote}
	if err := config.Save(cfg); err != nil {
		t.Fatalf("save config: %v", err)
	}
}

// TestUI_RegistrySkill exercises the registry-scope skill detail endpoint: it
// must list the skill's files (skill-relative, recursive) straight from the bare
// clone and carry the repo's version timeline, without the skill being installed.
func TestUI_RegistrySkill(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	seedBareRegistry(t, "demo-reg")
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/registries/demo-reg/skills/demo")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var got registrySkillDetail
	mustJSON(t, rec, &got)
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if got.Name != "demo" || got.Registry != "demo-reg" {
		t.Errorf("identity = %q/%q, want demo/demo-reg", got.Name, got.Registry)
	}
	if got.Path != "skills/demo" {
		t.Errorf("path = %q, want skills/demo", got.Path)
	}
	if got.Installed {
		t.Errorf("installed = true, want false (skill not in lock)")
	}
	assertRegistrySkillFiles(t, got)
	assertRegistrySkillVersions(t, got)
}

// assertRegistrySkillFiles checks the skill's file list is skill-relative and
// carries the expected entries.
func assertRegistrySkillFiles(t *testing.T, got registrySkillDetail) {
	t.Helper()
	wantFiles := map[string]bool{"SKILL.md": false, "scripts/run.sh": false}
	for _, f := range got.Files {
		if _, ok := wantFiles[f]; ok {
			wantFiles[f] = true
		}
		if strings.HasPrefix(f, "skills/demo/") {
			t.Errorf("file %q is repo-relative, want skill-relative", f)
		}
	}
	for f, seen := range wantFiles {
		if !seen {
			t.Errorf("missing file %q in %v", f, got.Files)
		}
	}
}

// assertRegistrySkillVersions checks the version timeline includes the main
// branch, marked current as the default branch.
func assertRegistrySkillVersions(t *testing.T, got registrySkillDetail) {
	t.Helper()
	var foundMain bool
	for _, v := range got.Versions {
		if v.Ref == "main" {
			foundMain = true
			if !v.Current {
				t.Errorf("main should be marked current (default branch)")
			}
		}
	}
	if !foundMain {
		t.Errorf("versions = %+v, want a main branch", got.Versions)
	}
}

// TestUI_RegistrySkill_UnknownRegistry asserts a missing registry 404s.
func TestUI_RegistrySkill_UnknownRegistry(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	rec := do(t, h, http.MethodGet, "/api/registries/nope/skills/demo")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
