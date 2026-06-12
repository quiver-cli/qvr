import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  prettyAgent,
  type ActivityBucket,
  type ActivityResponse,
  type SkillUsageRow,
  type SkippedBucket,
} from "../api";
import { Card, StackedBarChart, Tabs, type ChartCol } from "./qvr";
import { fmtCount, fmtShare } from "../lib/format";

// The activity charts of the skills-observability overview: the combined
// sessions chart and the by-skill breakdown. All numbers come from the
// unified session model (one row per session), the derived spans, and the
// scan ledger; nothing here reads transcripts.
//
// qvr's dimension vs a generic session browser is skills: every view answers
// "which skills earn their keep". Kept sessions split via their skill list;
// sessions the discovery scan proved skill-less were never stored, so their
// counts come from the scan ledger (global scope only — the ledger has no
// project dimension).

// Stable per-agent palette (chart + bars). Index by first-seen order.
const AGENT_COLORS = [
  "var(--accent)",
  "var(--info)",
  "var(--warning)",
  "var(--success)",
  "var(--danger)",
  "var(--text-faint)",
];

const TAGGED_COLOR = "var(--accent)";
const UNTAGGED_COLOR = "var(--text-faint)";

type SkillMetric = "sessions" | "invocations" | "tokens";

// activityCoverage exposes the all-sessions totals (stored + scan-skipped)
// for the overview's headline tiles.
export function activityCoverage(data: ActivityResponse): {
  totalSessions: number;
  taggedShare: number;
} {
  const { totalSessions, taggedShare } = coverageCols(data.series, data.skipped ?? []);
  return { totalSessions, taggedShare };
}

// avgSessionMs is the mean stored-session duration.
export function avgSessionMs(data: ActivityResponse): number {
  return data.summary.sessions > 0 ? data.summary.duration_ms / data.summary.sessions : 0;
}

// ActivityCharts renders the two activity graphs — the combined sessions
// chart (agent colors, solid = used skills, faded = no skill) and the
// by-skill breakdown. Purely presentational: Overview owns the fetches.
export function ActivityCharts({ data, skills }: { data: ActivityResponse; skills: SkillUsageRow[] }) {
  const skippedTotal = (data.skipped ?? []).reduce((n, b) => n + b.sessions, 0);
  const agentColor = useMemo(() => colorsByAgent(data), [data]);
  const coverage = useMemo(() => coverageCols(data.series, data.skipped ?? []), [data]);

  return (
    <>
      <Card title="sessions" className="qvr-section">
        <StackedBarChart cols={combinedCols(data.series, data.skipped ?? [], agentColor)} />
        <ChartLegend
          items={[
            ...[...agentColor.entries()].map(([agent, color]) => ({
              label: prettyAgent(agent),
              color,
            })),
            { label: "faded = no skill", color: "var(--text-faint)" },
          ]}
        />
        <p className="qvr-sub" style={{ marginTop: 6 }}>
          {fmtShare(coverage.taggedShare)} of all {fmtCount(coverage.totalSessions)} sessions
          used at least one skill
          {skippedTotal > 0 ? " — skill-less ones are counted at scan time, never stored" : ""}.
        </p>
      </Card>

      <div className="qvr-section">
        <BySkill skills={skills} />
      </div>
    </>
  );
}

function ChartLegend({ items }: { items: { label: string; color: string }[] }) {
  return (
    <div style={{ display: "flex", flexWrap: "wrap", gap: 12, marginTop: 8 }}>
      {items.map((it) => (
        <span key={it.label} style={{ display: "inline-flex", alignItems: "center", gap: 5 }}>
          <span style={{ width: 8, height: 8, borderRadius: 2, background: it.color }} />
          <span className="qvr-scan__scanner">{it.label}</span>
        </span>
      ))}
    </div>
  );
}

function colorsByAgent(data: ActivityResponse): Map<string, string> {
  const m = new Map<string, string>();
  data.summary.agents.forEach((a, i) => m.set(a.agent, AGENT_COLORS[i % AGENT_COLORS.length]));
  data.series.forEach((b) => {
    if (!m.has(b.agent)) m.set(b.agent, AGENT_COLORS[m.size % AGENT_COLORS.length]);
  });
  return m;
}

