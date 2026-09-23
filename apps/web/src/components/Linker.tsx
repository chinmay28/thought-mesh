import { useEffect, useMemo, useRef } from 'react';
import { wikilinkSpans } from '../lib/linkify.ts';
import { wikiTarget } from '../lib/markdown.tsx';

/**
 * How long a touch selection has to sit still before it counts. A phone
 * selects by long-press and then handle drags, and fires `selectionchange` on
 * every nudge; acting on the first one would link half a phrase. A mouse
 * selection is finished on release, so it doesn't wait.
 */
export const SETTLE_MS = 900;

interface LinkerProps {
  /** The note's markdown source — shown verbatim, so offsets are exact. */
  content: string;
  /** Whether a wikilink target names an existing note. */
  resolves: (target: string) => boolean;
  /** A settled, non-empty selection, as `[start, end)` offsets into `content`. */
  onPick: (start: number, end: number) => void;
}

/**
 * The "Add backlinks" view: the note's source, where selecting words is the
 * whole interaction.
 *
 * It shows the markdown rather than the rendered note on purpose. Rendering
 * drops characters (`**`, `#`, list markers), so a selection in the rendered
 * view can't be mapped back to the source without guessing; here the text on
 * screen IS the source, and a selection's offsets are exact. Existing links are
 * highlighted so it's clear what is already connected — and selecting inside
 * one removes it.
 */
export function Linker({ content, resolves, onPick }: LinkerProps) {
  const root = useRef<HTMLDivElement>(null);
  const pick = useRef(onPick);
  pick.current = onPick;

  const parts = useMemo(() => split(content), [content]);

  useEffect(() => {
    let timer = 0;
    const commit = () => {
      window.clearTimeout(timer);
      const el = root.current;
      const sel = window.getSelection();
      if (!el || !sel) return;
      const span = selectionOffsets(el, sel);
      if (!span) return;
      // Clear first: the edit re-renders this text, and a stale selection
      // would otherwise fire again against the new content.
      sel.removeAllRanges();
      pick.current(span[0], span[1]);
    };
    const onSelectionChange = () => {
      window.clearTimeout(timer);
      timer = window.setTimeout(commit, SETTLE_MS);
    };
    const onMouseUp = (e: MouseEvent) => {
      if (root.current?.contains(e.target as Node)) commit();
    };
    document.addEventListener('selectionchange', onSelectionChange);
    document.addEventListener('mouseup', onMouseUp);
    return () => {
      window.clearTimeout(timer);
      document.removeEventListener('selectionchange', onSelectionChange);
      document.removeEventListener('mouseup', onMouseUp);
    };
  }, []);

  return (
    <div className="linker" ref={root} aria-label="Note text — select words to link them">
      {parts.map((p, i) =>
        p.link ? (
          <mark
            key={i}
            className={`linker__link${resolves(wikiTarget(p.text.slice(2, -2))) ? '' : ' linker__link--new'}`}
          >
            {p.text}
          </mark>
        ) : (
          p.text
        ),
      )}
    </div>
  );
}

/**
 * The selection's `[start, end)` as offsets into `root`'s text, or null when
 * it's collapsed or reaches outside `root`. Counting the text of a range from
 * the root's start is what makes the highlighted links (separate elements)
 * transparent to the arithmetic.
 */
export function selectionOffsets(root: Node, sel: Selection): [number, number] | null {
  if (sel.rangeCount === 0 || sel.isCollapsed) return null;
  const range = sel.getRangeAt(0);
  if (!root.contains(range.startContainer) || !root.contains(range.endContainer)) return null;
  const before = document.createRange();
  before.selectNodeContents(root);
  before.setEnd(range.startContainer, range.startOffset);
  const start = before.toString().length;
  return [start, start + range.toString().length];
}

function split(content: string): { text: string; link: boolean }[] {
  const parts: { text: string; link: boolean }[] = [];
  let at = 0;
  for (const s of wikilinkSpans(content)) {
    if (s.start > at) parts.push({ text: content.slice(at, s.start), link: false });
    parts.push({ text: content.slice(s.start, s.end), link: true });
    at = s.end;
  }
  if (at < content.length) parts.push({ text: content.slice(at), link: false });
  return parts;
}
