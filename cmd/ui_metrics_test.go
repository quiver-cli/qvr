package cmd

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/astra-sh/qvr/internal/config"
	"github.com/astra-sh/qvr/internal/ops"
	"github.com/astra-sh/qvr/internal/ops/store"
	"github.com/google/uuid"
)

// seedVerifiedMetricsSession adds a second session to the seeded env's store:
// one LLM span carrying token usage and one SKILL span for code-reviewer with
// proven identity (the lock's commit + version). Together with seedUIEnv's
// identity-less skill span this gives the metrics endpoints a mixed
// known/unknown-version, multi-version fixture.
func seedVerifiedMetricsSession(t *testing.T, projectRoot string) uuid.UUID {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, store.OpenOptions{Path: ops.DBPath(cfg)})
	if err != nil {
		t.Fatalf("open seed store: %v", err)
	}
	defer func() { _ = st.Close() }()

	sessionID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	err = st.AppendRawTraces(ctx, []*ops.RawTrace{{
		AgentName: "claude", SessionID: sessionID, Source: ops.RawSourceTranscript,
		WorkingDirectory: projectRoot, CapturedAt: time.Now().UTC(),
		Raw: []byte(`{"type":"user","timestamp":"2026-06-03T00:00:00.000Z","message":{"role":"user","content":"review this"}}`),
	}}, nil)
	if err != nil {
		t.Fatalf("seed raw traces: %v", err)
	}

	startMs := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC).UnixMilli()
	rows := []*store.SpanRow{
		{
			SpanID: "m-llm-1", TraceID: "m-tr", SessionID: sessionID, AgentName: "claude",
			Kind: "LLM", Name: "turn", StartMs: startMs, EndMs: startMs + 1000,
			Attributes: `{"gen_ai.operation.name":"chat","gen_ai.usage.input_tokens":100,"gen_ai.usage.output_tokens":40}`,
		},
		{
			SpanID: "m-skill-1", TraceID: "m-tr", SessionID: sessionID, AgentName: "claude",
			Kind: "SKILL", Name: "code-reviewer", StartMs: startMs, EndMs: startMs,
			Attributes: `{"skill.name":"code-reviewer","skill.verified":true,"skill.commit":"a1b2c3d4567","skill.version":"v1.2.0"}`,
		},
	}
	if err := st.ReplaceSessionSpans(ctx, sessionID, rows); err != nil {
		t.Fatalf("seed spans: %v", err)
	}
	return sessionID
}

func TestUI_MetricsSkills(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	seedVerifiedMetricsSession(t, root)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/metrics/skills")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp skillMetricsResponse
	mustJSON(t, rec, &resp)

	if !resp.AuditEnabled {
		t.Fatalf("audit_enabled = false, want true")
	}
	if resp.Scope != "project" {
		t.Errorf("scope = %q, want project", resp.Scope)
	}
	assertMetricsHeadline(t, resp.Headline)
	assertMetricsRows(t, resp.Skills)
}

func assertMetricsHeadline(t *testing.T, h1 skillMetricsHeadline) {
	t.Helper()
	if h1.Installed != 2 || h1.Active != 1 || h1.NeverFired != 1 {
		t.Errorf("headline installed/active/never = %d/%d/%d, want 2/1/1",
			h1.Installed, h1.Active, h1.NeverFired)
	}
	if h1.TotalInvocations != 2 {
		t.Errorf("headline invocations = %d total, want 2", h1.TotalInvocations)
	}
	if len(h1.CoreSkills) != 1 || h1.CoreSkills[0] != "code-reviewer" || h1.CoreShare != 1.0 {
		t.Errorf("core = %v @ %.2f, want [code-reviewer] @ 1.00", h1.CoreSkills, h1.CoreShare)
	}
}

func assertMetricsRows(t *testing.T, skills []skillMetricsRow) {
	t.Helper()
	rows := map[string]skillMetricsRow{}
	for _, row := range skills {
		rows[row.Name] = row
	}
	cr, ok := rows["code-reviewer"]
	if !ok {
		t.Fatalf("code-reviewer row missing")
	}
	if !cr.Installed || cr.Invocations != 2 || cr.Sessions != 2 {
		t.Errorf("code-reviewer = %+v, want installed 2 inv / 2 sessions", cr)
	}
	if len(cr.Versions) != 1 || cr.Versions[0] != "v1.2.0" {
		t.Errorf("code-reviewer versions = %v, want [v1.2.0] (the proven span's ref)", cr.Versions)
	}
	// Token join: both sessions' LLM usage sums (5+100 in, 2+40 out), counted
	// once per session regardless of how many skill spans the session carried.
	if cr.TokensIn != 105 || cr.TokensOut != 42 {
		t.Errorf("code-reviewer tokens = %d/%d, want 105/42", cr.TokensIn, cr.TokensOut)
	}
	vs, ok := rows["valid-skill"]
	if !ok {
		t.Fatalf("valid-skill row missing (installed but never fired must still appear)")
	}
	if !vs.Installed || vs.Invocations != 0 {
		t.Errorf("valid-skill = %+v, want installed with 0 invocations", vs)
	}
}

func TestUI_MetricsSkills_NoStore(t *testing.T) {
	root, _ := seedUIEnv(t, false)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/metrics/skills")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp skillMetricsResponse
	mustJSON(t, rec, &resp)
	if resp.AuditEnabled {
		t.Errorf("audit_enabled = true, want false without a store")
	}
	// Lock entries still appear (zero usage) so the skills list renders.
	if len(resp.Skills) != 2 {
		t.Errorf("skills = %d rows, want 2 lock entries", len(resp.Skills))
	}
	if resp.Headline.Installed != 2 {
		t.Errorf("installed = %d, want 2", resp.Headline.Installed)
	}
}

