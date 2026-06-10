// Status-vocabulary → Badge tone mapping. One place so the scan gate,
// signature status, enabled/disabled, and span outcomes all color the same.

export type BadgeTone = "neutral" | "accent" | "success" | "danger" | "info" | "warning" | "solid";

export function toneFor(value?: string): BadgeTone {
  switch ((value ?? "").toLowerCase()) {
    case "success":
    case "allowed":
    case "verified":
    case "enabled":
    case "passed":
    case "synced":
    case "installed":
      return "success";
    case "critical":
    case "high":
    case "error":
    case "blocked":
    case "invalid":
    case "failed":
      return "danger";
    case "warning":
    case "medium":
    case "dirty":
    case "behind":
    case "unverified":
      return "warning";
    case "skipped":
    case "none":
    case "unscanned":
    case "disabled":
    case "low":
    case "":
      return "neutral";
    default:
      return "info";
  }
}

// Span kinds color distinctly on the session timeline: the SKILL span is the
// product's protagonist, so it gets the accent.
export function spanKindTone(kind?: string): BadgeTone {
  switch ((kind ?? "").toUpperCase()) {
    case "SKILL":
      return "accent";
    case "LLM":
      return "info";
    case "TOOL":
      return "neutral";
    default:
      return "neutral";
  }
}
