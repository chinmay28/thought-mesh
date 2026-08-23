package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

// Dropbox scopes: read metadata to browse folders and list snapshots, write
// content to upload the vault snapshot, read content to download one back
// for a restore, and read the account so the UI can name whose Dropbox this
// is. (`files.content.read` joined the set when restore did — an account
// connected before that needs a reconnect before it can restore.)
const dropboxScopes = "account_info.read files.metadata.read files.content.read files.content.write"

// Dropbox is the Dropbox provider. The three base URLs are fields rather than
// constants so tests can point them at an httptest server.
type Dropbox struct {
	Creds Credentials
	// AuthBase serves the authorize page (browser-facing).
	AuthBase string
	// APIBase serves the RPC endpoints (token exchange, metadata).
	APIBase string
	// ContentBase serves the upload endpoint — a separate host at Dropbox.
	ContentBase string

	client *http.Client
	now    func() time.Time
}

// NewDropbox builds the provider from the operator's registered app.
func NewDropbox(creds Credentials, client *http.Client, now func() time.Time) *Dropbox {
	if now == nil {
		now = time.Now
	}
	return &Dropbox{
		Creds:       creds,
		AuthBase:    "https://www.dropbox.com",
		APIBase:     "https://api.dropboxapi.com",
		ContentBase: "https://content.dropboxapi.com",
		client:      httpClient(client),
		now:         now,
	}
}

func (d *Dropbox) ID() string   { return ProviderDropbox }
func (d *Dropbox) Name() string { return "Dropbox" }

func (d *Dropbox) Configured() bool { return d.Creds.Set() }

// WithCredentials returns a copy bound to a different OAuth client. Dropbox
// app keys are interchangeable at this level, so a shallow copy is enough.
func (d *Dropbox) WithCredentials(creds Credentials) Provider {
	next := *d
	next.Creds = creds
	return &next
}

// RequiresSecret is false: a Dropbox app can be registered as a public client
// and authorized with PKCE alone.
func (d *Dropbox) RequiresSecret() bool { return false }

func (d *Dropbox) SetupURL() string { return "https://www.dropbox.com/developers/apps" }

// SupportsCodePaste is true: Dropbox authorizes with no redirect URI at all,
// showing the user a code to copy back. That's the escape hatch for a server
// whose origin can't be pre-registered — or isn't https.
func (d *Dropbox) SupportsCodePaste() bool { return true }

