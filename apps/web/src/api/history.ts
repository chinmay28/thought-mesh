import { ApiError, encodePath } from './client.ts';
import type { Note } from './client.ts';

/**
 * Typed client for the vault's version history.
 *
 * The history is an ordinary git repository in the vault folder, kept by the
 * server. Two shapes of question, and both matter: "what did this note say
 * before?" — the one people actually ask — and "put the whole vault back to
 * Tuesday", which is the recovery case.
 *
 * Nothing here rewrites anything. A rollback is a new version whose contents
 * are the old one's, so what it replaced stays reachable and rolling back the
 * rollback is the same action again. That is what makes it safe to try.
 */

/** What kind of moment a version records. "" for a commit made outside
 * Thought Mesh — someone's own `git commit` belongs in this history too. */
export type HistoryKind =
  | 'edit'
  | 'local'
  | 'sync'
  | 'conflict'
  | 'rollback'
  | 'checkpoint'
  | 'restore'
  | '';

/** One version of the vault. */
export interface HistoryCommit {
  ref: string;
  short: string;
  subject: string;
  /** Whatever was typed when this moment was marked; "" for most entries. */
  body: string;
  kind: HistoryKind;
  author: string;
  at_ms: number;
}

export interface HistoryState {
  /**
   * 0 when this server keeps no history — git isn't installed, or the
   * deployment turned it off. The routes still answer, so the client can tell
   * that apart from a server too old to have the feature.
   */
  available: 0 | 1;
  commits: HistoryCommit[];
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }
  const res = await fetch(`/api${path}`, init);
  const text = await res.text();
  const data = text ? (JSON.parse(text) as unknown) : undefined;
  if (!res.ok) {
    const message =
      data && typeof data === 'object' && 'error' in data
        ? String((data as { error: unknown }).error)
        : `request failed (${res.status})`;
    throw new ApiError(res.status, message);
  }
  return data as T;
}

/** The vault's recent history, newest first. */
export function fetchHistory(limit?: number): Promise<HistoryState> {
  const query = limit ? `?limit=${limit}` : '';
  return request('GET', `/history${query}`);
}

/** The versions that touched one note — a note's own history. */
export function fetchNoteHistory(path: string, limit?: number): Promise<HistoryState> {
  const query = limit ? `?limit=${limit}` : '';
  return request('GET', `/history/notes/${encodePath(path)}${query}`);
}

/** One note's content as of a version. Reading only: the note is untouched. */
export function fetchNoteVersion(
  path: string,
  ref: string,
): Promise<{ path: string; ref: string; content: string }> {
  return request('GET', `/history/version/${encodePath(path)}?ref=${encodeURIComponent(ref)}`);
}

/**
 * Put one note back to an older version, leaving the rest of the vault alone.
 *
 * It goes through the ordinary note save on the server, so everything a save
 * does still happens — links are re-indexed, and the next cloud sync carries it
 * like any other edit. What was in between is not erased; it stays in the log.
 */
export function restoreNoteVersion(path: string, ref: string): Promise<Note> {
  return request('POST', '/history/restore', { path, ref });
}

/** Mark this moment, with an optional message. Recorded even when nothing has
 * changed — the message is the point. */
export function checkpointHistory(message: string): Promise<HistoryState> {
  return request('POST', '/history/checkpoint', { message });
}

/** Put the whole vault back to a version, as a new version. */
export function rollbackHistory(ref: string): Promise<HistoryState> {
  return request('POST', '/history/rollback', { ref });
}

/** How each kind of version is labelled. Absent (a hand-made commit) reads as
 * what it is: something that happened outside the app. */
export const HISTORY_KIND_LABELS: Record<HistoryKind, string> = {
  edit: 'Edited',
  local: 'Before a sync',
  sync: 'Synced',
  conflict: 'Conflict settled',
  rollback: 'Rolled back',
  checkpoint: 'Checkpoint',
  restore: 'Restored',
  '': 'Outside the app',
};

/**
 * The part of a version's subject worth showing next to its timestamp.
 *
 * Commit subjects carry the time on purpose — they are read with `git log`,
 * where there is no column of dates beside them. In this list there is, so the
 * stamp is stripped, and a subject that then says nothing the kind label
 * doesn't ("Checkpoint") is dropped entirely rather than repeated.
 */
export function historyDetail(commit: HistoryCommit): string {
  const withoutStamp = commit.subject
    .replace(/,?\s+at\s+\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}\s*$/, '')
    .trim();
  const label = HISTORY_KIND_LABELS[commit.kind];
  if (withoutStamp === '' || withoutStamp.toLowerCase() === label.toLowerCase()) {
    return '';
  }
  return withoutStamp;
}