// ---- by skill ------------------------------------------------------------------

// BySkill is the centerpiece: each skill's observed work, switchable between
// invocations, sessions, and (session-attributed) tokens. Rows link into the
// skill's full report card.
function BySkill({ skills }: { skills: SkillUsageRow[] }) {
  const [metric, setMetric] = useState<SkillMetric>("sessions");
  const val = (s: SkillUsageRow) =>
    metric === "sessions" ? s.sessions : metric === "invocations" ? s.invocations : s.tokensIn + s.tokensOut;
  const rows = skills.filter((s) => val(s) > 0).sort((a, b) => val(b) - val(a)).slice(0, 8);
  if (rows.length === 0) return null;
  const max = Math.max(1, ...rows.map(val));
  const total = rows.reduce((n, s) => n + val(s), 0);

  return (
    <Card
      title="by skill"
      actions={
        <Tabs
          items={[
            { id: "sessions", label: "sessions" },
            { id: "invocations", label: "invocations" },
            { id: "tokens", label: "tokens" },
          ]}
          value={metric}
          onChange={(v) => setMetric(v as SkillMetric)}
        />
      }
    >
      <div style={{ display: "grid", gap: 10 }}>
        {rows.map((s) => (
          <div key={s.name}>
            <div style={{ display: "flex", alignItems: "baseline", gap: 10 }}>
              <Link
                to={`/skills/${encodeURIComponent(s.name)}`}
                style={{ fontFamily: "var(--font-mono)", fontSize: "var(--text-sm)" }}
              >
                {s.name}
              </Link>
              <span className="qvr-scan__scanner">
                {fmtCount(s.invocations)} invocations · {fmtCount(s.sessions)} sessions ·{" "}
                {fmtCount(s.tokensIn + s.tokensOut)} tok
              </span>
              <span
                style={{
                  marginLeft: "auto",
                  fontFamily: "var(--font-mono)",
                  fontSize: "var(--text-sm)",
                  color: "var(--text)",
                  whiteSpace: "nowrap",
                  flex: "none",
                }}
              >
                {fmtCount(val(s))} · {total > 0 ? Math.round((val(s) / total) * 100) : 0}% of {metric}
              </span>
            </div>
            <div className="qvr-barrow__track" style={{ marginTop: 4 }}>
              <span className="qvr-barrow__fill" style={{ width: `${Math.max(2, (val(s) / max) * 100)}%` }} />
            </div>
          </div>
        ))}
      </div>
      {metric === "tokens" && (
        <p className="qvr-sub" style={{ marginTop: 8 }}>
          tokens are session-attributed: a session that fired two skills counts toward
          both — relative weight, not exclusive cost.
        </p>
      )}
    </Card>
  );
}

// ---- bucketing ----------------------------------------------------------------

// keyer picks the chart bucket granularity from the span of days present:
// days for short ranges, months past ~13 weeks (the all-time view).
function keyer(days: string[]): (day: string) => string {
  if (days.length === 0) return (d) => d;
  const span = (Date.parse(days[days.length - 1]) - Date.parse(days[0])) / 86_400_000 + 1;
  return span > 92 ? (d) => d.slice(0, 7) : (d) => d;
}

// seedBuckets returns every bucket key in the span so gaps render as empty
// columns instead of silently compressing time.
function seedBuckets(days: string[], keyOf: (d: string) => string): string[] {
  const out: string[] = [];
  const monthly = keyOf(days[0]).length === 7;
  const last = new Date(days[days.length - 1] + "T00:00:00Z");
  for (let d = new Date(days[0] + "T00:00:00Z"); d <= last; ) {
    const k = keyOf(d.toISOString().slice(0, 10));
    if (out[out.length - 1] !== k) out.push(k);
    if (monthly) d.setUTCMonth(d.getUTCMonth() + 1);
    else d.setUTCDate(d.getUTCDate() + 1);
  }
  return out;
}

