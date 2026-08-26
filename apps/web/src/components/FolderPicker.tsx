import { useMemo, useState, type FormEvent } from 'react';
import type { Folder } from '../api/client.ts';

interface FolderPickerProps {
  /** The note's current folder. "" is the vault root — unfiled. */
  value: string;
  /** Called with the chosen folder. The caller moves the note; this is controlled. */
  onChange: (next: string) => void;
  /** Folders that already exist, offered as suggestions. */
  known: Folder[];
  disabled?: boolean;
}

/**
 * Where one note is filed — its folder, which is its category.
 *
 * Singular by construction: a file is in exactly one directory, so this is a
 * choice rather than a list to add to. That is the whole point of merging the
 * two ideas, and it's why this replaced a chip-list picker.
 *
 * Existing folders are suggested rather than required — there is no "create a
 * folder" step anywhere, here or on the server. A folder exists exactly as long
 * as a note is in it, so typing a new name is how one comes into being.
 */
export function FolderPicker({ value, onChange, known, disabled }: FolderPickerProps) {
  const [draft, setDraft] = useState(value);
  // The box starts pre-filled with the folder the note is already in, so it can
  // be edited rather than retyped. That text must not also filter the
  // suggestions, or opening the picker on a filed note would offer nothing —
  // its own folder is excluded from the list, and every other one fails the
  // match. Filtering starts when the user actually types something else.
  const [typed, setTyped] = useState(false);

  // Most-used first: the long tail is reachable by typing, and a list of thirty
  // is not a picker.
  const suggestions = useMemo(() => {
    const query = typed ? draft.trim().toLowerCase() : '';
    return known
      .filter((f) => f.path !== '' && f.path.toLowerCase() !== value.toLowerCase())
      .filter((f) => query === '' || f.path.toLowerCase().includes(query))
      .sort((a, b) => b.count - a.count || a.path.localeCompare(b.path))
      .slice(0, 8);
  }, [known, draft, value, typed]);

  // Unfiling is a destination like any other, so the list is worth showing for
  // it alone — a vault with one folder shouldn't hide the way back out of it.
  const showList = suggestions.length > 0 || value !== '';

  function commit(next: string) {
    const clean = next.trim().replace(/\s+/g, ' ').replace(/^\/+|\/+$/g, '');
    if (clean.toLowerCase() === value.toLowerCase()) return;
    onChange(clean);
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    commit(draft);
  }

  return (
    <div className="folder-picker">
      <form className="folder-picker__form" onSubmit={onSubmit}>
        <input
          className="cats__input"
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            setTyped(true);
          }}
          onKeyDown={(e) => {
            if (e.key !== 'Escape') return;
            setDraft(value);
            setTyped(false);
          }}
          placeholder="Unfiled — type a folder…"
          aria-label="Folder for this note"
          autoComplete="off"
          maxLength={200}
          disabled={disabled}
        />
        <button
          type="submit"
          className="btn btn--small"
          disabled={disabled || draft.trim().replace(/^\/+|\/+$/g, '').toLowerCase() === value.toLowerCase()}
        >
          Move
        </button>
      </form>

      {showList && (
        <ul className="cats__suggestions">
          {/* Unfiling is a real destination, not the absence of one, so it gets
              a button like any other rather than an empty box to submit. */}
          {value !== '' && (
            <li>
              <button
                type="button"
                className="btn btn--ghost btn--small"
                disabled={disabled}
                onClick={() => onChange('')}
              >
                Unfiled
              </button>
            </li>
          )}
          {suggestions.map((f) => (
            <li key={f.path}>
              <button
                type="button"
                className="btn btn--ghost btn--small"
                disabled={disabled}
                onClick={() => onChange(f.path)}
              >
                {f.path}
                <span className="cats__count">{f.count}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
