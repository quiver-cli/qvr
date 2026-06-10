import type { RegistryVersion, SkillVersionUsage } from "../api";
import { Badge, Tag } from "./qvr";
import { fmtCount, fmtShare, relTime, short } from "../lib/format";

// VersionTimeline renders a version history in the kit's .qvr-ver rhythm:
// dot + pill + ref + sha rows, the lock's pinned version filled success-soft.
// Two sources feed it:
//   - registry refs (branches/tags from the bare clone) via `versions`
//   - observed lineage (per-version usage from spans) via `usage`
// When usage is provided for a version, the row grows invocation/verified/token
// chips — the lineage view: how each pinned version actually behaved.

export interface TimelineRow {
  key: string;
  ref: string;
  sha?: string;
  kind?: "branch" | "tag" | "fired"; // pill label; "fired" = seen only in spans
  isDefault?: boolean;
  current?: boolean;
  when?: string; // ISO — commit time or last-fired
  subject?: string;
  usage?: SkillVersionUsage;
}

// fromRegistryVersions adapts the registry refs payload, marking the selected
// (installed) ref/sha current.
export function fromRegistryVersions(
  versions: RegistryVersion[],
  selectedRef?: string,
  selectedSha?: string,
): TimelineRow[] {
  return versions.map((v) => ({
    key: `${v.isTag ? "tag" : "branch"}:${v.ref}`,
    ref: v.ref,
    sha: v.sha,
    kind: v.isTag ? "tag" : "branch",
    isDefault: v.current,
    current:
      (!!selectedRef && v.ref === selectedRef) || (!!selectedSha && v.sha === selectedSha),
    when: v.time,
    subject: v.subject,
  }));
}

// fromVersionUsage adapts the observed lineage rollup (report card "versions"
// tab): one row per (ref, commit) the skill fired as.
export function fromVersionUsage(usage: SkillVersionUsage[]): TimelineRow[] {
  return usage.map((u, i) => ({
    key: `fired:${u.ref ?? ""}:${u.commit ?? ""}:${i}`,
    ref: u.ref || "(unidentified)",
    sha: u.commit,
    kind: "fired",
    current: u.current,
    when: u.lastFired,
    usage: u,
  }));
}

export default function VersionTimeline({ rows }: { rows: TimelineRow[] }) {
  if (rows.length === 0) {
    return <p className="qvr-sub">no versions found.</p>;
  }
  return (
    <div>
      {rows.map((v) => (
        <div key={v.key} className={"qvr-ver" + (v.current ? " qvr-ver--current" : "")}>
          <span className="qvr-ver__dot" />
          <div className="qvr-ver__body">
            <div className="qvr-ver__top">
              {v.kind && <span className="qvr-pill">{v.kind}</span>}
              <span className="qvr-ver__branch">{v.ref}</span>
              {v.sha && (
                <span className="qvr-ver__sha" title={v.sha}>
                  {short(v.sha)}
                </span>
              )}
              {v.isDefault && <Badge tone="neutral">default</Badge>}
              {v.current && (
                <Badge tone="success" dot>
                  current
                </Badge>
              )}
              {v.when && <span className="qvr-ver__when">{relTime(v.when)}</span>}
            </div>
            {v.subject && <p className="qvr-ver__msg">{v.subject}</p>}
            {v.usage && (
              <div className="qvr-ver__top" style={{ marginTop: 6, gap: 6 }}>
                <Tag>{fmtCount(v.usage.invocations)} runs</Tag>
                <Tag>
                  {fmtShare(
                    v.usage.invocations > 0 ? v.usage.verified / v.usage.invocations : undefined,
                  )}{" "}
                  verified
                </Tag>
                <Tag lead="↑">{fmtCount(v.usage.tokensIn)} tok</Tag>
                <Tag lead="↓">{fmtCount(v.usage.tokensOut)} tok</Tag>
                <Tag>{fmtCount(v.usage.sessions)} sessions</Tag>
              </div>
            )}
          </div>
        </div>
      ))}
    </div>
  );
}
