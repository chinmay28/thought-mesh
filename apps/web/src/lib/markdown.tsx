/**
 * A small markdown → React renderer.
 *
 * Deliberately hand-rolled rather than a markdown-to-HTML dependency: the
 * output is React elements built from text, so note content can never inject
 * markup, and [[wikilinks]] are first-class — rendered as router links,
 * resolved (or not) by the caller.
 *
 * Supported: headings, paragraphs (single newlines become line breaks — the
 * note-taking convention), bold/italic/strikethrough/inline code, fenced code
 * blocks, blockquotes, ordered/unordered/task lists (nested by indent),
 * horizontal rules, links, images, and [[Target]], [[Target|alias]],
 * [[Target#heading]] wikilinks.
 */
import { Fragment, type ReactNode } from 'react';
import { Link } from 'react-router-dom';

export interface RenderOptions {
  /** Resolve a wikilink target to a note path, or null when no note matches. */
  resolve: (target: string) => string | null;
  /** App URL for an existing note. */
  linkTo: (path: string) => string;
  /** App URL that offers to create the (missing) note a wikilink names. */
  createTo: (target: string) => string;
}

/** "Note#Heading|shown" → "Note" — the note a wikilink names. */
export function wikiTarget(inner: string): string {
  let t = inner;
  const pipe = t.indexOf('|');
  if (pipe >= 0) t = t.slice(0, pipe);
  const hash = t.indexOf('#');
  if (hash >= 0) t = t.slice(0, hash);
  return t.trim();
}

/** The text a wikilink displays: its alias if given, else the full target. */
export function wikiLabel(inner: string): string {
  const pipe = inner.indexOf('|');
  if (pipe >= 0) return inner.slice(pipe + 1).trim() || wikiTarget(inner);
  return inner.trim();
}

// ---------------------------------------------------------------------------
// Inline rendering
// ---------------------------------------------------------------------------

interface InlinePattern {
  re: RegExp;
  render: (m: RegExpExecArray, opts: RenderOptions, key: number) => ReactNode;
}

