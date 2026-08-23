package cloud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time {
	return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
}

func TestDropboxAuthorizeURL(t *testing.T) {
	d := NewDropbox(Credentials{ClientID: "app-key"}, nil, fixedNow)

	// Redirect mode carries the redirect URI and the state.
	raw := d.AuthorizeURL("https://example.com/cb", "state123", "challenge")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	q := u.Query()
	if q.Get("client_id") != "app-key" || q.Get("redirect_uri") != "https://example.com/cb" ||
		q.Get("state") != "state123" || q.Get("token_access_type") != "offline" ||
		q.Get("code_challenge") != "challenge" || q.Get("code_challenge_method") != "S256" {
		t.Errorf("authorize query = %v", q)
	}

	// Paste mode: no redirect URI and no state at all.
	raw = d.AuthorizeURL("", "", "challenge")
	u, _ = url.Parse(raw)
	q = u.Query()
	if q.Has("redirect_uri") || q.Has("state") {
		t.Errorf("paste-mode query should omit redirect_uri and state: %v", q)
	}
}

func TestDropboxExchangePasteMode(t *testing.T) {
	var gotForm url.Values
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			r.ParseForm()
			gotForm = r.PostForm
			json.NewEncoder(w).Encode(map[string]any{
				"access_token": "at", "refresh_token": "rt", "expires_in": 14400,
			})
		case "/2/users/get_current_account":
			json.NewEncoder(w).Encode(map[string]any{"email": "user@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	d := NewDropbox(Credentials{ClientID: "app-key"}, api.Client(), fixedNow)
	d.APIBase = api.URL

	token, account, err := d.Exchange(context.Background(), "the-code", "verifier", "")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	// A code issued without a redirect URI must be redeemed without one.
	if gotForm.Has("redirect_uri") {
		t.Errorf("paste-mode exchange sent a redirect_uri: %v", gotForm)
	}
	if gotForm.Get("code_verifier") != "verifier" || gotForm.Get("code") != "the-code" {
		t.Errorf("exchange form = %v", gotForm)
	}
	if gotForm.Has("client_secret") {
		t.Errorf("PKCE-only app sent a secret: %v", gotForm)
	}
	if token.AccessToken != "at" || token.RefreshToken != "rt" {
		t.Errorf("token = %+v", token)
	}
	if want := fixedNow().Add(14400 * time.Second); !token.ExpiresAt.Equal(want) {
		t.Errorf("expires = %v; want %v", token.ExpiresAt, want)
	}
	if account.Label != "user@example.com" {
		t.Errorf("account = %+v", account)
	}
}

func TestDropboxRefreshKeepsRefreshToken(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Dropbox refresh responses don't repeat the refresh token.
		json.NewEncoder(w).Encode(map[string]any{"access_token": "new-at", "expires_in": 14400})
	}))
	defer api.Close()

	d := NewDropbox(Credentials{ClientID: "app-key"}, api.Client(), fixedNow)
	d.APIBase = api.URL
	token, err := d.Refresh(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if token.AccessToken != "new-at" || token.RefreshToken != "old-refresh" {
		t.Errorf("token = %+v", token)
	}
}

func TestDropboxListFoldersAndUpload(t *testing.T) {
	var uploadArg string
	var uploadBody string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/2/files/list_folder" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Path string `json:"path"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		if req.Path != "" { // account root maps to ""
			t.Errorf("list path = %q", req.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"entries": []map[string]any{
				{".tag": "folder", "name": "Notes", "path_display": "/Notes"},
				{".tag": "file", "name": "readme.txt", "path_display": "/readme.txt"},
			},
		})
	}))
	defer api.Close()
	content := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploadArg = r.Header.Get("Dropbox-API-Arg")
		buf := new(strings.Builder)
		if _, err := io.Copy(buf, r.Body); err == nil {
			uploadBody = buf.String()
		}
		w.Write([]byte("{}"))
	}))
	defer content.Close()

	d := NewDropbox(Credentials{ClientID: "app-key"}, api.Client(), fixedNow)
	d.APIBase = api.URL
	d.ContentBase = content.URL

	folders, err := d.ListFolders(context.Background(), "token", "")
	if err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if len(folders) != 1 || folders[0].ID != "/Notes" || folders[0].Name != "Notes" {
		t.Errorf("folders = %+v", folders)
	}

	if err := d.Upload(context.Background(), "token", "/Notes", "vault.zip", []byte("zipbytes")); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	var arg struct {
		Path       string `json:"path"`
		Mode       string `json:"mode"`
		Autorename bool   `json:"autorename"`
	}
	json.Unmarshal([]byte(uploadArg), &arg)
	if arg.Path != "/Notes/vault.zip" || arg.Mode != "add" || !arg.Autorename {
		t.Errorf("upload arg = %s", uploadArg)
	}
	if uploadBody != "zipbytes" {
		t.Errorf("upload body = %q", uploadBody)
	}
}
