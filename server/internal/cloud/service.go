package cloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chinmay28/thought-mesh/server/internal/history"
)

// CallbackPath is where a provider sends the browser back after consent. It
// is part of the deployment contract, not just an internal route: the
// operator registers `<public origin>` + this path as the app's redirect URI.
const CallbackPath = "/api/cloud/sync/callback"

// refreshSkew renews an access token this far before it actually expires, so
// a long upload can't start on a token that dies mid-request.
const refreshSkew = 2 * time.Minute

// ConfigError is a request that can't proceed because cloud sync isn't set
// up: no account connected, no folder chosen, a provider the operator never
// registered. It maps to HTTP 400 — the caller can fix it.
type ConfigError struct{ Message string }

func (e *ConfigError) Error() string { return e.Message }

// ProviderError wraps a failure that came from Dropbox rather than from us —
// a rejected token, a deleted folder, a network timeout. It maps to HTTP 502
// so the UI can say honestly whose problem it is.
type ProviderError struct {
	Provider string
	Err      error
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("%s: %v", e.Provider, e.Err)
}

func (e *ProviderError) Unwrap() error { return e.Err }

// Service is the provider-agnostic half of cloud sync: it owns the settings
// file and the sync state, keeps the access token fresh, and turns "a run is
// due" into the vault and a Dropbox folder holding the same tree.
type Service struct {
	Store    *Store
	Registry Registry
	// State is the bookkeeping a two-way sync needs: what both sides looked
	// like when they last agreed. Kept beside the settings file, outside the
	// vault (see state.go).
	State *StateStore
	// Vault is the local half — the folder being synced.
	Vault LocalStore
	// History is the vault's git history, when the machine has one. A nil
	// *Repo is a working, disabled history: every method is a safe no-op, so
	// nothing below has to check whether git happened to be installed.
	History *history.Repo
	// Clock is the time source for schedules and token expiry; nil means
	// time.Now. Injected so tests can pin the instant.
	Clock func() time.Time
	// PublicURL, when set, is the origin the OAuth redirect URI is built
	// from. Left empty, the request's own scheme and host are used — right
	// for a LAN or Tailscale address reached directly, wrong behind a proxy
	// that rewrites neither.
	PublicURL string

	pending *pendingStore
}

// NewService wires a service and its in-flight-authorization store.
func NewService(store *Store, state *StateStore, local LocalStore, hist *history.Repo,
	reg Registry, clock func() time.Time, publicURL string) *Service {
	s := &Service{
		Store: store, State: state, Vault: local, History: hist, Registry: reg,
		Clock: clock, PublicURL: strings.TrimRight(publicURL, "/"),
	}
	s.pending = newPendingStore(s.Now)
	return s
}

// Now is the service's clock. Everything schedule-related goes through it so
// a test can pin the instant.
func (s *Service) Now() time.Time {
	if s.Clock != nil {
		return s.Clock()
	}
	return time.Now()
}

// Settings reads the current configuration.
func (s *Service) Settings() (*Settings, error) { return s.Store.Settings() }

// PublicProviders lists the destinations this build offers, with the state of
// each one's OAuth client — enough for the settings screen to render both the
// Connect buttons and the setup form without a second request.
func (s *Service) PublicProviders() []PublicProvider {
	stored, err := s.Store.Credentials()
	if err != nil {
		// A read failure here shouldn't blank the whole screen; fall back to
		// whatever the startup flags carry.
		stored = map[string]Credentials{}
	}
	out := make([]PublicProvider, 0, len(s.Registry))
	for _, p := range s.Registry {
		creds, source := resolveCredentials(p, stored)
		entry := PublicProvider{
			ID:       p.ID(),
			Name:     p.Name(),
			ClientID: creds.ClientID,
			Source:   source,
			SetupURL: p.SetupURL(),
		}
		if creds.Set() {
			entry.Configured = 1
		}
		if creds.ClientSecret != "" {
			entry.HasSecret = 1
		}
		if p.RequiresSecret() {
			entry.SecretRequired = 1
		}
		if p.SupportsCodePaste() {
			entry.SupportsCodePaste = 1
		}
		out = append(out, entry)
	}
	return out
}