// Tried in order at each position; the earliest match wins, ties broken by
// this order (so wikilinks beat [text](url) links on the same "[").
const INLINE: InlinePattern[] = [
  {
    re: /\[\[([^[\]]+)\]\]/,
    render: (m, opts, key) => {
      const inner = m[1]!;
      const target = wikiTarget(inner);
      const label = wikiLabel(inner);
      if (!target) return <Fragment key={key}>{label}</Fragment>;
      const path = opts.resolve(target);
      return path ? (
        <Link key={key} className="wikilink" to={opts.linkTo(path)}>
          {label}
        </Link>
      ) : (
        <Link
          key={key}
          className="wikilink wikilink--new"
          title={`"${target}" doesn't exist yet — tap to create it`}
          to={opts.createTo(target)}
        >
          {label}
        </Link>
      );
    },
  },
  {
    re: /!\[([^\]]*)\]\(([^)\s]+)\)/,
    render: (m, _opts, key) => (
      <img key={key} src={m[2]!} alt={m[1]!} loading="lazy" className="md-img" />
    ),
  },
  {
    re: /\[([^\]]+)\]\(([^)\s]+)\)/,
    render: (m, opts, key) => (
      <a key={key} href={m[2]!} target="_blank" rel="noreferrer">
        {renderInline(m[1]!, opts)}
      </a>
    ),
  },
  {
    re: /`([^`]+)`/,
    render: (m, _opts, key) => <code key={key}>{m[1]!}</code>,
  },
  {
    re: /\*\*([^*]+)\*\*/,
    render: (m, opts, key) => <strong key={key}>{renderInline(m[1]!, opts)}</strong>,
  },
  {
    re: /~~([^~]+)~~/,
    render: (m, opts, key) => <del key={key}>{renderInline(m[1]!, opts)}</del>,
  },
  {
    re: /\*([^*]+)\*/,
    render: (m, opts, key) => <em key={key}>{renderInline(m[1]!, opts)}</em>,
  },
  {
    re: /(?<![\w])_([^_]+)_(?![\w])/,
    render: (m, opts, key) => <em key={key}>{renderInline(m[1]!, opts)}</em>,
  },
];

/** Render one line's inline markup. */
export function renderInline(text: string, opts: RenderOptions): ReactNode {
  const out: ReactNode[] = [];
  let rest = text;
  let key = 0;
  while (rest.length > 0) {
    let earliest: { index: number; m: RegExpExecArray; p: InlinePattern } | null = null;
    for (const p of INLINE) {
      const m = p.re.exec(rest);
      if (m && (earliest === null || m.index < earliest.index)) {
        earliest = { index: m.index, m, p };
      }
    }
    if (!earliest) {
      out.push(rest);
      break;
    }
    if (earliest.index > 0) {
      out.push(rest.slice(0, earliest.index));
    }
    out.push(earliest.p.render(earliest.m, opts, key++));
    rest = rest.slice(earliest.index + earliest.m[0].length);
  }
  return out.length === 1 ? out[0] : out.map((n, i) => <Fragment key={i}>{n}</Fragment>);
}

// ---------------------------------------------------------------------------
// Block rendering
// ---------------------------------------------------------------------------

interface ListEntry {
  indent: number; // nesting level (two spaces or one tab per level)
  ordered: boolean;
  task: boolean | null; // null = not a task item; else checked state
  text: string;
}

const LIST_RE = /^(\s*)([-*+]|\d+[.)])\s+(.*)$/;
const TASK_RE = /^\[([ xX])\]\s+(.*)$/;

function parseListEntry(line: string): ListEntry | null {
  const m = LIST_RE.exec(line);
  if (!m) return null;
  const ws = m[1]!.replace(/\t/g, '  ');
  const entry: ListEntry = {
    indent: Math.floor(ws.length / 2),
    ordered: /\d/.test(m[2]![0]!),
    task: null,
    text: m[3]!,
  };
  const task = TASK_RE.exec(entry.text);
  if (task) {
    entry.task = task[1] !== ' ';
    entry.text = task[2]!;
  }
  return entry;
}

function renderList(entries: ListEntry[], opts: RenderOptions, keyBase: number): ReactNode {
  let pos = 0;
  function walk(level: number): ReactNode {
    const items: ReactNode[] = [];
    const ordered = entries[pos]!.ordered;
    while (pos < entries.length && entries[pos]!.indent >= level) {
      const entry = entries[pos]!;
      if (entry.indent > level) {
        // Children of the item just emitted: wrap them into it.
        const children = walk(entry.indent);
        const last = items.pop();
        items.push(
          <li key={items.length}>
            {(last as { props: { children?: ReactNode } } | undefined)?.props.children}
            {children}
          </li>,
        );
        continue;
      }
      pos++;
      items.push(
        <li key={items.length} className={entry.task !== null ? 'md-task' : undefined}>
          {entry.task !== null && (
            <input type="checkbox" checked={entry.task} readOnly aria-label="task" />
          )}
          {renderInline(entry.text, opts)}
        </li>,
      );
    }
    const Tag = ordered ? 'ol' : 'ul';
    return <Tag key={`${keyBase}-${level}-${pos}`}>{items}</Tag>;
  }
  return walk(entries[0]!.indent);
}

const HEADING_RE = /^(#{1,6})\s+(.*)$/;
const HR_RE = /^ {0,3}(?:(?:- *){3,}|(?:\* *){3,}|(?:_ *){3,})$/;

/**
 * Split a leading YAML frontmatter block off a note.
 *
 * Categories live up there (the server writes them; see internal/vault), and
 * they are metadata, not prose — rendering the block would put a stray
 * horizontal rule and a line of YAML at the top of every categorised note.
 *
 * The rule matches the server's: a block opens with `---` on the very first
 * line and closes with `---` or `...` on a line of its own. A leading `---`
 * with no closing fence is a horizontal rule, and stays in the body.
 */
export function splitFrontmatter(src: string): { frontmatter: string[]; body: string } {
  const text = src.startsWith('\ufeff') ? src.slice(1) : src;
  const lines = text.split('\n');
  if (lines[0]?.trimEnd() !== '---') return { frontmatter: [], body: src };
  for (let i = 1; i < lines.length && i <= MAX_FRONTMATTER_LINES; i++) {
    const fence = lines[i]!.trimEnd();
    if (fence === '---' || fence === '...') {
      return { frontmatter: lines.slice(1, i), body: lines.slice(i + 1).join('\n') };
    }
  }
  return { frontmatter: [], body: src };
}

/** How far to look for the closing fence before deciding the leading `---`
 * was a horizontal rule after all. Mirrors the server's limit. */
const MAX_FRONTMATTER_LINES = 200;

/**
 * Render a markdown document to React elements.
 *
 * Frontmatter is stripped first, and only here at the top level — the block
 * recursion below (blockquotes) calls renderBlocks directly, so a `---` inside
 * a quote is still the rule it looks like.
 */
export function renderMarkdown(src: string, opts: RenderOptions): ReactNode {
  return renderBlocks(splitFrontmatter(src).body, opts);
}

function renderBlocks(src: string, opts: RenderOptions): ReactNode {
  const lines = src.split('\n');
  const out: ReactNode[] = [];
  let i = 0;
  let key = 0;

  while (i < lines.length) {
    const line = lines[i]!;
    const trimmed = line.trim();

    if (trimmed === '') {
      i++;
      continue;
    }

    // Fenced code block.
    if (trimmed.startsWith('```') || trimmed.startsWith('~~~')) {
      const fence = trimmed.slice(0, 3);
      const lang = trimmed.slice(3).trim();
      const buf: string[] = [];
      i++;
      while (i < lines.length && !lines[i]!.trim().startsWith(fence)) {
        buf.push(lines[i]!);
        i++;
      }
      i++; // closing fence (or EOF)
      out.push(
        <pre key={key++}>
          <code className={lang ? `language-${lang}` : undefined}>{buf.join('\n')}</code>
        </pre>,
      );
      continue;
    }

    // Heading.
    const h = HEADING_RE.exec(trimmed);
    if (h) {
      const level = h[1]!.length;
      const Tag = `h${level}` as 'h1';
      out.push(<Tag key={key++}>{renderInline(h[2]!, opts)}</Tag>);
      i++;
      continue;
    }

    // Horizontal rule.
    if (HR_RE.test(line)) {
      out.push(<hr key={key++} />);
      i++;
      continue;
    }

    // Blockquote: gather the contiguous "&gt;" lines and recurse.
    if (trimmed.startsWith('>')) {
      const buf: string[] = [];
      while (i < lines.length && lines[i]!.trim().startsWith('>')) {
        buf.push(lines[i]!.trim().replace(/^> ?/, ''));
        i++;
      }
      out.push(<blockquote key={key++}>{renderBlocks(buf.join('\n'), opts)}</blockquote>);
      continue;
    }

    // List: gather contiguous list lines.
    if (parseListEntry(line)) {
      const entries: ListEntry[] = [];
      while (i < lines.length) {
        const entry = parseListEntry(lines[i]!);
        if (!entry) break;
        entries.push(entry);
        i++;
      }
      out.push(<Fragment key={key++}>{renderList(entries, opts, key)}</Fragment>);
      continue;
    }

    // Paragraph: gather until a blank line or the start of another block.
    const buf: string[] = [line.trim()];
    i++;
    while (i < lines.length) {
      const next = lines[i]!;
      const nt = next.trim();
      if (
        nt === '' ||
        nt.startsWith('```') ||
        nt.startsWith('~~~') ||
        nt.startsWith('>') ||
        HEADING_RE.test(nt) ||
        HR_RE.test(next) ||
        parseListEntry(next)
      ) {
        break;
      }
      buf.push(nt);
      i++;
    }
    out.push(
      <p key={key++}>
        {buf.map((l, idx) => (
          <Fragment key={idx}>
            {idx > 0 && <br />}
            {renderInline(l, opts)}
          </Fragment>
        ))}
      </p>,
    );
  }

  return <>{out}</>;
}
