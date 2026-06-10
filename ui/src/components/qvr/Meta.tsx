import type { ReactNode } from "react";

// Meta — the detail header's key/value strip: uppercase micro-keys, mono values.
export function Meta({ children, style }: { children: ReactNode; style?: React.CSSProperties }) {
  return (
    <div className="qvr-meta" style={style}>
      {children}
    </div>
  );
}

export function MetaItem({ k, children }: { k: string; children: ReactNode }) {
  return (
    <span className="qvr-meta__item">
      <span className="qvr-meta__k">{k}</span>
      <span className="qvr-meta__v">{children}</span>
    </span>
  );
}
