import { useCallback, useEffect, useState } from 'react';
import {
  CLOUD_FREQUENCIES,
  clearProviderCredentials,
  completeCloudConnect,
  disconnectCloudSync,
  fetchCloudSync,
  getCloudConflict,
  listCloudBackups,
  listCloudFolders,
  resolveCloudConflict,
  restoreCloudBackup,
  runCloudSync,
  setProviderCredentials,
  startCloudConnect,
  updateCloudSync,
  type CloudBackup,
  type CloudConflict,
  type CloudConflictDetail,
  type CloudConnectStart,
  type CloudFolder,
  type CloudProviderInfo,
  type CloudResolution,
  type CloudSyncFrequency,
  type CloudSyncResult,
  type CloudSyncState,
} from '../api/cloud.ts';
import {
  HISTORY_KIND_LABELS,
  checkpointHistory,
  fetchHistory,
  historyDetail,
  rollbackHistory,
  type HistoryCommit,
  type HistoryState,
} from '../api/history.ts';
import { formatDateTime } from '../lib/time.ts';

/** One step of the folder picker's trail, so "up" is just a pop. */
interface Crumb {
  id: string;
  name: string;
  path: string;
}

/**
 * Two-way sync of the vault with a cloud folder.
 *
 * Three things have to be true before anything moves: an account is connected,
 * a folder is chosen inside it, and the frequency isn't "off". The page is laid
 * out in that order, and each step only appears once the one before it is done
 * — the alternative is a screen of dead controls.
 *
 * The one thing that jumps the queue is a conflict. Everything else here is a
 * setting that can wait; an unresolved conflict is a note whose two versions
 * are both sitting there un-synced until somebody chooses, so it goes above the
 * controls whenever there is one.
 *
 * The server does the actual work; this is a settings surface over
 * `/api/cloud/sync`. On a server built without cloud support those routes
 * don't exist, and the page says so instead of rendering broken controls.
 */
