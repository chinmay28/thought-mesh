/**
 * Turning a selection of note text into a [[wikilink]] — and back.
 *
 * Pure string work over the note's markdown source, so the "Add backlinks"
 * mode can stay a thin view: it reports which characters were selected, and
 * everything about what that selection means is decided here, where it can be
 * tested without a DOM. Offsets are UTF-16 indices into the source, exactly as
 * `String.prototype.slice` counts them.
 */
import { wikiLabel } from './markdown.tsx';

/** A half-open range of the source: `[start, end)`. */
export interface Span {
  start: number;
  end: number;
}

/**
 * What selecting a range amounts to.
 *
 * - `link`: the range was wrapped in `[[ ]]`; `target` is the new link text.
 * - `unlink`: the range sat inside an existing link, which was replaced by the
 *   text it displayed — selecting a link again is how you take it back.
 * - `refused`: nothing changed; `reason` says why, in words for the user.
 * - `none`: nothing worth reporting (the selection was only spaces or
 *   punctuation) — the view just ignores it.
 */
export type LinkEdit =
  | { kind: 'link'; content: string; target: string; span: Span }
  | { kind: 'unlink'; content: string; target: string; span: Span }
  | { kind: 'refused'; reason: string }
  | { kind: 'none' };

// Characters a note name can't carry: the vault refuses them in a path, so a
// link built from them could never be followed or created. Kept in step with
// `vault.CleanPath` on the server.
const FORBIDDEN = /[#|[\]\\:*?"<>]/;

const WORD = /[\p{L}\p{N}_]/u;

/**
 * The ranges covered by fenced code blocks, fences included. A wikilink inside
 * one doesn't count (the mesh ignores it), so linking there would only look
 * like it worked.
 */
export function codeFenceSpans(src: string): Span[] {
  const spans: Span[] = [];
  let pos = 0;
  let open: { start: number; fence: string } | null = null;
  for (const line of src.split('\n')) {
    const trimmed = line.trim();
    if (open === null) {
      if (trimmed.startsWith('```') || trimmed.startsWith('~~~')) {
        open = { start: pos, fence: trimmed.slice(0, 3) };
      }
    } else if (trimmed.startsWith(open.fence)) {
      spans.push({ start: open.start, end: pos + line.length });
      open = null;
    }
    pos += line.length + 1;
  }
  // An unclosed fence runs to the end, as the renderer treats it.
  if (open !== null) spans.push({ start: open.start, end: src.length });
  return spans;
}

/** The ranges of every `[[…]]` outside code fences, brackets included. */
export function wikilinkSpans(src: string): Span[] {
  const fences = codeFenceSpans(src);
  const spans: Span[] = [];
  const re = /\[\[([^[\]\n]+)\]\]/g;
  for (let m = re.exec(src); m !== null; m = re.exec(src)) {
    const span = { start: m.index, end: m.index + m[0].length };
    if (!fences.some((f) => overlaps(f, span))) spans.push(span);
  }
  return spans;
}

/**
 * Link or unlink the selected range of `src`.
 *
 * A selection is widened to whole words (a thumb rarely lands on a word
 * boundary) and then trimmed of surrounding spaces and punctuation, so
 * selecting "**SATA cash sweep**," links just the words. It never crosses a
 * line: a wikilink is one line of text, and a selection that runs on is far
 * more often a slipped handle than an intent.
 */
export function toggleWikilink(src: string, start: number, end: number): LinkEdit {
  if (start > end) [start, end] = [end, start];
  start = Math.max(0, Math.min(start, src.length));
  end = Math.max(0, Math.min(end, src.length));
  if (start === end) return { kind: 'none' };

  const sel = { start, end };
  for (const link of wikilinkSpans(src)) {
    if (!overlaps(link, sel)) continue;
    if (link.start <= start && end <= link.end) return unlink(src, link);
    return { kind: 'refused', reason: 'That selection runs into an existing link.' };
  }
  if (codeFenceSpans(src).some((f) => overlaps(f, sel))) {
    return { kind: 'refused', reason: 'Links inside a code block don’t count.' };
  }

  // Widen to whole words, then trim anything that isn't part of a word.
  while (start > 0 && isWord(src[start - 1]!) && isWord(src[start]!)) start--;
  while (end < src.length && isWord(src[end - 1]!) && isWord(src[end]!)) end++;
  while (start < end && !isWord(src[start]!)) start++;
  while (end > start && !isWord(src[end - 1]!)) end--;
  if (start === end) return { kind: 'none' };

  const text = src.slice(start, end);
  if (text.includes('\n')) {
    return { kind: 'refused', reason: 'Select within one line — a link can’t span lines.' };
  }
  const bad = FORBIDDEN.exec(text) ?? /[`*~]/.exec(text);
  if (bad) {
    return { kind: 'refused', reason: `“${text}” can’t be a note name — it contains “${bad[0]}”.` };
  }

  const content = `${src.slice(0, start)}[[${text}]]${src.slice(end)}`;
  return { kind: 'link', content, target: text, span: { start, end: end + 4 } };
}

/**
 * Wrap text in a fenced code block that its own contents can't close: the
 * fence is one backtick longer than the longest run inside (and at least
 * three), which is how CommonMark nests fences.
 */
export function fenceAsCode(text: string): string {
  let longest = 0;
  for (const run of text.match(/`+/g) ?? []) longest = Math.max(longest, run.length);
  const fence = '`'.repeat(Math.max(3, longest + 1));
  const body = text.endsWith('\n') ? text : `${text}\n`;
  return `${fence}\n${body}${fence}\n`;
}

function unlink(src: string, link: Span): LinkEdit {
  const label = wikiLabel(src.slice(link.start + 2, link.end - 2));
  const content = src.slice(0, link.start) + label + src.slice(link.end);
  return {
    kind: 'unlink',
    content,
    target: label,
    span: { start: link.start, end: link.start + label.length },
  };
}

function overlaps(a: Span, b: Span): boolean {
  return a.start < b.end && b.start < a.end;
}

function isWord(ch: string): boolean {
  return WORD.test(ch);
}
