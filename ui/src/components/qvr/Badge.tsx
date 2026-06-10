import type { ReactNode } from "react";
import { toneFor, type BadgeTone } from "../../lib/tones";

// Badge — the DS status unit: mono micro-label with an optional leading dot in
// a semantic color. Status is ALWAYS a badge with a dot, never an emoji.
export function Badge({
  tone = "neutral",
  dot = false,
  title,
  children,
}: {
  tone?: BadgeTone;
  dot?: boolean;
  title?: string;
  children: ReactNode;
}) {
  return (
    <span className={`qvr-badge qvr-badge--${tone}`} title={title}>
      {dot && <span className="qvr-badge__dot" />}
      {children}
    </span>
  );
}

// StatusBadge auto-tones from the status vocabulary (allowed/blocked/verified/…).
export function StatusBadge({ value, dot = true }: { value?: string; dot?: boolean }) {
  return (
    <Badge tone={toneFor(value)} dot={dot}>
      {value || "—"}
    </Badge>
  );
}
