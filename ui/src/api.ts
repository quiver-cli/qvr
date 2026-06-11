// Typed client for the qvr ui JSON API. Types mirror the Go structs exactly
// (cmd/ui_server.go and the reused command structs) so the dashboard stays a
// faithful view of CLI truth — if a field changes in Go, it changes here.

import { useCallback, useEffect, useState } from "react";

// SessionMeta mirrors store.SessionMetaRow: the unified session model every
// page lists sessions from. Constructed by each agent's deriver from the
// verbatim raw rows — `title` is the first prompt the user typed, `agent_name`
// is the canonical target name (claude, codex, …), times are epoch ms.
export interface SessionMeta {
  session_id: string;
  agent_name: string;
  source_session_id?: string;
  source_path?: string;
  working_directory?: string;
  git_branch?: string;
  model?: string;
  title?: string;
  started_ms: number;
  ended_ms: number;
  turns: number;
  tools: number;
  // Distinct skills this session used, first-use order.
  skills?: string[];
  deriver_version: number;
  derived_at: string;
}

// SessionFilter narrows the Sessions list server-side. All fields optional; an
// empty filter returns the scoped list unfiltered. Dates are YYYY-MM-DD.
export interface SessionFilter {
  agent?: string;
  skill?: string;
  since?: string;
  until?: string;
}

// SpanRow mirrors store.SpanRow: one derived (processed) span. `attributes` is
// the OpenTelemetry gen_ai.* attribute map serialized as a JSON string. Times
// are epoch milliseconds. kind ∈ LLM | TOOL | SKILL | CHAIN.
export interface SpanRow {
  span_id: string;
  trace_id: string;
  parent_span_id?: string;
  session_id: string;
  agent_name: string;
  kind: string;
  name: string;
  start_ms: number;
  end_ms: number;
  attributes: string; // JSON
  deriver_version: number;
  derived_at: string;
}

// RawTraceView mirrors the UI server's rawTraceView: one verbatim raw row. `raw`
// arrives as inline JSON when the bytes parsed (the common case) or as a JSON
// string otherwise, so it can be pretty-printed directly.
export interface RawTraceView {
  seq: number;
  source: string; // "transcript" | "hook_payload"
  hook_type?: string;
  source_path?: string;
  captured_at: string;
  raw: unknown;
}

// SessionDetail carries both representations of a session so the detail page can
// toggle between the processed span timeline and the lossless raw rows.
export interface SessionDetail {
  session: SessionMeta;
  spans: SpanRow[];
  traces: RawTraceView[];
}

export interface Overview {
  audit_enabled: boolean;
  // Lens the whole payload (including sessions/events) is taken through:
  // "project" | "global" | "all". project_root is set in project scope.
  scope: string;
  project_root?: string;
  sessions: number;
  events: number;
  skills: number;
  registries: number;
  gate_allowed: number;
  gate_blocked: number;
  gate_unscanned: number;
  recent_sessions: SessionMeta[];
  // Locks that had to be skipped (e.g. an old schema version) — explains why
  // lock-derived panels may look empty, with the regeneration hint inline.
  lock_warnings?: string[];
}

export interface SkillRow {
  name: string;
  worktree?: string;
  scope?: string;
  registry?: string;
  ref?: string;
  commit?: string;
  source?: string;
  mode?: string;
  disabled?: boolean;
  targets?: string[];
}

export interface TargetDetail {
  target: string;
  path: string;
  ok: boolean;
  error?: string;
}

// ScanRef mirrors model.ScanRef: the recorded (install-time) scan-gate summary
// carried in the lock. Counts are on the lock's 5-rung scale (see SeverityCounts
// below). decision ∈ allowed | blocked | skipped.
export interface ScanRef {
  reportSHA?: string;
  scannerVersion?: string;
  counts: SeverityCounts;
  decision: string;
  reason?: string;
  sarifPath?: string;
}


export interface SkillInfo {
  name: string;
  description?: string;
  license?: string;
  compatibility?: string;
  allowedTools?: string;
  metadata?: Record<string, string>;
  registry?: string;
  ref?: string;
  commit?: string;
  commitDrift?: string;
  worktree?: string;
  source?: string;
  sourceUpstream?: string;
  subtreeHash?: string;
  treeOID?: string;
  mode?: string;
  editPath?: string;
  installedAt?: string;
  targets?: string[];
  targetDetails?: TargetDetail[];
  files?: string[];
  scan?: ScanRef;
}

