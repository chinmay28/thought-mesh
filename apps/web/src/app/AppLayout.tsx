import { useEffect, useState, useSyncExternalStore, type ReactNode } from 'react';
import { Link, NavLink, Outlet, useLocation } from 'react-router-dom';
import { isConnected, subscribeConnected } from '../api/client.ts';
import { APP_VERSION } from '../version.ts';

/** Primary destinations, shown in the desktop header and the mobile tab bar. */
const NAV_ITEMS: { to: string; label: string; icon: ReactNode }[] = [
  { to: '/', label: 'Notes', icon: <NotesIcon /> },
  { to: '/graph', label: 'Graph', icon: <GraphIcon /> },
  { to: '/today', label: 'Today', icon: <TodayIcon /> },
  { to: '/sync', label: 'Sync', icon: <SyncIcon /> },
];

/** How long the developer badge stays on screen when the header mark is
 * tapped. Kept in sync with the `dev-flash*` animation durations in
 * styles.css — the CSS fades out on its own clock, this unmounts it. */
const DEV_FLASH_MS = 3000;

/** App chrome: header, connectivity banner, the routed page outlet, and a
 * mobile bottom tab bar with a floating "new note" action. */
export function AppLayout() {
  const connected = useSyncExternalStore(subscribeConnected, isConnected, () => true);
  const { pathname } = useLocation();
  // The FAB *is* the "new note" action, so don't show it on that form.
  const showFab = pathname !== '/new';
  // Tapping the developer mark throws the badge up full screen for a beat.
  const [devFlash, setDevFlash] = useState(false);

  useEffect(() => {
    if (!devFlash) return;
    const timer = window.setTimeout(() => setDevFlash(false), DEV_FLASH_MS);
    // Nobody should be stuck waiting out an animation — Escape ends it early,
    // as does a tap anywhere on the overlay.
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setDevFlash(false);
    };
    window.addEventListener('keydown', onKey);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener('keydown', onKey);
    };
  }, [devFlash]);

  return (
    <div className="app">
      <header className="app__header">
        <Link to="/" className="app__brand">
          <img className="app__brand-logo" src="/icon.svg" alt="" aria-hidden="true" />
          {/* Name over version, as a lockup — same treatment on every device. */}
          <span className="app__brand-text">
            Thought Mesh
            <span className="app__brand-version">{APP_VERSION}</span>
          </span>
        </Link>
        {/* Everything that hangs off the right edge. Grouping the nav with the
            developer mark keeps them together when the nav collapses on
            mobile — the mark then sits alone opposite the brand. */}
        <div className="app__header-end">
          {/* Desktop / wide-screen navigation. The mobile tab bar mirrors it. */}
          <nav className="app__nav" aria-label="Primary">
            {NAV_ITEMS.map((item) => (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.to === '/'}
                className={({ isActive }) =>
                  `btn btn--ghost${isActive ? ' btn--active' : ''}`
                }
              >
                {item.label}
              </NavLink>
            ))}
            <Link to="/new" className="btn btn--primary">
              New note
            </Link>
          </nav>
          {/* Developer credit. Deliberately quiet — a muted disk that only
              comes to full strength on hover, so it never competes with the
              primary action next to it. Tapping it shows the badge full
              screen, which is the only place its detail is readable. */}
          <button
            type="button"
            className="app__dev"
            title="Built by CM Hegday · 0x434d"
            aria-label="Show the developer badge"
            onClick={() => setDevFlash(true)}
          >
            {/* The button carries the label; the image would only repeat it. */}
            <img className="app__dev-logo" src="/dev-badge.png" alt="" aria-hidden="true" />
          </button>
        </div>
      </header>

      {!connected && (
        <div className="banner banner--warn" role="status">
          Can’t reach the Thought Mesh server. Changes won’t be saved until the
          connection is restored.
        </div>
      )}

      <main className="app__main">
        <Outlet />
      </main>

      <footer className="app__footer">
        <span>Your notes are plain markdown files on your Thought Mesh server.</span>
      </footer>

      {/* Floating action button — the primary create action on phones. */}
      {showFab && (
        <Link to="/new" className="fab" aria-label="New note" title="New note">
          <PlusIcon />
        </Link>
      )}

      {/* Mobile bottom tab bar (hidden on wide screens via CSS). */}
      <nav className="tab-bar" aria-label="Primary">
        {NAV_ITEMS.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            end={item.to === '/'}
            className={({ isActive }) =>
              `tab-bar__item${isActive ? ' tab-bar__item--active' : ''}`
            }
          >
            <span className="tab-bar__icon" aria-hidden="true">
              {item.icon}
            </span>
            <span className="tab-bar__label">{item.label}</span>
          </NavLink>
        ))}
      </nav>

      {/* Developer badge, full screen for three seconds. It lives out here
          rather than in the header because the header's backdrop-filter makes
          it a containing block — a fixed overlay inside it would be trapped
          in the header's strip instead of covering the viewport. */}
      {devFlash && (
        <div
          className="dev-flash"
          role="presentation"
          onClick={() => setDevFlash(false)}
        >
          <div className="dev-flash__lockup">
            <img
              className="dev-flash__logo"
              src="/dev-badge-full.png"
              alt="Built by CM Hegday — 0x434d"
            />
            <span className="dev-flash__handle">github.com/chinmay28</span>
          </div>
        </div>
      )}
    </div>
  );
}

/* Inline, dependency-free icons. They inherit `currentColor` and a 24px box. */

function NotesIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M6 3h9l4 4v14H6z" />
      <path d="M14 3v5h5" />
      <path d="M9 13h7M9 17h5" />
    </svg>
  );
}

function GraphIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <circle cx="6" cy="6" r="2.5" />
      <circle cx="18" cy="8" r="2.5" />
      <circle cx="9" cy="18" r="2.5" />
      <path d="M8 7.2 15.6 8M7 8.3l1.4 7.3M16 9.8l-5.3 6.5" />
    </svg>
  );
}

function TodayIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <rect x="3" y="5" width="18" height="16" rx="2" />
      <path d="M3 10h18M8 3v4M16 3v4" />
    </svg>
  );
}

function SyncIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M17.5 19a4.5 4.5 0 0 0 .9-8.9 6 6 0 0 0-11.7-.6A4 4 0 0 0 7 17.5" />
      <path d="M12 12v8" />
      <path d="m8.8 15.2 3.2-3.2 3.2 3.2" />
    </svg>
  );
}

function PlusIcon() {
  return (
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5"
      strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}
