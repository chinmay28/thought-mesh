import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  ApiError,
  deleteNote,
  getNote,
  listNotes,
  renameNote,
  saveNote,
  type Note,
  type NoteInfo,
} from '../api/client.ts';
import { renderMarkdown } from '../lib/markdown.tsx';
import { relativeTime } from '../lib/time.ts';
import { Editor } from '../components/Editor.tsx';

const AUTOSAVE_MS = 900;

type SaveState = 'saved' | 'unsaved' | 'saving' | 'conflict';

/** One note: rendered view, editor, and its linked mentions. */
export function NotePage() {
  const path = useParams()['*'] ?? '';
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();

  const [note, setNote] = useState<Note | null>(null);
  const [error, setError] = useState('');
  const [missing, setMissing] = useState(false);
  const [allNotes, setAllNotes] = useState<NoteInfo[]>([]);

  const editing = params.get('edit') === '1';
  const [draft, setDraft] = useState('');
  const [saveState, setSaveState] = useState<SaveState>('saved');
  // The mtime the current draft is based on — how the server detects that the
  // file moved beneath us (edited on another device, or outside the app).
  const baseMtime = useRef(0);

  const load = useCallback(() => {
    setError('');
    setMissing(false);
    getNote(path)
      .then((n) => {
        setNote(n);
        setDraft(n.content);
        baseMtime.current = n.mtime_ms;
        setSaveState('saved');
      })
      .catch((e: unknown) => {
        if (e instanceof ApiError && e.status === 404) setMissing(true);
        else setError(e instanceof Error ? e.message : String(e));
      });
  }, [path]);

  useEffect(load, [load]);
  useEffect(() => {
    listNotes().then(setAllNotes).catch(() => {});
  }, [path]);

  const doSave = useCallback(
    async (content: string, force = false) => {
      setSaveState('saving');
      try {
        const saved = await saveNote(path, content, force ? undefined : baseMtime.current);
        baseMtime.current = saved.mtime_ms;
        setNote(saved);
        setSaveState('saved');
      } catch (e) {
        if (e instanceof ApiError && e.status === 409) {
          setSaveState('conflict');
        } else {
          setSaveState('unsaved');
        }
      }
    },
    [path],
  );

  // Autosave while editing, debounced from the last keystroke.
  useEffect(() => {
    if (!editing || note === null || draft === note.content || saveState === 'conflict') return;
    setSaveState('unsaved');
    const timer = window.setTimeout(() => void doSave(draft), AUTOSAVE_MS);
    return () => window.clearTimeout(timer);
  }, [draft, editing, note, doSave, saveState]);

  // Wikilink resolution for rendering: the server already resolved this
  // note's links, so display follows exactly what the API said.
  const resolver = useMemo(() => {
    const map = new Map<string, string>();
    for (const l of note?.links ?? []) {
      if (l.path) map.set(l.target.toLowerCase(), l.path);
    }
    return (target: string) => map.get(target.toLowerCase()) ?? null;
  }, [note]);

  const setEditing = (on: boolean) => {
    setParams(on ? { edit: '1' } : {}, { replace: true });
  };

  const finishEditing = async () => {
    if (note && draft !== note.content) await doSave(draft);
    setEditing(false);
  };

  const onRename = async () => {
    if (!note) return;
    const current = note.path.replace(/\.md$/i, '');
    const entered = window.prompt(
      'Rename note (folders allowed, e.g. "ideas/New name"). Links to it are updated everywhere.',
      current,
    );
    if (!entered || entered === current) return;
    try {
      const { note: renamed, updated_notes } = await renameNote(note.path, entered);
      if (updated_notes > 0) {
        // Quiet toast-by-banner: the list below refreshes on navigation.
        console.info(`updated links in ${updated_notes} note(s)`);
      }
      navigate(`/notes/${renamed.path}`, { replace: true });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const onDelete = async () => {
    if (!note) return;
    if (!window.confirm(`Delete "${note.name}"? The markdown file is removed.`)) return;
    try {
      await deleteNote(note.path);
      navigate('/', { replace: true });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  if (missing) {
    const name = path.replace(/\.md$/i, '').split('/').pop() ?? path;
    return (
      <div className="page">
        <div className="empty">
          <p className="empty__lead">“{name}” doesn’t exist yet.</p>
          <Link className="btn btn--primary" to={`/new?name=${encodeURIComponent(name)}`}>
            Create it
          </Link>
        </div>
      </div>
    );
  }

  if (!note) {
    return (
      <div className="page">
        {error ? <div className="banner banner--warn">{error}</div> : <p className="muted">Loading…</p>}
      </div>
    );
  }

  return (
    <div className="page note-page">
      <header className="note-head">
        <div className="note-head__titles">
          {note.dir && <span className="note-head__dir">{note.dir}/</span>}
          <h1 className="note-head__name">{note.name}</h1>
          <span className="note-head__time">edited {relativeTime(note.mtime_ms)}</span>
        </div>
        <div className="note-head__actions">
          {editing ? (
            <button type="button" className="btn btn--primary" onClick={() => void finishEditing()}>
              Done
            </button>
          ) : (
            <button type="button" className="btn btn--primary" onClick={() => setEditing(true)}>
              Edit
            </button>
          )}
          <button type="button" className="btn btn--ghost" onClick={() => void onRename()}>
            Rename
          </button>
          <button type="button" className="btn btn--ghost btn--danger" onClick={() => void onDelete()}>
            Delete
          </button>
        </div>
      </header>

      {error && <div className="banner banner--warn">{error}</div>}
      {saveState === 'conflict' && (
        <div className="banner banner--warn" role="alert">
          This note changed on the server while you were editing.
          <span className="banner__actions">
            <button type="button" className="btn btn--small" onClick={load}>
              Load theirs
            </button>
            <button type="button" className="btn btn--small" onClick={() => void doSave(draft, true)}>
              Keep mine
            </button>
          </span>
        </div>
      )}

      {editing ? (
        <>
          <Editor value={draft} onChange={setDraft} notes={allNotes} currentPath={note.path} />
          <div className="editor-status" role="status">
            {saveState === 'saved' && 'Saved'}
            {saveState === 'saving' && 'Saving…'}
            {saveState === 'unsaved' && 'Unsaved changes'}
            {saveState === 'conflict' && 'Not saved — resolve the conflict above'}
          </div>
        </>
      ) : (
        <article className="md">
          {note.content.trim() === '' ? (
            <p className="muted">
              This note is empty —{' '}
              <button type="button" className="link-btn" onClick={() => setEditing(true)}>
                start writing
              </button>
              .
            </p>
          ) : (
            renderMarkdown(note.content, {
              resolve: resolver,
              linkTo: (p) => `/notes/${p}`,
              createTo: (target) => `/new?name=${encodeURIComponent(target)}`,
            })
          )}
        </article>
      )}

      <section className="backlinks" aria-label="Linked mentions">
        <h2 className="section-title">
          Linked mentions{note.backlinks.length > 0 && ` (${note.backlinks.length})`}
        </h2>
        {note.backlinks.length === 0 ? (
          <p className="muted">
            No other note links here yet. Reference this one as{' '}
            <code>[[{note.name}]]</code> and it will show up.
          </p>
        ) : (
          <ul className="note-list">
            {note.backlinks.map((b) => (
              <li key={b.path}>
                <Link className="note-card" to={`/notes/${b.path}`}>
                  <span className="note-card__name">
                    {b.name}
                    {b.count > 1 && <span className="note-card__count">×{b.count}</span>}
                  </span>
                  <span className="note-card__meta">
                    <span className="note-card__snippet">{b.snippet}</span>
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
