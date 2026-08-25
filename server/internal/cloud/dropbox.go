package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Dropbox scopes: read metadata to list the synced folder, write content to
// push local changes up, read content to pull remote ones down, and read the
// account so the UI can name whose Dropbox this is. All four are needed for a
// two-way sync — an account connected against an older, upload-only scope set
// has to be reconnected before it can pull anything.
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
	Rev            string `json:"rev"`
	ContentHash    string `json:"content_hash"`
}

func (e dropboxEntry) path() string {
	if e.PathDisplay != "" {
		return e.PathDisplay
	}
	return e.PathLower
}

// maxListPages bounds a recursive listing. Dropbox pages at 1000 entries, so
// this allows a folder of two million files — far past any vault, and a stop
// short of looping forever on a cursor that never clears has_more.
const maxListPages = 2000

// listEntries fetches the Dropbox tree under folderID, one level deep or all
// the way down. Dropbox identifies an entry by its path, and the root is the
// empty string — which is exactly what an empty folderID means here, so no
// translation is needed.
//
// A recursive listing arrives in pages, and the continuation endpoint takes the
// cursor rather than the original arguments; both are followed here so callers
// always see one complete tree.
func (d *Dropbox) listEntries(ctx context.Context, accessToken, folderID string, recursive bool) ([]dropboxEntry, error) {
	payload, err := json.Marshal(map[string]any{
		"path":      normalizeDropboxPath(folderID),
		"recursive": recursive,
		"limit":     1000,
	})
	if err != nil {
		return nil, err
	}
	endpoint := d.APIBase + "/2/files/list_folder"
	var entries []dropboxEntry
	for page := 0; page < maxListPages; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
			bytes.NewReader(payload))
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
			Cursor  string         `json:"cursor"`
			HasMore bool           `json:"has_more"`
		}
		if err := decodeJSON(res, "Dropbox folder listing", &body); err != nil {
			return nil, err
		}
		entries = append(entries, body.Entries...)
		if !body.HasMore || body.Cursor == "" {
			return entries, nil
		}
		endpoint = d.APIBase + "/2/files/list_folder/continue"
		if payload, err = json.Marshal(map[string]any{"cursor": body.Cursor}); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// ListFolders lists the sub-folders of folderID, for the folder picker.
