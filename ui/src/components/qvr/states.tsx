import type { ReactNode } from "react";
import { CopyChip } from "./Prompt";

// Loading / error / code states in DS voice (terse, lowercase).

export function Loading() {
  return (
    <div className="qvr-empty" style={{ padding: "48px 20px" }}>
      <p style={{ fontFamily: "var(--font-mono)" }}>
        loading<span className="qvr-caret" />
      </p>
    </div>
  );
}

export function ErrorBox({ message }: { message: string }) {
  return (
    <div
      className="qvr-card"
      style={{
        borderColor: "var(--danger)",
        background: "var(--danger-soft)",
        padding: "12px 16px",
        fontFamily: "var(--font-mono)",
        fontSize: "var(--text-sm)",
        color: "var(--danger)",
      }}
      role="alert"
    >
      {message}
    </div>
  );
}

// CodeBlock — scrollable mono panel for pretty-printed JSON with a copy chip.
export function CodeBlock({ value, label }: { value: string; label?: ReactNode }) {
  return (
    <div style={{ marginTop: 8 }}>
      {label != null && (
        <div
          style={{
            display: "flex",
            alignItems: "center",
            justifyContent: "space-between",
            marginBottom: 4,
          }}
        >
          <span className="qvr-meta__k">{label}</span>
          <CopyChip value={value} />
        </div>
      )}
      <pre
        style={{
          maxHeight: 384,
          overflow: "auto",
          whiteSpace: "pre",
          margin: 0,
          padding: 12,
          background: "var(--surface-inset)",
          border: "1px solid var(--border)",
          borderRadius: "var(--radius-md)",
          fontFamily: "var(--font-code)",
          fontSize: "var(--text-xs)",
          lineHeight: "var(--leading-code)",
          color: "var(--text)",
        }}
      >
        {value}
      </pre>
    </div>
  );
}
