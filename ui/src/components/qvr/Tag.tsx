import type { ReactNode } from "react";

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