export function SyncPage() {
  const [state, setState] = useState<CloudSyncState | null>(null);
  const [supported, setSupported] = useState(true);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [picking, setPicking] = useState(false);
  const [restoring, setRestoring] = useState(false);
  // The conflict whose two versions are open for comparison, if any.
  const [openConflict, setOpenConflict] = useState<string | null>(null);
  const [history, setHistory] = useState<HistoryState | null>(null);
  // What the user typed to go with a manual sync, if anything.
  const [note, setNote] = useState('');
  const [setupFor, setSetupFor] = useState<string | null>(null);
  // A paste-mode authorization in progress: the user is off at the provider,
  // and we're waiting for the code they'll bring back. The provider id rides
  // along so the panel renders under the row it belongs to.
  const [pasting, setPasting] = useState<{
    provider: string;
    start: CloudConnectStart;
  } | null>(null);

  const load = useCallback(async () => {
    try {
      const next = await fetchCloudSync();
      if (next === null) {
        setSupported(false);
        return;
      }
      setState(next);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const loadHistory = useCallback(async () => {
    try {
      setHistory(await fetchHistory(30));
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }, []);

  useEffect(() => {
    void loadHistory();
  }, [loadHistory]);

  // The OAuth callback lands the browser back here with the outcome in the
  // query string (the provider redirects a whole navigation, so there's no
  // promise to await). Report it, then scrub the parameters so a refresh
  // doesn't replay a stale "connected".
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const outcome = params.get('cloud');
    if (!outcome) return;
    if (outcome === 'connected') {
      setMessage('Cloud account connected. Choose a folder to sync into.');
    } else {
      setError(params.get('cloud_error') ?? 'Connecting the cloud account failed.');
    }
    params.delete('cloud');
    params.delete('cloud_error');
    const query = params.toString();
    window.history.replaceState(
      null,
      '',
      window.location.pathname + (query ? `?${query}` : ''),
    );
  }, []);

  async function run(key: string, action: () => Promise<void>) {
    setBusy(key);
    setError(null);
    setMessage(null);
    try {
      await action();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      // A failed run still writes its outcome to the settings, so pull the
      // fresh state in rather than leaving stale status on screen.
      void load();
    } finally {
      setBusy(null);
    }
  }

  function connect(provider: string) {
    return run(`connect:${provider}`, async () => {
      const start = await startCloudConnect(provider, 'redirect');
      // A full navigation, not a popup: this has to survive an OAuth screen
      // that may bounce through a login and a device prompt, and popups are
      // blocked in an installed PWA more often than not.
      window.location.assign(start.authorize_url);
    });
  }

  /**
   * Start the paste flow. Nothing navigates: the user opens the provider in a
   * new tab from the link we render, authorizes, and comes back with a code.
   * Keeping this page alive is the point — there's no redirect to return to.
   */
  function connectByPaste(provider: string) {
    return run(`paste:${provider}`, async () => {
      setPasting({ provider, start: await startCloudConnect(provider, 'paste') });
    });
  }

  function submitPastedCode(code: string) {
    if (!pasting) return;
    return run('paste-code', async () => {
      setState(await completeCloudConnect(pasting.start.pending_id, code));
      setPasting(null);
      setMessage('Cloud account connected. Choose a folder to sync into.');
    });
  }

  function disconnect() {
    if (
      !window.confirm(
        'Disconnect this cloud account? Scheduled sync stops and the stored ' +
          'access is deleted. The files already in the cloud folder are left ' +
          'exactly as they are, and so is your vault.',
      )
    ) {
      return;
    }
    return run('disconnect', async () => {
      setState(await disconnectCloudSync());
      setPicking(false);
      setMessage('Cloud account disconnected.');
    });
  }

  function setFrequency(frequency: CloudSyncFrequency) {
    return run('frequency', async () => {
      setState(await updateCloudSync({ frequency }));
    });
  }

  function chooseFolder(folder: Crumb) {
    return run('folder', async () => {
      setState(await updateCloudSync({ folder_id: folder.id, folder_path: folder.path }));
      setPicking(false);
      setMessage(
        `Your vault and ${folder.path} will be kept in step. The first sync ` +
          `merges what's in both: nothing on either side is deleted for being new.`,
      );
    });
  }

  function syncNow() {
    return run('run', async () => {
      const { result, settings } = await runCloudSync(note);
      setState((prev) =>
        prev ? { ...prev, settings, conflicts: result.conflicts } : prev,
      );
      setNote('');
      setMessage(describeRun(result));
      void loadHistory();
    });
  }

  function checkpoint(message: string) {
    return run('checkpoint', async () => {
      setHistory(await checkpointHistory(message));
      setMessage('Marked this moment in the vault’s history.');
    });
  }

  function rollback(commit: HistoryCommit) {
    // The one action here that rewrites every note at once, so the confirm
    // spells out both what happens and why it is still undoable.
    if (
      !window.confirm(
        `Put the whole vault back to ${formatDateTime(new Date(commit.at_ms).toISOString())}?\n\n` +
          `Notes written since then are removed and notes changed since then go ` +
          `back. Nothing is erased: this is recorded as a new version, so it can ` +
          `be undone the same way.`,
      )
    ) {
      return;
    }
    return run(`rollback:${commit.ref}`, async () => {
      setHistory(await rollbackHistory(commit.ref));
      await load();
      setMessage(
        `The vault is back to how it was on ` +
          `${formatDateTime(new Date(commit.at_ms).toISOString())}.`,
      );
    });
  }

  function resolve(path: string, resolution: CloudResolution, content?: string) {
    return run(`resolve:${path}`, async () => {
      setState(await resolveCloudConflict(path, resolution, content));
      setOpenConflict(null);
      setMessage(`${path} settled — ${RESOLUTION_LABELS[resolution]}.`);
    });
  }

  function restoreBackup(backup: CloudBackup) {
    // The one destructive action on this page, so the confirm spells out
    // both what happens and the undo path.
    if (
      !window.confirm(
        `Restore "${backup.name}"?\n\nThis replaces every note in the vault with ` +
          `the backup's contents. A fresh backup of the vault as it is right now ` +
          `is saved on the server first, so this can itself be undone.`,
      )
    ) {
      return;
    }
    return run(`restore:${backup.name}`, async () => {
      const result = await restoreCloudBackup(backup.name);
      setRestoring(false);
      await load();
      setMessage(
        `Restored ${result.files} file${result.files === 1 ? '' : 's'} from ` +
          `${result.backup}. The next sync will work out what the cloud folder ` +
          `now needs.`,
      );
    });
  }

  if (!supported) {
    return (
      <div className="page page--narrow">
        <h1 className="page-title">Sync</h1>
        <p className="muted">This server was built without cloud sync.</p>
      </div>
    );
  }
  if (loading || !state) {
    return (
      <div className="page page--narrow">
        <h1 className="page-title">Sync</h1>
        {error ? <div className="banner banner--warn">{error}</div> : <p className="muted">Loading…</p>}
      </div>
    );
  }

  const { settings, providers } = state;
  const connected = settings.connected === 1;
  const redirectSupported = state.redirect_supported === 1;

  return (
    <div className="page page--narrow">
      <h1 className="page-title">Sync</h1>
      <p className="muted">
        Keep your vault and a folder in your Dropbox in step. The folder holds
        the same notes in the same folders under the same names — plain markdown
        files, readable with or without Thought Mesh — so a note edited on
        another device comes back down, and one deleted anywhere goes away
        everywhere. When the same note changed in both places, nothing is
        overwritten: you’re asked which version wins.
      </p>

      {!connected ? (
        <div className="cloud__connect">
          {/* One row per provider: connect it, or set it up first. A provider
              that isn't set up gets a working button that opens the form —
              never a dead control with the reason hidden in a tooltip nobody
              on a touch screen will ever see. */}
          <ul className="cloud__providers">
            {providers.map((provider) => {
              // Connectable unless this origin can't host a redirect and the
              // provider has no paste flow to fall back on.
              const connectable =
                redirectSupported || provider.supports_code_paste === 1;
              return (
                <li key={provider.id} className="cloud__provider">
                  <div className="cloud__provider-row">
                    <div className="cloud__provider-meta">
                      <span className="cloud__provider-name">{provider.name}</span>
                      <span className="muted">
                        {provider.configured === 1
                          ? provider.source === 'server'
                            ? 'Set up on the server'
                            : 'Ready to connect'
                          : 'Needs a one-time setup'}
                      </span>
                    </div>
                    {provider.configured === 1 ? (
                      // Which flow leads depends on whether a redirect could
                      // come back here at all. On a plain-http LAN address it
                      // can't — providers require https — so the paste flow is
                      // the only one that works, and it gets the primary button.
                      redirectSupported || provider.supports_code_paste === 0 ? (
                        <button
                          type="button"
                          className="btn btn--primary"
                          disabled={busy !== null || !connectable}
                          onClick={() => connect(provider.id)}
                        >
                          {busy === `connect:${provider.id}` ? 'Opening…' : 'Connect'}
                        </button>
                      ) : (
                        <button
                          type="button"
                          className="btn btn--primary"
                          disabled={busy !== null}
                          onClick={() => connectByPaste(provider.id)}
                        >
                          {busy === `paste:${provider.id}` ? 'Starting…' : 'Connect'}
                        </button>
                      )
                    ) : (
                      <button
                        type="button"
                        className="btn"
                        disabled={busy !== null}
                        onClick={() =>
                          setSetupFor((id) => (id === provider.id ? null : provider.id))
                        }
                      >
                        {setupFor === provider.id ? 'Cancel' : 'Set up'}
                      </button>
                    )}
                  </div>

                  {/* When a redirect can't reach this origin, say so once —
                      otherwise someone registers a redirect URI that can never
                      fire, or bounces off the provider's error page. */}
                  {provider.configured === 1 && !redirectSupported && (
                    <p className="muted cloud__provider-note">
                      This server isn’t on https, so {provider.name} can’t
                      redirect back to it. You’ll paste a code instead —
                      nothing to register.
                    </p>
                  )}

                  {provider.configured === 1 && (
                    <div className="cloud__provider-links">
                      <button
                        type="button"
                        className="btn btn--ghost btn--small"
                        disabled={busy !== null}
                        onClick={() =>
                          setSetupFor((id) => (id === provider.id ? null : provider.id))
                        }
                      >
                        {setupFor === provider.id ? 'Hide setup' : 'Edit setup'}
                      </button>
                      {/* The other flow stays reachable either way: a registered
                          redirect URI can still be wrong, and pasting a code
                          always works. */}
                      {provider.supports_code_paste === 1 && redirectSupported && (
                        <button
                          type="button"
                          className="btn btn--ghost btn--small"
                          disabled={busy !== null}
                          onClick={() => connectByPaste(provider.id)}
                        >
                          {busy === `paste:${provider.id}`
                            ? 'Starting…'
                            : 'Paste a code instead'}
                        </button>
                      )}
                    </div>
                  )}

                  {pasting?.provider === provider.id && (
                    <PasteCodePanel
                      providerName={provider.name}
                      authorizeUrl={pasting.start.authorize_url}
                      busy={busy !== null}
                      onSubmit={(code) => void submitPastedCode(code)}
                      onCancel={() => setPasting(null)}
                    />
                  )}

                  {setupFor === provider.id && (
                    <ProviderSetup
                      provider={provider}
                      redirectUri={state.redirect_uri}
                      busy={busy !== null}
                      onSave={(clientId, clientSecret) =>
                        void run(`credentials:${provider.id}`, async () => {
                          setState(
                            await setProviderCredentials(provider.id, {
                              client_id: clientId,
                              ...(clientSecret ? { client_secret: clientSecret } : {}),
                            }),
                          );
                          setSetupFor(null);
                          setMessage(`${provider.name} is set up. You can connect it now.`);
                        })
                      }
                      onClear={() =>
                        void run(`credentials:${provider.id}`, async () => {
                          setState(await clearProviderCredentials(provider.id));
                          setSetupFor(null);
                          setMessage(`${provider.name} setup cleared.`);
                        })
                      }
                    />
                  )}
                </li>
              );
            })}
          </ul>
        </div>
      ) : (
        <div className="cloud">
          {/* Above everything else, because it's the only thing here that is
              actively not in step until someone acts on it. */}
          {state.conflicts.length > 0 && (
            <ConflictSection
              conflicts={state.conflicts}
              openPath={openConflict}
              busy={busy}
              onOpen={setOpenConflict}
              onResolve={(path, resolution, content) => void resolve(path, resolution, content)}
              onError={setError}
            />
          )}

          <div className="cloud__account">
            <div>
              <span className="cloud__account-name">
                {settings.account_label ?? 'Connected account'}
              </span>
              <span className="muted">{providerName(providers, settings.provider)}</span>
            </div>
            <button
              type="button"
              className="btn"
              disabled={busy !== null}
              onClick={() => void disconnect()}
            >
              {busy === 'disconnect' ? 'Disconnecting…' : 'Disconnect'}
            </button>
          </div>

          <div className="cloud__row">
            <span className="cloud__label">Folder</span>
            <span className="cloud__value">
              {settings.folder_path ?? <span className="muted">No folder chosen yet</span>}
            </span>
            <button
              type="button"
              className="btn"
              disabled={busy !== null}
              onClick={() => setPicking((open) => !open)}
            >
              {picking ? 'Cancel' : settings.folder_id ? 'Change' : 'Choose folder'}
            </button>
          </div>

          {picking && (
            <FolderPicker
              disabled={busy !== null}
              onChoose={(crumb) => void chooseFolder(crumb)}
              onError={setError}
            />
          )}

          <div className="cloud__row">
            <label className="cloud__label" htmlFor="cloud-frequency">
              Frequency
            </label>
            <select
              id="cloud-frequency"
              className="cloud__value cloud__select"
              value={settings.frequency}
              disabled={busy !== null || settings.folder_id === null}
              onChange={(e) => void setFrequency(e.target.value as CloudSyncFrequency)}
            >
              {CLOUD_FREQUENCIES.map((option) => (
                <option key={option.value} value={option.value}>
                  {option.label}
                </option>
              ))}
            </select>
            <button
              type="button"
              className="btn btn--primary"
              disabled={busy !== null || settings.folder_id === null}
              onClick={() => void syncNow()}
            >
              {busy === 'run' ? 'Syncing…' : 'Sync now'}
            </button>
          </div>

          {/* An optional note to go with a manual sync. It becomes the message
              on the version this run records — six months from now it is the
              only thing that will tell one of these apart from the next. */}
          {settings.folder_id !== null && history?.available === 1 && (
            <label className="field cloud__note">
              <span className="field__label">
                Note for this sync <span className="muted">(optional)</span>
              </span>
              <input
                className="field__input"
                value={note}
                onChange={(e) => setNote(e.target.value)}
                placeholder="e.g. before the trip"
                maxLength={200}
                disabled={busy !== null}
              />
            </label>
          )}

          <CloudStatus settings={settings} />

          {/* The undo path, below the sync controls: rarer, heavier, and only
              interesting after a sync brought down something unwelcome. */}
          <div className="cloud__restore">
            <button
              type="button"
              className="btn"
              disabled={busy !== null}
              onClick={() => setRestoring((open) => !open)}
            >
              {restoring ? 'Hide backups' : 'Undo a sync…'}
            </button>
            {restoring && (
              <BackupPanel
                busy={busy}
                onRestore={(backup) => void restoreBackup(backup)}
                onError={setError}
              />
            )}
          </div>
        </div>
      )}

      {/* Version history stands on its own: it is the vault's, not the cloud
          account's, and it works whether or not Dropbox is ever set up. */}
      <VaultHistory
        history={history}
        busy={busy}
        onCheckpoint={(text) => void checkpoint(text)}
        onRollback={(commit) => void rollback(commit)}
      />

      {message && <p className="cloud__ok">{message}</p>}
      {error && <p className="cloud__error">{error}</p>}
    </div>
  );
}

/**
 * The vault's own history: every version, and the way back to one.
 *
 * It sits below cloud sync but does not belong to it — the server keeps this
 * whether or not an account is ever connected, because the thing worth being
 * able to undo is usually your own writing rather than a sync.
 */
function VaultHistory({
  history,
  busy,
  onCheckpoint,
  onRollback,
}: {
  history: HistoryState | null;
  busy: string | null;
  onCheckpoint: (message: string) => void;
  onRollback: (commit: HistoryCommit) => void;
}) {
  const [open, setOpen] = useState(false);
  const [draft, setDraft] = useState('');

  if (history === null) {
    return null;
  }
  if (history.available === 0) {
    return (
      <section className="cloud__history" aria-label="Version history">
        <h2 className="section-title">Version history</h2>
        <p className="muted">
          This server keeps none. It needs <code>git</code> installed — the vault
          is then an ordinary git repository, every version of every note is kept
          in it, and any of them can be put back.
        </p>
      </section>
    );
  }

  return (
    <section className="cloud__history" aria-label="Version history">
      <h2 className="section-title">Version history</h2>
      <p className="muted">
        Your vault is a git repository, so every version of every note is kept —
        recorded a couple of minutes after you stop writing, and around every
        sync. Nothing here erases anything: rolling back is itself a new version.
      </p>

      <form
        className="cloud__checkpoint"
        onSubmit={(e) => {
          e.preventDefault();
          onCheckpoint(draft);
          setDraft('');
        }}
      >
        <input
          className="field__input"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          placeholder="Mark this moment — e.g. before the big rewrite"
          aria-label="Checkpoint message"
          maxLength={200}
          disabled={busy !== null}
        />
        <button type="submit" className="btn" disabled={busy !== null}>
          {busy === 'checkpoint' ? 'Saving…' : 'Save a checkpoint'}
        </button>
      </form>

      {history.commits.length === 0 ? (
        <p className="muted">No versions recorded yet.</p>
      ) : (
        <>
          <ul className="history__list">
            {(open ? history.commits : history.commits.slice(0, 5)).map((commit, index) => (
              <li key={commit.ref} className="history__entry">
                <div className="history__row">
                  <span className="history__open history__open--static">
                    <span className="history__when">
                      {formatDateTime(new Date(commit.at_ms).toISOString())}
                    </span>
                    <span className="history__kind">
                      {index === 0 && !open ? 'Now' : HISTORY_KIND_LABELS[commit.kind]}
                    </span>
                    {historyDetail(commit) && (
                      <span className="history__subject">{historyDetail(commit)}</span>
                    )}
                    {commit.body && <span className="history__note">{commit.body}</span>}
                  </span>
                  {/* Rolling back to the newest version would do nothing. */}
                  {index > 0 && (
                    <button
                      type="button"
                      className="btn btn--small"
                      disabled={busy !== null}
                      onClick={() => onRollback(commit)}
                    >
                      {busy === `rollback:${commit.ref}` ? 'Restoring…' : 'Roll back'}
                    </button>
                  )}
                </div>
              </li>
            ))}
          </ul>
          {history.commits.length > 5 && (
            <button
              type="button"
              className="btn btn--ghost btn--small"
              onClick={() => setOpen((shown) => !shown)}
            >
              {open ? 'Show fewer' : `Show all ${history.commits.length}`}
            </button>
          )}
        </>
      )}
    </section>
  );
}

/**
 * The paste-a-code half of connecting an account.
 *
 * No redirect is involved, which is the entire point: the provider is opened
 * in a new tab, shows a code, and the user brings it back here. This page has
 * to stay alive to hold the pending authorization, so the link opens in a new
 * tab rather than navigating — losing this tab would lose the handle.
 */
function PasteCodePanel({
  providerName,
  authorizeUrl,
  busy,
  onSubmit,
  onCancel,
}: {
  providerName: string;
  authorizeUrl: string;
  busy: boolean;
  onSubmit: (code: string) => void;
  onCancel: () => void;
}) {
  const [code, setCode] = useState('');
  return (
    <form
      className="cloud__panel"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit(code);
      }}
    >
      <ol className="cloud__setup-steps">
        <li>
          <a
            href={authorizeUrl}
            target="_blank"
            rel="noreferrer noopener"
            className="btn btn--primary"
          >
            Open {providerName} and allow access
          </a>
        </li>
        <li>Copy the code {providerName} shows you, and paste it here.</li>
      </ol>

      <label className="field">
        <span className="field__label">Authorization code</span>
        <input
          className="field__input"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          placeholder="paste the code"
        />
      </label>

      <div className="form__actions">
        {/* Distinct from the row's "Connect": two buttons with the same
            name in one view is a maze for anyone navigating by label. */}
        <button
          type="submit"
          className="btn btn--primary"
          disabled={busy || code.trim() === ''}
        >
          {busy ? 'Connecting…' : 'Connect with this code'}
        </button>
        <button type="button" className="btn" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}

/**
 * One-time OAuth app setup for a provider.
 *
 * The client id has to come from somewhere: OAuth has no anonymous mode, and
 * a self-hosted server — reachable at an address nobody can predict — can't
 * share one shipped registration the way a store app can, because providers
 * require every redirect URI to be registered in advance. So the user
 * registers their own app once. What this form removes is the part that was
 * genuinely hostile: needing a command line to hand the result to the server.
 *
 * The redirect URI is shown first and copyable, because it's the step people
 * get wrong — it has to match character for character.
 */
function ProviderSetup({
  provider,
  redirectUri,
  busy,
  onSave,
  onClear,
}: {
  provider: CloudProviderInfo;
  redirectUri: string;
  busy: boolean;
  onSave: (clientId: string, clientSecret: string) => void;
  onClear: () => void;
}) {
  const [clientId, setClientId] = useState(provider.client_id);
  const [clientSecret, setClientSecret] = useState('');
  const [copied, setCopied] = useState(false);

  async function copyRedirect() {
    try {
      await navigator.clipboard.writeText(redirectUri);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 2000);
    } catch {
      // Clipboard access can be denied or absent (older iOS, insecure
      // origin). The URI is selectable text right there, so this is a
      // convenience failing, not the flow failing.
    }
  }

  return (
    <form
      className="cloud__panel"
      onSubmit={(e) => {
        e.preventDefault();
        onSave(clientId, clientSecret);
      }}
    >
      <ol className="cloud__setup-steps">
        <li>
          Open{' '}
          <a href={provider.setup_url} target="_blank" rel="noreferrer noopener">
            {provider.name}’s developer console
          </a>{' '}
          and create an app.
        </li>
        <li>
          Register this exact redirect URI:
          <span className="cloud__redirect">
            <code>{redirectUri}</code>
            <button
              type="button"
              className="btn btn--ghost btn--small"
              onClick={() => void copyRedirect()}
            >
              {copied ? 'Copied' : 'Copy'}
            </button>
          </span>
        </li>
        <li>Paste the app’s credentials below.</li>
      </ol>

      <label className="field">
        <span className="field__label">Client id</span>
        <input
          className="field__input"
          value={clientId}
          onChange={(e) => setClientId(e.target.value)}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          placeholder="from the provider's console"
        />
      </label>

      <label className="field">
        <span className="field__label">
          Client secret {provider.secret_required === 1 ? '(required)' : '(optional)'}
        </span>
        <input
          className="field__input"
          type="password"
          value={clientSecret}
          onChange={(e) => setClientSecret(e.target.value)}
          autoComplete="off"
          spellCheck={false}
          placeholder={
            provider.has_secret === 1
              ? 'stored — leave blank to keep it'
              : provider.secret_required === 1
                ? "from the provider's console"
                : 'not needed for a PKCE-only app'
          }
        />
      </label>

      <div className="form__actions">
        <button type="submit" className="btn btn--primary" disabled={busy}>
          Save setup
        </button>
        {provider.source === 'settings' && (
          <button type="button" className="btn" disabled={busy} onClick={onClear}>
            Clear
          </button>
        )}
      </div>
    </form>
  );
}

