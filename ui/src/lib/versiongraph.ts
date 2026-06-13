// Pure lane-layout for the version git-tree. Given the backend's commit nodes
// (already newest-first, each carrying its parent hashes), assign every commit a
// column so concurrent branches never collide, and emit the child→parent edges
// to draw. This is the standard `git log --graph` lane-reservation algorithm,
// kept side-effect-free so it can be unit-tested without the DOM.

import type { VersionGraphNode } from "../api";

export interface LaidOutNode {
  node: VersionGraphNode;
  row: number;
  col: number;
}

export interface GraphEdge {
  fromRow: number;
  fromCol: number;
  toRow: number;
  toCol: number;
}

export interface GraphLayout {
  nodes: LaidOutNode[];
  edges: GraphEdge[];
  width: number; // lane count (columns)
}

// layout assigns lanes and computes edges. `lanes[i]` holds the sha each active
// lane is currently routing toward (the next commit expected in that column).
export function layout(nodes: VersionGraphNode[]): GraphLayout {
  const lanes: (string | null)[] = [];
  const colOf = new Map<string, number>();
  const rowOf = new Map<string, number>();
  const laid: LaidOutNode[] = [];

  const firstFree = (): number => {
    for (let i = 0; i < lanes.length; i++) {
      if (lanes[i] == null) return i;
    }
    lanes.push(null);
    return lanes.length - 1;
  };

  nodes.forEach((node, row) => {
    const sha = node.sha;
    // Defensive: a detached/unknown node may arrive with parents absent (older
    // backend serializing nil as null) — treat as no parents rather than crash.
    const parents = node.parents ?? [];
    // A commit's column is the lane already waiting for it (set by a child), or
    // a fresh lane when it's a branch tip nobody pointed at yet. The detached
    // unknown bucket (sha "") is never awaited, so it always takes a fresh lane.
    let col = sha !== "" ? lanes.indexOf(sha) : -1;
    if (col === -1) col = firstFree();
    colOf.set(sha, col);
    rowOf.set(sha, row);

    // This lane continues straight down toward the first parent (or frees).
    lanes[col] = parents[0] ?? null;
    // Other lanes that were also waiting for this commit converge here — free.
    for (let j = 0; j < lanes.length; j++) {
      if (j !== col && lanes[j] === sha) lanes[j] = null;
    }
    // Extra parents (a merge) each reserve a lane unless one already routes there.
    for (let k = 1; k < parents.length; k++) {
      const p = parents[k];
      if (lanes.indexOf(p) === -1) lanes[firstFree()] = p;
    }

    laid.push({ node, row, col });
  });

  let width = 1;
  for (const n of laid) width = Math.max(width, n.col + 1);

  const edges: GraphEdge[] = [];
  for (const { node, row, col } of laid) {
    for (const p of node.parents ?? []) {
      const toRow = rowOf.get(p);
      const toCol = colOf.get(p);
      // Parent outside the walked window → truncation root; no edge to draw.
      if (toRow == null || toCol == null) continue;
      edges.push({ fromRow: row, fromCol: col, toRow, toCol });
    }
  }
  return { nodes: laid, edges, width };
}
