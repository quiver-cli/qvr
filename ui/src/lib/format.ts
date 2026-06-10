// Display formatting helpers. Pure functions, no React.

// short truncates a SHA-like identifier for display.
export function short(sha?: string, n = 7): string {
  if (!sha) return "—";
  const body = sha.startsWith("sha256:") ? sha.slice(7) : sha;
  return body.length > n ? body.slice(0, n) : body;
}

// fmtTime renders an ISO timestamp in the viewer's locale.
export function fmtTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

// relTime renders "2d ago"-style relative time from an ISO timestamp.
export function relTime(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (isNaN(d.getTime())) return iso;
  const s = Math.max(0, (Date.now() - d.getTime()) / 1000);
  if (s < 60) return "just now";
  const m = s / 60;
  if (m < 60) return `${Math.floor(m)}m ago`;
  const h = m / 60;
  if (h < 24) return `${Math.floor(h)}h ago`;
  const days = h / 24;
  if (days < 30) return `${Math.floor(days)}d ago`;
  const months = days / 30;
  if (months < 12) return `${Math.floor(months)}mo ago`;
  return `${Math.floor(months / 12)}y ago`;
}

// fmtCount humanizes a count: 1842 → "1.8k", 184220 → "184.2k", 2400000 → "2.4m".
export function fmtCount(n?: number): string {
  if (n == null) return "—";
  if (n < 1000) return String(n);
  if (n < 1_000_000) return `${(n / 1000).toFixed(1).replace(/\.0$/, "")}k`;
  return `${(n / 1_000_000).toFixed(1).replace(/\.0$/, "")}m`;
}

// fmtShare renders a 0..1 share as a percentage ("95%").
export function fmtShare(x?: number): string {
  if (x == null || isNaN(x)) return "—";
  return `${Math.round(x * 100)}%`;
}

// fmtMs renders a span duration.
export function fmtMs(ms: number): string {
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${Math.floor(ms / 60_000)}m ${Math.round((ms % 60_000) / 1000)}s`;
}

// prettyJSON stringifies an arbitrary JSON value with 2-space indent.
export function prettyJSON(v: unknown): string {
  try {
    return JSON.stringify(v, null, 2);
  } catch {
    return String(v);
  }
}
