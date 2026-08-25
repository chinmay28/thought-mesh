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
  /**
   * The note's categories, in the order it lists them. Always an array —
   * a note with none is the ordinary case, not a missing value.
   *
   * They live in the note's own YAML frontmatter on the server, which is why
   * changing them changes `content` and `mtime_ms` too.
   */
  categories: string[];
}

/** One category in the vault's vocabulary, with how many notes use it. */
export interface Category {
  name: string;
  count: number;
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

/** Every note, newest-agnostic (the server sorts by path). `category`
 * narrows the list to the notes carrying it, matched case-insensitively. */
export async function listNotes(category?: string): Promise<NoteInfo[]> {
  const query = category ? `?category=${encodeURIComponent(category)}` : '';
  const body = await request<{ notes: NoteInfo[] }>('GET', `/api/notes${query}`);
  return body.notes;
}

export function getNote(path: string): Promise<Note> {
  return request('GET', `/api/notes/${encodePath(path)}`);
}

export function createNote(input: {
  path?: string;
  name?: string;
  dir?: string;
  content?: string;
  categories?: string[];
}): Promise<Note> {
  return request('POST', '/api/notes', {
    path: input.path ?? '',
    name: input.name ?? '',
    dir: input.dir ?? '',
    content: input.content ?? '',
    categories: input.categories ?? [],
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
// Categories
//
// There is no "create a category" call, deliberately: a category exists as
// long as some note declares it in its frontmatter, so assigning one to a note
// is the only way to make one, and unassigning the last is the only way to lose
// one. The vault-wide rename and delete exist because a name means the same
// thing everywhere — leaving half the notes on the old spelling would silently
// split one category into two.
// ---------------------------------------------------------------------------

/** Every category in the vault, sorted by name, with note counts. */
export async function listCategories(): Promise<Category[]> {
  const body = await request<{ categories: Category[] }>('GET', '/api/categories');
  return body.categories;
}

/**
 * Replace one note's categories. Pass baseMtimeMs (the mtime_ms the screen was
 * showing) to get a 409 instead of quietly reverting an edit made elsewhere.
 */
export function setNoteCategories(
  path: string,
  categories: string[],
  baseMtimeMs?: number,
): Promise<Note> {
  const body: { path: string; categories: string[]; base_mtime_ms?: number } = {
    path,
    categories,
  };
  if (baseMtimeMs !== undefined) body.base_mtime_ms = baseMtimeMs;
  return request('POST', '/api/categories/assign', body);
}

/** Rename a category everywhere. Renaming onto an existing name merges them. */
export function renameCategory(
  from: string,
  to: string,
): Promise<{ categories: Category[]; updated_notes: number }> {
  return request('POST', '/api/categories/rename', { from, to });
}

/** Strip a category from every note carrying it. The notes are left alone. */
export function deleteCategory(
  name: string,
): Promise<{ categories: Category[]; updated_notes: number }> {
  return request('POST', '/api/categories/delete', { name });
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
