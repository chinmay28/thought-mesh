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
  last_file_name: string | null;
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

export interface CloudRunResult {
  file_name: string;
  bytes: number;
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

/** Zip the vault and upload it right now, outside the schedule. */
export function runCloudSync(): Promise<CloudRunResult> {
  return request('POST', '/sync/run', {});
}

/** One vault snapshot sitting in the connected folder — a restore candidate. */
export interface CloudSnapshot {
  id: string; // the provider's handle; what restore takes
  name: string;
  size: number;
  modified_ms: number;
}

/** List the `.vault.zip` snapshots in the connected folder, newest first. */
export async function listCloudSnapshots(): Promise<CloudSnapshot[]> {
  const body = await request<{ snapshots: CloudSnapshot[] }>('GET', '/sync/snapshots');
  return body.snapshots;
}

export interface CloudRestoreResult {
  snapshot: string;
  files: number;
  /** Local pre-restore backup of the vault as it was — the undo path. */
  backup_file: string;
}

/**
 * Replace the vault with a snapshot's contents. The server writes a local
 * backup of the current vault first, and validates/stages the archive before
 * touching anything, so a failed restore leaves the vault as it was.
 */
export function restoreCloudSnapshot(id: string): Promise<CloudRestoreResult> {
  return request('POST', '/sync/restore', { id });
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
