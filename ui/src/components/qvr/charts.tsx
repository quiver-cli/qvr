import { useState } from "react";
import type { ReactNode } from "react";
import { Card } from "./Card";

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

// StatCard — overview stat tile: lime icon, 30px number, micro label, and an
// optional quieter detail line (keeps the primary label on one clean line).
export function StatCard({
  icon,
  value,
  label,
  sub,
}: {
  icon: ReactNode;
  value: ReactNode;
  label: string;
  sub?: string;
}) {
  return (
    <Card className="qvr-stat">
      {icon}
      <div className="qvr-stat__num">{value}</div>
      <div className="qvr-stat__label" title={label}>
        {label}
      </div>
      {sub && (
        <div className="qvr-stat__sub" title={sub}>
          {sub}
        </div>
      )}
    </Card>
  );
}

// ---- interactive stacked bar chart -------------------------------------------

// ChartSeg is one stacked segment of a column. dim renders the segment at
// reduced opacity — the "same series, lesser state" encoding (e.g. an agent's
// sessions that used no skill).
export interface ChartSeg {
  label: string;
  value: number;
  color: string;
  dim?: boolean;
}

// ChartCol is one x-axis column: a label plus its stacked segments.
export interface ChartCol {
  label: string;
  segs: ChartSeg[];
  // note renders an extra line in the hover tooltip (e.g. a percentage).
  note?: string;
}

// StackedBarChart — hand-rolled stacked bars with a real y-axis, x labels, and
// a hover tooltip showing each segment's value. No chart library: hairline
// strokes on token colors fit the kit, and the payloads are tiny.
export function StackedBarChart({
  cols,
  height = 150,
  yFmt = (n: number) => String(n),
}: {
  cols: ChartCol[];
  height?: number;
  yFmt?: (n: number) => string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  const max = Math.max(1, ...cols.map((c) => c.segs.reduce((n, s) => n + s.value, 0)));
  // Up to three ticks (max, mid, 0) — deduped so a tiny scale doesn't repeat.
  const ticks = [...new Set([max, Math.ceil(max / 2), 0])];

  return (
    <div style={{ display: "flex", gap: 8 }}>
      <div
        style={{
          display: "flex",
          flexDirection: "column",
          justifyContent: "space-between",
          height,
          flex: "none",
          textAlign: "right",
          minWidth: 28,
        }}
      >
        {ticks.map((t) => (
          <span key={t} className="qvr-scan__scanner">
            {yFmt(t)}
          </span>
        ))}
      </div>
      <div style={{ flex: 1, minWidth: 0 }}>
        <div
          style={{
            position: "relative",
            display: "flex",
            alignItems: "flex-end",
            gap: 3,
            height,
            borderLeft: "var(--border-w) solid var(--border-subtle)",
            borderBottom: "var(--border-w) solid var(--border-subtle)",
            paddingLeft: 3,
          }}
          onMouseLeave={() => setHover(null)}
        >
          {cols.map((c, i) => {
            const total = c.segs.reduce((n, s) => n + s.value, 0);
            return (
              <div
                key={c.label}
                onMouseEnter={() => setHover(i)}
                style={{
                  flex: 1,
                  minWidth: 3,
                  display: "flex",
                  flexDirection: "column-reverse",
                  height: "100%",
                  cursor: "default",
                  outline: hover === i ? "var(--border-w) solid var(--border-strong)" : "none",
                  outlineOffset: 1,
                }}
              >
                {c.segs.map((s) => {
                  const base = s.dim ? 0.3 : 0.9;
                  return (
                    <div
                      key={s.label}
                      style={{
                        height: `${(s.value / max) * 100}%`,
                        background: s.color,
                        borderRadius: 1,
                        opacity: hover === null || hover === i ? base : base / 2,
                      }}
                    />
                  );
                })}
                {total === 0 && <div style={{ height: 1 }} />}
              </div>
            );
          })}
          {hover !== null && cols[hover] && (
            <ChartTip col={cols[hover]} index={hover} count={cols.length} />
          )}
        </div>
        <div style={{ display: "flex", justifyContent: "space-between", marginTop: 6 }}>
          <span className="qvr-scan__scanner">{cols[0]?.label}</span>
          {cols.length > 2 && (
            <span className="qvr-scan__scanner">{cols[Math.floor(cols.length / 2)]?.label}</span>
          )}
          <span className="qvr-scan__scanner">{cols[cols.length - 1]?.label}</span>
        </div>
      </div>
    </div>
  );
}

// ChartTip is the hover tooltip: column label, per-segment values, total, and
// the optional note line. Anchored over the hovered column, flipping side at
// the chart's midpoint so it never clips.
function ChartTip({ col, index, count }: { col: ChartCol; index: number; count: number }) {
  const leftSide = index < count / 2;
  const x = ((index + 0.5) / count) * 100;
  const total = col.segs.reduce((n, s) => n + s.value, 0);
  return (
    <div
      style={{
        position: "absolute",
        top: 0,
        left: leftSide ? `${x}%` : undefined,
        right: leftSide ? undefined : `${100 - x}%`,
        background: "var(--surface-overlay)",
        border: "var(--border-w) solid var(--border)",
        borderRadius: "var(--radius-md)",
        padding: "6px 9px",
        pointerEvents: "none",
        zIndex: 5,
        whiteSpace: "nowrap",
        boxShadow: "var(--shadow-pop)",
      }}
    >
      <div
        style={{
          fontFamily: "var(--font-mono)",
          fontSize: "var(--text-xs)",
          color: "var(--text)",
          marginBottom: 3,
        }}
      >
        {col.label} · {total}
      </div>
      {col.segs.filter((s) => s.value > 0).map((s) => (
        <div
          key={s.label}
          style={{ display: "flex", alignItems: "center", gap: 6, fontSize: "var(--text-xs)" }}
        >
          <span style={{ width: 7, height: 7, borderRadius: 2, background: s.color }} />
          <span style={{ color: "var(--text-muted)" }}>{s.label}</span>
          <span style={{ marginLeft: "auto", color: "var(--text)", fontFamily: "var(--font-mono)" }}>
            {s.value}
          </span>
        </div>
      ))}
      {col.note && (
        <div className="qvr-scan__scanner" style={{ marginTop: 3 }}>
          {col.note}
        </div>
      )}
    </div>
  );
}
