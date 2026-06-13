import { Fragment, useState } from "react";
import { Link } from "react-router-dom";
import { ChevronDown, ChevronRight } from "lucide-react";
import { api, prettyAgent, scopeToken, useFetch, type SkillUsageRow } from "../api";
import { Badge, Card, Table, Td, Th } from "./qvr";
import { fmtCount, fmtShare, fmtTok, relTimeMs, relTime } from "../lib/format";

// SkillLedger is the overview's Module C: the per-skill breakdown. A dense,
// sortable table (tokens | sessions | invocations) of every fired skill, with a
// token-intensity bar (tokens per session, amber past a hog threshold), then a
// divider and the installed-but-never-fired dead weight below. Rows are
// click-to-expand (single-open accordion) into a tokens-by-agent + recent-traces
// drill; the skill name still links into the full report. Token sides are
// session-attributed (exposure, not exclusive).

type SortKey = "tokens" | "sessions" | "invocations";

// Tokens-per-session above this flags a token hog (matches the handoff's 18m).
const INTENSITY_AMBER = 18_000_000;
const COLS = 8;

const skillTokens = (s: SkillUsageRow) => (s.tokensIn ?? 0) + (s.tokensOut ?? 0);
const intensity = (s: SkillUsageRow) => (s.sessions > 0 ? skillTokens(s) / s.sessions : 0);

