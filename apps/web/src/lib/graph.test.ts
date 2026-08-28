import { describe, expect, it } from 'vitest';
import type { GraphEdge, GraphNode } from '../api/client.ts';
import { connectedComponents, layoutComponents } from './graph.ts';

function note(
  id: string,
  linksIn = 0,
  linksOut = 0,
  missing: 0 | 1 = 0,
): GraphNode {
  return {
    id,
    name: id.replace(/\.md$/, ''),
    missing,
    links_in: linksIn,
    links_out: linksOut,
  };
}

function edge(from: string, to: string): GraphEdge {
  return { from, to };
}

// A vault with two linked clusters and one stray note:
//   hub.md <-> a.md, hub.md -> b.md   (cluster of 3, hub best connected)
//   x.md -> y.md                      (cluster of 2)
//   lonely.md                         (no links at all)
const nodes = [
  note('a.md', 1, 1),
  note('lonely.md'),
  note('hub.md', 1, 2),
  note('b.md', 1, 0),
  note('x.md', 0, 1),
  note('y.md', 1, 0),
];
const edges = [
  edge('hub.md', 'a.md'),
  edge('a.md', 'hub.md'),
  edge('hub.md', 'b.md'),
  edge('x.md', 'y.md'),
];

describe('connectedComponents', () => {
  it('splits the graph into its clusters, biggest first', () => {
    const comps = connectedComponents(nodes, edges);
    expect(comps.map((c) => c.nodes.map((n) => n.id).sort())).toEqual([
      ['a.md', 'b.md', 'hub.md'],
      ['x.md', 'y.md'],
      ['lonely.md'],
    ]);
  });

  it('keeps each edge with its own cluster', () => {
    const comps = connectedComponents(nodes, edges);
    expect(comps[0]!.edges).toHaveLength(3);
    expect(comps[1]!.edges).toEqual([edge('x.md', 'y.md')]);
    expect(comps[2]!.edges).toEqual([]);
  });

  it('labels a cluster with its best-connected note', () => {
    const comps = connectedComponents(nodes, edges);
    expect(comps.map((c) => c.label)).toEqual(['hub', 'x', 'lonely']);
  });

  it('counts only notes that exist, but a ghost hub may still name the cluster', () => {
    const ghosts = [
      note('one.md', 0, 1),
      note('two.md', 0, 1),
      note('missing:ghost', 2, 0, 1),
    ];
    const ghostEdges = [
      edge('one.md', 'missing:ghost'),
      edge('two.md', 'missing:ghost'),
    ];
    const comps = connectedComponents(ghosts, ghostEdges);
    expect(comps).toHaveLength(1);
    expect(comps[0]!.noteCount).toBe(2);
    expect(comps[0]!.label).toBe('missing:ghost');
  });

  it('handles the empty vault', () => {
    expect(connectedComponents([], [])).toEqual([]);
  });
});

describe('layoutComponents', () => {
  it('places every node with finite coordinates', () => {
    const placed = layoutComponents(connectedComponents(nodes, edges));
    expect(placed).toHaveLength(nodes.length);
    for (const p of placed) {
      expect(Number.isFinite(p.x)).toBe(true);
      expect(Number.isFinite(p.y)).toBe(true);
      expect(p.r).toBeGreaterThan(0);
    }
  });

  it('is deterministic', () => {
    const comps = connectedComponents(nodes, edges);
    expect(layoutComponents(comps)).toEqual(layoutComponents(comps));
  });

  it('packs clusters so their bounding boxes never overlap', () => {
    const comps = connectedComponents(nodes, edges);
    const placed = layoutComponents(comps);
    const byId = new Map(placed.map((p) => [p.id, p]));
    const boxes = comps.map((c) => {
      const members = c.nodes.map((n) => byId.get(n.id)!);
      return {
        minX: Math.min(...members.map((p) => p.x - p.r)),
        maxX: Math.max(...members.map((p) => p.x + p.r)),
        minY: Math.min(...members.map((p) => p.y - p.r)),
        maxY: Math.max(...members.map((p) => p.y + p.r)),
      };
    });
    for (let i = 0; i < boxes.length; i++) {
      for (let j = i + 1; j < boxes.length; j++) {
        const a = boxes[i]!;
        const b = boxes[j]!;
        const apart =
          a.maxX <= b.minX ||
          b.maxX <= a.minX ||
          a.maxY <= b.minY ||
          b.maxY <= a.minY;
        expect(apart).toBe(true);
      }
    }
  });

  it('keeps a lone cluster the size its own layout chose', () => {
    const comps = connectedComponents(nodes, edges);
    const [big] = comps;
    const alone = layoutComponents([big!]);
    expect(alone).toHaveLength(3);
    // Normalized to the origin: nothing hangs off the top-left.
    for (const p of alone) {
      expect(p.x - p.r).toBeGreaterThanOrEqual(-1e-6);
      expect(p.y - p.r).toBeGreaterThanOrEqual(-1e-6);
    }
  });

  it('handles nothing at all', () => {
    expect(layoutComponents([])).toEqual([]);
  });
});