// combinedCols stacks ALL sessions per bucket per agent: a solid agent-color
// segment for the sessions that used skills, a faded one for those that
// didn't (kept skill-less sessions plus the scan ledger's unstored ones, both
// attributed to their agent). One chart answers "who ran sessions, and what
// share of each agent's work used skills".
function combinedCols(
  series: ActivityBucket[],
  skipped: SkippedBucket[],
  agentColor: Map<string, string>,
): ChartCol[] {
  const days = [...new Set([...series.map((b) => b.day), ...skipped.map((b) => b.day)])].sort();
  if (days.length === 0) return [];
  const keyOf = keyer(days);

  type slice = { skill: number; noSkill: number };
  const byKey = new Map<string, Map<string, slice>>();
  const at = (k: string, agent: string): slice => {
    const agents = byKey.get(k) ?? new Map<string, slice>();
    byKey.set(k, agents);
    const s = agents.get(agent) ?? { skill: 0, noSkill: 0 };
    agents.set(agent, s);
    return s;
  };
  for (const b of series) {
    const s = at(keyOf(b.day), b.agent);
    s.skill += b.skill_sessions;
    s.noSkill += b.sessions - b.skill_sessions;
  }
  for (const b of skipped) {
    at(keyOf(b.day), b.agent).noSkill += b.sessions;
  }

  return seedBuckets(days, keyOf).map((k) => {
    const segs = [];
    let tagged = 0;
    let total = 0;
    for (const [agent, s] of byKey.get(k) ?? new Map<string, slice>()) {
      const color = agentColor.get(agent) ?? "var(--accent)";
      if (s.skill > 0) {
        segs.push({ label: `${prettyAgent(agent)} · used skills`, value: s.skill, color });
      }
      if (s.noSkill > 0) {
        segs.push({ label: `${prettyAgent(agent)} · no skill`, value: s.noSkill, color, dim: true });
      }
      tagged += s.skill;
      total += s.skill + s.noSkill;
    }
    return {
      label: k,
      segs,
      note: total > 0 ? `${Math.round((tagged / total) * 100)}% used skills` : undefined,
    };
  });
}

// coverageCols merges stored sessions (tagged vs untagged) with the scan
// ledger's skipped (skill-less, unstored) counts into one all-sessions view:
// every column is total sessions, split into "used skills" vs "no skill",
// with the tagged share carried as the hover note.
function coverageCols(
  series: ActivityBucket[],
  skipped: SkippedBucket[],
): { cols: ChartCol[]; totalSessions: number; taggedShare: number } {
  const days = [...new Set([...series.map((b) => b.day), ...skipped.map((b) => b.day)])].sort();
  if (days.length === 0) return { cols: [], totalSessions: 0, taggedShare: 0 };
  const keyOf = keyer(days);

  const tagged = new Map<string, number>();
  const untagged = new Map<string, number>();
  for (const b of series) {
    const k = keyOf(b.day);
    tagged.set(k, (tagged.get(k) ?? 0) + b.skill_sessions);
    untagged.set(k, (untagged.get(k) ?? 0) + (b.sessions - b.skill_sessions));
  }
  for (const b of skipped) {
    const k = keyOf(b.day);
    untagged.set(k, (untagged.get(k) ?? 0) + b.sessions);
  }

  let taggedSum = 0;
  let totalSum = 0;
  const cols = seedBuckets(days, keyOf).map((k) => {
    const tg = tagged.get(k) ?? 0;
    const un = untagged.get(k) ?? 0;
    taggedSum += tg;
    totalSum += tg + un;
    return {
      label: k,
      segs: [
        { label: "used skills", value: tg, color: TAGGED_COLOR },
        { label: "no skill", value: un, color: UNTAGGED_COLOR },
      ],
      note: tg + un > 0 ? `${Math.round((tg / (tg + un)) * 100)}% used skills` : undefined,
    };
  });
  return { cols, totalSessions: totalSum, taggedShare: totalSum > 0 ? taggedSum / totalSum : 0 };
}
