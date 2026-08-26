/**
 * Typed HTTP client for the Thought Mesh server.
 *
 * These shapes mirror the REST API exactly (snake_case fields, integer
 * flags, `{"error": …}` bodies) — the contract is pinned server-side by
 * server/internal/api/api_test.go. Change them only together.
 *
 * All business logic (link resolution, backlinks, rename rewriting, search)
 * lives server-side; this file is transport only.
 */

export interface NoteInfo {
  path: string; // vault-relative, "/"-separated, ends ".md"
  name: string; // file name without ".md"
  dir: string; // parent folder, "" at the vault root
  size: number;
  mtime_ms: number;
}

/**
 * One folder in the vault — which is also one category. There is exactly one
 * per note (the directory the file is in), so there is no separate list of
 * categories on a note and no way for the two to disagree.
 */
export interface Folder {
  /** Vault-relative, "/"-separated. "" is the root, where unfiled notes live. */
  path: string;
  /** The last segment — what to show in a list. "" for the root. */
  name: string;
  /** How many folders sit above this one; 0 at the root. */
  depth: number;
  /** Notes directly inside; `total` includes every folder below. */
  count: number;
  total: number;
}

export interface NoteLink {
  target: string; // the wikilink's text
  path: string; // resolved note path, "" when no note matches yet
  name: string;
}

export interface Backlink {
  path: string;
  name: string;
  snippet: string;
  count: number;
}

export interface Note extends NoteInfo {
  content: string;
  links: NoteLink[];
  backlinks: Backlink[];
}

export interface SearchResult {
  path: string;
  name: string;
  snippet: string; // "" for a name-only match
  line: number;
}

export interface GraphNode {
  id: string; // note path, or "missing:<target>" for a ghost node
  name: string;
  missing: 0 | 1;
  links_in: number;
  links_out: number;
}

export interface GraphEdge {
  from: string;
  to: string;
}

export interface Health {
  status: string;
  version: string;
  notes: number;
}

