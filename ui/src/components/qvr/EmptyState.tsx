import type { ReactNode } from "react";
import { QVR_ASCII } from "../../assets/quiverAscii";

// EmptyState — the lime ASCII quiver watermark over terse copy. The only
// "illustration" this brand has.
export function EmptyState({
  title,
  children,
  art = true,
}: {
  title: string;
  children?: ReactNode;
  art?: boolean;
}) {
  return (
    <div className="qvr-empty">
      {art && <pre className="qvr-empty__art">{QVR_ASCII}</pre>}
      <h3>{title}</h3>
      {children != null && <p>{children}</p>}
    </div>
  );
}
