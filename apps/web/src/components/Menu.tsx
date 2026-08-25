import { useEffect, useId, useRef, useState } from 'react';

export interface MenuItem {
  label: string;
  onSelect: () => void;
  /** Renders in the danger colour — destructive, and it should look it. */
  danger?: boolean;
  /** Marked as the current choice (a toggle that is currently on). */
  checked?: boolean;
}

interface MenuProps {
  /** Accessible name for the trigger, e.g. "More actions for this note". */
  label: string;
  items: MenuItem[];
}

/**
 * A "…" button that opens a short list of secondary actions.
 *
 * Secondary actions crowd a header when they all sit in it at once: on a phone
 * the row wraps, and four side-by-side buttons of similar weight leave nothing
 * looking like the thing you came to press. One primary button plus this is the
 * same actions with an obvious first move.
 *
 * Closes on Escape (returning focus to the trigger, so the keyboard doesn't
 * land back at the top of the page), on a press outside, and after a choice —
 * every item here either navigates away or opens a prompt of its own.
 */
export function Menu({ label, items }: MenuProps) {
  const [open, setOpen] = useState(false);
  const wrap = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const panelId = useId();

  useEffect(() => {
    if (!open) return;
    const onPointerDown = (e: PointerEvent) => {
      if (!wrap.current?.contains(e.target as Node)) setOpen(false);
    };
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return;
      setOpen(false);
      trigger.current?.focus();
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [open]);

  return (
    <div className="menu" ref={wrap}>
      <button
        type="button"
        ref={trigger}
        className={`btn btn--ghost menu__trigger${open ? ' btn--active' : ''}`}
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        onClick={() => setOpen((on) => !on)}
      >
        <span aria-hidden="true">···</span>
      </button>
      {open && (
        <div className="menu__panel" id={panelId} role="menu" aria-label={label}>
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              // A toggle announces its state; a plain action has none to
              // announce, and `menuitem` with aria-checked would be a lie.
              role={item.checked === undefined ? 'menuitem' : 'menuitemcheckbox'}
              {...(item.checked === undefined ? {} : { 'aria-checked': item.checked })}
              className={`menu__item${item.danger ? ' menu__item--danger' : ''}`}
              onClick={() => {
                setOpen(false);
                item.onSelect();
              }}
            >
              {item.label}
              {item.checked && (
                <span className="menu__check" aria-hidden="true">
                  ✓
                </span>
              )}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
