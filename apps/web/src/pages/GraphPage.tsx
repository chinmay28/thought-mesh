import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { graph, type GraphEdge, type GraphNode } from '../api/client.ts';

interface PlacedNode extends GraphNode {
  x: number;
  y: number;
  r: number;
}

/**
 * Fruchterman–Reingold force layout, run to completion up front (the graph is
 * a picture to explore, not a physics toy — a settled layout is calmer and
 * cheaper on a phone). Deterministic: nodes start on a circle in index order,
 * so the same vault draws the same map every time.
 */
function layout(nodes: GraphNode[], edges: GraphEdge[]): PlacedNode[] {
  const n = nodes.length;
  if (n === 0) return [];
  const size = Math.max(300, Math.sqrt(n) * 120);
  const placed: PlacedNode[] = nodes.map((node, i) => {
    const angle = (2 * Math.PI * i) / n;
    // Two rings so dense graphs don't start as one perfect (unstable) circle.
    const radius = (size / 3) * (i % 2 === 0 ? 1 : 0.55);
    const degree = node.links_in + node.links_out;
    return {
      ...node,
      x: size / 2 + radius * Math.cos(angle),
      y: size / 2 + radius * Math.sin(angle),
      r: 7 + Math.min(14, Math.sqrt(degree) * 3),
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

/** The whole vault as a map: notes are dots, wikilinks are the threads. */
export function GraphPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<{ nodes: GraphNode[]; edges: GraphEdge[] } | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    graph()
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, []);

  const placed = useMemo(
    () => (data ? layout(data.nodes, data.edges) : []),
    [data],
  );
  const byId = useMemo(() => new Map(placed.map((p) => [p.id, p])), [placed]);

  if (error) {
    return (
      <div className="page">
        <div className="banner banner--warn">{error}</div>
      </div>
    );
  }
  if (!data) {
    return (
      <div className="page">
        <p className="muted">Loading the mesh…</p>
      </div>
    );
  }
  if (placed.length === 0) {
    return (
      <div className="page">
        <div className="empty">
          <p className="empty__lead">Nothing to map yet.</p>
          <p className="muted">Write a couple of notes and link them with [[wikilinks]].</p>
        </div>
      </div>
    );
  }

  const pad = 40;
  const minX = Math.min(...placed.map((p) => p.x)) - pad;
  const minY = Math.min(...placed.map((p) => p.y)) - pad;
  const maxX = Math.max(...placed.map((p) => p.x)) + pad;
  const maxY = Math.max(...placed.map((p) => p.y)) + pad;
  const showLabels = placed.length <= 80;

  const open = (node: PlacedNode) => {
    if (node.missing === 1) {
      navigate(`/new?name=${encodeURIComponent(node.name)}`);
    } else {
      navigate(`/notes/${node.id}`);
    }
  };

  return (
    <div className="page graph-page">
      <h1 className="page-title">
        Graph{' '}
        <span className="muted graph-page__stats">
          {data.nodes.filter((n) => n.missing === 0).length} notes ·{' '}
          {data.edges.length} links
        </span>
      </h1>
      <div className="graph-canvas">
        <svg
          viewBox={`${minX} ${minY} ${maxX - minX} ${maxY - minY}`}
          className="graph-svg"
          role="img"
          aria-label="Note graph"
        >
          {data.edges.map((e, i) => {
            const a = byId.get(e.from);
            const b = byId.get(e.to);
            if (!a || !b) return null;
            return (
              <line
                key={i}
                x1={a.x}
                y1={a.y}
                x2={b.x}
                y2={b.y}
                className="graph-edge"
              />
            );
          })}
          {placed.map((p) => (
            <g
              key={p.id}
              className={`graph-node${p.missing === 1 ? ' graph-node--missing' : ''}`}
              onClick={() => open(p)}
              tabIndex={0}
              role="link"
              aria-label={p.missing === 1 ? `${p.name} (not created yet)` : p.name}
              onKeyDown={(e) => e.key === 'Enter' && open(p)}
            >
              <circle cx={p.x} cy={p.y} r={p.r} />
              {showLabels && (
                <text x={p.x} y={p.y + p.r + 14} textAnchor="middle">
                  {p.name}
                </text>
              )}
            </g>
          ))}
        </svg>
      </div>
      <p className="muted graph-page__hint">
        Tap a dot to open its note. Hollow dots are notes that are linked to
        but not written yet — tap to create them.
      </p>
    </div>
  );
}
