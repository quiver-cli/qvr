import { Copy } from "lucide-react";
import { useToast } from "./Toast";

// Prompt — the CLI-command surface. Every management affordance in the
// read-only dashboard routes through this: the UI never mutates, it hands you
// the exact `qvr …` command with a copy button. The chevron is the brand mark.
export function Prompt({ command, hint }: { command: string; hint?: string }) {
  const toast = useToast();
  return (
    <span className="qvr-prompt" style={{ width: "100%" }}>
      <span className="qvr-prompt__chevron">›</span>
      <span style={{ flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis" }}>
        {command}
      </span>
      <button
        type="button"
        className="qvr-prompt__copy"
        aria-label={`copy: ${command}`}
        onClick={() => {
          void navigator.clipboard?.writeText(command).then(
            () => {
              toast.push({ tone: "accent", title: "copied", body: hint ?? command });
            },
            (err: unknown) => {
              toast.push({
                tone: "danger",
                title: "copy failed",
                body: err instanceof Error ? err.message : "clipboard unavailable",
              });
            },
          );
        }}
      >
        <Copy />
      </button>
    </span>
  );
}

// CopyChip — the tiny inline copy button next to SHAs / hashes in meta strips.
export function CopyChip({ value, label = "copy" }: { value: string; label?: string }) {
  const toast = useToast();
  return (
    <button
      type="button"
      className="qvr-copy"
      onClick={() => {
        void navigator.clipboard?.writeText(value).then(
          () => {
            toast.push({ tone: "accent", title: "copied", body: value });
          },
          (err: unknown) => {
            toast.push({
              tone: "danger",
              title: "copy failed",
              body: err instanceof Error ? err.message : "clipboard unavailable",
            });
          },
        );
      }}
    >
      <Copy />
      {label}
    </button>
  );
}
