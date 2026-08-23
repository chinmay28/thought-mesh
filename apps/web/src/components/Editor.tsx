import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type ChangeEvent,
  type KeyboardEvent,
} from 'react';
import type { NoteInfo } from '../api/client.ts';

interface EditorProps {
  value: string;
  onChange: (value: string) => void;
  /** Every note in the vault, for [[wikilink]] autocomplete. */
  notes: NoteInfo[];
  /** The note being edited — excluded from its own suggestions. */
  currentPath: string;
}

const MAX_SUGGESTIONS = 6;

/**
 * The markdown editor: a plain textarea (mobile keyboards and undo behave
 * best in one), a formatting toolbar, and [[wikilink]] autocomplete that
 * appears as a chip bar the moment "[[" is typed.
 */
export function Editor({ value, onChange, notes, currentPath }: EditorProps) {
  const ref = useRef<HTMLTextAreaElement>(null);
  // The active "[[" context: characters typed after it, or null when closed.
  const [linkPrefix, setLinkPrefix] = useState<string | null>(null);
  const [selected, setSelected] = useState(0);

  // Auto-grow: the textarea always fits its content, so the page (not the
  // textarea) scrolls — the natural feel on phones.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${el.scrollHeight + 2}px`;
  }, [value]);

  const suggestions = useMemo(() => {
    if (linkPrefix === null) return [];
    const q = linkPrefix.toLowerCase();
    const starts: NoteInfo[] = [];
    const contains: NoteInfo[] = [];
    for (const n of notes) {
      if (n.path === currentPath) continue;
      const name = n.name.toLowerCase();
      if (q === '' || name.startsWith(q)) starts.push(n);
      else if (name.includes(q)) contains.push(n);
    }
    return [...starts, ...contains].slice(0, MAX_SUGGESTIONS);
  }, [linkPrefix, notes, currentPath]);

  /** Find an unclosed "[[" immediately before the caret. */
  const detectLinkContext = (text: string, caret: number): string | null => {
    const open = text.lastIndexOf('[[', caret - 1);
    if (open < 0) return null;
    const between = text.slice(open + 2, caret);
    if (between.includes(']]') || between.includes('\n') || between.length > 80) return null;
    return between;
  };

  const handleChange = (e: ChangeEvent<HTMLTextAreaElement>) => {
    onChange(e.target.value);
    setLinkPrefix(detectLinkContext(e.target.value, e.target.selectionStart));
    setSelected(0);
  };

  const insertSuggestion = (name: string) => {
    const el = ref.current;
    if (!el || linkPrefix === null) return;
    const caret = el.selectionStart;
    const open = value.lastIndexOf('[[', caret - 1);
    // Close the link; skip a "]]" the user may already have typed ahead.
    const after = value.slice(caret);
    const closeAlready = after.startsWith(']]');
    const next = `${value.slice(0, open + 2)}${name}]]${closeAlready ? after.slice(2) : after}`;
    onChange(next);
    setLinkPrefix(null);
    const pos = open + 2 + name.length + 2;
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(pos, pos);
    });
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (linkPrefix !== null && suggestions.length > 0) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setSelected((s) => (s + 1) % suggestions.length);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setSelected((s) => (s - 1 + suggestions.length) % suggestions.length);
        return;
      }
      if (e.key === 'Enter' || e.key === 'Tab') {
        e.preventDefault();
        insertSuggestion(suggestions[selected]!.name);
        return;
      }
      if (e.key === 'Escape') {
        setLinkPrefix(null);
        return;
      }
    }
  };

  // -- Toolbar ---------------------------------------------------------------

  /** Wrap the selection (or insert a placeholder) with before/after markers. */
  const wrap = (before: string, after: string, placeholder: string) => {
    const el = ref.current;
    if (!el) return;
    const { selectionStart: start, selectionEnd: end } = el;
    const selectedText = value.slice(start, end) || placeholder;
    const next = value.slice(0, start) + before + selectedText + after + value.slice(end);
    onChange(next);
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(start + before.length, start + before.length + selectedText.length);
    });
  };

  /** Prefix each line of the selection (heading, list, quote, task). */
  const prefixLines = (prefix: string) => {
    const el = ref.current;
    if (!el) return;
    const { selectionStart: start, selectionEnd: end } = el;
    const lineStart = value.lastIndexOf('\n', start - 1) + 1;
    const sliceEnd = end > lineStart ? end : lineStart;
    const block = value.slice(lineStart, sliceEnd);
    const prefixed = block
      .split('\n')
      .map((l) => (l.startsWith(prefix) ? l.slice(prefix.length) : prefix + l))
      .join('\n');
    const next = value.slice(0, lineStart) + prefixed + value.slice(sliceEnd);
    onChange(next);
    const delta = prefixed.length - block.length;
    requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(sliceEnd + delta, sliceEnd + delta);
    });
  };

  return (
    <div className="editor">
      <div className="editor__toolbar" role="toolbar" aria-label="Formatting">
        <button type="button" className="tool" title="Wikilink" onClick={() => wrap('[[', ']]', 'Note name')}>
          [[ ]]
        </button>
        <button type="button" className="tool tool--bold" title="Bold" onClick={() => wrap('**', '**', 'bold')}>
          B
        </button>
        <button type="button" className="tool tool--italic" title="Italic" onClick={() => wrap('*', '*', 'italic')}>
          I
        </button>
        <button type="button" className="tool" title="Inline code" onClick={() => wrap('`', '`', 'code')}>
          {'<>'}
        </button>
        <button type="button" className="tool" title="Heading" onClick={() => prefixLines('## ')}>
          H
        </button>
        <button type="button" className="tool" title="Bulleted list" onClick={() => prefixLines('- ')}>
          •—
        </button>
        <button type="button" className="tool" title="Task" onClick={() => prefixLines('- [ ] ')}>
          ☑
        </button>
        <button type="button" className="tool" title="Quote" onClick={() => prefixLines('> ')}>
          ❝
        </button>
      </div>

      {linkPrefix !== null && suggestions.length > 0 && (
        <div className="editor__suggest" role="listbox" aria-label="Link to note">
          {suggestions.map((n, i) => (
            <button
              key={n.path}
              type="button"
              role="option"
              aria-selected={i === selected}
              className={`chip${i === selected ? ' chip--active' : ''}`}
              // mousedown, not click: click fires after the textarea loses
              // focus, which closes the suggestion bar before it lands.
              onMouseDown={(e) => {
                e.preventDefault();
                insertSuggestion(n.name);
              }}
            >
              {n.name}
              {n.dir && <span className="chip__dir">{n.dir}</span>}
            </button>
          ))}
        </div>
      )}

      <textarea
        ref={ref}
        className="editor__text"
        value={value}
        onChange={handleChange}
        onKeyDown={handleKeyDown}
        onClick={(e) =>
          setLinkPrefix(detectLinkContext(value, (e.target as HTMLTextAreaElement).selectionStart))
        }
        placeholder={'Write in markdown. Link other notes with [[Note name]].'}
        aria-label="Note content"
        autoCapitalize="sentences"
        spellCheck
      />
    </div>
  );
}