/** The provider's display name, falling back to its raw id. */
function providerName(providers: CloudProviderInfo[], id: string | null): string {
  if (!id) return '';
  return providers.find((p) => p.id === id)?.name ?? id;
}

/** When the last sync happened, how it went, and when the next one is due. */
function CloudStatus({ settings }: { settings: CloudSyncState['settings'] }) {
  const failed = settings.last_status === 'error';
  const summary = settings.last_result;
  return (
    <div className="cloud__status" role="status">
      {settings.last_run_at ? (
        <p className={failed ? 'cloud__error' : 'muted'}>
          {failed
            ? `Last sync failed ${formatDateTime(settings.last_run_at)}: ${
                settings.last_error ?? 'unknown error'
              }`
            : `Last sync ${formatDateTime(settings.last_run_at)}${
                summary ? ` — ${describeSummary(summary)}` : ''
              }`}
        </p>
      ) : (
        <p className="muted">No sync has run yet.</p>
      )}
      {settings.frequency !== 'off' && settings.next_run_at && (
        <p className="muted">Next sync {formatDateTime(settings.next_run_at)}.</p>
      )}
    </div>
  );
}

/**
 * What a run did, in a sentence.
 *
 * "Everything was already in step" is the healthy outcome on a schedule and
 * deserves saying plainly — a bare "0 uploaded, 0 downloaded" reads like a
 * failure, which is exactly what it isn't.
 */
