import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { RefreshCw } from "lucide-react";
import { api, prettyAgent, scopeToken, useFetch, type DiscoverReport } from "../api";
import {
  Badge,
  Button,
  EmptyState,
  ErrorBox,
  Field,
  Loading,
  PageHead,
  RefreshButton,
  Select,
  Table,
  Td,
  Th,
} from "../components/qvr";
import { fmtEpochMs, fmtSpan, fmtTokenPair } from "../lib/format";

// The agents qvr can discover today. The filter offers these plus any agent
// actually present in the loaded rows (so an unexpected one still shows up).
const KNOWN_AGENTS = [
  "claude",
  "codex",
  "copilot",
  "cursor",
  "droid",
  "gemini",
  "hermes",
  "openclaw",
  "opencode",
  "pi",
];

export default function Sessions() {
  const [agent, setAgent] = useState("");
  const [skill, setSkill] = useState("");
  const [since, setSince] = useState("");
  const [until, setUntil] = useState("");
  // Token sort is server-side (the list is limit-truncated), so it lives in
  // the fetch key like the filters.
  const [sortTokens, setSortTokens] = useState(false);

  // Re-fetch whenever the scope or any filter changes (the key encodes them all).
  const key = `sessions:${scopeToken()}:${agent}|${skill}|${since}|${until}|${sortTokens}`;
  // 10s polling keeps the list live against the server's background scan.
  const { data, error, loading, reload } = useFetch(
    () => api.sessions({ agent, skill, since, until, sort: sortTokens ? "tokens" : undefined }),
    key,
    10_000,
  );

  // Skill dropdown options: installed skills in scope, unioned with any skill
  // seen in the current rows (covers skills used but since removed/ejected).
  const skillsList = useFetch(() => api.skills(), `sessions-skills:${scopeToken()}`);
  const agentOptions = useMemo(() => {
    const set = new Set(KNOWN_AGENTS);
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
        actions={
          <>
            <RefreshButton onClick={reload} busy={loading} />
            <DiscoverButton onDone={reload} />
          </>
        }
      />

      <div style={{ display: "flex", flexWrap: "wrap", alignItems: "flex-end", gap: 12, marginBottom: 16 }}>
        <Field label="agent">
          <Select value={agent} onChange={(e) => setAgent(e.target.value)}>
            <option value="">all</option>
            {agentOptions.map((a) => (
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
            : "Skill-using agent sessions appear here. Hit discover (or run qvr audit discover) to back-fill from your agents' own session stores — no agent setup needed."}
        </EmptyState>
      )}
      {data && data.length > 0 && (
        <Table
          head={
            <tr>
              <Th>session</Th>
              <Th>agent</Th>
              <Th>skills</Th>
              <Th>started</Th>
              <Th>turns</Th>
              <Th>tools</Th>
              <Th>duration</Th>
              <Th onSort={() => setSortTokens((v) => !v)} sortActive={sortTokens}>
                tokens (in / out)
              </Th>
            </tr>
          }
        >
          {data.map((s) => (
            <tr key={s.session_id}>
              <Td title={s.title || undefined}>
                <div
                  style={{
                    maxWidth: "42ch",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                    whiteSpace: "nowrap",
                  }}
                >
                  <Link to={`/sessions/${s.session_id}`}>
                    {s.title || <span className="qvr-table__muted">untitled session</span>}
                  </Link>
                </div>
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
              <Td muted>{fmtEpochMs(s.started_ms)}</Td>
              <Td muted>{s.turns}</Td>
              <Td muted>{s.tools}</Td>
              <Td muted>{fmtSpan(s.ended_ms - s.started_ms)}</Td>
              <Td muted={s.tokens_in == null && s.tokens_out == null}>
                {fmtTokenPair(s.tokens_in, s.tokens_out)}
              </Td>
            </tr>
          ))}
        </Table>
      )}
    </>
  );
}

// DiscoverButton triggers POST /api/discover — scan the agents' native session
// stores for new/changed sessions — then reports the outcome inline and
// reloads the list.
function DiscoverButton({ onDone }: { onDone: () => void }) {
  const [busy, setBusy] = useState(false);
  const [note, setNote] = useState<string | null>(null);

  const run = async () => {
    setBusy(true);
    setNote(null);
    try {
      const rep = await api.discover();
      setNote(summarizeDiscover(rep));
      onDone();
    } catch (e) {
      setNote(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <span style={{ display: "inline-flex", alignItems: "center", gap: 10 }}>
      {note && <span className="qvr-scan__scanner">{note}</span>}
      <Button size="sm" onClick={run} disabled={busy} leftIcon={<RefreshCw size={13} />}>
        {busy ? "scanning…" : "discover"}
      </Button>
    </span>
  );
}

function summarizeDiscover(rep: DiscoverReport): string {
  let ingested = 0;
  let skipped = 0;
  let unchanged = 0;
  rep.agents?.forEach((a) => {
    ingested += a.ingested;
    skipped += a.skipped;
    unchanged += a.unchanged;
  });
  if (ingested === 0 && skipped === 0) return `up to date (${unchanged} unchanged)`;
  const parts = [`${ingested} recorded`];
  if (skipped > 0) parts.push(`${skipped} without skills skipped`);
  return parts.join(", ");
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
