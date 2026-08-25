package cloud

import (
	"context"
	"encoding/json"
	"errors"
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

// A recursive listing is the remote half of a sync: it has to describe the
// whole tree, follow the cursor when Dropbox pages it, report each file's
// content hash and revision, and leave hidden folders alone.
func TestDropboxListTreeFollowsPagesAndSkipsHidden(t *testing.T) {
	page := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/2/files/list_folder":
			var req struct {
				Path      string `json:"path"`
				Recursive bool   `json:"recursive"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Path != "/Notes" || !req.Recursive {
				t.Errorf("list request = %+v", req)
			}
			page++
			json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{".tag": "file", "name": "Idea.md", "path_display": "/Notes/Idea.md",
						"size": 12, "rev": "0123", "content_hash": "aaa",
						"server_modified": "2026-08-20T10:00:00Z"},
					{".tag": "folder", "name": "journal", "path_display": "/Notes/journal"},
				},
				"cursor": "next", "has_more": true,
			})
		case "/2/files/list_folder/continue":
			var req struct {
				Cursor string `json:"cursor"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			if req.Cursor != "next" {
				t.Errorf("continue cursor = %q", req.Cursor)
			}
			page++
			json.NewEncoder(w).Encode(map[string]any{
				"entries": []map[string]any{
					{".tag": "file", "name": "2026-08-23.md", "path_display": "/Notes/journal/2026-08-23.md",
						"size": 3, "rev": "0456", "content_hash": "bbb"},
					{".tag": "file", "name": "config", "path_display": "/Notes/.obsidian/config",
						"size": 1, "rev": "0789", "content_hash": "ccc"},
				},
				"has_more": false,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	d := NewDropbox(Credentials{ClientID: "app-key"}, api.Client(), fixedNow)
	d.APIBase = api.URL

	files, err := d.ListTree(context.Background(), "token", "/Notes")
	if err != nil {
		t.Fatalf("ListTree: %v", err)
	}
	if page != 2 {
		t.Errorf("pages fetched = %d; want 2", page)
	}
	if len(files) != 2 {
		t.Fatalf("files = %+v", files)
	}
	if files[0].Rel != "Idea.md" || files[0].Hash != "aaa" || files[0].Rev != "0123" ||
		files[0].ModifiedMs != time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC).UnixMilli() {
		t.Errorf("first file = %+v", files[0])
	}
	if files[1].Rel != "journal/2026-08-23.md" {
		t.Errorf("nested file = %+v", files[1])
	}
}

func TestDropboxDownloadAndDeleteFile(t *testing.T) {
	var downloadArg, deletePath string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		deletePath = req.Path
		w.Write([]byte("{}"))
	}))
	defer api.Close()
	content := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloadArg = r.Header.Get("Dropbox-API-Arg")
		w.Write([]byte("note bytes"))
	}))
	defer content.Close()

	d := NewDropbox(Credentials{ClientID: "app-key"}, api.Client(), fixedNow)
	d.APIBase = api.URL
	d.ContentBase = content.URL

	data, err := d.DownloadFile(context.Background(), "token", "/Notes", "journal/2026-08-23.md")
	if err != nil {
		t.Fatalf("DownloadFile: %v", err)
	}
	if string(data) != "note bytes" {
		t.Errorf("data = %q", data)
	}
	var arg struct {
		Path string `json:"path"`
	}
	json.Unmarshal([]byte(downloadArg), &arg)
	if arg.Path != "/Notes/journal/2026-08-23.md" {
		t.Errorf("download arg = %s", downloadArg)
	}

	if err := d.DeleteFile(context.Background(), "token", "/Notes", "Idea.md"); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}
	if deletePath != "/Notes/Idea.md" {
		t.Errorf("delete path = %q", deletePath)
	}
}

// A file already gone is what the caller wanted; not an error.
func TestDropboxDeleteMissingFileSucceeds(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error_summary":"path_lookup/not_found/..."}`))
	}))
	defer api.Close()
	d := NewDropbox(Credentials{ClientID: "app-key"}, api.Client(), fixedNow)
	d.APIBase = api.URL
	if err := d.DeleteFile(context.Background(), "token", "/Notes", "gone.md"); err != nil {
		t.Errorf("deleting a missing file = %v; want nil", err)
	}
}

func TestDropboxListFoldersAndUploadFile(t *testing.T) {
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
		json.NewEncoder(w).Encode(map[string]any{
			"rev": "0abc", "size": 8, "content_hash": "hash-from-dropbox",
		})
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

	// No rev: an unconditional overwrite, which is what a first upload (or a
	// resolved conflict) is.
	out, err := d.UploadFile(context.Background(), "token", "/Notes", "sub/Idea.md", []byte("idea..."), "")
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if out.Rev != "0abc" || out.Hash != "hash-from-dropbox" {
		t.Errorf("uploaded = %+v", out)
	}
	var arg struct {
		Path       string `json:"path"`
		Mode       any    `json:"mode"`
		Autorename bool   `json:"autorename"`
	}
	json.Unmarshal([]byte(uploadArg), &arg)
	if arg.Path != "/Notes/sub/Idea.md" || arg.Mode != "overwrite" || arg.Autorename {
		t.Errorf("upload arg = %s", uploadArg)
	}
	if uploadBody != "idea..." {
		t.Errorf("upload body = %q", uploadBody)
	}

	// With a rev the write must be conditional — that is the guard against a
	// change made in Dropbox between this sync's listing and its upload.
	if _, err := d.UploadFile(context.Background(), "token", "/Notes", "Idea.md", []byte("x"), "0123"); err != nil {
		t.Fatalf("conditional UploadFile: %v", err)
	}
	var conditional struct {
		Mode struct {
			Tag    string `json:".tag"`
			Update string `json:"update"`
		} `json:"mode"`
	}
	json.Unmarshal([]byte(uploadArg), &conditional)
	if conditional.Mode.Tag != "update" || conditional.Mode.Update != "0123" {
		t.Errorf("conditional upload arg = %s", uploadArg)
	}
}

// A refused conditional upload is not a failure — it is a conflict, and the
// engine has to be able to tell them apart.
func TestDropboxUploadRevisionConflict(t *testing.T) {
	content := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		w.Write([]byte(`{"error_summary":"path/conflict/file/..."}`))
	}))
	defer content.Close()
	d := NewDropbox(Credentials{ClientID: "app-key"}, api(t), fixedNow)
	d.ContentBase = content.URL
	_, err := d.UploadFile(context.Background(), "token", "/Notes", "Idea.md", []byte("x"), "0123")
	if !errors.Is(err, ErrRevisionConflict) {
		t.Errorf("stale-rev upload = %v; want ErrRevisionConflict", err)
	}
}

// api is a throwaway client for tests that only exercise the content host.
func api(t *testing.T) *http.Client {
	t.Helper()
	return http.DefaultClient
}
