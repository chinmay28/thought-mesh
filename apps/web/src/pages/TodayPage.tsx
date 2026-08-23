import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { ApiError, createNote, getNote } from '../api/client.ts';
import { todayStamp } from '../lib/time.ts';

/**
 * The daily note: opens journal/YYYY-MM-DD.md, creating it on first visit of
 * the day. A route rather than a button handler so it's linkable and appears
 * in the tab bar.
 */
export function TodayPage() {
  const navigate = useNavigate();
  const [error, setError] = useState('');

  useEffect(() => {
    const stamp = todayStamp();
    const path = `journal/${stamp}.md`;
    let alive = true;
    (async () => {
      try {
        await getNote(path);
        if (alive) navigate(`/notes/${path}`, { replace: true });
      } catch (e) {
        if (!(e instanceof ApiError) || e.status !== 404) {
          if (alive) setError(e instanceof Error ? e.message : String(e));
          return;
        }
        try {
          const note = await createNote({ path, content: `# ${stamp}\n\n` });
          if (alive) navigate(`/notes/${note.path}?edit=1`, { replace: true });
        } catch (err) {
          if (alive) setError(err instanceof Error ? err.message : String(err));
        }
      }
    })();
    return () => {
      alive = false;
    };
  }, [navigate]);

  return (
    <div className="page">
      {error ? (
        <div className="banner banner--warn">{error}</div>
      ) : (
        <p className="muted">Opening today’s note…</p>
      )}
    </div>
  );
}