func TestUI_MetricsSkillReport(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	seedVerifiedMetricsSession(t, root)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/metrics/skills/code-reviewer")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp skillReportResponse
	mustJSON(t, rec, &resp)

	if !resp.Installed || resp.Entry == nil {
		t.Fatalf("report = installed %v entry %v, want installed with entry", resp.Installed, resp.Entry)
	}
	if resp.Entry.Registry != "acme" || resp.Entry.Ref != "v1.2.0" {
		t.Errorf("entry = %+v, want acme @ v1.2.0", resp.Entry)
	}
	assertReportMetrics(t, &resp)
	assertReportVersions(t, resp.Versions)
	if len(resp.RecentSessions) < 1 {
		t.Errorf("recentSessions empty, want >=1")
	}
}

func assertReportMetrics(t *testing.T, resp *skillReportResponse) {
	t.Helper()
	if resp.Totals.Invocations != 2 {
		t.Errorf("totals = %+v, want 2 invocations", resp.Totals)
	}
	if len(resp.Agents) != 1 || resp.Agents[0].Agent != "claude" {
		t.Errorf("agents = %+v, want one claude row", resp.Agents)
	}
	if len(resp.Agents) == 1 {
		if vs := resp.Agents[0].Versions; len(vs) != 1 || vs[0] != "v1.2.0" {
			t.Errorf("agent versions = %v, want [v1.2.0]; the identity-less invocation stays unknown", vs)
		}
	}
	if len(resp.Series) == 0 {
		t.Errorf("series empty, want day buckets")
	}
	if resp.Tokens.Input != 105 || resp.Tokens.Output != 42 {
		t.Errorf("tokens = %d/%d, want 105/42", resp.Tokens.Input, resp.Tokens.Output)
	}
}

func assertReportVersions(t *testing.T, versions []skillReportVersion) {
	t.Helper()
	// Two versions fired: the lock-verified commit (current) and the
	// identity-less span from the basic seed (empty ref/commit).
	if len(versions) != 2 {
		t.Fatalf("versions = %d, want 2", len(versions))
	}
	var current *skillReportVersion
	for i := range versions {
		if versions[i].Current {
			current = &versions[i]
		}
	}
	if current == nil || current.Commit != "a1b2c3d4567" {
		t.Errorf("current version = %+v, want commit a1b2c3d4567", current)
	}
}

func TestUI_MetricsSkillReport_InstalledNeverFired(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/metrics/skills/valid-skill")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (installed, zero usage)", rec.Code)
	}
	var resp skillReportResponse
	mustJSON(t, rec, &resp)
	if !resp.Installed || resp.Totals.Invocations != 0 {
		t.Errorf("report = installed %v / %d invocations, want installed with 0", resp.Installed, resp.Totals.Invocations)
	}
}

func TestUI_MetricsSkillReport_Unknown(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)
	if rec := do(t, h, http.MethodGet, "/api/metrics/skills/never-heard-of-it"); rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 for a skill neither installed nor fired", rec.Code)
	}
}

func TestUI_MetricsDeadweight(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/metrics/deadweight")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp deadweightResponse
	mustJSON(t, rec, &resp)
	if !resp.AuditEnabled {
		t.Fatalf("audit_enabled = false, want true")
	}
	// code-reviewer fired (seeded span); valid-skill never did → dead weight.
	if len(resp.Rows) != 1 || resp.Rows[0].Name != "valid-skill" {
		t.Fatalf("rows = %+v, want exactly [valid-skill]", resp.Rows)
	}
	if resp.Rows[0].AgeDays <= 0 {
		t.Errorf("ageDays = %d, want > 0 (installed 2026-01-01)", resp.Rows[0].AgeDays)
	}
}

func TestUI_MetricsDeadweight_NoStore(t *testing.T) {
	root, _ := seedUIEnv(t, false)
	h := newUITestServer(t, root)

	rec := do(t, h, http.MethodGet, "/api/metrics/deadweight")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp deadweightResponse
	mustJSON(t, rec, &resp)
	// Unmeasurable usage must NOT read as "prune everything".
	if resp.AuditEnabled || len(resp.Rows) != 0 {
		t.Errorf("no-store deadweight = enabled %v / %d rows, want disabled with none",
			resp.AuditEnabled, len(resp.Rows))
	}
}

func TestUI_Metrics_Rescoping(t *testing.T) {
	root, _ := seedUIEnv(t, true)
	h := newUITestServer(t, root)

	// Global scope is the MACHINE view: the global lock unioned with every
	// known project's lock — including the launch project — so the two skills
	// seeded in the launch project's lock count as installed here too.
	rec := do(t, h, http.MethodGet, "/api/metrics/skills?scope=global")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp skillMetricsResponse
	mustJSON(t, rec, &resp)
	if resp.Scope != "global" {
		t.Errorf("scope = %q, want global", resp.Scope)
	}
	if resp.Headline.Installed != 2 {
		t.Errorf("global installed = %d, want 2 (machine view unions project locks)", resp.Headline.Installed)
	}

	// Unknown ?project= is rejected, mirroring every other scoped endpoint.
	if rec := do(t, h, http.MethodGet, "/api/metrics/skills?project=/nope/nothere"); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown project status = %d, want 400", rec.Code)
	}
}
