import { Link } from "react-router-dom";
import { api, prettyAgent, useFetch } from "../api";
import {
  Card,
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  PageHeader,
  Pill,
  StatCard,
} from "../components/ui";

export default function Overview() {
  const { data, error, loading } = useFetch(api.overview, "overview");

  return (
    <>
      <PageHeader
        title="Overview"
        subtitle={
          data
            ? data.scope === "project"
              ? "Scoped to this project — sessions, events, skills, and gate."
              : data.scope === "global"
                ? "Global scope (--global) — every recorded session, event, and skill."
                : "All scopes (--all) — project and global combined."
            : "What Quiver has recorded on this machine."
        }
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && (
        <>
          {!data.audit_enabled && (
            <div className="mb-6 rounded-[6px] border border-[#d3ba70] bg-[#f7efd9] px-4 py-3 text-sm text-[#6c5012]">
              Audit pipeline not enabled — session history is empty. Run{" "}
              <code className="rounded-[3px] bg-white/70 px-1.5 py-0.5">qvr audit enable</code> to start
              recording agent sessions.
            </div>
          )}

          <div className="grid grid-cols-2 gap-4 md:grid-cols-4">
            <StatCard label="Sessions" value={data.sessions} />
            <StatCard label="Traces" value={data.events} />
            <StatCard label="Skills" value={data.skills} />
            <StatCard label="Registries" value={data.registries} />
          </div>
          {data.project_root && (
            <p className="mt-2 text-xs text-[#708078]">
              Sessions and events scoped to{" "}
              <code className="rounded-[3px] bg-[#ecefed] px-1 py-0.5">{data.project_root}</code> — run{" "}
              <code className="rounded-[3px] bg-[#ecefed] px-1 py-0.5">qvr ui --global</code> for all activity.
            </p>
          )}

          <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-2">
            <Card title="Scan gate">
              <div className="flex flex-wrap gap-6">
                <GateStat label="Allowed" value={data.gate_allowed} tone="green" />
                <GateStat label="Blocked" value={data.gate_blocked} tone="red" />
                <GateStat label="Unscanned" value={data.gate_unscanned} tone="gray" />
              </div>
              <p className="mt-4 text-xs text-[#63706a]">
                Recorded at install time. Open a{" "}
                <Link className="font-medium text-[#2f765d] hover:underline" to="/skills">
                  skill
                </Link>{" "}
                to run a live scan.
              </p>
            </Card>

            <Card title="Recent sessions">
              {data.recent_sessions.length === 0 ? (
                <Empty>No sessions recorded yet.</Empty>
              ) : (
                <ul className="divide-y divide-[#eef0ef]">
                  {data.recent_sessions.map((s) => (
                    <li
                      key={s.session_id}
                      className="flex items-center justify-between gap-3 py-2"
                    >
                      <Link
                        to={`/sessions/${s.session_id}`}
                        className="min-w-0 truncate text-sm font-medium text-[#2f765d] hover:underline"
                        title={s.title || s.session_id}
                      >
                        {s.title || "untitled session"}
                      </Link>
                      <span className="flex shrink-0 items-center gap-2">
                        <Pill tone="blue">{prettyAgent(s.agent_name)}</Pill>
                        <span className="text-xs text-[#708078]">{fmtTime(s.started_at)}</span>
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </Card>
          </div>
        </>
      )}
    </>
  );
}

function GateStat({
  label,
  value,
  tone,
}: {
  label: string;
  value: number;
  tone: "green" | "red" | "gray";
}) {
  return (
    <div>
      <div className="font-mono text-2xl font-semibold text-[#121816]">{value}</div>
      <Pill tone={tone}>{label}</Pill>
    </div>
  );
}
