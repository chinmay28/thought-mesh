import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import {
  deleteFolder,
  listFolders,
  renameFolder,
  type Folder,
} from '../api/client.ts';
import { Menu } from '../components/Menu.tsx';

/**
 * Every folder in the vault — which is to say every category, since they are
 * the same thing.
 *
 * The list is a tree flattened into rows: the server returns folders sorted by
 * path, so indenting each by its own depth draws the hierarchy without the
 * client having to build one. That keeps the "no business logic in the client"
 * rule even for something as structural as a tree.
 *
 * Renaming and deleting move files, so both say plainly what happened —
 * "12 notes moved, 4 links updated" — rather than just closing.
 */
export function FoldersPage() {
  const [folders, setFolders] = useState<Folder[] | null>(null);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState('');

  const load = useCallback(() => {
    setError('');
    listFolders()
      .then(setFolders)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)));
  }, []);

  useEffect(load, [load]);

  /** Report a move in the terms the user cares about: notes, then links. */
  const report = (verb: string, moved: number, updated: number) => {
    const notes = `${moved} note${moved === 1 ? '' : 's'}`;
    if (updated === 0) return `${verb} — ${notes} moved.`;
    return `${verb} — ${notes} moved, ${updated} link${updated === 1 ? '' : 's'} updated.`;
  };

  const onRename = async (folder: Folder) => {
    const entered = window.prompt(
      `Rename "${folder.path}". Everything inside moves with it, and links that ` +
        `point into it are updated. Use "/" to nest, e.g. "Reading/2026".`,
      folder.path,
    );
    if (entered === null || entered.trim() === '' || entered === folder.path) return;
    setBusy(folder.path);
    setError('');
    try {
      const res = await renameFolder(folder.path, entered);
      setFolders(res.folders);
      setNotice(report(`Renamed to “${entered}”`, res.moved_notes, res.updated_notes));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy('');
    }
  };

  const onDelete = async (folder: Folder) => {
    // Worth spelling out: "delete" here has never meant deleting notes, and a
    // folder full of them is exactly when someone would fear that it does.
    const ok = window.confirm(
      `Remove the folder "${folder.path}"?\n\n` +
        `The ${folder.total} note${folder.total === 1 ? '' : 's'} inside are kept — ` +
        `they move up one level. Nothing is deleted.`,
    );
    if (!ok) return;
    setBusy(folder.path);
    setError('');
    try {
      const res = await deleteFolder(folder.path);
      setFolders(res.folders);
      setNotice(report(`Removed “${folder.path}”`, res.moved_notes, res.updated_notes));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy('');
    }
  };

  if (folders === null) {
    return (
      <div className="page">
        {error ? <div className="banner banner--warn">{error}</div> : <p className="muted">Loading…</p>}
      </div>
    );
  }

  // The root is always present but isn't a folder anyone can rename — it's
  // shown as "Unfiled" so the notes in it are reachable, and nothing more.
  const root = folders.find((f) => f.path === '');
  const real = folders.filter((f) => f.path !== '');

  return (
    <div className="page">
      <header className="page-head">
        <h1 className="page-head__title">Folders</h1>
        <p className="muted page-head__lead">
          A folder is a category — a note has exactly one, and it’s the folder the
          file is really in. Renaming one moves the notes and updates the links
          that point into it.
        </p>
      </header>

      {error && <div className="banner banner--warn">{error}</div>}
      {notice && (
        <div className="banner" role="status">
          {notice}
        </div>
      )}

      <ul className="folder-list">
        {root && root.count > 0 && (
          <li className="folder-row" style={{ '--depth': 0 } as React.CSSProperties}>
            <Link className="folder-row__main" to="/?folder=">
              <span className="folder-row__name muted">Unfiled</span>
              <span className="folder-row__count">{root.count}</span>
            </Link>
          </li>
        )}
        {real.map((f) => (
          <li
            key={f.path}
            className={`folder-row${busy === f.path ? ' folder-row--busy' : ''}`}
            style={{ '--depth': f.depth - 1 } as React.CSSProperties}
          >
            <Link className="folder-row__main" to={`/?folder=${encodeURIComponent(f.path)}`}>
              <span className="folder-row__name">{f.name}</span>
              {/* A folder holding only subfolders reports the total instead, so
                  a parent never reads as empty when it isn't. */}
              <span className="folder-row__count">
                {f.count > 0 ? f.count : `${f.total} below`}
              </span>
            </Link>
            <Menu
              label={`More actions for the folder ${f.path}`}
              items={[
                { label: 'Rename…', onSelect: () => void onRename(f) },
                { label: 'Remove folder…', danger: true, onSelect: () => void onDelete(f) },
              ]}
            />
          </li>
        ))}
      </ul>

      {real.length === 0 && (
        <p className="muted">
          No folders yet. Open a note and file it somewhere, or{' '}
          <Link to="/new">create one in a folder</Link>.
        </p>
      )}
    </div>
  );
}