func (d *Dropbox) ListFolders(ctx context.Context, accessToken, folderID string) ([]Folder, error) {
	entries, err := d.listEntries(ctx, accessToken, folderID, false)
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

// ListTree lists every file under folderID, at any depth — the remote half of
// a sync. Folder entries are dropped: a folder in Dropbox exists only because
// something is in it, exactly as in the vault, so mirroring the files mirrors
// the structure.
func (d *Dropbox) ListTree(ctx context.Context, accessToken, folderID string) ([]RemoteFile, error) {
	entries, err := d.listEntries(ctx, accessToken, folderID, true)
	if err != nil {
		return nil, err
	}
	prefix := normalizeDropboxPath(folderID)
	files := []RemoteFile{}
	for _, e := range entries {
		if e.Tag != "file" {
			continue
		}
		rel := relativeDropboxPath(prefix, e.path())
		if rel == "" {
			continue // outside the folder we asked about, or the folder itself
		}
		// Hidden entries are not synced, matching what the vault walk skips:
		// .git and .obsidian belong to whatever tool made them, on the machine
		// that made them.
		if hasHiddenSegment(rel) {
			continue
		}
		f := RemoteFile{Rel: rel, Hash: e.ContentHash, Rev: e.Rev, Size: e.Size}
		if t, err := time.Parse(time.RFC3339, e.ServerModified); err == nil {
			f.ModifiedMs = t.UnixMilli()
		}
		files = append(files, f)
	}
	return files, nil
}

// UploadFile writes one file into the synced folder.
//
// With a rev the write is conditional (`update` mode): Dropbox replaces that
// exact revision or refuses, which is what turns "somebody edited it in Dropbox
// while we were uploading" into a conflict instead of a silent overwrite.
// `autorename` is off throughout — a sync must land on the path it meant, and a
// "file (1).md" appearing in the vault next round would be worse than a
// reported failure.
func (d *Dropbox) UploadFile(ctx context.Context, accessToken, folderID, rel string, data []byte, rev string) (RemoteFile, error) {
	var mode any = "overwrite"
	if rev != "" {
		mode = map[string]any{".tag": "update", "update": rev}
	}
	arg, err := json.Marshal(map[string]any{
		"path":       joinDropboxPath(folderID, rel),
		"mode":       mode,
		"autorename": false,
		"mute":       true,
	})
	if err != nil {
		return RemoteFile{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.ContentBase+"/2/files/upload", bytes.NewReader(data))
	if err != nil {
		return RemoteFile{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Dropbox-API-Arg", string(arg))
	res, err := d.client.Do(req)
	if err != nil {
		return RemoteFile{}, err
	}
	var entry dropboxEntry
	if err := decodeJSON(res, "Dropbox upload", &entry); err != nil {
		if isRevisionConflict(err) {
			return RemoteFile{}, ErrRevisionConflict
		}
		return RemoteFile{}, err
	}
	out := RemoteFile{Rel: rel, Hash: entry.ContentHash, Rev: entry.Rev, Size: entry.Size}
	if out.Hash == "" {
		// Older responses omit the hash; we know the bytes we just sent.
		out.Hash = contentHash(data)
		out.Size = int64(len(data))
	}
	if t, err := time.Parse(time.RFC3339, entry.ServerModified); err == nil {
		out.ModifiedMs = t.UnixMilli()
	}
	return out, nil
}

// isRevisionConflict recognizes the one upload failure that is not really a
// failure: `update` mode refused because the remote revision moved. Dropbox
// reports it as a `conflict` summary inside the error body.
func isRevisionConflict(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "conflict") && strings.Contains(msg, "path")
}

// DownloadFile reads one file back out of the synced folder.
func (d *Dropbox) DownloadFile(ctx context.Context, accessToken, folderID, rel string) ([]byte, error) {
	arg, err := json.Marshal(map[string]any{"path": joinDropboxPath(folderID, rel)})
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
		return nil, fmt.Errorf("%s is larger than %d bytes — refusing to download it", rel, maxDownload)
	}
	return data, nil
}

// DeleteFile removes one file from the synced folder. A file that is already
// gone counts as success: the caller wanted it absent.
func (d *Dropbox) DeleteFile(ctx context.Context, accessToken, folderID, rel string) error {
	payload, err := json.Marshal(map[string]any{"path": joinDropboxPath(folderID, rel)})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.APIBase+"/2/files/delete_v2", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		return err
	}
	if err := decodeJSON(res, "Dropbox delete", nil); err != nil {
		if strings.Contains(err.Error(), "not_found") {
			return nil
		}
		return err
	}
	return nil
}

// maxDownload bounds what a single file transfer will pull down — well past
// any plausible note or attachment, small enough that a hostile object at the
// path can't exhaust the server's memory.
const maxDownload = 1 << 30 // 1 GiB

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

// joinDropboxPath composes the absolute path of one synced file: the chosen
// folder plus the vault-relative path, which is the only place the two naming
// schemes meet.
func joinDropboxPath(folderID, rel string) string {
	return normalizeDropboxPath(folderID) + "/" + strings.Trim(rel, "/")
}

// relativeDropboxPath turns an absolute Dropbox path back into a
// vault-relative one. The comparison is case-insensitive because Dropbox is:
// path_display preserves the case a file was created with, while the folder
// handle carries whatever case the picker showed, and the two need not match.
// Returns "" for anything that isn't inside the folder.
func relativeDropboxPath(prefix, full string) string {
	if prefix == "" {
		return strings.TrimPrefix(full, "/")
	}
	if !strings.HasPrefix(strings.ToLower(full), strings.ToLower(prefix)+"/") {
		return ""
	}
	return full[len(prefix)+1:]
}

// hasHiddenSegment reports whether any part of a path is dot-prefixed. The
// vault walk skips those, so the sync has to as well — otherwise a .obsidian
// folder from one machine would be pushed onto every other one.
func hasHiddenSegment(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
