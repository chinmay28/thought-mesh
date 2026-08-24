/**
 * Naming a note that was typed, not titled.
 *
 * The home-page composer takes a body and nothing else, so the file stem has
 * to come out of what was written: the first line with anything in it, minus
 * the markdown that would only be noise in a file name. The line stays in the
 * note — nothing typed is thrown away; the name is a label for it.
 *
 * The server has the last word (`vault.SanitizeName` replaces the characters
 * a path may not carry, and its own length cap), so this only has to produce
 * something a person would recognize in a list.
 */

/** Long enough to stay recognizable, short enough to read in a list. */
const MAX_NAME = 60;

/** A file stem for `body`, falling back to a timestamp for an empty one. */
export function noteNameFromBody(body: string, now = new Date()): string {
  for (const raw of body.split('\n')) {
    const line = stripMarkup(raw);
    if (line) return truncate(line, MAX_NAME);
  }
  return fallbackName(now);
}

/**
 * Drop the markdown a title wouldn't carry, keeping the words it wraps.
 * Returns "" for a line that has no word left in it — a bare "###" or a "---"
 * rule is punctuation, not a name.
 */
function stripMarkup(line: string): string {
  const out = (
    line
      .trim()
      // Leading block markers: quotes, bullets, ordered numbers.
      .replace(/^(?:[>\-*+]\s+|\d+[.)]\s+)+/, '')
      // Headings, closing "#"s and all.
      .replace(/^#{1,6}\s+/, '')
      .replace(/\s+#+$/, '')
      // A task box, once its bullet is gone.
      .replace(/^\[[ xX]\]\s+/, '')
      // Links keep the text a reader would have seen.
      .replace(/\[\[([^\]]+)\]\]/g, (_m, target: string) => target.split('|').pop() ?? '')
      .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
      // Emphasis and code markers mean nothing in a file name.
      .replace(/[*_`~]/g, '')
      .replace(/\s+/g, ' ')
      .trim()
  );
  return /[\p{L}\p{N}]/u.test(out) ? out : '';
}

/** Cut to `max`, preferring a word boundary in the back half. */
function truncate(s: string, max: number): string {
  if (s.length <= max) return s;
  const cut = s.slice(0, max);
  const space = cut.lastIndexOf(' ');
  return (space > max / 2 ? cut.slice(0, space) : cut).trimEnd();
}

/** "Note 2026-08-24 09.04" — ":" is not allowed in a vault path. */
function fallbackName(now: Date): string {
  const p = (n: number) => String(n).padStart(2, '0');
  const date = `${now.getFullYear()}-${p(now.getMonth() + 1)}-${p(now.getDate())}`;
  return `Note ${date} ${p(now.getHours())}.${p(now.getMinutes())}`;
}