// resolveCredentials picks the OAuth client to use: what was entered on the
// Sync page wins over what a startup flag carries. The settings file is the
// more specific, more recent statement of intent, and it's the one the user
// is looking at — an operator who wants the flag to win can leave the form
// empty. Returns the source so the UI can say where the active pair came from.
func resolveCredentials(p Provider, stored map[string]Credentials) (Credentials, string) {
	if creds, ok := stored[p.ID()]; ok && creds.Set() {
		return creds, SourceSettings
	}
	if built := builtinCredentials(p); built.Set() {
		return built, SourceServer
	}
	return Credentials{}, ""
}

// builtinCredentials reads back whatever the registry entry was constructed
// with — the --dropbox-client-id fallback.
func builtinCredentials(p Provider) Credentials {
	if d, ok := p.(*Dropbox); ok {
		return d.Creds
	}
	// A provider outside this package (a test double) can expose its
	// construction-time credentials through this optional interface…
	if bc, ok := p.(interface{ BuiltinCredentials() Credentials }); ok {
		return bc.BuiltinCredentials()
	}
	// …or report only whether it considers itself configured, which is
	// enough to keep it usable.
	if p.Configured() {
		return Credentials{ClientID: "configured"}
	}
	return Credentials{}
}

// providerFor resolves a provider id to an instance bound to the credentials
// this deployment actually has, or a ConfigError explaining what's missing.
func (s *Service) providerFor(id string) (Provider, error) {
	base := s.Registry.Get(id)
	if base == nil {
		return nil, &ConfigError{Message: `Unknown cloud provider "` + id + `"`}
	}
	stored, err := s.Store.Credentials()
	if err != nil {
		return nil, err
	}
	creds, _ := resolveCredentials(base, stored)
	if !creds.Set() {
		return nil, &ConfigError{Message: base.Name() +
			" is not set up yet: add the client id from an OAuth app you registered with " +
			base.Name() + ", under Provider setup on this page."}
	}
	return base.WithCredentials(creds), nil
}

// SetCredentials stores the OAuth client for one provider, as entered on the
// Sync page.
func (s *Service) SetCredentials(providerID, clientID, clientSecret string) error {
	base := s.Registry.Get(providerID)
	if base == nil {
		return &ConfigError{Message: `Unknown cloud provider "` + providerID + `"`}
	}
	creds, err := validateCredentials(base, clientID, clientSecret)
	if err != nil {
		return err
	}
	return s.replaceCredentials(providerID, func() error {
		return s.Store.SaveCredentials(providerID, creds, toISO(s.Now()))
	})
}

// ClearCredentials forgets a provider's stored OAuth client, falling back to
// the startup flag if the server has one.
func (s *Service) ClearCredentials(providerID string) error {
	if s.Registry.Get(providerID) == nil {
		return &ConfigError{Message: `Unknown cloud provider "` + providerID + `"`}
	}
	return s.replaceCredentials(providerID, func() error {
		return s.Store.DeleteCredentials(providerID)
	})
}

// replaceCredentials runs a credential write and drops the connection if it
// changed which OAuth client is in effect.
//
// Tokens belong to the client that minted them: a Dropbox refresh token issued
// to app key A is worthless to app key B. Keeping the connection across a
// client id change would leave something that looks connected on screen and
// fails at the next refresh — hours later, in the scheduler, where nobody is
// watching. Better to say "reconnect" now. Re-saving the same id (to correct a
// secret, say) is not a change and keeps the account.
func (s *Service) replaceCredentials(providerID string, write func() error) error {
	before, err := s.effectiveClientID(providerID)
	if err != nil {
		return err
	}
	if err := write(); err != nil {
		return err
	}
	after, err := s.effectiveClientID(providerID)
	if err != nil {
		return err
	}
	if before == after {
		return nil
	}
	set, err := s.Settings()
	if err != nil {
		return err
	}
	if set.Connected() && set.Provider != nil && *set.Provider == providerID {
		_, err = s.Disconnect()
		return err
	}
	return nil
}

// effectiveClientID is the client id a connection would currently be made
// with — the stored one, else the startup flag, else empty.
func (s *Service) effectiveClientID(providerID string) (string, error) {
	base := s.Registry.Get(providerID)
	if base == nil {
		return "", nil
	}
	stored, err := s.Store.Credentials()
	if err != nil {
		return "", err
	}
	creds, _ := resolveCredentials(base, stored)
	return creds.ClientID, nil
}

