import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { graph, type GraphEdge, type GraphNode } from '../api/client.ts';
import {
  connectedComponents,
  layoutComponents,
  type PlacedNode,
} from '../lib/graph.ts';

/** "all", "unlinked", or the index of a linked cluster as a string. */
type View = string;

/** The whole vault as a map: notes are dots, wikilinks are the threads. */
export function GraphPage() {
  const navigate = useNavigate();
  const [data, setData] = useState<{ nodes: GraphNode[]; edges: GraphEdge[] } | null>(null);
  const [error, setError] = useState('');
  const [view, setView] = useState<View>('all');

  useEffect(() => {
    graph()
      .then(setData)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, []);

  const components = useMemo(
    () => (data ? connectedComponents(data.nodes, data.edges) : []),
    [data],
  );
  // Lone notes with no links are one "unlinked" group rather than a dropdown
  // entry each — as a set they're the answer to "what have I never linked?".
  const linked = useMemo(
    () => components.filter((c) => c.nodes.length > 1),
    [components],
  );
  const unlinked = useMemo(
    () => components.filter((c) => c.nodes.length === 1),
    [components],
  );

  const shown = useMemo(() => {
    if (view === 'unlinked' && unlinked.length > 0) return unlinked;
    const picked = linked[Number(view)];
    if (view !== 'all' && picked) return [picked];
    return components;
  }, [components, linked, unlinked, view]);

  const placed = useMemo(() => layoutComponents(shown), [shown]);
  const byId = useMemo(() => new Map(placed.map((p) => [p.id, p])), [placed]);
  const shownEdges = useMemo(
    () => shown.flatMap((c) => c.edges),
    [shown],
  );

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
  if (components.length === 0) {
    return (
      <div className="page">
        <div className="empty">
          <p className="empty__lead">Nothing to map yet.</p>
          <p className="muted">Write a couple of notes and link them with [[wikilinks]].</p>
        </div>
      </div>
    );
  }

  // A tiny cluster viewed alone would otherwise be blown up until two dots
  // fill the screen; a floor on the viewBox keeps the zoom sane.
  const pad = 40;
  const minViewW = 480;
  const minViewH = 300;
  let minX = Math.min(...placed.map((p) => p.x)) - pad;
  let minY = Math.min(...placed.map((p) => p.y)) - pad;
  let maxX = Math.max(...placed.map((p) => p.x)) + pad;
  let maxY = Math.max(...placed.map((p) => p.y)) + pad;
  if (maxX - minX < minViewW) {
    const grow = (minViewW - (maxX - minX)) / 2;
    minX -= grow;
    maxX += grow;
  }
  if (maxY - minY < minViewH) {
    const grow = (minViewH - (maxY - minY)) / 2;
    minY -= grow;
    maxY += grow;
  }
  const showLabels = placed.length <= 80;

  const noteWord = (n: number) => (n === 1 ? 'note' : 'notes');
  // The picker earns its row only when there's a real choice: several linked
  // clusters, or one cluster plus unlinked strays.
  const showPicker =
    linked.length > 1 || (linked.length === 1 && unlinked.length > 0);

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
          {components.length > 1 && <> · {components.length} clusters</>}
        </span>
      </h1>
      {showPicker && (
        <div className="graph-toolbar">
          <select
            className="graph-toolbar__select"
            aria-label="Cluster to show"
            value={view}
            onChange={(e) => setView(e.target.value)}
          >
            <option value="all">All clusters</option>
            {linked.map((c, i) => (
              <option key={c.nodes[0]!.id} value={String(i)}>
                {c.label} · {c.noteCount} {noteWord(c.noteCount)}
              </option>
            ))}
            {unlinked.length > 0 && (
              <option value="unlinked">
                Unlinked · {unlinked.length} {noteWord(unlinked.length)}
              </option>
            )}
          </select>
        </div>
      )}
      <div className="graph-canvas">
        <svg
          viewBox={`${minX} ${minY} ${maxX - minX} ${maxY - minY}`}
          className="graph-svg"
          role="img"
          aria-label="Note graph"
        >
          {shownEdges.map((e, i) => {
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
        {showPicker && <> Pick a cluster above to see it full size.</>}
      </p>
    </div>
  );
}
