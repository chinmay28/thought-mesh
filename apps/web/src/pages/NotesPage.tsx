import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';
import { QuickCapture } from '../components/QuickCapture.tsx';
import {
  listCategories,
  listNotes,
  search,
  type Category,
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
 * `?category=` narrows the list. It lives in the URL rather than in state so a
 * filtered view is a place you can go back to, link to, and land on from a
 * category chip on a note.
 */
export function NotesPage() {
  const [notes, setNotes] = useState<NoteInfo[] | null>(null);
  const [categories, setCategories] = useState<Category[]>([]);
  const [error, setError] = useState('');
  const [query, setQuery] = useState('');
  const [results, setResults] = useState<SearchResult[] | null>(null);
  const composeRef = useRef<HTMLTextAreaElement>(null);
  const [params, setParams] = useSearchParams();
  const category = params.get('category') ?? '';

  // "?new=1" is how the "+" action asks for the composer from anywhere else.
  // It is consumed on arrival so a reload doesn't grab focus all over again.
  // Any other parameter — a category filter — is left alone.
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
    listNotes(category || undefined)
      .then((n) => alive && setNotes(n))
      .catch((e: unknown) => alive && setError(e instanceof Error ? e.message : String(e)));
    return () => {
      alive = false;
    };
  }, [category]);

  // The vocabulary follows the notes: assigning a category on another screen
  // and coming back should show it, without a reload.
  useEffect(() => {
    let alive = true;
    listCategories()
      .then((c) => alive && setCategories(c))
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

  /** Set (or clear) the category filter, keeping it in the URL. */
  function setFilter(name: string) {
    const next = new URLSearchParams(params);
    if (name === '') next.delete('category');
    else next.set('category', name);
    setParams(next, { replace: true });
  }

  return (
    <div className="page">
      <QuickCapture
        textRef={composeRef}
        onCreated={(note) => setNotes((prev) => [note, ...(prev ?? [])])}
      />

      {/* The vault's own vocabulary, as a filter. Shown only once something has
          been categorised — an empty row of chips would just be a claim that a
          feature exists. */}
      {categories.length > 0 && (
        <nav className="cat-filter" aria-label="Filter by category">
          <button
            type="button"
            className={`chip chip--button${category === '' ? ' chip--active' : ''}`}
            onClick={() => setFilter('')}
          >
            All
          </button>
          {categories.map((c) => (
            <button
              key={c.name}
              type="button"
              className={`chip chip--button${
                c.name.toLowerCase() === category.toLowerCase() ? ' chip--active' : ''
              }`}
              onClick={() => setFilter(c.name)}
            >
              {c.name}
              <span className="cats__count">{c.count}</span>
            </button>
          ))}
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
          {category ? (
            <>
              <p className="empty__lead">Nothing in “{category}” yet.</p>
              <p className="muted">
                Open a note and add the category to it, or{' '}
                <button type="button" className="link-btn" onClick={() => setFilter('')}>
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
            {category && <> in “{category}”</>}
          </h2>
          <ul className="note-list">
            {recent.map((n) => (
              <li key={n.path}>
                <Link className="note-card" to={`/notes/${n.path}`}>
                  <span className="note-card__name">{n.name}</span>
                  <span className="note-card__meta">
                    {n.dir && <span className="note-card__dir">{n.dir}</span>}
                    {n.categories.map((name) => (
                      <span key={name} className="note-card__cat">
                        {name}
                      </span>
                    ))}
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