/** An error response from the API, carrying the HTTP status. */
export class ApiError extends Error {
  constructor(
    readonly status: number,
    message: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

// ---------------------------------------------------------------------------
// Connectivity: every request reports success/failure so the app shell can
// show a "can't reach the server" banner without its own polling.
// ---------------------------------------------------------------------------

let connected = true;
const listeners = new Set<() => void>();

function setConnected(value: boolean) {
  if (connected === value) return;
  connected = value;
  for (const l of listeners) l();
}

/** For useSyncExternalStore in the app shell. */
export function subscribeConnected(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function isConnected(): boolean {
  return connected;
}

// ---------------------------------------------------------------------------

/** Encode a note path for a URL, keeping the "/" separators. */
export function encodePath(path: string): string {
  return path.split('/').map(encodeURIComponent).join('/');
}

async function request<T>(method: string, url: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }
  let res: Response;
  try {
    res = await fetch(url, init);
  } catch (err) {
    setConnected(false);
    throw err;
  }
  setConnected(true);
  if (res.status === 204) {
    return undefined as T;
  }
  const data: unknown = await res.json().catch(() => ({}));
  if (!res.ok) {
    const msg =
      typeof data === 'object' && data !== null && 'error' in data
        ? String((data as { error: unknown }).error)
        : `request failed (${res.status})`;
    throw new ApiError(res.status, msg);
  }
  return data as T;
}

export function health(): Promise<Health> {
  return request('GET', '/api/health');
}

/**
 * Every note, newest-agnostic (the server sorts by path).
 *
 * `folder` narrows the list to exactly that folder, matched case-insensitively;
 * subfolders are their own entries in `listFolders`, so a browser asks per
 * level. Passing "" means the vault root (unfiled notes) — which is why this
 * checks for `undefined` rather than falsiness.
 */
export async function listNotes(folder?: string): Promise<NoteInfo[]> {
  const query = folder === undefined ? '' : `?folder=${encodeURIComponent(folder)}`;
  const body = await request<{ notes: NoteInfo[] }>('GET', `/api/notes${query}`);
  return body.notes;
}

export function getNote(path: string): Promise<Note> {
  return request('GET', `/api/notes/${encodePath(path)}`);
}

export function createNote(input: {
  path?: string;
  /** The folder to file it in — which is to say its category. */
  dir?: string;
  name?: string;
  content?: string;
}): Promise<Note> {
  return request('POST', '/api/notes', {
    path: input.path ?? '',
    name: input.name ?? '',
    dir: input.dir ?? '',
    content: input.content ?? '',
  });
}

/**
 * Save a note's content. Pass baseMtimeMs (the mtime_ms the edit started
 * from) to get a 409 instead of silently overwriting an edit made elsewhere.
 */
export function saveNote(path: string, content: string, baseMtimeMs?: number): Promise<Note> {
  const body: { content: string; base_mtime_ms?: number } = { content };
  if (baseMtimeMs !== undefined) body.base_mtime_ms = baseMtimeMs;
  return request('PUT', `/api/notes/${encodePath(path)}`, body);
}

export function deleteNote(path: string): Promise<void> {
  return request('DELETE', `/api/notes/${encodePath(path)}`);
}

/** Rename/move a note; the server rewrites wikilinks in referring notes. */
export function renameNote(
  path: string,
  newPath: string,
): Promise<{ note: Note; updated_notes: number }> {
  return request('POST', '/api/rename', { path, new_path: newPath });
}

export async function search(q: string): Promise<SearchResult[]> {
  const body = await request<{ results: SearchResult[] }>(
    'GET',
    `/api/search?q=${encodeURIComponent(q)}`,
  );
  return body.results;
}

export function graph(): Promise<{ nodes: GraphNode[]; edges: GraphEdge[] }> {
  return request('GET', '/api/graph');
}

// ---------------------------------------------------------------------------
// Folders
//
// A folder IS a category — the same one thing, stored as the directory the file
// is in. So there is no "create a folder" call: one exists exactly as long as a
// note is in it, which is the rule categories always had, now kept by the
// filesystem instead of by us.
//
// Every write here moves files, so every write reports both how many notes
// moved and how many wikilinks were rewritten to follow them.
// ---------------------------------------------------------------------------

/** Result of a folder write: the tree as it now stands, plus what it touched. */
export interface FolderChange {
  folders: Folder[];
  moved_notes: number;
  updated_notes: number;
}

/** Every folder in the vault, sorted by path — walk it in order to draw a tree. */
export async function listFolders(): Promise<Folder[]> {
  const body = await request<{ folders: Folder[] }>('GET', '/api/folders');
  return body.folders;
}

/**
 * Rename a folder, moving it and everything under it. Renaming onto an existing
 * folder merges the two. Wikilinks that pointed into it are rewritten.
 */
export function renameFolder(from: string, to: string): Promise<FolderChange> {
  return request('POST', '/api/folders/rename', { from, to });
}

/**
 * Unfile a folder: its notes move up one level and it stops existing. No note
 * is ever deleted by this — "delete a category" only meant "stop filing under
 * it", and it means the same here.
 */
export function deleteFolder(path: string): Promise<FolderChange> {
  return request('POST', '/api/folders/delete', { path });
}

/** File one note under a folder, keeping its name. "" moves it to the root. */
export function moveNote(
  path: string,
  folder: string,
): Promise<{ note: NoteInfo; updated_notes: number }> {
  return request('POST', '/api/folders/move', { path, folder });
}

// ---------------------------------------------------------------------------
// Merge
// ---------------------------------------------------------------------------

export interface MergeResult {
  merged: string;
  /** Regions both sides rewrote; 0 means the merge can be saved as-is. */
  conflicts: number;
}

/**
 * Combine two versions of a note that both descend from `base`.
 *
 * The server owns the algorithm (the same one cloud sync uses to settle its
 * conflicts), but only the browser still holds the version the edit started
 * from — so the three texts travel there and the merged draft comes back.
 * Omit `base` when there isn't one; the merge then reconciles the shared
 * prefix and suffix and hands the middle over marked.
 */
export function mergeText(mine: string, theirs: string, base?: string): Promise<MergeResult> {
  const body: { mine: string; theirs: string; base?: string } = { mine, theirs };
  if (base !== undefined) body.base = base;
  return request('POST', '/api/merge', body);
}
