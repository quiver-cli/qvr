import type { ReactNode } from "react";

// Table — mono data table: hairline row dividers, uppercase micro-headers,
// raised-row hover. Same head/children seam as the old shell so pages convert
// mechanically.
export function Table({ head, children }: { head: ReactNode; children: ReactNode }) {
  return (
    <div className="qvr-card" style={{ overflowX: "auto" }}>
      <table className="qvr-table">
        <thead>{head}</thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export function Th({ children }: { children?: ReactNode }) {
  return <th scope="col">{children}</th>;
}

export function Td({
  children,
  className,
  title,
  muted = false,
}: {
  children: ReactNode;
  className?: string;
  title?: string;
  muted?: boolean;
}) {
  const cls = [muted ? "qvr-table__muted" : "", className ?? ""].filter(Boolean).join(" ");
  return (
    <td title={title} className={cls || undefined}>
      {children}
    </td>
  );
}
