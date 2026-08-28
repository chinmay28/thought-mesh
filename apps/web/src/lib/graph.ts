import type { GraphEdge, GraphNode } from '../api/client.ts';

export interface PlacedNode extends GraphNode {
  x: number;
  y: number;
  r: number;
}

/**
 * One connected cluster of the vault graph: the nodes that reach each other
 * through wikilinks (in either direction), with the edges between them.
 */
export interface GraphComponent {
  nodes: GraphNode[];
  edges: GraphEdge[];
  /** Name of the best-connected node — what the cluster is "about". */
  label: string;
  /** Notes that actually exist (ghost nodes excluded). */
  noteCount: number;
}

function degree(n: GraphNode): number {
  return n.links_in + n.links_out;
}

/**
 * Split the graph into connected components, biggest first. Node and edge
 * order inside each component follows the input order, so the force layout
 * downstream stays deterministic.
 */
export function connectedComponents(
  nodes: GraphNode[],
  edges: GraphEdge[],
): GraphComponent[] {
  const adjacency = new Map<string, string[]>();
  for (const n of nodes) adjacency.set(n.id, []);
  for (const e of edges) {
    adjacency.get(e.from)?.push(e.to);
    adjacency.get(e.to)?.push(e.from);
  }

  const componentOf = new Map<string, number>();
  let count = 0;
  for (const start of nodes) {
    if (componentOf.has(start.id)) continue;
    const c = count++;
    const queue = [start.id];
    componentOf.set(start.id, c);
    while (queue.length > 0) {
      const id = queue.pop()!;
      for (const next of adjacency.get(id) ?? []) {
        if (!componentOf.has(next)) {
          componentOf.set(next, c);
          queue.push(next);
        }
      }
    }
  }

  const grouped: { nodes: GraphNode[]; edges: GraphEdge[] }[] = Array.from(
    { length: count },
    () => ({ nodes: [], edges: [] }),
  );
  for (const n of nodes) grouped[componentOf.get(n.id)!]!.nodes.push(n);
  for (const e of edges) {
    const c = componentOf.get(e.from) ?? componentOf.get(e.to);
    if (c !== undefined) grouped[c]!.edges.push(e);
  }

  const components = grouped.map((g) => {
    // The cluster is named for its hub: highest degree wins, existing notes
    // beat ghosts on a tie, then the alphabet settles it.
    const hub = [...g.nodes].sort(
      (a, b) =>
        degree(b) - degree(a) ||
        a.missing - b.missing ||
        a.name.localeCompare(b.name),
    )[0]!;
    return {
      ...g,
      label: hub.name,
      noteCount: g.nodes.filter((n) => n.missing === 0).length,
    };
  });
  return components.sort(
    (a, b) =>
      b.noteCount - a.noteCount ||
      b.nodes.length - a.nodes.length ||
      a.label.localeCompare(b.label),
  );
}

/**
 * Fruchterman–Reingold force layout for ONE connected component, run to
 * completion up front (the graph is a picture to explore, not a physics toy —
 * a settled layout is calmer and cheaper on a phone). Deterministic: nodes
 * start on a circle in index order, so the same vault draws the same map
 * every time.
 */
