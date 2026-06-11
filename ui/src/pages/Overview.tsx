import { useState } from "react";
import { Link } from "react-router-dom";
import { Clock, Coins, Layers, Library, MessagesSquare, Package } from "lucide-react";
import { api, prettyAgent, scopeToken, useFetch, type SkillUsageRow } from "../api";
import {
  Badge,
  Card,
  EmptyState,
  ErrorBox,
  Loading,
  PageHead,
  Prompt,
  SkillRowItem,
  RefreshButton,
  StatCard,
  StatusBadge,
} from "../components/qvr";
import { ActivityCharts, activityCoverage, avgSessionMs } from "../components/ActivityPanel";
import { fmtCount, fmtCountWhole, fmtDuration, fmtShare, relTimeMs } from "../lib/format";

// Overview — the dashboard home: stat tiles, the verified-work headline
// ("N installed · M do 90% of verified work · K never fired"), the scan-gate
// rollup, what needs attention, and the latest sessions.
export default function Overview() {
  const [nonce, setNonce] = useState(0);
  // 10s polling keeps the dashboard live while the server's background scan
  // ingests new sessions — no manual refresh needed.
  const ov = useFetch(api.overview, `overview:${scopeToken()}:${nonce}`, 10_000);
  const metrics = useFetch(() => api.metricsSkills(), `overview-metrics:${scopeToken()}:${nonce}`, 10_000);
  const act = useFetch(() => api.activity(), `overview-activity:${scopeToken()}:${nonce}`, 10_000);
  const data = ov.data;
  const m = metrics.data;
  const activity = act.data?.audit_enabled && act.data.summary.sessions > 0 ? act.data : null;
  const cov = activity ? activityCoverage(activity) : null;

  return (
    <>
      <PageHead
        title="Overview"
        sub={
          data ? (
            <>
              Scope ·{" "}
              <span style={{ fontFamily: "var(--font-mono)", color: "var(--text-muted)" }}>
                {data.scope === "project"
                  ? (data.project_root ?? "").split("/").pop() || "this project"
                  : data.scope}
              </span>
            </>
          ) : (
            "What Quiver has recorded on this machine."
          )
        }
        actions={<RefreshButton onClick={() => setNonce((n) => n + 1)} busy={ov.loading} />}
      />
      {ov.loading && <Loading />}
      {ov.error && <ErrorBox message={ov.error} />}
      {data && (
        <>
          {!data.audit_enabled && (
            <div style={{ margin: "0 0 18px" }}>
              <p className="qvr-sub" style={{ margin: "0 0 8px" }}>
                audit pipeline off — no session history. start recording:
              </p>
              <Prompt command="qvr audit enable" />
            </div>
          )}

          {data.lock_warnings && data.lock_warnings.length > 0 && (
            <div style={{ margin: "0 0 18px" }}>
              {data.lock_warnings.map((w) => (
                <ErrorBox key={w} message={w} />
              ))}
            </div>
          )}

          <div
            style={{
              display: "grid",
              gridTemplateColumns: activity
                ? "repeat(6, minmax(0, 1fr))"
                : "repeat(2, minmax(0, 1fr))",
              gap: 12,
              marginBottom: 18,
            }}
          >
            <StatCard icon={<Package />} value={data.skills} label="skills pinned" />
            <StatCard icon={<Library />} value={data.registries} label="registries" />
            {activity && cov && (
              <>
                <StatCard
                  icon={<Layers />}
                  value={fmtCount(cov.totalSessions)}
                  label={`sessions · ${fmtShare(cov.taggedShare)} used skills`}
                />
                <StatCard
                  icon={<MessagesSquare />}
                  value={fmtCount(activity.summary.turns)}
                  label={`turns · ${fmtCount(activity.summary.tools)} tool calls`}
                />
                <StatCard
                  icon={<Coins />}
                  value={fmtCountWhole(activity.summary.tokens_in + activity.summary.tokens_out)}
                  label={`tokens · ${fmtCount(activity.summary.tokens_in)} in / ${fmtCount(activity.summary.tokens_out)} out`}
                />
                <StatCard
                  icon={<Clock />}
                  value={fmtDuration(activity.summary.duration_ms)}
                  label={`session time · ${fmtDuration(avgSessionMs(activity))} avg`}
                />
              </>
            )}
          </div>

          {m?.audit_enabled && m.headline.total_invocations > 0 && (
            <Card variant="accent" className="qvr-stat" >
              <p
                style={{
                  margin: 0,
                  fontFamily: "var(--font-brand)",
                  fontSize: "var(--text-lg)",
                  color: "var(--text)",
                }}
              >
                {m.headline.installed} skills installed ·{" "}
                <Link to="/skills">
                  {m.headline.core_skills.length}{" "}
                  {m.headline.core_skills.length === 1 ? "does" : "do"}{" "}
                  {fmtShare(m.headline.core_share)} of verified work
                </Link>{" "}
                ·{" "}
                <Link to="/deadweight">{m.headline.never_fired} never fired</Link>
              </p>
              <p className="qvr-sub" style={{ marginTop: 6 }}>
                {fmtCount(m.headline.verified_invocations)} of{" "}
                {fmtCount(m.headline.total_invocations)} skill invocations were lock-verified.
              </p>
            </Card>
          )}

          {activity && <ActivityCharts data={activity} skills={m?.skills ?? []} />}

          <div
            style={{
              display: "grid",
              gridTemplateColumns: "minmax(0, 1fr) minmax(0, 1fr)",
              gap: 12,
              marginTop: 18,
            }}
          >
            <Card title="scan gate">
              <div style={{ display: "flex", gap: 24, flexWrap: "wrap" }}>
                <GateStat value={data.gate_allowed} label="allowed" />
                <GateStat value={data.gate_blocked} label="blocked" />
                <GateStat value={data.gate_unscanned} label="unscanned" />
              </div>
              <p className="qvr-sub" style={{ marginTop: 12 }}>
                recorded at install time. open a <Link to="/skills">skill</Link> to run a
                live scan.
              </p>
            </Card>

            <Card title="recent sessions">
              {data.recent_sessions.length === 0 ? (
                <EmptyState title="no sessions yet" art={false}>
                  skill-bearing agent sessions appear here.
                </EmptyState>
              ) : (
                <ul style={{ listStyle: "none", margin: 0, padding: 0 }}>
                  {data.recent_sessions.map((s) => (
                    <li
                      key={s.session_id}
                      className="qvr-frow"
                      style={{ justifyContent: "space-between" }}
                    >
                      <Link
                        to={`/sessions/${s.session_id}`}
                        style={{ minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}
                        title={s.title || s.session_id}
                      >
                        {s.title || "untitled session"}
                      </Link>
                      <span style={{ display: "flex", gap: 8, alignItems: "center", flex: "none" }}>
                        <Badge tone="info">{prettyAgent(s.agent_name)}</Badge>
                        <span className="qvr-ver__when">{relTimeMs(s.started_ms)}</span>
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          </div>

          {m && <NeedsAttention rows={m.skills} auditEnabled={m.audit_enabled} />}
        </>
      )}
    </>
  );
}

function GateStat({ value, label }: { value: number; label: string }) {
  return (
    <div>
      <div className="qvr-stat__num" style={{ fontSize: 24, marginTop: 0 }}>
        {value}
      </div>
      <StatusBadge value={label} dot />
    </div>
  );
}

// NeedsAttention surfaces what a maintainer should look at: blocked gates,
// disabled skills, and installed-but-never-fired dead weight (top 5).
function NeedsAttention({
  rows,
  auditEnabled,
}: {
  rows: SkillUsageRow[];
  auditEnabled: boolean;
}) {
  const attention = rows.filter(
    (s) =>
      s.installed &&
      (s.gate === "blocked" || s.disabled || (auditEnabled && s.invocations === 0)),
  );
  if (attention.length === 0) return null;
  return (
    <div className="qvr-section">
      <h3 className="qvr-cardtitle">needs attention</h3>
      <div style={{ marginTop: 10 }}>
        {attention.slice(0, 5).map((s) => (
          <SkillRowItem
            key={s.name}
            to={`/skills/${encodeURIComponent(s.name)}`}
            lead={
              s.gate === "blocked" ? (
                <StatusBadge value="blocked" />
              ) : s.disabled ? (
                <Badge tone="neutral" dot>
                  disabled
                </Badge>
              ) : (
                <Badge tone="warning" dot>
                  never fired
                </Badge>
              )
            }
            name={s.name}
            right={<span className="qvr-skillrow__reg">{s.registry}</span>}
          />
        ))}
      </div>
    </div>
  );
}
