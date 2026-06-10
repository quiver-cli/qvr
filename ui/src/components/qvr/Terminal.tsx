import type { ReactNode } from "react";
import { Terminal as TerminalIcon } from "lucide-react";

// Terminal — the signature DS surface: warm-ink window chrome with traffic
// lights, used for raw traces and JSON payloads.
export function Terminal({
  title = "quiver — zsh",
  children,
  maxHeight,
}: {
  title?: ReactNode;
  children: ReactNode;
  maxHeight?: number | string;
}) {
  return (
    <div className="qvr-term">
      <div className="qvr-term__bar">
        <span className="qvr-term__dots">
          <span className="qvr-term__dot" />
          <span className="qvr-term__dot" />
          <span className="qvr-term__dot" />
        </span>
        <span className="qvr-term__title">
          <TerminalIcon />
          {title}
        </span>
      </div>
      <div className="qvr-term__body" style={maxHeight != null ? { maxHeight } : undefined}>
        {children}
      </div>
    </div>
  );
}

type LineTone = "default" | "muted" | "add" | "remove" | "warn" | "ok";

export function TerminalLine({
  prompt = false,
  path,
  tone = "default",
  children,
}: {
  prompt?: boolean | string;
  path?: ReactNode;
  tone?: LineTone;
  children?: ReactNode;
}) {
  const cls = ["qvr-line", tone !== "default" ? `qvr-line--${tone}` : ""].filter(Boolean).join(" ");
  return (
    <div className={cls}>
      {prompt !== false && (
        <span className="qvr-line__prompt">{prompt === true ? "›" : prompt}</span>
      )}
      {path != null && <span className="qvr-line__path">{path}</span>}
      <span className="qvr-line__cmd">{children}</span>
    </div>
  );
}
