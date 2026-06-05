// Typed client for the qvr ui JSON API. Types mirror the Go structs exactly
// (cmd/ui_server.go and the reused command structs) so the dashboard stays a
// faithful view of CLI truth — if a field changes in Go, it changes here.

import { useCallback, useEffect, useState } from "react";

// RawSession mirrors store.RawSession: a per-session summary derived on the fly
// from the raw_traces rows that share a session_id. `title` is the first prompt
// the user typed (the session's human name); `agent_name` is the harness that
// produced it (claude-code, codex, …).
export interface RawSession {
  session_id: string;
  title?: string;
  agent_name: string;
  agent_session_id?: string;
  working_directory?: string;
  started_at: string;
  last_at: string;
  transcript_lines: number;
  hook_payloads: number;
  total_rows: number;
  // Distinct skills this session used (from its SKILL-attributed spans).
  skills?: string[];
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
  session: RawSession;
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
  recent_sessions: RawSession[];
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

// VerificationRecord mirrors model.VerificationRecord — the supply-chain signals
// recorded on a skill. Only `scan` is surfaced today; the other slots
// (signature, eval, …) are reserved and added when their writers ship.
export interface VerificationRecord {
  scan?: ScanRef;
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
  verification?: VerificationRecord;
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
}

// Lock-scale severity counts (matches model.SeverityCounts / the recorded
// gate's verification.scan.counts), so a live re-scan compares 1:1.
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
    getJSON<RawSession[]>(`/api/sessions${sessionsQuery(f)}`),
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
  // Global endpoints — not scoped.
  registries: () => getJSON<RegistryStatus[]>("/api/registries"),
  projects: () => getJSON<ProjectSummary[]>("/api/projects"),
};

// prettyAgent maps a harness's internal agent_name to a short display label for
// the "Harness" column (claude-code → claude, etc.). Unknown agents pass through.
export function prettyAgent(agent?: string): string {
  if (!agent) return "—";
  const map: Record<string, string> = {
    "claude-code": "claude",
    claudecode: "claude",
    codex: "codex",
    cursor: "cursor",
    opencode: "opencode",
    copilot: "copilot",
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
// loading/error state. Returns a reload() for manual refresh.
export function useFetch<T>(loader: () => Promise<T>, key: string): AsyncState<T> {
  const [data, setData] = useState<T | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError(null);
    loader()
      .then((d) => {
        if (!cancelled) setData(d);
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

  return { data, error, loading, reload };
}