export interface TreeSkill {
  name: string;
  ref: string;
  commit?: string;
  mode?: string;
  disabled?: boolean;
  targets: string[];
}

export interface TreeGroup {
  scope?: string;
  registry: string;
  skills: TreeSkill[];
}

export interface ProvenanceView {
  name: string;
  source?: string;
  subdirectory?: string;
  requested?: string;
  resolved?: string;
  treeOID?: string;
  subtreeHash?: string;
  signatureStatus: string;
  signer?: string;
  signedRef?: string;
  scanDecision?: string;
  scannerVersion?: string;
  install: string;
  status: string;
}

export interface ScanSummaryRow {
  name: string;
  registry?: string;
  decision?: string;
  scannerVersion?: string;
  mode?: string;
}

// Mirrors model.RegistryStatus (embeds model.Registry). Registries are global —
// configured once at the Quiver home root and shared across every project.
export interface RegistryStatus {
  name: string;
  url: string;
  path?: string;
  skill_count: number;
  skipped_count?: number;
  last_fetched: string;
  default_branch?: string;
  has_upstream_changes?: boolean;
  error?: string;
}

// One row in the project switcher (Go: projectSummary). Sourced from Quiver's
// on-disk project index plus a synthetic Global entry.
export interface ProjectSummary {
  path: string;
  name: string;
  scope: "project" | "global";
  lockPath?: string;
  hasLock: boolean;
  current: boolean;
  skills: number;
  sessions: number;
  events: number;
  lastSeen?: string;
}

// ---- registry detail (skills + version tree) -------------------------------

// One installable ref of a registry repo, resolved to its commit with a
// timestamp (Go: registryVersion). `current` marks the repo's default branch.
export interface RegistryVersion {
  ref: string;
  isTag: boolean;
  sha: string;
  time: string;
  subject?: string;
  current?: boolean;
}

// One skill a registry offers, annotated with install status in the active
// scope (Go: registrySkillRow).
export interface RegistrySkillRow {
  name: string;
  description?: string;
  path?: string;
  installed: boolean;
  installedRef?: string;
  installedCommit?: string;
}

// Registry detail payload (Go: registrySkillsResponse): the registry's skills
// plus its repo-level branch/tag version timeline.
export interface RegistrySkillsResponse {
  registry: string;
  url?: string;
  defaultBranch?: string;
  versions: RegistryVersion[];
  skills: RegistrySkillRow[];
  error?: string;
}

// Registry-scope skill detail (Go: registrySkillDetail). One skill browsed from
// a registry at a chosen ref — its file structure (listed from the bare clone,
// no checkout), the repo's version timeline, and install status in the active
// scope. `files` are skill-relative paths, matching SkillInfo.files.
export interface RegistrySkillDetail {
  registry: string;
  name: string;
  description?: string;
  path?: string;
  ref?: string;
  commit?: string;
  files: string[];
  installed: boolean;
  installedRef?: string;
  installedCommit?: string;
  versions: RegistryVersion[];
  error?: string;
}

export interface Finding {
  check: string;
  rule_id?: string;
  category?: string;
  severity: string;
  confidence?: number;
  file?: string;
  line?: number;
  evidence?: string;
  message: string;
  remediation?: string;
}

export interface ScanResult {
  path: string;
  skill: string;
  scanned_at?: string;
  checks: string[];
  // The scanner marshals a nil slice as JSON null (not []) when there are no
  // findings, so consumers must normalise before reading .length / mapping.
  findings: Finding[] | null;
  summary: {
    critical: number;
    error: number;
    warning: number;
    info: number;
  };
  // Advisory agentskills.io spec-lint that rides alongside the scan. Absent on
  // older payloads; `valid` skills omit issues. Never blocks an install — the
  // UI surfaces it as a `lint:(count)` badge next to the security counts.
  lint?: {
    valid: boolean;
    count: number;
    issues?: { field: string; message: string; severity: string }[];
  };
}

