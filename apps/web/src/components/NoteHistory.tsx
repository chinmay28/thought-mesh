import { useEffect, useState } from 'react';
import {
  HISTORY_KIND_LABELS,
  fetchNoteHistory,
  fetchNoteVersion,
  type HistoryCommit,
} from '../api/history.ts';
import { formatDateTime } from '../lib/time.ts';

interface NoteHistoryProps {
  path: string;
  /** Called after a version has been put back, with the saved note's content. */
  onRestored: () => void;
  /** Restore is the caller's to perform — it owns the note and its editor. */
  onRestore: (ref: string) => Promise<void>;
  onError: (message: string) => void;
}

/**
 * One note's own history: what it said before, and putting a version back.
 *
 * This is the question people actually ask of a history — "what did I write
 * here last week" far more often than "restore the whole vault" — so it lives
 * on the note rather than behind a vault-wide screen.
 *
 * Choosing a version *shows* it; it does not apply it. Reading an old version
 * costs nothing and changes nothing, and putting one back is a separate,
 * deliberate press with the text already on screen to look at first.
 */
export function NoteHistory({ path, onRestore, onRestored, onError }: NoteHistoryProps) {
  const [state, setState] = useState<{ available: 0 | 1; commits: HistoryCommit[] } | null>(null);
  const [selected, setSelected] = useState<HistoryCommit | null>(null);
  const [content, setContent] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setState(null);
    setSelected(null);
    setContent(null);
    fetchNoteHistory(path, 50).then(
      (next) => {
        if (!cancelled) setState(next);
      },
      (err: unknown) => {
        if (cancelled) return;
        onError(err instanceof Error ? err.message : String(err));
        setState({ available: 0, commits: [] });
      },
    );
    return () => {
      cancelled = true;
    };
    // One fetch per note; the panel unmounts when closed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path]);

  function open(commit: HistoryCommit) {
    if (selected?.ref === commit.ref) {
      setSelected(null);
      setContent(null);
      return;
    }
    setSelected(commit);
    setContent(null);
    fetchNoteVersion(path, commit.ref).then(
      (version) => setContent(version.content),
      (err: unknown) => onError(err instanceof Error ? err.message : String(err)),
    );
  }

  async function restore(commit: HistoryCommit) {
    if (
      !window.confirm(
        `Put this note back to the version from ${formatDateTime(
          new Date(commit.at_ms).toISOString(),
        )}?\n\nThe current version isn't lost — it stays in the history, so this ` +
          `can be undone the same way.`,
      )
    ) {
      return;
    }
    setBusy(true);
    try {
      await onRestore(commit.ref);
      onRestored();
    } finally {
      setBusy(false);
    }
  }

  if (state === null) {
    return <p className="muted">Looking through this note’s history…</p>;
  }
  if (state.available === 0) {
    return (
      <p className="muted">
        This server keeps no version history. It needs <code>git</code> installed —
        the vault is then an ordinary git repository, and every version of every
        note is kept in it.
      </p>
    );
  }
  if (state.commits.length === 0) {
    return (
      <p className="muted">
        No earlier versions yet. The server records the vault a couple of minutes
        after you stop writing, and around every sync.
      </p>
    );
  }

  return (
    <div className="history">
      <ul className="history__list">
        {state.commits.map((commit, index) => (
          <li key={commit.ref} className="history__entry">
            <div className="history__row">
              <button
                type="button"
                className="history__open"
                aria-expanded={selected?.ref === commit.ref}
                onClick={() => open(commit)}
              >
                <span className="history__when">
                  {formatDateTime(new Date(commit.at_ms).toISOString())}
                </span>
                <span className="history__kind">
                  {index === 0 ? 'Current' : HISTORY_KIND_LABELS[commit.kind]}
                </span>
                {commit.body && <span className="history__note">{commit.body}</span>}
              </button>
            </div>
            {selected?.ref === commit.ref && (
              <div className="history__version">
                {content === null ? (
                  <p className="muted">Loading…</p>
                ) : (
                  <>
                    <pre className="history__text">{content === '' ? '(empty)' : content}</pre>
                    {/* The newest entry is what the note already says. */}
                    {index > 0 && (
                      <button
                        type="button"
                        className="btn btn--small btn--primary"
                        disabled={busy}
                        onClick={() => void restore(commit)}
                      >
                        {busy ? 'Restoring…' : 'Put this version back'}
                      </button>
                    )}
                  </>
                )}
              </div>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}