function layoutComponent(nodes: GraphNode[], edges: GraphEdge[]): PlacedNode[] {
  const n = nodes.length;
  if (n === 0) return [];
  const size = Math.max(140, Math.sqrt(n) * 120);
  const placed: PlacedNode[] = nodes.map((node, i) => {
    const angle = (2 * Math.PI * i) / n;
    // Two rings so dense graphs don't start as one perfect (unstable) circle.
    const radius = (size / 3) * (i % 2 === 0 ? 1 : 0.55);
    return {
      ...node,
      x: size / 2 + radius * Math.cos(angle),
      y: size / 2 + radius * Math.sin(angle),
      r: 7 + Math.min(14, Math.sqrt(degree(node)) * 3),
    };
  });
  const index = new Map(placed.map((p, i) => [p.id, i]));
  const k = size / Math.sqrt(n) / 1.4; // ideal spring length
  const iterations = 250;
  for (let it = 0; it < iterations; it++) {
    const temp = (size / 10) * (1 - it / iterations);
    const dx = new Array<number>(n).fill(0);
    const dy = new Array<number>(n).fill(0);
    // Repulsion between every pair.
    for (let i = 0; i < n; i++) {
      for (let j = i + 1; j < n; j++) {
        const a = placed[i]!;
        const b = placed[j]!;
        let vx = a.x - b.x;
        let vy = a.y - b.y;
        let d2 = vx * vx + vy * vy;
        if (d2 < 0.01) {
          vx = (i - j) * 0.1;
          vy = 0.1;
          d2 = vx * vx + vy * vy;
        }
        const d = Math.sqrt(d2);
        const force = (k * k) / d;
        dx[i]! += (vx / d) * force;
        dy[i]! += (vy / d) * force;
        dx[j]! -= (vx / d) * force;
        dy[j]! -= (vy / d) * force;
      }
    }
    // Attraction along edges.
    for (const e of edges) {
      const i = index.get(e.from);
      const j = index.get(e.to);
      if (i === undefined || j === undefined) continue;
      const a = placed[i]!;
      const b = placed[j]!;
      const vx = a.x - b.x;
      const vy = a.y - b.y;
      const d = Math.max(0.1, Math.sqrt(vx * vx + vy * vy));
      const force = (d * d) / k;
      dx[i]! -= (vx / d) * force;
      dy[i]! -= (vy / d) * force;
      dx[j]! += (vx / d) * force;
      dy[j]! += (vy / d) * force;
    }
    for (let i = 0; i < n; i++) {
      const p = placed[i]!;
      const d = Math.max(0.1, Math.sqrt(dx[i]! * dx[i]! + dy[i]! * dy[i]!));
      p.x += (dx[i]! / d) * Math.min(d, temp);
      p.y += (dy[i]! / d) * Math.min(d, temp);
    }
  }
  return placed;
}

/** Extra room under a node for its label, so packed clusters don't collide. */
const LABEL_ROOM = 22;

interface Box {
  placed: PlacedNode[];
  minX: number;
  minY: number;
  w: number;
  h: number;
}

function boundingBox(placed: PlacedNode[]): Box {
  let minX = Infinity;
  let minY = Infinity;
  let maxX = -Infinity;
  let maxY = -Infinity;
  for (const p of placed) {
    minX = Math.min(minX, p.x - p.r);
    minY = Math.min(minY, p.y - p.r);
    maxX = Math.max(maxX, p.x + p.r);
    maxY = Math.max(maxY, p.y + p.r + LABEL_ROOM);
  }
  return { placed, minX, minY, w: maxX - minX, h: maxY - minY };
}

/**
 * Lay out each component on its own, then shelf-pack the results into rows.
 * Running one force simulation over a disconnected graph lets the clusters
 * repel each other forever — they end up in the far corners and the fit-all
 * viewBox shrinks everything to dust. Packing keeps every cluster at full
 * size, side by side, in a frame that's roughly landscape.
 */
export function layoutComponents(components: GraphComponent[]): PlacedNode[] {
  const boxes = components
    .map((c) => layoutComponent(c.nodes, c.edges))
    .filter((placed) => placed.length > 0)
    .map(boundingBox);
  if (boxes.length === 0) return [];

  const gap = 48;
  const area = boxes.reduce((sum, b) => sum + (b.w + gap) * (b.h + gap), 0);
  const targetWidth = Math.max(
    ...boxes.map((b) => b.w),
    Math.sqrt(area * 1.6),
  );

  const out: PlacedNode[] = [];
  let x = 0;
  let y = 0;
  let rowH = 0;
  for (const b of boxes) {
    if (x > 0 && x + b.w > targetWidth) {
      x = 0;
      y += rowH + gap;
      rowH = 0;
    }
    for (const p of b.placed) {
      out.push({ ...p, x: p.x - b.minX + x, y: p.y - b.minY + y });
    }
    x += b.w + gap;
    rowH = Math.max(rowH, b.h);
  }
  return out;
}