// Lock-scale severity counts (matches model.SeverityCounts / the recorded
// gate's recorded scan counts), so a live re-scan compares 1:1.
export interface SeverityCounts {
  critical: number;
  high: number;
  medium: number;
  low: number;
  info: number;
}

// Live re-scan verdict, computed under the recorded gate's block_severity
// policy and reported on the lock's 5-rung scale (issue #140).
export interface ScanRunGate {
  decision: string; // allowed | blocked
  threshold: string;
  counts: SeverityCounts;
}

export interface ScanRunResult extends ScanResult {
  gate: ScanRunGate;
}

// ---- skill metrics (observability) ------------------------------------------
// Types mirror cmd/ui_metrics.go exactly.

// Overview rollup data: "N installed · M did 90% of verified work · K never fired".
export interface SkillMetricsHeadline {
  installed: number;
  active: number;
  never_fired: number;
  verified_invocations: number;
  total_invocations: number;
  core_skills: string[];
  core_share: number;
}

// One skill's usage merged with its lock identity. Token fields are
// session-attributed: "tokens in sessions where this skill fired" (a session
// that fired two skills counts toward both), never exclusive cost.
export interface SkillUsageRow {
  name: string;
  installed: boolean;
  registry?: string;
  ref?: string;
  commit?: string;
  disabled?: boolean;
  gate?: string;
  installedAt?: string;
  invocations: number;
  sessions: number;
  verified: number;
  verifiedShare: number;
  firstFired?: string;
  lastFired?: string;
  tokensIn: number;
  tokensOut: number;
  tokenSessions: number;
}

export interface SkillMetricsResponse {
  audit_enabled: boolean;
  scope: string;
  headline: SkillMetricsHeadline;
  skills: SkillUsageRow[];
}

export interface SkillReportEntry {
  registry?: string;
  source?: string;
  ref?: string;
  commit?: string;
  subtreeHash?: string;
  mode?: string;
  targets?: string[];
  gate?: string;
  installedAt?: string;
  disabled?: boolean;
}

export interface SkillReportTotals {
  invocations: number;
  sessions: number;
  verified: number;
  unverified: number;
  firstFired?: string;
  lastFired?: string;
}

export interface SkillReportAgent {
  agent: string;
  invocations: number;
  verified: number;
  sessions: number;
  lastFired?: string;
}

export interface SkillReportSeriesPoint {
  day: string;
  agent: string;
  invocations: number;
  verified: number;
}

// One (ref, commit) the skill fired as — the lineage row. `current` marks the
// lock's pinned commit.
export interface SkillVersionUsage {
  ref?: string;
  commit?: string;
  invocations: number;
  sessions: number;
  verified: number;
  firstFired?: string;
  lastFired?: string;
  tokensIn: number;
  tokensOut: number;
  current?: boolean;
}

export interface SkillReport {
  audit_enabled: boolean;
  name: string;
  installed: boolean;
  entry?: SkillReportEntry;
  totals: SkillReportTotals;
  agents: SkillReportAgent[];
  series: SkillReportSeriesPoint[];
  tokens: { sessions: number; input: number; output: number };
  versions: SkillVersionUsage[];
  recentSessions: SessionMeta[];
}

export interface DeadweightRow {
  name: string;
  registry?: string;
  ref?: string;
  scope?: string;
  installedAt?: string;
  ageDays: number;
  disabled?: boolean;
}

export interface DeadweightResponse {
  audit_enabled: boolean;
  scope: string;
  rows: DeadweightRow[];
}

export interface Health {
  ok: boolean;
  version: string;
  audit_enabled: boolean;
}

// ---- activity analytics (overview) ------------------------------------------
// Types mirror cmd/ui_activity.go exactly.

// One (day, agent) cell of the sessions-over-time series. Days are YYYY-MM-DD
// (UTC); skill_sessions counts the sessions that used at least one skill.
export interface ActivityBucket {
  day: string;
  agent: string;
  sessions: number;
  skill_sessions: number;
  turns: number;
  duration_ms: number;
}

export interface AgentActivity {
  agent: string;
  sessions: number;
  // Sessions that used at least one skill (per-agent coverage numerator).
  skill_sessions: number;
  turns: number;
  tools: number;
  duration_ms: number;
  avg_session_ms: number;
}