function describeSummary(summary: {
  uploaded: number;
  downloaded: number;
  deleted_local: number;
  deleted_remote: number;
  conflicts: number;
  failed: number;
}): string {
  const parts: string[] = [];
  if (summary.uploaded > 0) parts.push(`${summary.uploaded} up`);
  if (summary.downloaded > 0) parts.push(`${summary.downloaded} down`);
  const deleted = summary.deleted_local + summary.deleted_remote;
  if (deleted > 0) parts.push(`${deleted} deleted`);
  if (summary.failed > 0) parts.push(`${summary.failed} failed`);
  if (summary.conflicts > 0) {
    parts.push(`${summary.conflicts} needing a decision`);
  }
  return parts.length === 0 ? 'everything was already in step' : parts.join(', ');
}

function describeRun(result: CloudSyncResult): string {
  const summary = describeSummary({ ...result, conflicts: result.conflicts.length });
  const backup =
    result.backup_file === ''
      ? ''
      : ` A copy of the vault as it was is saved on the server as ${result.backup_file}.`;
  return `Synced — ${summary}.${backup}`;
}

/** What each resolution did, for the confirmation line. */
const RESOLUTION_LABELS: Record<CloudResolution, string> = {
  keep_local: 'this server’s version now wins everywhere',
  keep_remote: 'the cloud’s version now wins everywhere',
  merge: 'the merged version is now on both sides',
};

