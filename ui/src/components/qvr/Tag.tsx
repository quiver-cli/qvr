import type { ReactNode } from "react";
import { short } from "../../lib/format";

// Tag — small mono chip for refs and SHAs, with an optional dimmed lead glyph
// ("@" for versions, "#" for commits).
export function Tag({
  lead,
  title,
  children,
}: {
  lead?: ReactNode;
  title?: string;
  children: ReactNode;
}) {
  return (
    <span className="qvr-tag qvr-tag--mono" title={title}>
      {lead != null && <span className="qvr-tag__lead">{lead}</span>}
      {children}
    </span>
  );
}

// VersionTag — the version pin, the one unit every observability surface uses
// to talk about a skill's identity: "@ref #sha7" when an invocation's load
// path proved which artifact ran, or the unpinned ghost "@unknown" when the
// agent's records carry no evidence. Unknown renders quiet (dashed, faint):
// missing telemetry is an absence, never an alarm.
export function VersionTag({
  refName,
  sha,
  title,
}: {
  refName?: string;
  sha?: string;
  title?: string;
}) {
  if (!refName && !sha) {
    return (
      <span
        className="qvr-tag qvr-tag--mono qvr-tag--ghost"
        title={
          title ??
          "version unknown — this agent's records don't show which copy of the skill loaded"
        }
      >
        <span className="qvr-tag__lead">@</span>unknown
      </span>
    );
  }
  return (
    <span className="qvr-tag qvr-tag--mono" title={title ?? (sha || refName)}>
      <span className="qvr-tag__lead">@</span>
      {refName || short(sha ?? "")}
      {refName && sha ? <span className="qvr-tag__lead">#{short(sha)}</span> : null}
    </span>
  );
}
