/** Human-friendly "how long ago" for note modification times. */
export function relativeTime(mtimeMs: number, now = Date.now()): string {
  const s = Math.max(0, Math.floor((now - mtimeMs) / 1000));
  if (s < 45) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  if (d < 7) return `${d}d ago`;
  const date = new Date(mtimeMs);
  return date.toLocaleDateString(undefined, {
    month: 'short',
    day: 'numeric',
    year: date.getFullYear() === new Date(now).getFullYear() ? undefined : 'numeric',
  });
}

/** A stored ISO timestamp as local "Aug 23, 2:41 PM" style text. */
export function formatDateTime(iso: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return iso;
  return t.toLocaleString(undefined, {
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    year: t.getFullYear() === new Date().getFullYear() ? undefined : 'numeric',
  });
}

/** Local calendar date as YYYY-MM-DD — the daily note's name. */
export function todayStamp(now = new Date()): string {
  const y = now.getFullYear();
  const m = String(now.getMonth() + 1).padStart(2, '0');
  const d = String(now.getDate()).padStart(2, '0');
  return `${y}-${m}-${d}`;
}