export interface ActivitySummary {
  sessions: number;
  skill_sessions: number;
  turns: number;
  tools: number;
  duration_ms: number;
  // Real token usage summed from the scoped sessions' LLM spans.
  tokens_in: number;
  tokens_out: number;
  agents: AgentActivity[];
}

// Sessions the discovery scan proved skill-less and did NOT store, counted
// from the scan ledger (machine-global; absent in project scope).
export interface SkippedBucket {
  day: string;
  agent: string;
  sessions: number;
}

export interface ActivityResponse {
  audit_enabled: boolean;
  scope: string;
  summary: ActivitySummary;
  series: ActivityBucket[];
  skipped?: SkippedBucket[];
}

// ---- discovery ---------------------------------------------------------------
// Mirrors discover.Report / discover.AgentReport.

export interface DiscoverAgentReport {
  agent: string;
  seen: number;
  unchanged: number;
  ingested: number;
  skipped: number;
  errors: number;
  lines: number;
  spans: number;
}

export interface DiscoverReport {
  agents: DiscoverAgentReport[] | null;
  dry_run?: boolean;
}

// Optional time window for the metrics list (YYYY-MM-DD, inclusive).
export interface MetricsWindow {
  since?: string;
  until?: string;
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url);
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body?.error) msg = body.error;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

async function postJSON<T>(url: string): Promise<T> {
  const res = await fetch(url, { method: "POST" });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body?.error) msg = body.error;
    } catch {
      /* ignore */
    }
    throw new Error(msg);
  }
  return res.json() as Promise<T>;
}

// ---- scope (project switcher) ----------------------------------------------
// The dashboard answers every scoped endpoint through the selected project (or
// Global). The choice lives here as module state, persisted to localStorage, so
// a single running `qvr ui` can browse any project on the machine. Registries
// and the project list are global and are NOT scoped.

export interface Scope {
  project?: string; // absolute project root
  scope?: "global" | "all";
}

const SCOPE_STORAGE_KEY = "qvr-ui-scope";

function readStoredScope(): Scope {
  try {
    const raw = localStorage.getItem(SCOPE_STORAGE_KEY);
    if (raw) return JSON.parse(raw) as Scope;
  } catch {
    /* ignore malformed/absent storage */
  }
  return {};
}

let currentScope: Scope = readStoredScope();

export function getScope(): Scope {
  return currentScope;
}

export function setScope(s: Scope): void {
  currentScope = s;
  try {
    localStorage.setItem(SCOPE_STORAGE_KEY, JSON.stringify(s));
  } catch {
    /* non-fatal: scope just won't persist across reloads */
  }
}

// scopeToken is a stable string for useFetch keys / Routes remount, so switching
// scope re-runs every loader.
export function scopeToken(s: Scope = currentScope): string {
  if (s.scope) return s.scope;
  if (s.project) return `p:${s.project}`;
  return "default";
}

// scopeParams seeds a URLSearchParams with the active scope, so callers can add
// their own params (e.g. session filters) on top.
function scopeParams(): URLSearchParams {
  const p = new URLSearchParams();
  if (currentScope.scope) p.set("scope", currentScope.scope);
  else if (currentScope.project) p.set("project", currentScope.project);
  return p;
}

function scopeQuery(): string {
  const q = scopeParams().toString();
  return q ? `?${q}` : "";
}

function sessionsQuery(f: SessionFilter): string {
  const p = scopeParams();
  if (f.agent) p.set("agent", f.agent);
  if (f.skill) p.set("skill", f.skill);
  if (f.since) p.set("since", f.since);
  if (f.until) p.set("until", f.until);
  const q = p.toString();
  return q ? `?${q}` : "";
}

