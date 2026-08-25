import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import {
  ApiError,
  deleteNote,
  getNote,
  listCategories,
  listNotes,
  mergeText,
  renameNote,
  saveNote,
  setNoteCategories,
  type Category,
  type Note,
  type NoteInfo,
} from '../api/client.ts';
import { renderMarkdown } from '../lib/markdown.tsx';
import { relativeTime } from '../lib/time.ts';
import { Editor } from '../components/Editor.tsx';
import { CategoryPicker } from '../components/CategoryPicker.tsx';
import { NoteHistory } from '../components/NoteHistory.tsx';
import { Menu } from '../components/Menu.tsx';
import { restoreNoteVersion } from '../api/history.ts';

const AUTOSAVE_MS = 900;

/**
 * `merged` is a fourth state, not a flavour of `unsaved`: a merge that still
 * has contested regions must not autosave, because the conflict markers would
 * go into the vault (and out to every synced device) before anyone had a chance
 * to read them. It waits for an explicit save.
 */
type SaveState = 'saved' | 'unsaved' | 'saving' | 'conflict' | 'merged';

/** One unresolved region, as the merge marks it. */
const CONFLICT_MARKER = '<<<<<<< ';

/** One note: rendered view, editor, categories, and its linked mentions. */
export function NotePage() {
  const path = useParams()['*'] ?? '';
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();

  const [note, setNote] = useState<Note | null>(null);
  const [error, setError] = useState('');
  const [missing, setMissing] = useState(false);
  const [allNotes, setAllNotes] = useState<NoteInfo[]>([]);
  const [knownCategories, setKnownCategories] = useState<Category[]>([]);
  const [editingCategories, setEditingCategories] = useState(false);
  const [mergeNotice, setMergeNotice] = useState<number | null>(null);
  const [showHistory, setShowHistory] = useState(false);

  const editing = params.get('edit') === '1';
  const [draft, setDraft] = useState('');
  const [saveState, setSaveState] = useState<SaveState>('saved');
  // The mtime the current draft is based on — how the server detects that the
  // file moved beneath us (edited on another device, or outside the app).
  const baseMtime = useRef(0);
  // The content the edit started from. Only the browser still has it, and it
  // is what turns "two versions" into a merge the server can actually reason
  // about rather than guess at.
  const baseContent = useRef('');

  const load = useCallback(() => {
    setError('');
    setMissing(false);
    setMergeNotice(null);
    getNote(path)
      .then((n) => {
        setNote(n);
        setDraft(n.content);
        baseMtime.current = n.mtime_ms;
        baseContent.current = n.content;
        setSaveState('saved');
      })
      .catch((e: unknown) => {
        if (e instanceof ApiError && e.status === 404) setMissing(true);
        else setError(e instanceof Error ? e.message : String(e));
      });
  }, [path]);

  useEffect(load, [load]);
  useEffect(() => {
    listNotes()
      .then(setAllNotes)
      .catch(() => {});
    listCategories()
      .then(setKnownCategories)
      .catch(() => {});
  }, [path]);

  const doSave = useCallback(
    async (content: string, force = false) => {
      setSaveState('saving');
      try {
        const saved = await saveNote(path, content, force ? undefined : baseMtime.current);
        baseMtime.current = saved.mtime_ms;
        baseContent.current = saved.content;
        setNote(saved);
        setSaveState('saved');
        setMergeNotice(null);
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

  // Autosave while editing, debounced from the last keystroke. A conflict — or
  // a merge still carrying markers — stops the clock until the user decides.
  useEffect(() => {
    if (!editing || note === null || draft === note.content) return;
    if (saveState === 'conflict' || saveState === 'merged') return;
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
    if (note && draft !== note.content && saveState !== 'merged') await doSave(draft);
    setEditing(false);
  };

  /**
   * The third way out of a save conflict.
   *
   * Fetch what the server now has, and merge it with the draft against the
   * version this editor started from. Disjoint edits combine silently; anything
   * both sides rewrote comes back marked, in the editor, for the person who
   * wrote both halves to settle.
   */
  const onMerge = async () => {
    try {
      const theirs = await getNote(path);
      const { merged, conflicts } = await mergeText(draft, theirs.content, baseContent.current);
      setNote(theirs);
      baseMtime.current = theirs.mtime_ms;
      baseContent.current = theirs.content;
      setDraft(merged);
      setMergeNotice(conflicts);
      // A clean merge can just be saved; one with markers waits for a human.
      setSaveState(conflicts > 0 ? 'merged' : 'unsaved');
      setEditing(true);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const onCategoriesChange = async (next: string[]) => {
    if (!note) return;
    // Show the new chips at once; the save either confirms them or the error
    // banner explains why they went back.
    const previous = note.categories;
    const previousContent = note.content;
    setNote({ ...note, categories: next });
    try {
      const saved = await setNoteCategories(note.path, next, baseMtime.current);
      setNote(saved);
      baseMtime.current = saved.mtime_ms;
      baseContent.current = saved.content;
      // Categories live in the frontmatter, so the note's text changed too —
      // keep an untouched editor in step rather than letting it save back the
      // version without them.
      if (draft === previousContent) setDraft(saved.content);
      listCategories()
        .then(setKnownCategories)
        .catch(() => {});
    } catch (e) {
      setNote({ ...note, categories: previous });
      setError(
        e instanceof ApiError && e.status === 409
          ? 'This note changed elsewhere while you were editing its categories. Reload to see the current version.'
          : e instanceof Error
            ? e.message
            : String(e),
      );
    }
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

  const markersLeft = draft.includes(CONFLICT_MARKER);

  return (
    <div className="page note-page">
      <header className="note-head">
        <div className="note-head__titles">
          {note.dir && <span className="note-head__dir">{note.dir}/</span>}
          <h1 className="note-head__name">{note.name}</h1>
          <span className="note-head__time">edited {relativeTime(note.mtime_ms)}</span>
        </div>
        {/* One primary action, everything else behind "…". Edit is what a note
            is opened for; history, rename and delete are occasional, and side
            by side in the header they used to wrap into a second row of
            look-alike buttons with no obvious first move. */}
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
          <Menu
            label="More actions for this note"
            items={[
              {
                label: 'Version history',
                checked: showHistory,
                onSelect: () => setShowHistory((open) => !open),
              },
              { label: 'Rename…', onSelect: () => void onRename() },
              { label: 'Delete note…', danger: true, onSelect: () => void onDelete() },
            ]}
          />
        </div>
      </header>

      {/* Categories sit under the title because that's what they describe.
          Read-only until asked for: the common case is glancing at them. */}
      <section
        className={`note-cats${editingCategories ? ' note-cats--editing' : ''}`}
        aria-label="Categories"
      >
        {editingCategories ? (
          <>
            <CategoryPicker
              value={note.categories}
              known={knownCategories}
              onChange={(next) => void onCategoriesChange(next)}
            />
            {/* Spelled out rather than a bare "Done": the note's own Done
                button is a few pixels away, and two controls with the same
                name doing different things is a maze — worst of all read
                aloud. */}
            <button
              type="button"
              className="btn btn--ghost btn--small"
              onClick={() => setEditingCategories(false)}
            >
              Done with categories
            </button>
          </>
        ) : (
          /* The control that adds one rides at the end of the list it adds to,
             as one more chip. A separate button off to the side was a second
             thing competing with the header a line above it. */
          <ul className="cats__list">
            {note.categories.map((name) => (
              <li key={name} className="chip">
                {/* A category is only useful if it leads somewhere: tapping one
                    filters the notes list by it. */}
                <Link className="chip__label" to={`/?category=${encodeURIComponent(name)}`}>
                  {name}
                </Link>
              </li>
            ))}
            <li>
              <button
                type="button"
                className="chip chip--add"
                aria-label="Edit this note’s categories"
                onClick={() => setEditingCategories(true)}
              >
                {/* The "+" only where it's honest: with nothing listed yet
                    this chip adds the first one, and with chips beside it the
                    same tap opens a picker that also removes them. */}
                {note.categories.length === 0 ? (
                  <>
                    <span aria-hidden="true">+</span>Add categories
                  </>
                ) : (
                  'Edit'
                )}
              </button>
            </li>
          </ul>
        )}
      </section>

      {error && <div className="banner banner--warn">{error}</div>}

      {/* What this note said before. Loaded only when asked for: most visits
          are to read or write the note, not to look back at it. */}
      {showHistory && (
        <section className="note-history" aria-label="Note history">
          <h2 className="section-title">Earlier versions</h2>
          <NoteHistory
            path={note.path}
            onError={setError}
            onRestore={async (ref) => {
              const restored = await restoreNoteVersion(note.path, ref);
              setNote(restored);
              setDraft(restored.content);
              baseMtime.current = restored.mtime_ms;
              baseContent.current = restored.content;
              setSaveState('saved');
            }}
            onRestored={() => setShowHistory(false)}
          />
        </section>
      )}

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
            {/* Neither version has to lose: a merge keeps both sets of edits
                and asks only about the parts that genuinely collide. */}
            <button type="button" className="btn btn--small btn--primary" onClick={() => void onMerge()}>
              Merge
            </button>
          </span>
        </div>
      )}

      {mergeNotice !== null && saveState !== 'conflict' && (
        <div className={`banner ${mergeNotice > 0 ? 'banner--warn' : ''}`} role="status">
          {mergeNotice === 0 ? (
            'Both sets of edits were merged — nothing collided.'
          ) : (
            <>
              Merged, with {mergeNotice} {mergeNotice === 1 ? 'region' : 'regions'} both sides
              rewrote. Each is marked <code>&lt;&lt;&lt;&lt;&lt;&lt;&lt; mine</code> …{' '}
              <code>&gt;&gt;&gt;&gt;&gt;&gt;&gt; theirs</code> in the editor — delete the half you
              don’t want, then save.
              <span className="banner__actions">
                <button
                  type="button"
                  className="btn btn--small btn--primary"
                  onClick={() => void doSave(draft)}
                >
                  {markersLeft ? 'Save anyway' : 'Save merged note'}
                </button>
              </span>
            </>
          )}
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
            {saveState === 'merged' && 'Merged — review the marked regions, then save'}
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
