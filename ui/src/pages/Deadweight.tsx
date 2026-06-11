import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api, scopeToken, useFetch, type SkillUsageRow } from "../api";
import {
  EmptyState,
  ErrorBox,
  Loading,
  PageHead,
  Prompt,
  RefreshButton,
  Section,
  Table,
  Tag,
  Td,
  Th,
} from "../components/qvr";
import { fmtCount, fmtTime, relTime } from "../lib/format";

// How long a skill can sit idle before the stale tier flags it.
const STALE_DAYS = 30;

// Deadweight — installed-but-never-fired skills (server-computed: lock entries
// with zero SKILL spans ever), plus a client-side "stale" tier (fired once
// upon a time, silent for 30+ days). The UI never prunes; every row hands you
// the exact command.
export default function Deadweight() {
  const [nonce, setNonce] = useState(0);
  const dw = useFetch(api.deadweight, `deadweight:${scopeToken()}:${nonce}`);
  const metrics = useFetch(() => api.metricsSkills(), `deadweight-metrics:${scopeToken()}:${nonce}`);

  const stale = useMemo(() => staleRows(metrics.data?.skills), [metrics.data]);

  return (
    <>
      <PageHead
        title="Dead weight"
        sub="Skills that take up lock space but do no observed work in any recorded
session. Counts cover the sessions qvr has discovered — a back-filled history
can predate an install, so check the installed date before pruning."
        actions={<RefreshButton onClick={() => setNonce((n) => n + 1)} busy={dw.loading} />}
      />
      {dw.loading && <Loading />}
      {dw.error && <ErrorBox message={dw.error} />}
      {dw.data && !dw.data.audit_enabled && (
        <>
          <EmptyState title="can't measure dead weight yet">
            <span>
              usage is unknown until the audit pipeline records sessions — nothing here
              suggests pruning. start measuring:
            </span>
          </EmptyState>
          <div style={{ maxWidth: 420, margin: "0 auto" }}>
            <Prompt command="qvr audit enable" />
          </div>
        </>
      )}
      {dw.data?.audit_enabled && dw.data.rows.length === 0 && (
        <EmptyState title="no dead weight">every pinned skill has fired. clean quiver.</EmptyState>
      )}
      {dw.data?.audit_enabled && dw.data.rows.length > 0 && (
        <Table
          head={
            <tr>
              <Th>skill</Th>
              <Th>registry</Th>
              <Th>installed</Th>
              <Th>age</Th>
              <Th>prune</Th>
            </tr>
          }
        >
          {dw.data.rows.map((r) => (
            <tr key={`${r.scope ?? ""}/${r.name}`}>
              <Td>
                <Link to={`/skills/${encodeURIComponent(r.name)}`}>{r.name}</Link>
                {r.disabled && <span className="qvr-table__muted"> · disabled</span>}
              </Td>
              <Td muted>
                {r.registry || "—"}
                {r.ref ? <Tag lead="@">{r.ref}</Tag> : null}
              </Td>
              <Td muted title={r.installedAt}>
                {fmtTime(r.installedAt)}
              </Td>
              <Td muted>{r.ageDays > 0 ? `${r.ageDays}d` : "—"}</Td>
              <Td>
                <div style={{ minWidth: 260 }}>
                  <Prompt command={`qvr remove ${r.name}`} />
                </div>
              </Td>
            </tr>
          ))}
        </Table>
      )}

      {dw.data?.audit_enabled && stale.length > 0 && (
        <Section title={`stale (no runs in ${STALE_DAYS}d)`}>
          <Table
            head={
              <tr>
                <Th>skill</Th>
                <Th>lifetime runs</Th>
                <Th>last fired</Th>
                <Th>disable</Th>
              </tr>
            }
          >
            {stale.map((s) => (
              <tr key={s.name}>
                <Td>
                  <Link to={`/skills/${encodeURIComponent(s.name)}`}>{s.name}</Link>
                </Td>
                <Td muted>{fmtCount(s.invocations)}</Td>
                <Td muted title={s.lastFired}>
                  {relTime(s.lastFired)}
                </Td>
                <Td>
                  <div style={{ minWidth: 260 }}>
                    <Prompt command={`qvr disable ${s.name}`} />
                  </div>
                </Td>
              </tr>
            ))}
          </Table>
        </Section>
      )}
    </>
  );
}

// staleRows: installed skills that HAVE fired but not within the window.
function staleRows(rows?: SkillUsageRow[]): SkillUsageRow[] {
  if (!rows) return [];
  const cutoff = Date.now() - STALE_DAYS * 24 * 3600 * 1000;
  return rows
    .filter((s) => {
      if (!s.installed || s.disabled || s.invocations === 0 || !s.lastFired) return false;
      const t = new Date(s.lastFired).getTime();
      return !isNaN(t) && t < cutoff;
    })
    .sort((a, b) => new Date(a.lastFired!).getTime() - new Date(b.lastFired!).getTime());
}