/** A file size the way people read them. */
function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${Math.round(bytes / 1024)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/**
 * The local pre-sync copies of the vault, newest first.
 *
 * The server writes one whenever a sync is about to overwrite or delete
 * something in the vault, which makes this the undo button for "the cloud sent
 * down something I didn't want". They live beside the settings file on the
 * server, never inside the vault — a backup swept into the next sync would
 * upload the vault into itself.
 */
function BackupPanel({
  busy,
  onRestore,
  onError,
}: {
  busy: string | null;
  onRestore: (backup: CloudBackup) => void;
  onError: (message: string) => void;
}) {
  const [backups, setBackups] = useState<CloudBackup[] | null>(null);

  useEffect(() => {
    let cancelled = false;
    listCloudBackups().then(
      (next) => {
        if (!cancelled) setBackups(next);
      },
      (err: unknown) => {
        if (cancelled) return;
        onError(err instanceof Error ? err.message : String(err));
        setBackups([]);
      },
    );
    return () => {
      cancelled = true;
    };
    // One fetch per open — the panel unmounts when hidden.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  if (backups === null) {
    return (
      <div className="cloud__picker">
        <p className="muted">Looking for backups…</p>
      </div>
    );
  }
  if (backups.length === 0) {
    return (
      <div className="cloud__picker">
        <p className="muted">
          No backups yet. The server writes one automatically whenever a sync is
          about to replace or delete a note in your vault.
        </p>
      </div>
    );
  }
  return (
    <div className="cloud__picker">
      <ul className="cloud__snapshots">
        {backups.map((backup) => (
          <li key={backup.name} className="cloud__snapshot">
            <div className="cloud__snapshot-meta">
              <span className="cloud__snapshot-name">{backup.name}</span>
              <span className="muted">
                {formatSize(backup.size)}
                {backup.modified_ms > 0 && (
                  <> · {formatDateTime(new Date(backup.modified_ms).toISOString())}</>
                )}
              </span>
            </div>
            <button
              type="button"
              className="btn btn--small"
              disabled={busy !== null}
              onClick={() => onRestore(backup)}
            >
              {busy === `restore:${backup.name}` ? 'Restoring…' : 'Restore'}
            </button>
          </li>
        ))}
      </ul>
      <p className="muted cloud__restore-note">
        Restoring replaces the vault with the backup; a fresh backup of the
        current vault is taken first. Each one is a plain zip, so unzipping it by
        hand works too.
      </p>
    </div>
  );
}

/**
 * The notes both sides changed, waiting on a decision.
 *
 * Listed rather than modal: a conflict is not urgent enough to block the page,
 * and several at once is ordinary after a week away from one device. Opening
 * one loads both versions — that costs a download, so it happens on demand.
 */
function ConflictSection({
  conflicts,
  openPath,
  busy,
  onOpen,
  onResolve,
  onError,
}: {
  conflicts: CloudConflict[];
  openPath: string | null;
  busy: string | null;
  onOpen: (path: string | null) => void;
  onResolve: (path: string, resolution: CloudResolution, content?: string) => void;
  onError: (message: string) => void;
}) {
  return (
    <section className="cloud__conflicts" aria-label="Sync conflicts">
      <h2 className="section-title">
        {conflicts.length} {conflicts.length === 1 ? 'note needs' : 'notes need'} a decision
      </h2>
      <p className="muted">
        These changed here <em>and</em> in the cloud since they last agreed.
        Nothing has been overwritten — both versions are exactly where they were,
        and syncing skips them until you choose.
      </p>
      <ul className="cloud__conflict-list">
        {conflicts.map((conflict) => (
          <li key={conflict.path} className="cloud__conflict">
            <div className="cloud__conflict-row">
              <div className="cloud__conflict-meta">
                <span className="cloud__conflict-path">{conflict.path}</span>
                <span className="muted">{describeConflict(conflict)}</span>
              </div>
              <button
                type="button"
                className="btn btn--small"
                disabled={busy !== null}
                onClick={() => onOpen(openPath === conflict.path ? null : conflict.path)}
              >
                {openPath === conflict.path ? 'Close' : 'Compare'}
              </button>
            </div>
            {openPath === conflict.path && (
              <ConflictResolver
                conflict={conflict}
                busy={busy}
                onResolve={onResolve}
                onError={onError}
              />
            )}
          </li>
        ))}
      </ul>
    </section>
  );
}

/** What kind of collision this is, in the fewest words that stay honest. */
function describeConflict(conflict: CloudConflict): string {
  if (conflict.local_missing === 1) return 'deleted here, edited in the cloud';
  if (conflict.remote_missing === 1) return 'edited here, deleted in the cloud';
  if (conflict.mergeable === 0) return 'changed on both sides — not text, so it can’t be merged';
  return 'changed on both sides';
}

/**
 * One conflict, open: both versions side by side and the three ways out.
 *
 * The merge is computed by the server the moment this opens, and lands in an
 * editable box rather than being applied. Anything the merge could settle on
 * its own already is; what's left is marked, and the person who wrote both
 * halves is the only one who can say which stays.
 */
function ConflictResolver({
  conflict,
  busy,
  onResolve,
  onError,
}: {
  conflict: CloudConflict;
  busy: string | null;
  onResolve: (path: string, resolution: CloudResolution, content?: string) => void;
  onError: (message: string) => void;
}) {
  const [detail, setDetail] = useState<CloudConflictDetail | null>(null);
  const [merged, setMerged] = useState('');
  const [showMerge, setShowMerge] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getCloudConflict(conflict.path).then(
      (next) => {
        if (cancelled) return;
        setDetail(next);
        setMerged(next.merged);
      },
      (err: unknown) => {
        if (cancelled) return;
        onError(err instanceof Error ? err.message : String(err));
      },
    );
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [conflict.path]);

  const working = busy === `resolve:${conflict.path}`;
  const canMerge = conflict.mergeable === 1 && detail !== null && detail.mergeable === 1;

  return (
    <div className="cloud__panel cloud__resolver">
      {detail === null ? (
        <p className="muted">Loading both versions…</p>
      ) : (
        <>
          {canMerge && (
            <div className="cloud__versions">
              <ConflictVersion title="Here (this server)" text={detail.local} />
              <ConflictVersion title="In the cloud" text={detail.remote} />
            </div>
          )}

          {canMerge && showMerge && (
            <label className="field">
              <span className="field__label">
                Merged{' '}
                {detail.merge_conflicts > 0 ? (
                  <span className="muted">
                    — {detail.merge_conflicts}{' '}
                    {detail.merge_conflicts === 1 ? 'region' : 'regions'} both sides rewrote,
                    marked below. Delete the half you don’t want.
                  </span>
                ) : (
                  <span className="muted">— nothing collided; this is ready to save.</span>
                )}
              </span>
              <textarea
                className="field__input cloud__merge-text"
                value={merged}
                onChange={(e) => setMerged(e.target.value)}
                rows={12}
                spellCheck={false}
              />
              {detail.has_base === 0 && (
                <span className="muted">
                  The version these two grew apart from isn’t available, so the
                  merge could only keep what they still have in common.
                </span>
              )}
            </label>
          )}

          <div className="form__actions cloud__resolutions">
            <button
              type="button"
              className="btn"
              disabled={busy !== null}
              onClick={() => onResolve(conflict.path, 'keep_local')}
            >
              {conflict.local_missing === 1 ? 'Keep it deleted' : 'Keep mine'}
            </button>
            <button
              type="button"
              className="btn"
              disabled={busy !== null}
              onClick={() => onResolve(conflict.path, 'keep_remote')}
            >
              {conflict.remote_missing === 1 ? 'Delete it here too' : 'Use the cloud’s'}
            </button>
            {canMerge &&
              (showMerge ? (
                <button
                  type="button"
                  className="btn btn--primary"
                  disabled={busy !== null}
                  onClick={() => onResolve(conflict.path, 'merge', merged)}
                >
                  {working ? 'Saving…' : 'Save merged version'}
                </button>
              ) : (
                <button
                  type="button"
                  className="btn btn--primary"
                  disabled={busy !== null}
                  onClick={() => setShowMerge(true)}
                >
                  Merge them
                </button>
              ))}
          </div>
          <p className="muted">
            Whichever you pick, both sides end up holding it — the note stops
            being a conflict rather than becoming one again next run.
          </p>
        </>
      )}
    </div>
  );
}

/** One version of a contested note, shown read-only for comparison. */
function ConflictVersion({ title, text }: { title: string; text: string }) {
  return (
    <div className="cloud__version">
      <h3 className="cloud__version-title">{title}</h3>
      <pre className="cloud__version-text">{text === '' ? '(empty)' : text}</pre>
    </div>
  );
}

/**
 * Browse the connected account and pick a destination.
 *
 * The trail is kept here rather than asked of the server: the picker only
 * ever descends, so the way back up is the list of folders it came through.
 * That's one API call per level instead of two.
 */
function FolderPicker({
  disabled,
  onChoose,
  onError,
}: {
  disabled: boolean;
  onChoose: (folder: Crumb) => void;
  onError: (message: string) => void;
}) {
  const ROOT: Crumb = { id: '', name: 'Account root', path: '/' };
  const [trail, setTrail] = useState<Crumb[]>([ROOT]);
  const [folders, setFolders] = useState<CloudFolder[]>([]);
  const [loading, setLoading] = useState(true);
  const here = trail[trail.length - 1] ?? ROOT;

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    listCloudFolders(here.id || undefined).then(
      (next) => {
        if (cancelled) return;
        setFolders(next);
        setLoading(false);
      },
      (err: unknown) => {
        if (cancelled) return;
        onError(err instanceof Error ? err.message : String(err));
        setLoading(false);
      },
    );
    return () => {
      cancelled = true;
    };
    // `here.id` is the whole input: a different folder, a different listing.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [here.id]);

  function open(folder: CloudFolder) {
    // A provider that identifies folders by an opaque id would return a bare
    // name as the path, so the readable path is built from the trail.
    const path = folder.path.startsWith('/')
      ? folder.path
      : `${here.path.replace(/\/$/, '')}/${folder.name}`;
    setTrail((prev) => [...prev, { id: folder.id, name: folder.name, path }]);
  }

  return (
    <div className="cloud__picker">
      <nav className="cloud__crumbs" aria-label="Folder path">
        {trail.map((crumb, index) => (
          <span key={`${crumb.id}:${index}`}>
            {index > 0 && <span aria-hidden="true"> / </span>}
            <button
              type="button"
              className="btn btn--ghost btn--small"
              disabled={index === trail.length - 1}
              onClick={() => setTrail((prev) => prev.slice(0, index + 1))}
            >
              {crumb.name}
            </button>
          </span>
        ))}
      </nav>

      {loading ? (
        <p className="muted">Loading folders…</p>
      ) : folders.length === 0 ? (
        <p className="muted">No sub-folders here.</p>
      ) : (
        <ul className="cloud__folders">
          {folders.map((folder) => (
            <li key={folder.id}>
              <button
                type="button"
                className="btn btn--ghost cloud__folder"
                onClick={() => open(folder)}
              >
                <FolderIcon />
                {folder.name}
              </button>
            </li>
          ))}
        </ul>
      )}

      <button
        type="button"
        className="btn btn--primary"
        disabled={disabled}
        onClick={() => onChoose(here)}
      >
        Use {here.path}
      </button>
    </div>
  );
}

function FolderIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      className="cloud__folder-icon"
    >
      <path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" />
    </svg>
  );
}
