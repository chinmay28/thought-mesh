import { ApiError } from './client.ts';

/**
 * Typed client for the automatic cloud sync endpoints.
 *
 * These live apart from the note client on purpose: cloud sync has no domain
 * half in the browser — the schedule, the OAuth grant and the upload all
 * belong to the server. The shapes mirror the REST API exactly (snake_case,
 * 0|1 flags, explicit nulls); the contract is pinned server-side by
 * server/internal/api/cloud_test.go.
 */

/** A cloud destination this server build knows about. */
export interface CloudProviderInfo {
  id: string;
  name: string;
  /** 0 until an OAuth client has been registered for it. */
  configured: 0 | 1;
  /** The client id in effect — not a secret, it rides in the authorize URL. */
  client_id: string;
  has_secret: 0 | 1;
  /** 1 for providers that reject a PKCE-only (secret-less) client. */
  secret_required: 0 | 1;
  /** "settings" (entered here), "server" (a startup flag), or "". */
  source: 'settings' | 'server' | '';
  /** The provider's developer console, where the OAuth app is registered. */
  setup_url: string;
  /**
   * 1 when the provider will authorize with no redirect URI, showing the user
   * a code to copy back instead. It's the escape hatch for a server whose
   * origin can't be registered — or isn't https. Dropbox has it.
   */
  supports_code_paste: 0 | 1;
}

/** What one sync moved. A sync's outcome is a handful of counts rather than a
 * file name — the difference between mirroring a tree and dropping an archive
 * in a folder. */
export interface CloudRunSummary {
  uploaded: number;
  downloaded: number;
  deleted_local: number;
  deleted_remote: number;
  unchanged: number;
  conflicts: number;
  failed: number;
}

/** The server's cloud sync configuration, with every token redacted. */
export interface CloudSyncSettings {
  provider: string | null;
  account_label: string | null;
  connected: 0 | 1;
  folder_id: string | null;
  folder_path: string | null;
  frequency: CloudSyncFrequency;
  next_run_at: string | null;
  last_run_at: string | null;
  last_status: 'ok' | 'error' | null;
  last_error: string | null;
  last_result: CloudRunSummary | null;
}

/**
 * One path both sides changed since they last agreed, waiting on a decision.
 *
 * Nothing is transferred for a conflicted path — neither version is touched —
 * until it is resolved, so a contested note can sit here without either copy
 * being lost.
 */
export interface CloudConflict {
  path: string;
  local_hash: string;
  remote_hash: string;
  remote_rev: string;
  base_hash: string;
  local_size: number;
  remote_size: number;
  /** 1 when this side deleted the file while the other kept editing it. */
  local_missing: 0 | 1;
  remote_missing: 0 | 1;
  /** 1 when both versions are text, so a merge can be offered at all. */
  mergeable: 0 | 1;
  /** 1 when the version the two sides diverged from is still available, which
   * is what makes the merge a real three-way one. */
  has_base: 0 | 1;
  detected_at: string;
}

/** Both versions of a contested path, plus a merge already computed. */
export interface CloudConflictDetail extends CloudConflict {
  local: string;
  remote: string;
  base: string;
  merged: string;
  /** Regions the merge couldn't settle by itself; 0 means it's ready to save. */
  merge_conflicts: number;
}

/** How a conflict is settled. */
export type CloudResolution = 'keep_local' | 'keep_remote' | 'merge';

/** One local pre-sync copy of the vault — the undo path for a sync that
 * pulled down something unwelcome. */
export interface CloudBackup {
  name: string;
  size: number;
  modified_ms: number;
}

export type CloudSyncFrequency = 'off' | 'hourly' | 'daily' | 'weekly' | 'monthly';

/** How each frequency is labelled in the picker, in the order it's offered. */
export const CLOUD_FREQUENCIES: ReadonlyArray<{
  value: CloudSyncFrequency;
  label: string;
}> = [
  { value: 'off', label: 'Off' },
  { value: 'hourly', label: 'Every hour' },
  { value: 'daily', label: 'Every day' },
  { value: 'weekly', label: 'Every week' },
  { value: 'monthly', label: 'Every month' },
];

export interface CloudSyncState {
  settings: CloudSyncSettings;
  providers: CloudProviderInfo[];
  /** The paths waiting on a decision. They ride along with the settings
   * because an unresolved conflict is the one thing on the Sync page that
   * stops being in step until somebody acts. */
  conflicts: CloudConflict[];
  /**
   * The exact redirect URI to register with the provider. The server derives
   * it from the origin this request arrived on, so the setup form can show
   * something copy-pasteable instead of asking the user to assemble it.
   */
  redirect_uri: string;
  /**
   * 0 when this origin can't be a registered redirect URI at all (plain http
   * on something other than localhost — providers require https). The UI
   * then leads with the paste flow rather than a button that cannot work.
   */
  redirect_supported: 0 | 1;
}

/** One folder in the connected account. */
export interface CloudFolder {
  id: string;
  name: string;
  path: string;
}

export interface CloudSyncResult {
  uploaded: number;
  downloaded: number;
  deleted_local: number;
  deleted_remote: number;
  unchanged: number;
  conflicts: CloudConflict[];
  /** The pre-sync copy of the vault, written only when the run was about to
   * overwrite or delete something locally. "" otherwise. */
  backup_file: string;
  failed: number;
  error: string;
}

export interface CloudRunResponse {
  result: CloudSyncResult;
  settings: CloudSyncSettings;
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const init: RequestInit = { method };
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }
  const res = await fetch(`/api/cloud${path}`, init);
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

