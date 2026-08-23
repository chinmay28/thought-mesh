// Package cloud uploads a snapshot of the vault to a folder the user picked
// in their Dropbox, on a schedule they chose.
//
// The pieces are deliberately separated (the same shape as CountRoster's
// cloud backup, which this is ported from): `Provider` is everything
// account-specific (the OAuth dance, browsing folders, uploading bytes),
// `Service` is the provider-agnostic domain (the settings, token refresh,
// when the next run is due), and `Scheduler` is the goroutine that ticks.
// Only this package talks to a third party over the network.
//
// One departure from CountRoster: Thought Mesh has no database, so settings
// and tokens live in a small JSON file (see Store) — outside the vault, on
// purpose, so credentials never ride along when the vault itself is synced
// or copied by other means.
package cloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Provider IDs. These are the values the settings file and the wire contract
// carry, so they are frozen.
const (
	ProviderDropbox = "dropbox"
)

// Token is an OAuth grant: the bearer token plus what's needed to renew it.
// A zero ExpiresAt means the provider didn't say, in which case the token is
// used until it is rejected.
type Token struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

// Folder is one directory in the connected account. ID is the provider's
// native handle — a Dropbox path — and is what gets stored as `folder_id`;
// Path is what the UI shows.
type Folder struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path"`
}

// Account is the connected identity, shown so the user can tell which account
// their vault snapshots are landing in.
type Account struct {
	Label string
}

// SnapshotFile is one file in the connected folder — a restore candidate.
// ID is the provider's native handle (a Dropbox path); ModifiedMs is the
// provider's server-side modification time.
type SnapshotFile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedMs int64  `json:"modified_ms"`
}

// Provider is one cloud destination. Implementations are stateless apart
// from their configured client credentials, so one instance serves every
// request.
type Provider interface {
	// ID is the stable wire identifier ("dropbox").
	ID() string
	// Name is the human label ("Dropbox").
	Name() string
	// Configured reports whether this instance carries client credentials.
	// An unconfigured provider is advertised but can't be connected.
	Configured() bool
	// WithCredentials returns a copy bound to different credentials. The
	// credentials a deployment registered are resolved per request (settings
	// file first, startup flags second), so they can't be baked in at
	// construction — this is how the resolved pair reaches the provider.
	WithCredentials(Credentials) Provider
	// RequiresSecret reports whether the provider rejects a public
	// (PKCE-only) client, so the setup form can mark the secret required.
	RequiresSecret() bool
	// SetupURL is the provider's developer console — where a user registers
	// the OAuth app whose id they're about to paste in.
	SetupURL() string
	// SupportsCodePaste reports whether the provider will authorize with no
	// redirect URI at all, showing the user a code to copy back instead.
	//
	// That mode is worth having because it sidesteps the one thing a
	// self-hosted server can't satisfy: providers demand a pre-registered,
	// https redirect URI, and this server's origin is neither predictable nor
	// necessarily https.
	SupportsCodePaste() bool
	// AuthorizeURL is where the browser is sent to grant access. An empty
	// redirectURI selects the paste-a-code mode (see SupportsCodePaste), and
	// an empty state omits the parameter — there is no redirect to protect.
	AuthorizeURL(redirectURI, state, codeChallenge string) string
	// Exchange trades the code for a token, and reports whose account it
	// belongs to. redirectURI must be exactly what AuthorizeURL was given,
	// empty included: a code issued without a redirect URI must be redeemed
	// without one.
	Exchange(ctx context.Context, code, codeVerifier, redirectURI string) (Token, Account, error)
	// Refresh renews an expired access token. The returned token keeps the
	// refresh token when the provider doesn't issue a new one.
	Refresh(ctx context.Context, refreshToken string) (Token, error)
	// ListFolders lists the sub-folders of folderID (empty = the account
	// root), for the folder picker.
	ListFolders(ctx context.Context, accessToken, folderID string) ([]Folder, error)
	// ListFiles lists the files directly inside folderID, for the restore
	// picker. The service filters to vault snapshots; the provider doesn't.
	ListFiles(ctx context.Context, accessToken, folderID string) ([]SnapshotFile, error)
	// Upload writes data as a new file called name inside folderID.
	Upload(ctx context.Context, accessToken, folderID, name string, data []byte) error
	// Download reads one file back, by the ID ListFiles reported.
	Download(ctx context.Context, accessToken, fileID string) ([]byte, error)
}

// Credentials are the OAuth client the operator registered with a provider.
// Thought Mesh is self-hosted, so there is no shipped application identity to
// borrow: each deployment registers its own app and gives Thought Mesh the id
// (and, where the provider demands one, the secret) — from the Sync page, or
// as a startup flag.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// Set reports whether these credentials can be used at all.
func (c Credentials) Set() bool { return c.ClientID != "" }

// Registry is the set of providers this build knows about, in the order the
// UI should offer them.
type Registry []Provider

// NewRegistry builds the standard registry. The credentials passed here are
// the startup-flag fallback; whatever was entered on the Sync page wins at
// request time. Providers with no credentials from either source are still
// listed — the UI shows them as needing setup rather than pretending they
// don't exist.
//
// `now` is the clock token expiries are resolved against; it comes from the
// service's clock so a test can pin it.
func NewRegistry(dropbox Credentials, client *http.Client, now func() time.Time) Registry {
	return Registry{
		NewDropbox(dropbox, client, now),
	}
}

// Get returns the provider with the given id, or nil.
func (r Registry) Get(id string) Provider {
	for _, p := range r {
		if p.ID() == id {
			return p
		}
	}
	return nil
}

// ErrNotConfigured is returned when a provider is asked to do OAuth work
// without client credentials. It surfaces as a 400, not a 500 — it's a
// deployment gap the operator can close, not a bug.
var ErrNotConfigured = errors.New("cloud provider is not configured on this server")

// httpClient is the transport shared by the providers. A timeout is
// deliberate: a hung upload must not wedge the scheduler goroutine.
func httpClient(c *http.Client) *http.Client {
	if c != nil {
		return c
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// apiError renders a failed provider call. The body is included (truncated)
// because the provider's own message — "insufficient_scope", "path/not_found"
// — is the only useful thing to show the user.
func apiError(what string, res *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = res.Status
	}
	return fmt.Errorf("%s failed (%d): %s", what, res.StatusCode, msg)
}

// decodeJSON reads a JSON response body into v, turning a non-2xx into an
// apiError first so every caller reports failures the same way.
func decodeJSON(res *http.Response, what string, v any) error {
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return apiError(what, res)
	}
	if v == nil {
		io.Copy(io.Discard, res.Body)
		return nil
	}
	return json.NewDecoder(res.Body).Decode(v)
}

// tokenResponse is the shape the provider's /token endpoint returns.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// toToken converts a token response, resolving expires_in against now. The
// refresh token falls back to the one we already hold: a refresh response
// usually omits it, and losing it would silently break the schedule.
func (t tokenResponse) toToken(now time.Time, previousRefresh string) Token {
	tok := Token{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken}
	if tok.RefreshToken == "" {
		tok.RefreshToken = previousRefresh
	}
	if t.ExpiresIn > 0 {
		tok.ExpiresAt = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	}
	return tok
}