// AuthorizeURL asks for an offline grant — without `token_access_type=offline`
// Dropbox issues a 4-hour token and no refresh token, which would make a
// scheduled sync stop working the same afternoon it was set up.
func (d *Dropbox) AuthorizeURL(redirectURI, state, codeChallenge string) string {
	q := url.Values{
		"client_id":             {d.Creds.ClientID},
		"response_type":         {"code"},
		"token_access_type":     {"offline"},
		"scope":                 {dropboxScopes},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	// No redirect URI means the paste-a-code flow: Dropbox renders the code
	// on screen instead of redirecting. `state` guards a redirect against
	// CSRF, so with no redirect there is nothing for it to guard.
	if redirectURI != "" {
		q.Set("redirect_uri", redirectURI)
		if state != "" {
			q.Set("state", state)
		}
	}
	return d.AuthBase + "/oauth2/authorize?" + q.Encode()
}

func (d *Dropbox) Exchange(ctx context.Context, code, verifier, redirectURI string) (Token, Account, error) {
	if !d.Configured() {
		return Token{}, Account{}, ErrNotConfigured
	}
	form := url.Values{
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"client_id":     {d.Creds.ClientID},
		"code_verifier": {verifier},
	}
	// A code issued without a redirect URI must be redeemed without one —
	// Dropbox rejects the exchange otherwise.
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	// A Dropbox app may be registered as a public (PKCE-only) or a
	// confidential client; send the secret only when the operator has one.
	if d.Creds.ClientSecret != "" {
		form.Set("client_secret", d.Creds.ClientSecret)
	}
	var body tokenResponse
	if err := d.postForm(ctx, d.APIBase+"/oauth2/token", form, "Dropbox token exchange", &body); err != nil {
		return Token{}, Account{}, err
	}
	token := body.toToken(d.now(), "")
	account, err := d.account(ctx, token.AccessToken)
	if err != nil {
		// The grant is good even if the courtesy lookup failed; carry on with
		// an unnamed account rather than losing the connection.
		account = Account{Label: "Dropbox"}
	}
	return token, account, nil
}

func (d *Dropbox) Refresh(ctx context.Context, refreshToken string) (Token, error) {
	if !d.Configured() {
		return Token{}, ErrNotConfigured
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {d.Creds.ClientID},
	}
	if d.Creds.ClientSecret != "" {
		form.Set("client_secret", d.Creds.ClientSecret)
	}
	var body tokenResponse
	if err := d.postForm(ctx, d.APIBase+"/oauth2/token", form, "Dropbox token refresh", &body); err != nil {
		return Token{}, err
	}
	return body.toToken(d.now(), refreshToken), nil
}

// account names the connected Dropbox for the settings screen.
func (d *Dropbox) account(ctx context.Context, accessToken string) (Account, error) {
	// This RPC takes a bare `null` body rather than an empty one.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.APIBase+"/2/users/get_current_account", strings.NewReader("null"))
	if err != nil {
		return Account{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		return Account{}, err
	}
	var body struct {
		Email string `json:"email"`
		Name  struct {
			DisplayName string `json:"display_name"`
		} `json:"name"`
	}
	if err := decodeJSON(res, "Dropbox account lookup", &body); err != nil {
		return Account{}, err
	}
	label := body.Email
	if label == "" {
		label = body.Name.DisplayName
	}
	if label == "" {
		label = "Dropbox"
	}
	return Account{Label: label}, nil
}

// dropboxEntry is one row of a /2/files/list_folder response — a folder or a
// file, distinguished by the ".tag".
type dropboxEntry struct {
	Tag            string `json:".tag"`
	Name           string `json:"name"`
	PathDisplay    string `json:"path_display"`
	PathLower      string `json:"path_lower"`
	Size           int64  `json:"size"`
	ServerModified string `json:"server_modified"`
}

func (e dropboxEntry) path() string {
	if e.PathDisplay != "" {
		return e.PathDisplay
	}
	return e.PathLower
}

// listEntries fetches one level of the Dropbox tree. Dropbox identifies an
// entry by its path, and the root is the empty string — which is exactly
// what an empty folderID means here, so no translation is needed.
func (d *Dropbox) listEntries(ctx context.Context, accessToken, folderID string) ([]dropboxEntry, error) {
	payload, err := json.Marshal(map[string]any{
		"path":      normalizeDropboxPath(folderID),
		"recursive": false,
		"limit":     1000,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.APIBase+"/2/files/list_folder", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	var body struct {
		Entries []dropboxEntry `json:"entries"`
	}
	if err := decodeJSON(res, "Dropbox folder listing", &body); err != nil {
		return nil, err
	}
	return body.Entries, nil
}

// ListFolders lists the sub-folders of folderID, for the folder picker.
func (d *Dropbox) ListFolders(ctx context.Context, accessToken, folderID string) ([]Folder, error) {
	entries, err := d.listEntries(ctx, accessToken, folderID)
	if err != nil {
		return nil, err
	}
	folders := []Folder{}
	for _, e := range entries {
		if e.Tag != "folder" {
			continue
		}
		folders = append(folders, Folder{ID: e.path(), Name: e.Name, Path: e.path()})
	}
	return folders, nil
}

// ListFiles lists the files directly inside folderID, for the restore picker.
func (d *Dropbox) ListFiles(ctx context.Context, accessToken, folderID string) ([]SnapshotFile, error) {
	entries, err := d.listEntries(ctx, accessToken, folderID)
	if err != nil {
		return nil, err
	}
	files := []SnapshotFile{}
	for _, e := range entries {
		if e.Tag != "file" {
			continue
		}
		f := SnapshotFile{ID: e.path(), Name: e.Name, Size: e.Size}
		if t, err := time.Parse(time.RFC3339, e.ServerModified); err == nil {
			f.ModifiedMs = t.UnixMilli()
		}
		files = append(files, f)
	}
	return files, nil
}

// Upload writes the snapshot into the chosen folder. `autorename` is on so
// two runs in the same minute can't fail on a name clash — the schedule
// matters more than the exact filename.
func (d *Dropbox) Upload(ctx context.Context, accessToken, folderID, name string, data []byte) error {
	arg, err := json.Marshal(map[string]any{
		"path":       path.Join("/"+strings.Trim(normalizeDropboxPath(folderID), "/"), name),
		"mode":       "add",
		"autorename": true,
		"mute":       true,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.ContentBase+"/2/files/upload", bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Dropbox-API-Arg", string(arg))
	res, err := d.client.Do(req)
	if err != nil {
		return err
	}
	return decodeJSON(res, "Dropbox upload", nil)
}

// maxDownload bounds what a restore will pull down — well past any plausible
// vault of markdown, small enough that a mistaken selection (or a hostile
// object at the path) can't exhaust the server's memory.
const maxDownload = 1 << 30 // 1 GiB

// Download reads one file back, for a restore.
func (d *Dropbox) Download(ctx context.Context, accessToken, fileID string) ([]byte, error) {
	arg, err := json.Marshal(map[string]any{"path": fileID})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.ContentBase+"/2/files/download", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Dropbox-API-Arg", string(arg))
	res, err := d.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return nil, apiError("Dropbox download", res)
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxDownload+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownload {
		return nil, fmt.Errorf("Dropbox download larger than %d bytes — refusing to restore it", maxDownload)
	}
	return data, nil
}

func (d *Dropbox) postForm(ctx context.Context, endpoint string, form url.Values, what string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := d.client.Do(req)
	if err != nil {
		return err
	}
	if err := decodeJSON(res, what, out); err != nil {
		return err
	}
	if body, ok := out.(*tokenResponse); ok && body.AccessToken == "" {
		return fmt.Errorf("%s returned no access token", what)
	}
	return nil
}

// normalizeDropboxPath maps our folder handle onto what the API wants: the
// root is "" (not "/"), and everything else is an absolute path.
func normalizeDropboxPath(folderID string) string {
	trimmed := strings.Trim(strings.TrimSpace(folderID), "/")
	if trimmed == "" {
		return ""
	}
	return "/" + trimmed
}
