import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api, prettyAgent, scopeToken, useFetch } from "../api";
import {
  Empty,
  ErrorBox,
  fmtTime,
  Loading,
  PageHeader,
  Pill,
  Table,
  Td,
  Th,
} from "../components/ui";

// The harnesses qvr can capture. The Harness filter offers these plus any agent
// actually present in the loaded rows (so an unexpected one still shows up).
const KNOWN_HARNESSES = ["claude-code", "codex", "cursor", "opencode", "copilot"];

export default function Sessions() {
  const [agent, setAgent] = useState("");
  const [skill, setSkill] = useState("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");

  // Re-fetch whenever the scope or any filter changes (the key encodes them all).
  const key = `sessions:${scopeToken()}:${agent}|${skill}|${since}|${until}`;
  const { data, error, loading } = useFetch(
    () => api.sessions({ agent, skill, since, until }),
    key,
  );

  // Skill dropdown options: installed skills in scope, unioned with any skill
  // seen in the current rows (covers skills used but since removed/ejected).
  const skillsList = useFetch(() => api.skills(), `sessions-skills:${scopeToken()}`);
  const harnessOptions = useMemo(() => {
    const set = new Set(KNOWN_HARNESSES);
    data?.forEach((s) => s.agent_name && set.add(s.agent_name));
    return [...set].sort();
  }, [data]);
  const skillOptions = useMemo(() => {
    const set = new Set<string>();
    skillsList.data?.forEach((s) => set.add(s.name));
    data?.forEach((s) => s.skills?.forEach((n) => set.add(n)));
    return [...set].sort();
  }, [skillsList.data, data]);

  const active = agent || skill || since || until;
  const clear = () => {
    setAgent("");
    setSkill("");
    setSince("");
    setUntil("");
  };

  return (
    <>
      <PageHeader
        title="Sessions"
        subtitle="Recorded agent sessions, newest first. Named by the first prompt you typed."
      />

      <div className="mb-4 flex flex-wrap items-end gap-3">
        <Field label="Harness">
          <Select value={agent} onChange={setAgent}>
            <option value="">All</option>
            {harnessOptions.map((a) => (
              <option key={a} value={a}>
                {prettyAgent(a)}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="Skill">
          <Select value={skill} onChange={setSkill}>
            <option value="">All</option>
            {skillOptions.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="From">
          <DateInput value={since} onChange={setSince} />
        </Field>
        <Field label="To">
          <DateInput value={until} onChange={setUntil} />
        </Field>
        {active && (
          <button
            type="button"
            onClick={clear}
            className="rounded-[4px] border border-[#cbd2ce] bg-white px-3 py-1.5 text-sm font-medium text-[#52615a] hover:bg-[#f4f6f5]"
          >
            Clear
          </button>
        )}
      </div>

      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && (
        <Empty>
          {active ? (
            <>No sessions match these filters.</>
          ) : (
            <>
              No sessions recorded. Enable the audit pipeline with{" "}
              <code className="rounded-[3px] bg-[#ecefed] px-1.5 py-0.5">qvr audit enable</code>.
            </>
          )}
        </Empty>
      )}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>Session</Th>
              <Th>Harness</Th>
              <Th>Skills</Th>
              <Th>Started</Th>
              <Th>Transcript</Th>
              <Th>Hooks</Th>
            </tr>
          }
        >
          {data.map((s) => (
            <tr key={s.session_id} className="hover:bg-[#f7f9f8]">
              <Td>
                <Link
                  to={`/sessions/${s.session_id}`}
                  className="font-medium text-[#2f765d] hover:underline"
                  title={s.title || s.session_id}
                >
                  {s.title || (
                    <span className="italic text-[#7a8580]">untitled session</span>
                  )}
                </Link>
              </Td>
              <Td>
                <Pill tone="blue">{prettyAgent(s.agent_name)}</Pill>
              </Td>
              <Td>
                {s.skills && s.skills.length > 0 ? (
                  <span className="flex flex-wrap gap-1">
                    {s.skills.map((n) => (
                      <Pill key={n} tone="amber">
                        {n}
                      </Pill>
                    ))}
                  </span>
                ) : (
                  <span className="text-[#9ba6a1]">—</span>
                )}
              </Td>
              <Td>{fmtTime(s.started_at)}</Td>
              <Td>{s.transcript_lines}</Td>
              <Td>{s.hook_payloads}</Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1 text-xs font-semibold uppercase text-[#63706a]">
      {label}
      {children}
    </label>
  );
}

function Select({
  value,
  onChange,
  children,
}: {
  value: string;
  onChange: (v: string) => void;
  children: React.ReactNode;
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="rounded-[4px] border border-[#cbd2ce] bg-white px-2 py-1.5 text-sm text-[#22302b] shadow-[0_1px_0_rgba(22,32,28,0.04)]"
    >
      {children}
    </select>
  );
}

function DateInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <input
      type="date"
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="rounded-[4px] border border-[#cbd2ce] bg-white px-2 py-1.5 text-sm text-[#22302b] shadow-[0_1px_0_rgba(22,32,28,0.04)]"
    />
  );
}
