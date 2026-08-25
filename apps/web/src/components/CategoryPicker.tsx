import { useMemo, useState, type FormEvent, type KeyboardEvent } from 'react';
import type { Category } from '../api/client.ts';

interface CategoryPickerProps {
  /** The note's current categories, in the order it lists them. */
  value: string[];
  /** Called with the new list. The caller saves; this component is controlled. */
  onChange: (next: string[]) => void;
  /** The vault's vocabulary, offered as suggestions. */
  known: Category[];
  disabled?: boolean;
}

/**
 * A note's categories, as removable chips plus a box that adds one.
 *
 * Existing names are suggested rather than required: categories are the user's
 * own vocabulary, and there is no "create a category" step anywhere in the app
 * — a category exists exactly as long as some note claims it. Suggesting what
 * is already in use is how the vocabulary stays small without a registry
 * enforcing it.
 *
 * Editing is optimistic in the sense that this component only reports the new
 * list; the page decides when to save and how to report a failure.
 */
export function CategoryPicker({ value, onChange, known, disabled }: CategoryPickerProps) {
  const [draft, setDraft] = useState('');
  const [open, setOpen] = useState(false);

  const assigned = useMemo(() => new Set(value.map((c) => c.toLowerCase())), [value]);

  // Suggestions: what the vault already uses, minus what this note has, matched
  // on what's been typed so far. Most-used first — the long tail is reachable
  // by typing it out, and a list of thirty is not a picker.
  const suggestions = useMemo(() => {
    const query = draft.trim().toLowerCase();
    return known
      .filter((c) => !assigned.has(c.name.toLowerCase()))
      .filter((c) => query === '' || c.name.toLowerCase().includes(query))
      .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name))
      .slice(0, 8);
  }, [known, assigned, draft]);

  function add(name: string) {
    const clean = name.trim().replace(/\s+/g, ' ');
    if (clean === '' || assigned.has(clean.toLowerCase())) {
      setDraft('');
      return;
    }
    onChange([...value, clean]);
    setDraft('');
  }

  function remove(name: string) {
    onChange(value.filter((c) => c !== name));
  }

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    add(draft);
  }

  // Backspace on an empty box removes the last chip — the convention for a
  // field made of chips, and the only way to undo without aiming at an "×".
  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'Backspace' && draft === '' && value.length > 0) {
      e.preventDefault();
      remove(value[value.length - 1]!);
    }
    if (e.key === 'Escape') {
      setDraft('');
      setOpen(false);
    }
  }

  return (
    <div className="cats">
      <ul className="cats__list" aria-label="Categories">
        {value.map((name) => (
          <li key={name} className="chip">
            <span className="chip__label">{name}</span>
            <button
              type="button"
              className="chip__remove"
              aria-label={`Remove category ${name}`}
              disabled={disabled}
              onClick={() => remove(name)}
            >
              ×
            </button>
          </li>
        ))}
        {value.length === 0 && <li className="muted cats__empty">No categories yet</li>}
      </ul>

      <form className="cats__add" onSubmit={onSubmit}>
        <input
          className="cats__input"
          value={draft}
          onChange={(e) => {
            setDraft(e.target.value);
            setOpen(true);
          }}
          onFocus={() => setOpen(true)}
          // A blur that lands on a suggestion button would close the list
          // before the click registers, so give the click a beat to happen.
          onBlur={() => window.setTimeout(() => setOpen(false), 150)}
          onKeyDown={onKeyDown}
          placeholder="Add a category…"
          aria-label="Add a category"
          autoComplete="off"
          maxLength={80}
          disabled={disabled}
        />
        <button type="submit" className="btn btn--small" disabled={disabled || draft.trim() === ''}>
          Add
        </button>
      </form>

      {open && suggestions.length > 0 && (
        <ul className="cats__suggestions">
          {suggestions.map((c) => (
            <li key={c.name}>
              <button
                type="button"
                className="btn btn--ghost btn--small"
                disabled={disabled}
                onClick={() => add(c.name)}
              >
                {c.name}
                <span className="cats__count">{c.count}</span>
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