export default function SkillLedger({
  rows,
  totalSessions,
  refreshKey,
}: {
  rows: SkillUsageRow[];
  totalSessions: number;
  // The parent Overview's refresh epoch (its nonce); forwarded into the drill's
  // cache key so a manual refresh re-fetches the open drill in lockstep.
  refreshKey?: number;
}) {
  const [sort, setSort] = useState<SortKey>("tokens");
  const [open, setOpen] = useState<string | null>(null);

  const fired = rows.filter((s) => s.invocations > 0);
  const dead = rows.filter((s) => s.installed && s.invocations === 0);

  const sorted = [...fired].sort((a, b) => {
    if (sort === "sessions") return b.sessions - a.sessions;
    if (sort === "invocations") return b.invocations - a.invocations;
    return skillTokens(b) - skillTokens(a);
  });

  // The intensity bar scales to the busiest skill so the column reads relatively.
  const maxIntensity = Math.max(1, ...sorted.map(intensity));

  return (
    <Card
      title="skill ledger"
      actions={
        <span className="ovr-seg">
          {(["tokens", "sessions", "invocations"] as SortKey[]).map((k) => (
            <button
              key={k}
              className={"ovr-seg__btn" + (sort === k ? " ovr-seg__btn--on" : "")}
              onClick={() => setSort(k)}
            >
              {k}
            </button>
          ))}
        </span>
      }
    >
      <Table
        head={
          <tr>
            <Th>#</Th>
            <Th>skill</Th>
            <Th>tokens (in / out)</Th>
            <Th>tokens / session</Th>
            <Th>sessions</Th>
            <Th>invocations</Th>
            <Th>last fired</Th>
            <Th> </Th>
          </tr>
        }
      >
        {sorted.map((s, i) => {
          const intens = intensity(s);
          const hog = intens > INTENSITY_AMBER;
          const isOpen = open === s.name;
          return (
            <Fragment key={s.name}>
            <tr
              className={"ovr-ledrow" + (isOpen ? " ovr-ledrow--open" : "")}
              onClick={() => setOpen(isOpen ? null : s.name)}
            >
              <Td muted>{i + 1}</Td>
              <Td>
                <Link
                  to={`/skills/${encodeURIComponent(s.name)}`}
                  onClick={(e) => e.stopPropagation()}
                >
                  {s.name}
                </Link>
                {s.registry && <div className="ovr-skill__reg">{s.registry}</div>}
              </Td>
              <Td muted={s.tokensIn == null && s.tokensOut == null}>
                <b>{s.tokensIn == null ? "n/a" : fmtTok(s.tokensIn)}</b>
                {" / "}
                {s.tokensOut == null ? "—" : fmtTok(s.tokensOut)}
              </Td>
              <Td>
                <span className="ovr-int">
                  <span className="ovr-int__bar">
                    <span
                      className="ovr-int__fill"
                      style={{
                        width: `${Math.min((intens / maxIntensity) * 100, 100)}%`,
                        background: hog ? "var(--warning)" : "var(--brand-600)",
                      }}
                    />
                  </span>
                  <span className="ovr-int__v">{intens > 0 ? fmtTok(intens) : "—"}</span>
                </span>
              </Td>
              <Td muted>
                {fmtCount(s.sessions)}
                {totalSessions > 0 && (
                  <span className="ovr-num--faint"> · {fmtShare(s.sessions / totalSessions)}</span>
                )}
              </Td>
              <Td muted>{fmtCount(s.invocations)}</Td>
              <Td muted>{relTime(s.lastFired)}</Td>
              <Td>
                <span className="ovr-ledrow__chev">
                  {isOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
                </span>
              </Td>
            </tr>
            {isOpen && (
              <tr className="ovr-drill">
                <td colSpan={COLS}>
                  <SkillDrill name={s.name} refreshKey={refreshKey} />
                </td>
              </tr>
            )}
            </Fragment>
          );
        })}
        {dead.length > 0 && (
          <tr className="ovr-divrow">
            <td colSpan={COLS}>
              pinned · never fired — {dead.length} skill{dead.length === 1 ? "" : "s"} costing scan
              + maintenance, returning nothing
            </td>
          </tr>
        )}
        {dead.map((s) => (
          <tr key={s.name} className="ovr-dead">
            <Td muted>—</Td>
            <Td>
              <Link to={`/skills/${encodeURIComponent(s.name)}`}>{s.name}</Link>
              {s.registry && <div className="ovr-skill__reg">{s.registry}</div>}
            </Td>
            <Td muted>— / —</Td>
            <Td muted>—</Td>
            <Td muted>0</Td>
            <Td muted>0</Td>
            <Td>
              <Badge tone="warning" dot>
                never
              </Badge>
            </Td>
            <Td> </Td>
          </tr>
        ))}
      </Table>
    </Card>
  );
}

// SkillDrill lazily loads one skill's report (mounted only while the row is
// open) and shows two columns: tokens-by-agent and the recent traces that fired
// it — the per-skill detail the handoff reveals inline.
function SkillDrill({ name, refreshKey }: { name: string; refreshKey?: number }) {
  // Poll on the same 10s cadence as the parent Overview (auto-refresh), and key
  // on the parent's refresh epoch so a manual refresh re-fetches immediately —
  // the drill's totals never drift out of sync with the ledger row.
  const rep = useFetch(
    () => api.skillReport(name),
    `drill:${name}:${scopeToken()}:${refreshKey ?? 0}`,
    10_000,
  );
  if (rep.loading) return <span className="qvr-sub">loading…</span>;
  if (rep.error) return <span className="qvr-sub">couldn't load detail: {rep.error}</span>;
  if (!rep.data) return <span className="qvr-sub">no detail available.</span>;

  const r = rep.data;
  const agentTok = (a: { tokensIn?: number; tokensOut?: number }) =>
    (a.tokensIn ?? 0) + (a.tokensOut ?? 0);
  const maxAgent = Math.max(1, ...r.agents.map(agentTok));
  const traces = (r.recentSessions ?? []).slice(0, 6);

  return (
    <div className="ovr-drill__in">
      <div>
        <div className="ovr-drill__h">tokens by agent</div>
        {r.agents.length === 0 ? (
          <span className="qvr-sub">no agent breakdown.</span>
        ) : (
          r.agents.map((a) => (
            <div key={a.agent} className="ovr-dr">
              <Badge tone="info">{prettyAgent(a.agent)}</Badge>
              <span className="ovr-dr__bar">
                <span
                  className="ovr-dr__fill"
                  style={{ width: `${(agentTok(a) / maxAgent) * 100}%` }}
                />
              </span>
              <span className="ovr-dr__v">
                {a.tokensIn == null ? "n/a" : fmtTok(a.tokensIn)} / {fmtTok(a.tokensOut ?? 0)} ·{" "}
                {a.invocations} inv
              </span>
            </div>
          ))
        )}
      </div>
      <div>
        <div className="ovr-drill__h">recent traces that fired this skill</div>
        {traces.length === 0 ? (
          <span className="qvr-sub">no recent traces.</span>
        ) : (
          traces.map((s) => (
            <Link key={s.session_id} to={`/sessions/${s.session_id}`} className="ovr-dr">
              <span className="ovr-dr__cost">
                {s.tokens_in == null && s.tokens_out == null
                  ? "n/a"
                  : fmtTok((s.tokens_in ?? 0) + (s.tokens_out ?? 0))}
              </span>
              <span className="ovr-dr__title">{s.title || "untitled session"}</span>
              <span className="ovr-dr__when">
                {prettyAgent(s.agent_name)} · {relTimeMs(s.started_ms)}
              </span>
            </Link>
          ))
        )}
      </div>
    </div>
  );
}