// Update applies a settings patch: the schedule and the destination folder,
// the only two things the browser owns. Anything absent is left alone.
//
// Changing the frequency re-bases the schedule from now, so picking "daily"
// means "a day from now", not "some leftover deadline from the hourly setting
// you just changed".
func (s *Service) Update(frequency *string, folderID, folderPath *string) (*Settings, error) {
	current, err := s.Settings()
	if err != nil {
		return nil, err
	}
	now := s.Now()

	if frequency != nil {
		if err := validateFrequency(*frequency); err != nil {
			return nil, err
		}
		if *frequency != FrequencyOff && !current.Connected() {
			return nil, &ConfigError{Message: "Connect a cloud account before scheduling sync."}
		}
	}
	// Pointing sync at a different folder means everything recorded about the
	// old one is meaningless: the same path in a new folder is a different
	// file, and the stale hashes would read as "every note was deleted
	// remotely". Forget them, and let the first run there work it out fresh.
	if folderID != nil && (current.FolderID == nil || *current.FolderID != *folderID) {
		if err := s.State.Reset(*folderID, toISO(now)); err != nil {
			return nil, err
		}
	}
	return s.Store.UpdateSettings(toISO(now), func(set *Settings) {
		if frequency != nil {
			set.Frequency = *frequency
			set.NextRunAt = nextRunISO(now, *frequency)
		}
		if folderID != nil {
			set.FolderID = folderID
			set.FolderPath = folderPath
			// A folder chosen after the schedule was set shouldn't have to wait
			// out a deadline that was pinned before there was anywhere to write.
			if set.Frequency != FrequencyOff && set.NextRunAt == nil {
				set.NextRunAt = nextRunISO(now, set.Frequency)
			}
		}
	})
}

// Connect modes. "redirect" is the ordinary flow: the provider sends the
// browser back to this server. "paste" asks for no redirect at all — the
// provider shows the user a code, which they bring back themselves.
const (
	ModeRedirect = "redirect"
	ModePaste    = "paste"
)

// ConnectStart is what the client needs to carry an authorization through.
type ConnectStart struct {
	// AuthorizeURL is where the user grants access.
	AuthorizeURL string
	// Mode is ModeRedirect or ModePaste.
	Mode string
	// PendingID identifies this in-flight authorization. In paste mode the
	// client holds it and sends it back with the code; in redirect mode it
	// travels as the OAuth `state` and the callback carries it.
	PendingID string
}

// StartConnect begins an OAuth authorization and returns where to send the
// user. The PKCE verifier and the exact redirect URI are remembered against
// the returned PendingID — the token exchange has to repeat both.
//
// `paste` selects the no-redirect flow, for the case a self-hosted server is
// most likely to hit: providers require a pre-registered https redirect URI,
// and this server's origin may be plain http, or a LAN address that can't be
// registered at all. Then there is nothing to redirect to, and a code the user
// carries back by hand is the only way through.
func (s *Service) StartConnect(providerID, requestOrigin string, paste bool) (*ConnectStart, error) {
	provider, err := s.providerFor(providerID)
	if err != nil {
		return nil, err
	}
	if paste && !provider.SupportsCodePaste() {
		return nil, &ConfigError{Message: provider.Name() +
			" has no paste-a-code sign-in; it needs a registered https redirect URI."}
	}
	pendingID, err := randomURLSafe(24)
	if err != nil {
		return nil, err
	}
	// RFC 7636 wants 43–128 characters; 48 random bytes lands at 64.
	verifier, err := randomURLSafe(48)
	if err != nil {
		return nil, err
	}

	mode, redirectURI, state := ModeRedirect, s.redirectURI(requestOrigin), pendingID
	if paste {
		// No redirect URI, and therefore no `state`: it exists to bind a
		// redirect to the request that started it, and there is no redirect.
		mode, redirectURI, state = ModePaste, "", ""
	}
	s.pending.put(pendingID, pending{
		provider:    provider.ID(),
		verifier:    verifier,
		redirectURI: redirectURI,
	})
	return &ConnectStart{
		AuthorizeURL: provider.AuthorizeURL(redirectURI, state, codeChallenge(verifier)),
		Mode:         mode,
		PendingID:    pendingID,
	}, nil
}

