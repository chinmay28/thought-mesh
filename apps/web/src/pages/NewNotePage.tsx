import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { createNote, listNotes, type NoteInfo } from '../api/client.ts';

/** Create a note: a name, an optional folder, straight into the editor. */
export function NewNotePage() {
  const navigate = useNavigate();
  const [params] = useSearchParams();
  // Arriving from a [[link]] that names a missing note prefills the name.
  const [name, setName] = useState(params.get('name') ?? '');
  const [dir, setDir] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [notes, setNotes] = useState<NoteInfo[]>([]);

  useEffect(() => {
    listNotes().then(setNotes).catch(() => {});
  }, []);

  const dirs = useMemo(() => {
    const set = new Set<string>();
    for (const n of notes) if (n.dir) set.add(n.dir);
    return [...set].sort();
  }, [notes]);

  const onSubmit = async (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError('');
    try {
      const note = await createNote({ name, dir, content: '' });
      navigate(`/notes/${note.path}?edit=1`, { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <div className="page page--narrow">
      <h1 className="page-title">New note</h1>
      <form className="form" onSubmit={(e) => void onSubmit(e)}>
        <label className="field">
          <span className="field__label">Name</span>
          <input
            className="field__input"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="What is this note about?"
            autoFocus
            required
            maxLength={180}
          />
        </label>
        <label className="field">
          <span className="field__label">
            Folder <span className="muted">(optional)</span>
          </span>
          <input
            className="field__input"
            value={dir}
            onChange={(e) => setDir(e.target.value)}
            placeholder="e.g. ideas or journal"
            list="tm-dirs"
          />
          <datalist id="tm-dirs">
            {dirs.map((d) => (
              <option key={d} value={d} />
            ))}
          </datalist>
        </label>
        {error && <div className="banner banner--warn">{error}</div>}
        <div className="form__actions">
          <button type="submit" className="btn btn--primary" disabled={busy || !name.trim()}>
            {busy ? 'Creating…' : 'Create note'}
          </button>
          <button type="button" className="btn btn--ghost" onClick={() => navigate(-1)}>
            Cancel
          </button>
        </div>
      </form>
      <p className="muted">
        The note becomes a markdown file in your vault
        {dir.trim() ? ` under “${dir.trim()}/”` : ''}. Link to it from any other
        note with <code>[[{name.trim() || 'its name'}]]</code>.
      </p>
    </div>
  );
}