/**
 * Read the current configuration and the destinations on offer.
 *
 * A server built without cloud support doesn't register these routes at all,
 * so a 404 here means "this deployment doesn't do cloud sync" — the caller
 * gets `null` and hides the section rather than showing a broken one.
 */
export async function fetchCloudSync(): Promise<CloudSyncState | null> {
  try {
    return await request<CloudSyncState>('GET', '/sync');
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

/** Patch the schedule and/or the destination folder. */
export function updateCloudSync(patch: {
  frequency?: CloudSyncFrequency;
  folder_id?: string;
  folder_path?: string;
}): Promise<CloudSyncState> {
  return request('PATCH', '/sync', patch);
}

/** How the user will come back from the provider's consent screen. */
export type CloudConnectMode = 'redirect' | 'paste';

export interface CloudConnectStart {
  authorize_url: string;
  mode: CloudConnectMode;
  /**
   * Handle for this in-flight authorization. In paste mode the client holds it
   * and sends it back with the code; in redirect mode the callback carries it.
   */
  pending_id: string;
}

/**
 * Begin connecting an account. The server returns the provider's consent URL
 * rather than redirecting: this is a cross-origin hop, and a `fetch` that
 * followed the redirect would pull the consent page into an XHR instead of
 * putting it in front of the user.
 *
 * `mode: 'paste'` asks for the no-redirect flow — the provider shows a code
 * the user brings back by hand. That's the only route when the server's origin
 * can't be a registered redirect URI, which is the common case for a LAN or
 * plain-http deployment.
 */
export function startCloudConnect(
  provider: string,
  mode: CloudConnectMode = 'redirect',
): Promise<CloudConnectStart> {
  return request('POST', '/sync/connect', { provider, mode });
}

/** Finish a paste-mode authorization with the code the provider displayed. */
export function completeCloudConnect(pendingId: string, code: string): Promise<CloudSyncState> {
  return request('POST', '/sync/complete', { pending_id: pendingId, code });
}

export function disconnectCloudSync(): Promise<CloudSyncState> {
  return request('POST', '/sync/disconnect', {});
}

/** List the folders inside `folderId` — omit it for the account root. */
export async function listCloudFolders(folderId?: string): Promise<CloudFolder[]> {
  const query = folderId ? `?folder_id=${encodeURIComponent(folderId)}` : '';
  const body = await request<{ folders: CloudFolder[] }>('GET', `/sync/folders${query}`);
  return body.folders;
}

/**
 * Sync now, outside the schedule.
 *
 * "Sync" is bidirectional: local changes go up, remote ones come down,
 * deletions propagate both ways, and anything both sides changed comes back in
 * `result.conflicts` with neither version touched.
 *
 * `message` is an optional note from whoever pressed the button. It becomes the
 * message on the version this run records in the vault's history — six months
 * later it is the only thing that will tell one sync apart from the next.
 */
export function runCloudSync(message = ''): Promise<CloudRunResponse> {
  return request('POST', '/sync/run', { message });
}

/** The paths currently waiting on a decision. */
export async function listCloudConflicts(): Promise<CloudConflict[]> {
  const body = await request<{ conflicts: CloudConflict[] }>('GET', '/sync/conflicts');
  return body.conflicts;
}

/**
 * Both versions of one contested path, plus a merge of them.
 *
 * The remote side is fetched on demand rather than when the conflict was
 * detected: a run that finds twenty conflicts shouldn't download twenty files
 * nobody has opened yet.
 */
export function getCloudConflict(path: string): Promise<CloudConflictDetail> {
  const encoded = path.split('/').map(encodeURIComponent).join('/');
  return request('GET', `/sync/conflicts/${encoded}`);
}

/**
 * Settle one conflict. `content` is required for `merge` — the merged text the
 * user was shown and allowed to edit — and ignored otherwise.
 *
 * Every resolution leaves both sides holding the same bytes, merge included:
 * fixing only one side would bring the conflict straight back.
 */
export function resolveCloudConflict(
  path: string,
  resolution: CloudResolution,
  content?: string,
): Promise<CloudSyncState> {
  return request('POST', '/sync/resolve', { path, resolution, content: content ?? '' });
}

/** The local pre-sync copies of the vault, newest first. */
export async function listCloudBackups(): Promise<CloudBackup[]> {
  const body = await request<{ backups: CloudBackup[] }>('GET', '/sync/backups');
  return body.backups;
}

/**
 * Replace the vault with one of those backups — the undo button for a sync.
 * The server takes a fresh backup of the current vault first, so restoring the
 * wrong one is itself undoable.
 */
export function restoreCloudBackup(name: string): Promise<{ backup: string; files: number }> {
  return request('POST', '/sync/backups/restore', { name });
}

/**
 * Store the OAuth client for a provider — the client id (and secret, where
 * the provider needs one) from an app the user registered themselves.
 *
 * This is what makes the feature reachable from a phone. The client id has to
 * come from *somewhere*: OAuth has no anonymous mode, and a self-hosted server
 * at an address nobody can predict can't share one shipped registration.
 * Entering it here beats a startup flag, because a phone has no command line.
 */
export function setProviderCredentials(
  provider: string,
  credentials: { client_id: string; client_secret?: string },
): Promise<CloudSyncState> {
  return request('PUT', `/sync/providers/${encodeURIComponent(provider)}`, credentials);
}

/** Forget a stored OAuth client, falling back to the server's startup flag. */
export function clearProviderCredentials(provider: string): Promise<CloudSyncState> {
  return request('DELETE', `/sync/providers/${encodeURIComponent(provider)}`);
}
