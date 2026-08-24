import { useEffect, useState, type KeyboardEvent, type RefObject } from 'react';
import { Link } from 'react-router-dom';
import { ApiError, createNote, encodePath, type Note } from '../api/client.ts';
import { noteNameFromBody } from '../lib/noteName.ts';

interface QuickCaptureProps {
  /** The saved note, so the page can fold it into the list it already has. */
  onCreated: (note: Note) => void;
  /** Owned by the page so the "+" action can put the caret in here. */
  textRef: RefObject<HTMLTextAreaElement>;
}

/**
 * How many " 2", " 3" … suffixes to try when the derived name is taken.
 * Two notes that open with the same line are ordinary; twenty are a sign the
 * name isn't doing any work, and the error says so instead of spinning.
 */
const MAX_NAME_TRIES = 20;

/**
 * The composer at the top of the home page: type, save, keep typing.
 *
 * Capture is the thing this app is opened for, so it costs no navigation —
 * there is no name field either, because the first line of what you wrote is
 * the name (see lib/noteName.ts). Anything that wants a folder or a name of
 * its own goes through the full form at /new, linked below.
 */
export function QuickCapture({ onCreated, textRef }: QuickCaptureProps) {
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [saved, setSaved] = useState<Note | null>(null);

  // Auto-grow, as in the note editor: the page scrolls, never the box.
  useEffect(() => {
    const el = textRef.current;
    if (!el) return;
    el.style.height = 'auto';
    el.style.height = `${el.scrollHeight + 2}px`;
  }, [text, textRef]);

  const save = async () => {
    if (busy || !text.trim()) return;
    setBusy(true);
    setError('');
    // Markdown files end in a newline; the trim keeps a stray blank line from
    // riding along at the end of every quick note.
    const content = `${text.trimEnd()}\n`;
    const base = noteNameFromBody(text);
    try {
      for (let i = 0; i < MAX_NAME_TRIES; i++) {
        try {
          // A name already in the vault is a 409, so walk the suffixes until
          // one lands rather than making the writer rename anything.
          const note = await createNote({ name: i === 0 ? base : `${base} ${i + 1}`, content });
          setText('');
          setSaved(note);
          onCreated(note);
          textRef.current?.focus();
          return;
        } catch (err) {
          if (!(err instanceof ApiError) || err.status !== 409) throw err;
        }
      }
      setError(`“${base}” and ${MAX_NAME_TRIES - 1} numbered variants all exist already.`);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  // Enter is a newline in a note, so the keyboard shortcut is the usual
  // "submit this multi-line thing" chord.
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      void save();
    }
  };

  const name = text.trim() ? noteNameFromBody(text) : '';

  return (
    <section className="quick-note" aria-label="New note">
      <textarea
        ref={textRef}
        className="quick-note__text"
        value={text}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Write a note… the first line names it."
        aria-label="New note"
        rows={3}
      />
      {error && <div className="banner banner--warn">{error}</div>}
      <div className="quick-note__row">
        <button
          type="button"
          className="btn btn--primary"
          onClick={() => void save()}
          disabled={busy || !text.trim()}
        >
          {busy ? 'Saving…' : 'Save note'}
        </button>
        <span className="quick-note__hint">
          {name ? (
            <>
              Saves as <strong>{name}</strong>
            </>
          ) : saved ? (
            <>
              Saved{' '}
              <Link className="quick-note__saved" to={`/notes/${encodePath(saved.path)}`}>
                {saved.name}
              </Link>
            </>
          ) : (
            <Link to="/new">Name it or file it in a folder →</Link>
          )}
        </span>
      </div>
    </section>
  );
}
