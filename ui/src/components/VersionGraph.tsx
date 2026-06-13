import type { VersionGraph as VG, VersionGraphNode } from "../api";
import { layout } from "../lib/versiongraph";
import { Badge, Tag, VersionTag } from "./qvr";
import { fmtCount, relTime, short } from "../lib/format";

// VersionGraph renders a skill's version history as a real git tree: the backend
// supplies commit nodes with parent edges, lib/versiongraph lays them into
// lanes, and we draw a left SVG gutter (lane lines, branch/merge curves, commit
// dots) beside a compact one-line body per commit (a `git log --oneline --graph`
// rhythm). Two node shapes flow through: catalogue nodes carry branch/tag refs,
// lineage nodes carry observed usage. The empty-sha node is the detached
// unknown-version bucket. Callers fall back to VersionTimeline when no graph
// payload is present.

const ROW_H = 44;
const LANE_W = 18;
const DOT_R = 4.5;
const GUTTER_PAD = 8;

// Lane palette — distinct hues so concurrent branches read apart. Cycles.
const LANE_COLORS = [
  "var(--brand-600)",
  "var(--blue-500)",
  "var(--iris-400)",
  "var(--green-500)",
  "var(--yellow-500)",
  "var(--red-500)",
];
const laneColor = (col: number) => LANE_COLORS[col % LANE_COLORS.length];

export default function VersionGraph({ graph }: { graph: VG }) {
  const nodes = graph?.nodes ?? [];
  if (nodes.length === 0) {
    return <p className="qvr-sub">no versions found.</p>;
  }

  const { nodes: laid, edges, width } = layout(nodes);
  const gutterW = width * LANE_W + GUTTER_PAD;
  const height = laid.length * ROW_H;
  const cx = (col: number) => GUTTER_PAD / 2 + col * LANE_W + LANE_W / 2;
  const cy = (row: number) => row * ROW_H + ROW_H / 2;

  return (
    <div className="qvr-graph" style={{ position: "relative" }}>
      <svg
        className="qvr-graph__svg"
        width={gutterW}
        height={height}
        style={{ position: "absolute", left: 0, top: 0 }}
        aria-hidden="true"
      >
        {edges.map((e, i) => {
          const x1 = cx(e.fromCol);
          const y1 = cy(e.fromRow);
          const x2 = cx(e.toCol);
          const y2 = cy(e.toRow);
          const d =
            x1 === x2
              ? `M ${x1} ${y1} L ${x2} ${y2}`
              : `M ${x1} ${y1} C ${x1} ${y1 + ROW_H * 0.6}, ${x2} ${y2 - ROW_H * 0.6}, ${x2} ${y2}`;
          return (
            <path
              key={i}
              d={d}
              fill="none"
              stroke={laneColor(e.fromCol)}
              strokeWidth={1.5}
              opacity={0.5}
            />
          );
        })}
        {laid.map(({ node, row, col }) => {
          const unknown = node.sha === "";
          const color = laneColor(col);
          return (
            <circle
              key={row}
              cx={cx(col)}
              cy={cy(row)}
              r={DOT_R}
              fill={node.current ? color : "var(--surface)"}
              stroke={color}
              strokeWidth={1.6}
              strokeDasharray={unknown ? "2 2" : undefined}
              opacity={unknown ? 0.6 : 1}
            />
          );
        })}
      </svg>
      <div>
        {laid.map(({ node, row }) => (
          <div
            key={row}
            className={"qvr-graph__row" + (node.current ? " qvr-graph__row--current" : "")}
            style={{ height: ROW_H, paddingLeft: gutterW }}
          >
            <NodeBody node={node} />
          </div>
        ))}
      </div>
    </div>
  );
}

// NodeBody is the one-line commit row to the right of the graph gutter: refs (or
// the unknown tag), short sha, current badge, an optional subject, usage chips,
// and a relative time — whichever the node carries.
function NodeBody({ node }: { node: VersionGraphNode }) {
  const u = node.usage;
  const when = node.time ?? u?.lastFired;
  const hasRefs = (node.refs?.length ?? 0) > 0;
  return (
    <div className="qvr-graph__body">
      {(node.refs ?? []).map((r) => (
        <span key={r.kind + ":" + r.name} className="qvr-graph__ref">
          <span className="qvr-pill">{r.kind}</span>
          <span className="qvr-ver__branch">{r.name}</span>
        </span>
      ))}
      {node.sha === "" && !hasRefs && (
        <VersionTag title="runs whose records never identified which copy of the skill loaded" />
      )}
      {node.sha !== "" && (
        <span className="qvr-ver__sha" title={node.sha}>
          {short(node.sha)}
        </span>
      )}
      {node.current && (
        <Badge tone="success" dot>
          current
        </Badge>
      )}
      {node.subject && !hasRefs && !u && (
        <span className="qvr-graph__subject">{node.subject}</span>
      )}
      {u && (
        <span className="qvr-graph__chips">
          <Tag>{fmtCount(u.invocations)} runs</Tag>
          {/* Absent token side = the sessions reported none; omit, never show 0. */}
          {u.tokensIn != null && <Tag lead="↑">{fmtCount(u.tokensIn)} tok</Tag>}
          {u.tokensOut != null && <Tag lead="↓">{fmtCount(u.tokensOut)} tok</Tag>}
        </span>
      )}
      {when && <span className="qvr-ver__when">{relTime(when)}</span>}
    </div>
  );
}