// RedirectSupported reports whether an authorization could come back to this
// origin at all. Providers require https for a registered redirect URI —
// localhost being the documented exception — so a server reached over plain
// http on a LAN address can only use the paste flow, and the UI should lead
// with it rather than offering a button that cannot work.
func (s *Service) RedirectSupported(requestOrigin string) bool {
	origin := s.PublicURL
	if origin == "" {
		origin = requestOrigin
	}
	if strings.HasPrefix(origin, "https://") {
		return true
	}
	rest, ok := strings.CutPrefix(origin, "http://")
	if !ok {
		return false
	}
	host, _, _ := strings.Cut(rest, ":")
	return host == "localhost" || host == "127.0.0.1" || host == "[::1]"
}

// RedirectURI is where the provider sends the browser back — the string the
// user has to register with the provider, so the setup form shows it verbatim.
func (s *Service) RedirectURI(requestOrigin string) string {
	return s.redirectURI(requestOrigin)
}

// redirectURI is where the provider sends the browser back. An explicitly
// configured public URL wins; otherwise the origin the request arrived on is
// the best guess available.
func (s *Service) redirectURI(requestOrigin string) string {
	origin := s.PublicURL
	if origin == "" {
		origin = strings.TrimRight(requestOrigin, "/")
	}
	return origin + CallbackPath
}

// CompleteConnect finishes the authorization: trade the code for a token,
// remember whose account it is, and leave the schedule off until the user
// picks a folder. `pendingID` is the OAuth `state` in redirect mode and the
// handle the client kept in paste mode; the code is the same either way.
func (s *Service) CompleteConnect(ctx context.Context, pendingID, code string) (*Settings, error) {
	p, ok := s.pending.take(pendingID)
	if !ok {
		return nil, &ConfigError{Message: ErrUnknownState.Error()}
	}
	provider, err := s.providerFor(p.provider)
	if err != nil {
		return nil, err
	}
	token, account, err := provider.Exchange(ctx, code, p.verifier, p.redirectURI)
	if err != nil {
		return nil, &ProviderError{Provider: provider.Name(), Err: err}
	}

	current, err := s.Settings()
	if err != nil {
		return nil, err
	}
	// A fresh connection starts clean: a folder from a previous account is
	// meaningless, and so is that account's run history.
	next := &Settings{
		Provider:     ptr(provider.ID()),
		AccountLabel: ptr(account.Label),
		AccessToken:  ptr(token.AccessToken),
		Frequency:    FrequencyOff,
	}
	if token.RefreshToken != "" {
		next.RefreshToken = ptr(token.RefreshToken)
	}
	if !token.ExpiresAt.IsZero() {
		next.TokenExpiresAt = ptr(toISO(token.ExpiresAt))
	}
	// Reconnecting the same account keeps its folder and schedule — that's a
	// token refresh in the user's eyes, not a reset.
	if current.Provider != nil && *current.Provider == provider.ID() &&
		current.AccountLabel != nil && *current.AccountLabel == account.Label {
		next.FolderID = current.FolderID
		next.FolderPath = current.FolderPath
		next.Frequency = current.Frequency
		next.NextRunAt = current.NextRunAt
		next.LastRunAt = current.LastRunAt
		next.LastStatus = current.LastStatus
		next.LastError = current.LastError
		next.LastResult = current.LastResult
	}
	if err := s.Store.SaveSettings(next, toISO(s.Now())); err != nil {
		return nil, err
	}
	return s.Settings()
}

// Disconnect forgets the account. The tokens are dropped rather than kept
// "in case" — a disconnect the user asked for should leave nothing behind
// that could still write to their Dropbox.
func (s *Service) Disconnect() (*Settings, error) {
	cleared := &Settings{Frequency: FrequencyOff}
	if err := s.Store.SaveSettings(cleared, toISO(s.Now())); err != nil {
		return nil, err
	}
	// The sync state describes a folder this server can no longer reach, and
	// keeping it would let a reconnection to a *different* account inherit
	// hashes that describe somebody else's files.
	if err := s.State.Reset("", toISO(s.Now())); err != nil {
		return nil, err
	}
	return s.Settings()
}

// ListFolders browses the connected account for the folder picker. An empty
// folderID lists the account root.
func (s *Service) ListFolders(ctx context.Context, folderID string) ([]Folder, error) {
	provider, token, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	folders, err := provider.ListFolders(ctx, token, folderID)
	if err != nil {
		return nil, &ProviderError{Provider: provider.Name(), Err: err}
	}
	return folders, nil
}

