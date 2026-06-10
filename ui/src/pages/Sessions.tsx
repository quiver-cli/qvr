import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api, prettyAgent, scopeToken, useFetch } from "../api";
import {
  Badge,
  Button,
  EmptyState,
  ErrorBox,
  Field,
  Loading,
  PageHead,
  Select,
  Table,
  Td,
  Th,
} from "../components/qvr";
import { fmtTime } from "../lib/format";

// The harnesses qvr can capture. The harness filter offers these plus any agent
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
      <PageHead
        title="Sessions"
        sub="Recorded agent sessions, newest first. Named by the first prompt you typed."
      />

      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: 12, marginBottom: 16 }}>
        <Field label="harness">
          <Select value={agent} onChange={(e) => setAgent(e.target.value)}>
            <option value="">all</option>
            {harnessOptions.map((a) => (
              <option key={a} value={a}>
                {prettyAgent(a)}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="skill">
          <Select value={skill} onChange={(e) => setSkill(e.target.value)}>
            <option value="">all</option>
            {skillOptions.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </Field>
        <Field label="from">
          <DateInput value={since} onChange={setSince} />
        </Field>
        <Field label="to">
          <DateInput value={until} onChange={setUntil} />
        </Field>
        {active && (
          <Button variant="ghost" size="sm" onClick={clear}>
            clear
          </Button>
        )}
      </div>

      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && data.length === 0 && (
        <EmptyState title={active ? "no sessions match" : "no sessions recorded"}>
          {active
            ? "Loosen the filters — nothing in this window."
            : "Agent sessions that use skills from this lock will appear here. Enable capture with qvr audit enable."}
        </EmptyState>
      )}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>session</Th>
              <Th>harness</Th>
              <Th>skills</Th>
              <Th>started</Th>
              <Th>transcript</Th>
              <Th>hooks</Th>
            </tr>
          }
        >
          {data.map((s) => (
            <tr key={s.session_id}>
              <Td title={s.title || undefined}>
                <Link to={`/sessions/${s.session_id}`}>
                  {s.title || <span className="qvr-table__muted">untitled session</span>}
                </Link>
              </Td>
              <Td>
                <Badge tone="info">{prettyAgent(s.agent_name)}</Badge>
              </Td>
              <Td>
                {s.skills && s.skills.length > 0 ? (
                  <span style={{ display: "flex", flexWrap: "wrap", gap: 4 }}>
                    {s.skills.map((n) => (
                      <Badge key={n} tone="accent">
                        {n}
                      </Badge>
                    ))}
                  </span>
                ) : (
                  <span className="qvr-table__muted">—</span>
                )}
              </Td>
              <Td muted>{fmtTime(s.started_at)}</Td>
              <Td muted>{s.transcript_lines}</Td>
              <Td muted>{s.hook_payloads}</Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}

function DateInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <span className="qvr-input-wrap">
      <input
        type="date"
        className="qvr-input"
        value={value}
        onChange={(e) => onChange(e.target.value)}
      />
    </span>
  );
}
