import type { ReactNode } from "react";
import { Badge } from "./Badge";
import { Card } from "./Card";
import { fmtShare } from "../../lib/format";

// Inline-SVG chart primitives. No chart library: hairline strokes on token
// colors fit the mechanical terminal aesthetic, and payloads are tiny.

export interface SparkPoint {
  label: string; // x label (day)
  value: number;
}

// Sparkline — 1.5px accent polyline with a soft area fill.
export function Sparkline({
  points,
  height = 48,
  title,
}: {
  points: SparkPoint[];
  height?: number;
  title?: string;
}) {
  const w = 100; // viewBox units; scales to container width
  const max = Math.max(1, ...points.map((p) => p.value));
  const step = points.length > 1 ? w / (points.length - 1) : w;
  const y = (v: number) => 4 + (1 - v / max) * (40 - 8);
  const coords = points.map((p, i) => [points.length > 1 ? i * step : w / 2, y(p.value)]);
  const line = coords.map(([x, py]) => `${x.toFixed(2)},${py.toFixed(2)}`).join(" ");
  const area = `0,40 ${line} ${w},40`;
  return (
    <svg
      className="qvr-spark"
      viewBox={`0 0 ${w} 40`}
      preserveAspectRatio="none"
      style={{ height }}
      role="img"
      aria-label={title ?? "invocation trend"}
    >
      {points.length > 0 && (
        <>
          <polygon points={area} fill="var(--accent-soft)" stroke="none" />
          <polyline
            points={line}
            fill="none"
            stroke="var(--accent)"
            strokeWidth="1.5"
            vectorEffect="non-scaling-stroke"
            strokeLinejoin="round"
            strokeLinecap="round"
          />
          {coords.length === 1 && (
            <circle cx={coords[0][0]} cy={coords[0][1]} r="2" fill="var(--accent)" />
          )}
        </>
      )}
    </svg>
  );
}

// BarRow — one horizontal comparison bar (token rollups, per-agent shares).
export function BarRow({
  label,
  value,
  max,
  display,
}: {
  label: string;
  value: number;
  max: number;
  display: string;
}) {
  const pct = value > 0 && max > 0 ? Math.max(2, (value / max) * 100) : 0;
  return (
    <div className="qvr-barrow">
      <span className="qvr-barrow__label">{label}</span>
      <span className="qvr-barrow__track">
        <span className="qvr-barrow__fill" style={{ width: `${pct}%` }} />
      </span>
      <span className="qvr-barrow__value">{display}</span>
    </div>
  );
}

// ShareStat — the LOUD verified-share hero: accent card, 38px brand number.
// The moat metric (lock-proven attribution) renders louder than anything else.
export function ShareStat({
  share,
  label,
  sub,
}: {
  share: number | undefined;
  label: string;
  sub?: ReactNode;
}) {
  return (
    <Card variant="accent" className="qvr-stat">
      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
        <span className="qvr-sharestat__num">{fmtShare(share)}</span>
        <Badge tone="accent" dot>
          verified
        </Badge>
      </div>
      <div className="qvr-stat__label" style={{ marginTop: 8 }}>
        {label}
      </div>
      {sub != null && (
        <div className="qvr-stat__label" style={{ marginTop: 2 }}>
          {sub}
        </div>
      )}
    </Card>
  );
}

// StatCard — overview stat tile: lime icon, 30px number, micro label.
export function StatCard({
  icon,
  value,
  label,
}: {
  icon: ReactNode;
  value: ReactNode;
  label: string;
}) {
  return (
    <Card className="qvr-stat">
      {icon}
      <div className="qvr-stat__num">{value}</div>
      <div className="qvr-stat__label">{label}</div>
    </Card>
  );
}