// authorize resolves the connected provider and a usable access token,
// refreshing first when the stored one is at or near expiry.
func (s *Service) authorize(ctx context.Context) (Provider, string, error) {
	set, err := s.Settings()
	if err != nil {
		return nil, "", err
	}
	if !set.Connected() {
		return nil, "", &ConfigError{Message: "No cloud account is connected."}
	}
	provider, err := s.providerFor(*set.Provider)
	if err != nil {
		return nil, "", err
	}

	if !s.tokenExpiring(set) {
		return provider, *set.AccessToken, nil
	}
	if set.RefreshToken == nil || *set.RefreshToken == "" {
		return nil, "", &ConfigError{Message: provider.Name() +
			" access has expired and there is no refresh token — reconnect the account."}
	}
	token, err := provider.Refresh(ctx, *set.RefreshToken)
	if err != nil {
		return nil, "", &ProviderError{Provider: provider.Name(), Err: err}
	}
	if err := s.saveToken(token); err != nil {
		return nil, "", err
	}
	return provider, token.AccessToken, nil
}

// tokenExpiring reports whether the stored access token is inside the refresh
// window. A token with no recorded expiry is used until the provider rejects
// it — that's the contract for providers that don't tell us.
func (s *Service) tokenExpiring(set *Settings) bool {
	if set.TokenExpiresAt == nil || *set.TokenExpiresAt == "" {
		return false
	}
	at, ok := parseISO(*set.TokenExpiresAt)
	if !ok {
		return true
	}
	return !at.After(s.Now().Add(refreshSkew))
}

// saveToken writes just the credential fields, leaving the schedule and the
// run history to whoever owns them.
func (s *Service) saveToken(token Token) error {
	_, err := s.Store.UpdateSettings(toISO(s.Now()), func(set *Settings) {
		set.AccessToken = ptr(token.AccessToken)
		if token.ExpiresAt.IsZero() {
			set.TokenExpiresAt = nil
		} else {
			set.TokenExpiresAt = ptr(toISO(token.ExpiresAt))
		}
		if token.RefreshToken != "" {
			set.RefreshToken = ptr(token.RefreshToken)
		}
	})
	return err
}

// recordRun stamps the outcome and schedules the next attempt. A failure is
// rescheduled on the same interval rather than retried tightly: the usual
// causes (revoked access, a deleted folder) need a human, and hammering the
// provider wouldn't help.
func (s *Service) recordRun(result *SyncResult, runErr error) error {
	now := s.Now()
	nowISO := toISO(now)
	_, err := s.Store.UpdateSettings(nowISO, func(set *Settings) {
		set.LastRunAt = ptr(nowISO)
		if runErr != nil {
			set.LastStatus = ptr(StatusError)
			set.LastError = ptr(runErr.Error())
		} else {
			set.LastStatus = ptr(StatusOK)
			set.LastError = nil
		}
		// Even a failed run usually moved some files; what it managed is worth
		// keeping on screen, and a nil result (the run never started) clears
		// the counts rather than leaving the previous run's on display.
		set.LastResult = summarize(result)
		set.NextRunAt = nextRunISO(now, set.Frequency)
	})
	return err
}

// summarize projects a run onto the small record the settings file keeps.
func summarize(result *SyncResult) *RunSummary {
	if result == nil {
		return nil
	}
	return &RunSummary{
		Uploaded:      result.Uploaded,
		Downloaded:    result.Downloaded,
		DeletedLocal:  result.DeletedLocal,
		DeletedRemote: result.DeletedRemote,
		Unchanged:     result.Unchanged,
		Conflicts:     len(result.Conflicts),
		Failed:        result.Failed,
	}
}

// RunIfDue runs a scheduled sync when one is owed. It reports whether it
// ran, so the caller can log at the right volume.
func (s *Service) RunIfDue(ctx context.Context) (bool, error) {
	set, err := s.Settings()
	if err != nil {
		return false, err
	}
	if !set.due(s.Now()) {
		return false, nil
	}
	if _, err := s.Sync(ctx, ""); err != nil {
		return true, err
	}
	return true, nil
}

// IsConfigError reports whether err is a user-fixable configuration problem.
func IsConfigError(err error) bool {
	var ce *ConfigError
	return errors.As(err, &ce)
}

// IsProviderError reports whether err came from the upstream cloud service.
func IsProviderError(err error) bool {
	var pe *ProviderError
	return errors.As(err, &pe)
}
