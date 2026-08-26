import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { QuickCapture } from '../components/QuickCapture.tsx';
import {
  listFolders,
  listNotes,
  search,
  type Folder,
  type NoteInfo,
  type SearchResult,
} from '../api/client.ts';
import { relativeTime } from '../lib/time.ts';

const SEARCH_DEBOUNCE_MS = 250;

/**
 * Home: write a note, then search across the vault or browse it, newest
 * first. The composer leads because capture is what the app gets opened for
 * — reaching it should never cost a tap.
 *
 * `?folder=` narrows the list to one folder — which is to say one category,
 * since they are the same thing. It lives in the URL rather than in state so a
 * filtered view is a place you can go back to, link to, and land on from a
 * folder chip on a note. An empty `?folder=` is the vault root (unfiled), which
 * is a real filter and not the absence of one.
 */
export function NotesPage() {
  const [notes, setNotes] = useState<NoteInfo[] | null>(null);
  const [folders, setFolders] = useState<Folder[]>([]);
  const [error, setError] = useState('');
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const composeRef = useRef<HTMLTextAreaElement>(null);
  const [params, setParams] = useSearchParams();
  // null means "no filter"; "" means the vault root. They are different views.
  const folder = params.get('folder');

  // "?new=1" is how the "+" action asks for the composer from anywhere else.
  // It is consumed on arrival so a reload doesn't grab focus all over again.
  // Any other parameter — a folder filter — is left alone.
  useEffect(() => {
    if (!params.has('new')) return;
    composeRef.current?.focus();
    composeRef.current?.scrollIntoView({ block: 'start' });
    const next = new URLSearchParams(params);
    next.delete('new');
    setParams(next, { replace: true });
  }, [params, setParams]);

  useEffect(() => {
    let alive = true;
    setNotes(null);
    listNotes(folder ?? undefined)
      .then((n) => alive && setNotes(n))
      .catch((e: unknown) => alive && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      alive = false;
    };
  }, [folder]);

  // The tree follows the notes: filing one on another screen and coming back
  // should show it, without a reload.
  useEffect(() => {
    let alive = true;
    listFolders()
      .then((f) => alive && setFolders(f))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [notes]);

  // Debounced server-side search; a blank query goes back to browsing.
  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setResults(null);
      return;
    }
    const timer = window.setTimeout(() => {
      search(q)
        .then(setResults)
        .catch(() => setResults([]));
    }, SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(timer);
  }, [query]);

  const recent = useMemo(
    () => (notes ? [...notes].sort((a, b) => b.mtime_ms - a.mtime_ms) : []),
    [notes],
  );

  /** Set (or clear) the folder filter, keeping it in the URL. `null` clears it;
   *  `""` is the vault root, so the two can't share a value. */
  function setFilter(path: string | null) {
    const next = new URLSearchParams(params);
    if (path === null) next.delete('folder');
    else next.set('folder', path);
    setParams(next, { replace: true });
  }

  return (
    <div className="page">
      <QuickCapture
        textRef={composeRef}
        onCreated={(note) => setNotes((prev) => [note, ...(prev ?? [])])}
      />

      {/* The vault's folders, as a filter. Shown only once something has been
          filed — an empty row of chips would just be a claim that a feature
          exists. "Folders" leads to the page that renames and removes them. */}
      {folders.some((f) => f.path !== '') && (
        <nav className="cat-filter" aria-label="Filter by folder">
          <button
            type="button"
            className={`chip chip--button${folder === null ? ' chip--active' : ''}`}
            onClick={() => setFilter(null)}
          >
            All
          </button>
          {folders.map((f) =>
            f.path === '' ? (
              // Unfiled is only worth offering when something is unfiled.
              f.count > 0 && (
                <button
                  key="__root"
                  type="button"
                  className={`chip chip--button${folder === '' ? ' chip--active' : ''}`}
                  onClick={() => setFilter('')}
                >
                  Unfiled
                  <span className="cats__count">{f.count}</span>
                </button>
              )
            ) : (
              <button
                key={f.path}
                type="button"
                className={`chip chip--button${
                  f.path.toLowerCase() === (folder ?? '\u0000').toLowerCase() ? ' chip--active' : ''
                }`}
                onClick={() => setFilter(f.path)}
              >
                {f.path}
                <span className="cats__count">{f.count}</span>
              </button>
            ),
          )}
          <Link className="chip chip--add" to="/folders">
            <span aria-hidden="true">⋯</span>
            Manage
          </Link>
        </nav>
      )}

      <div className="searchbar">
        <SearchIcon />
        <input
          type="search"
          className="searchbar__input"
          placeholder="Search notes…"
          aria-label="Search notes"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          autoComplete="off"
        />
      </div>

      {error && <div className="banner banner--warn">{error}</div>}

      {results !== null ? (
        <section aria-label="Search results">
          <h2 className="section-title">
            {results.length === 0
              ? 'No matches'
              : `${results.length} match${results.length === 1 ? '' : 'es'}`}
          </h2>
          <ul className="note-list">
            {results.map((r) => (
              <li key={r.path}>
                <Link className="note-card" to={`/notes/${r.path}`}>
                  <span className="note-card__name">{r.name}</span>
                  <span className="note-card__meta">
                    {r.snippet ? (
                      <span className="note-card__snippet">{r.snippet}</span>
                    ) : (
                      <span className="note-card__dir">{r.path}</span>
                    )}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      ) : notes === null ? (
        <p className="muted">Loading…</p>
      ) : notes.length === 0 ? (
        <div className="empty">
          {folder !== null ? (
            <>
              <p className="empty__lead">
                Nothing {folder === '' ? 'unfiled' : `in “${folder}”`} yet.
              </p>
              <p className="muted">
                Open a note and file it there, or{' '}
                <button type="button" className="link-btn" onClick={() => setFilter(null)}>
                  show every note
                </button>
                .
              </p>
            </>
          ) : (
            <>
              <p className="empty__lead">Your mesh is empty.</p>
              <p className="muted">
                Write the first note above. Notes are plain markdown files — link
                them with <code>[[double brackets]]</code> and Thought Mesh keeps
                track of what connects to what.
              </p>
            </>
          )}
        </div>
      ) : (
        <section aria-label="All notes">
          <h2 className="section-title">
            {notes.length} note{notes.length === 1 ? '' : 's'}
            {folder !== null && (folder === '' ? <> unfiled</> : <> in “{folder}”</>)}
          </h2>
          <ul className="note-list">
            {recent.map((n) => (
              <li key={n.path}>
                <Link className="note-card" to={`/notes/${n.path}`}>
                  <span className="note-card__name">{n.name}</span>
                  <span className="note-card__meta">
                    {/* One chip: the folder is the category, so a note filed
                        in Money/ can no longer also be labelled "Money" and
                        show the same word twice. */}
                    {n.dir && <span className="note-card__cat">{n.dir}</span>}
                    <span className="note-card__time">{relativeTime(n.mtime_ms)}</span>
                  </span>
                </Link>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function SearchIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" aria-hidden="true" className="searchbar__icon">
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-3.8-3.8" />
    </svg>
  );
}