export const api = {
  // Scoped endpoints carry the active project/scope.
  overview: () => getJSON<Overview>(`/api/overview${scopeQuery()}`),
  sessions: (f: SessionFilter = {}) =>
    getJSON<SessionMeta[]>(`/api/sessions${sessionsQuery(f)}`),
  session: (id: string) => getJSON<SessionDetail>(`/api/sessions/${id}`),
  skills: () => getJSON<SkillRow[]>(`/api/skills${scopeQuery()}`),
  skill: (name: string) =>
    getJSON<SkillInfo>(`/api/skills/${encodeURIComponent(name)}${scopeQuery()}`),
  tree: () => getJSON<TreeGroup[]>(`/api/tree${scopeQuery()}`),
  provenance: () => getJSON<ProvenanceView[]>(`/api/provenance${scopeQuery()}`),
  scanSummary: () => getJSON<ScanSummaryRow[]>(`/api/scan${scopeQuery()}`),
  runScan: (name: string) =>
    postJSON<ScanRunResult>(`/api/scan/${encodeURIComponent(name)}${scopeQuery()}`),
  // Registries are global, but the registry detail's install/current markers
  // are scoped, so the detail endpoint carries the active scope.
  registrySkills: (name: string) =>
    getJSON<RegistrySkillsResponse>(
      `/api/registries/${encodeURIComponent(name)}/skills${scopeQuery()}`,
    ),
  registrySkill: (registry: string, name: string, ref?: string) => {
    const p = scopeParams();
    if (ref) p.set("ref", ref);
    const q = p.toString();
    return getJSON<RegistrySkillDetail>(
      `/api/registries/${encodeURIComponent(registry)}/skills/${encodeURIComponent(name)}${
        q ? `?${q}` : ""
      }`,
    );
  },
  // Observability metrics — scoped like every other lock/audit panel.
  metricsSkills: (w: MetricsWindow = {}) => {
    const p = scopeParams();
    if (w.since) p.set("since", w.since);
    if (w.until) p.set("until", w.until);
    const q = p.toString();
    return getJSON<SkillMetricsResponse>(`/api/metrics/skills${q ? `?${q}` : ""}`);
  },
  skillReport: (name: string) =>
    getJSON<SkillReport>(`/api/metrics/skills/${encodeURIComponent(name)}${scopeQuery()}`),
  deadweight: () => getJSON<DeadweightResponse>(`/api/metrics/deadweight${scopeQuery()}`),
  activity: (w: MetricsWindow = {}) => {
    const p = scopeParams();
    if (w.since) p.set("since", w.since);
    if (w.until) p.set("until", w.until);
    const q = p.toString();
    return getJSON<ActivityResponse>(`/api/metrics/activity${q ? `?${q}` : ""}`);
  },
  // Discover: scan the agents' native session stores (a write action — the
  // one deliberate exception to the read-only dashboard, since it only
  // imports what agents already recorded).
  discover: () => postJSON<DiscoverReport>(`/api/discover`),
  // Global endpoints — not scoped.
  registries: () => getJSON<RegistryStatus[]>("/api/registries"),
  projects: () => getJSON<ProjectSummary[]>("/api/projects"),
  health: () => getJSON<Health>("/api/health"),
};

// prettyAgent maps an agent name to its display label. Agents are stored as
// canonical target names already (claude, codex, …); the map only folds the
// legacy aliases older databases may still carry.
export function prettyAgent(agent?: string): string {
  if (!agent) return "—";
  const map: Record<string, string> = {
    "claude-code": "claude",
    claudecode: "claude",
  };
  return map[agent] ?? agent;
}

export interface AsyncState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
  reload: () => void;
}

// useFetch runs an async loader on mount (and when `key` changes) and tracks
// loading/error state. Returns a reload() for manual refresh. Re-runs after
// the first load are SILENT (loading stays false while stale data shows), so
// live polling never flashes a spinner over a populated page. pollMs > 0
// re-runs the loader on that interval for as long as the component is
// mounted — the live-tracking mode the dashboard uses.
export function useFetch<T>(loader: () => Promise<T>, key: string, pollMs = 0): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(data === null);
    loader()
      .then((d) => {
        if (!cancelled) {
          setData(d);
          setError(null);
        }
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e));
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
    // loader identity changes per render; key is the real dependency.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, nonce]);

  useEffect(() => {
    if (pollMs <= 0) return;
    const id = setInterval(() => setNonce((n) => n + 1), pollMs);
    return () => clearInterval(id);
  }, [pollMs]);

  return { data, error, loading, reload };
}
