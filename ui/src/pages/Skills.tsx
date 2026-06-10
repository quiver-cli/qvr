import { useMemo, useState } from "react";
import { Search } from "lucide-react";
import { api, scopeToken, useFetch, type SkillUsageRow } from "../api";
import {
  Badge,
  EmptyState,
  ErrorBox,
  Input,
  Loading,
  PageHead,
  Prompt,
  SkillRowItem,
  StatusBadge,
  Tag,
} from "../components/qvr";
import { fmtCount, fmtShare, relTime } from "../lib/format";

// Skills — every skill in the lock, ranked by observed verified work (the
// server sorts by verified desc), each row carrying its usage evidence.
// Skills that fired historically but are no longer installed stay listed
// (installed:false) so history reads honestly.
export default function Skills() {
  const { data, error, loading } = useFetch(
    () => api.metricsSkills(),
    `skills-metrics:${scopeToken()}`,
  );
  const [filter, setFilter] = useState("");

  const rows = useMemo(() => {
    const all = data?.skills ?? [];
    if (!filter) return all;
    const f = filter.toLowerCase();
    return all.filter((s) => s.name.toLowerCase().includes(f) || s.registry?.includes(f));
  }, [data, filter]);

  const registries = useMemo(
    () => new Set((data?.skills ?? []).map((s) => s.registry).filter(Boolean)).size,
    [data],
  );

  return (
    <>
      <PageHead
        title="Skills"
        sub={
          data
            ? `${data.headline.installed} pinned in qvr.lock · ${registries} ${registries === 1 ? "registry" : "registries"}`
            : "Installed skills recorded in the lock file."
        }
        actions={
          <Input
            icon={<Search size={15} />}
            placeholder="filter skills…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        }
      />
      {loading && <Loading />}
      {error && <ErrorBox message={error} />}
      {data && !data.audit_enabled && data.skills.length > 0 && (
        <p className="qvr-sub" style={{ marginBottom: 14 }}>
          usage columns need the audit pipeline — enable capture to see which skills earn
          their keep:{" "}
          <span style={{ fontFamily: "var(--font-mono)" }}>qvr audit enable</span>
        </p>
      )}
      {data && rows.length === 0 && !filter && (
        <>
          <EmptyState title="empty quiver">
            <span>
              no skills pinned yet. add one:
            </span>
          </EmptyState>
          <div style={{ maxWidth: 420, margin: "0 auto" }}>
            <Prompt command="qvr add <skill>" />
          </div>
        </>
      )}
      {data && rows.length === 0 && filter && (
        <EmptyState title="no match" art={false}>
          nothing in the lock matches “{filter}”.
        </EmptyState>
      )}
      <div>
        {rows.map((s) => (
          <SkillRow key={s.name} s={s} auditEnabled={data?.audit_enabled ?? false} />
        ))}
      </div>
    </>
  );
}

function SkillRow({ s, auditEnabled }: { s: SkillUsageRow; auditEnabled: boolean }) {
  return (
    <SkillRowItem
      to={`/skills/${encodeURIComponent(s.name)}`}
      lead={leadBadge(s)}
      name={s.name}
      desc={
        auditEnabled
          ? s.invocations > 0
            ? `${fmtCount(s.invocations)} runs · ${fmtShare(s.verifiedShare)} verified · ${fmtCount(
                s.tokensIn + s.tokensOut,
              )} tok · last fired ${relTime(s.lastFired)}`
            : "never fired"
          : undefined
      }
      right={
        <>
          {s.registry && <span className="qvr-skillrow__reg">{s.registry}</span>}
          {s.ref && <Tag lead="@">{s.ref}</Tag>}
        </>
      }
    />
  );
}

// leadBadge picks the row's status: removed > disabled > gate decision.
function leadBadge(s: SkillUsageRow) {
  if (!s.installed) {
    return (
      <Badge tone="neutral" dot>
        removed
      </Badge>
    );
  }
  if (s.disabled) {
    return (
      <Badge tone="neutral" dot>
        disabled
      </Badge>
    );
  }
  return <StatusBadge value={s.gate || "unscanned"} />;
}
