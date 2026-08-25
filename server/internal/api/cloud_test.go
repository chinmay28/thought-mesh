package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/chinmay28/thought-mesh/server/internal/cloud"
	"github.com/chinmay28/thought-mesh/server/internal/mesh"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// stubProvider implements cloud.Provider for route-level tests, holding the
// remote folder in memory.
type stubProvider struct {
	creds cloud.Credentials
	tree  map[string][]byte
	revs  int
}

func (p *stubProvider) ID() string              { return "dropbox" }
func (p *stubProvider) Name() string            { return "Dropbox" }
func (p *stubProvider) Configured() bool        { return p.creds.Set() }
func (p *stubProvider) RequiresSecret() bool    { return false }
func (p *stubProvider) SetupURL() string        { return "https://example.com/apps" }
func (p *stubProvider) SupportsCodePaste() bool { return true }

func (p *stubProvider) BuiltinCredentials() cloud.Credentials { return p.creds }

// WithCredentials keeps the same remote tree — the tests assert against it
// after the service has rebound credentials.
func (p *stubProvider) WithCredentials(c cloud.Credentials) cloud.Provider {
	p.creds = c
	return p
}

func (p *stubProvider) put(rel string, data []byte) {
	if p.tree == nil {
		p.tree = map[string][]byte{}
	}
	p.revs++
	p.tree[rel] = data
}

func (p *stubProvider) AuthorizeURL(redirectURI, state, challenge string) string {
	return "https://example.com/authorize"
}

func (p *stubProvider) Exchange(context.Context, string, string, string) (cloud.Token, cloud.Account, error) {
	return cloud.Token{AccessToken: "at", RefreshToken: "rt"}, cloud.Account{Label: "user@example.com"}, nil
}

func (p *stubProvider) Refresh(context.Context, string) (cloud.Token, error) {
	return cloud.Token{AccessToken: "at2"}, nil
}

func (p *stubProvider) ListFolders(context.Context, string, string) ([]cloud.Folder, error) {
	return []cloud.Folder{{ID: "/Notes", Name: "Notes", Path: "/Notes"}}, nil
}

func (p *stubProvider) ListTree(context.Context, string, string) ([]cloud.RemoteFile, error) {
	out := []cloud.RemoteFile{}
	for rel, data := range p.tree {
		out = append(out, cloud.RemoteFile{
			Rel: rel, Hash: stubHash(data), Rev: "r1", Size: int64(len(data)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

func (p *stubProvider) UploadFile(_ context.Context, _, _, rel string, data []byte, _ string) (cloud.RemoteFile, error) {
	p.put(rel, data)
	return cloud.RemoteFile{Rel: rel, Hash: stubHash(data), Rev: "r1", Size: int64(len(data))}, nil
}

func (p *stubProvider) DownloadFile(_ context.Context, _, _, rel string) ([]byte, error) {
	data, ok := p.tree[rel]
	if !ok {
		return nil, errors.New("not found: " + rel)
	}
	return data, nil
}

func (p *stubProvider) DeleteFile(_ context.Context, _, _, rel string) error {
	delete(p.tree, rel)
	return nil
}

// stubHash stands in for the provider's content hash. Only its agreement with
// itself matters here — the real algorithm is pinned in internal/cloud.
func stubHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func newCloudServer(t *testing.T) (http.Handler, *vault.Vault, *stubProvider) {
	t.Helper()
	v, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	provider := &stubProvider{creds: cloud.Credentials{ClientID: "app"}}
	settings := filepath.Join(t.TempDir(), "cloud.json")
	svc := cloud.NewService(
		cloud.NewStore(settings),
		cloud.NewStateStore(settings),
		v,
		cloud.Registry{provider},
		func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
		"")
	return New(v, mesh.New(v), svc), v, provider
}

// connected walks the paste flow and picks a folder — where every sync-level
// route test starts.
func connected(t *testing.T, h http.Handler) {
	t.Helper()
	start := decode(t, do(t, h, "POST", "/api/cloud/sync/connect", `{"provider":"dropbox","mode":"paste"}`))
	do(t, h, "POST", "/api/cloud/sync/complete",
		`{"pending_id":"`+start["pending_id"].(string)+`","code":"c"}`)
	do(t, h, "PATCH", "/api/cloud/sync", `{"folder_id":"/Notes","folder_path":"/Notes"}`)
}

func TestCloudRoutesAbsentWithoutService(t *testing.T) {
	h := newServer(t) // built with a nil cloud service
	rec := do(t, h, "GET", "/api/cloud/sync", "")
	if rec.Code != 404 {
		t.Errorf("GET /api/cloud/sync without service = %d; want 404", rec.Code)
	}
}

func TestCloudSettingsShape(t *testing.T) {
	h, _, _ := newCloudServer(t)
	rec := do(t, h, "GET", "/api/cloud/sync", "")
	if rec.Code != 200 {
		t.Fatalf("settings = %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	settings := body["settings"].(map[string]any)
	if settings["connected"] != float64(0) || settings["frequency"] != "off" {
		t.Errorf("settings = %v", settings)
	}
	// The public shape must never leak a token, even as a null-valued key.
	for _, forbidden := range []string{"access_token", "refresh_token", "token_expires_at"} {
		if _, ok := settings[forbidden]; ok {
			t.Errorf("settings leaks %q", forbidden)
		}
	}
	providers := body["providers"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers = %v", providers)
	}
	p := providers[0].(map[string]any)
	if p["id"] != "dropbox" || p["configured"] != float64(1) ||
		p["supports_code_paste"] != float64(1) || p["client_id"] != "app" {
		t.Errorf("provider = %v", p)
	}
	if body["redirect_uri"] == "" {
		t.Errorf("no redirect_uri in %v", body)
	}
}

func TestCloudConnectCompleteScheduleRun(t *testing.T) {
	h, _, _ := newCloudServer(t)

	// Paste-mode connect returns the authorize URL and a pending handle.
	rec := do(t, h, "POST", "/api/cloud/sync/connect", `{"provider":"dropbox","mode":"paste"}`)
	if rec.Code != 200 {
		t.Fatalf("connect = %d: %s", rec.Code, rec.Body.String())
	}
	start := decode(t, rec)
	if start["mode"] != "paste" || start["pending_id"] == "" || start["authorize_url"] == "" {
		t.Fatalf("start = %v", start)
	}

	rec = do(t, h, "POST", "/api/cloud/sync/complete",
		`{"pending_id":"`+start["pending_id"].(string)+`","code":" the-code \n"}`)
	if rec.Code != 200 {
		t.Fatalf("complete = %d: %s", rec.Code, rec.Body.String())
	}
	settings := decode(t, rec)["settings"].(map[string]any)
	if settings["connected"] != float64(1) || settings["account_label"] != "user@example.com" {
		t.Errorf("after connect = %v", settings)
	}

	// Folder then schedule.
	rec = do(t, h, "PATCH", "/api/cloud/sync", `{"folder_id":"/Notes","folder_path":"/Notes"}`)
	if rec.Code != 200 {
		t.Fatalf("folder = %d: %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, "PATCH", "/api/cloud/sync", `{"frequency":"daily"}`)
	settings = decode(t, rec)["settings"].(map[string]any)
	if settings["frequency"] != "daily" || settings["next_run_at"] == nil {
		t.Errorf("schedule = %v", settings)
	}
	// A bad frequency is a 400 with an error body.
	rec = do(t, h, "PATCH", "/api/cloud/sync", `{"frequency":"fortnightly"}`)
	if rec.Code != 400 || decode(t, rec)["error"] == "" {
		t.Errorf("bad frequency = %d %s", rec.Code, rec.Body.String())
	}

	// Folder browsing and a manual run.
	rec = do(t, h, "GET", "/api/cloud/sync/folders", "")
	folders := decode(t, rec)["folders"].([]any)
	if len(folders) != 1 {
		t.Errorf("folders = %v", folders)
	}
	rec = do(t, h, "POST", "/api/cloud/sync/run", "{}")
	if rec.Code != 200 {
		t.Fatalf("run = %d: %s", rec.Code, rec.Body.String())
	}
	run := decode(t, rec)
	result := run["result"].(map[string]any)
	for _, key := range []string{"uploaded", "downloaded", "deleted_local", "deleted_remote", "conflicts"} {
		if _, ok := result[key]; !ok {
			t.Errorf("result is missing %q: %v", key, result)
		}
	}
	settings = run["settings"].(map[string]any)
	if settings["last_status"] != "ok" {
		t.Errorf("run settings = %v", settings)
	}

	// Disconnect clears the account.
	rec = do(t, h, "POST", "/api/cloud/sync/disconnect", "{}")
	settings = decode(t, rec)["settings"].(map[string]any)
	if settings["connected"] != float64(0) {
		t.Errorf("after disconnect = %v", settings)
	}
}

func TestCloudCredentialRoutes(t *testing.T) {
	h, _, _ := newCloudServer(t)

	rec := do(t, h, "PUT", "/api/cloud/sync/providers/dropbox", `{"client_id":"has space"}`)
	if rec.Code != 400 {
		t.Errorf("bad client id = %d", rec.Code)
	}
	rec = do(t, h, "PUT", "/api/cloud/sync/providers/dropbox", `{"client_id":"newapp"}`)
	if rec.Code != 200 {
		t.Fatalf("set credentials = %d: %s", rec.Code, rec.Body.String())
	}
	p := decode(t, rec)["providers"].([]any)[0].(map[string]any)
	if p["client_id"] != "newapp" || p["source"] != "settings" {
		t.Errorf("provider = %v", p)
	}
	rec = do(t, h, "DELETE", "/api/cloud/sync/providers/dropbox", "")
	p = decode(t, rec)["providers"].([]any)[0].(map[string]any)
	if p["client_id"] != "app" || p["source"] != "server" {
		t.Errorf("after clear = %v", p)
	}
	// An unknown provider is a 400 setup error, not a 404 route miss.
	rec = do(t, h, "PUT", "/api/cloud/sync/providers/nope", `{"client_id":"x"}`)
	if rec.Code != 400 {
		t.Errorf("unknown provider = %d", rec.Code)
	}
}

// The sync routes end to end: push a note up, pull one down, then manufacture
// a conflict and settle it three different ways through the API.
func TestCloudSyncRoutesMirrorTheTree(t *testing.T) {
	h, v, provider := newCloudServer(t)
	connected(t, h)

	if _, err := v.Write("Local.md", "written here\n"); err != nil {
		t.Fatal(err)
	}
	provider.put("Remote.md", []byte("written there\n"))

	rec := do(t, h, "POST", "/api/cloud/sync/run", "{}")
	if rec.Code != 200 {
		t.Fatalf("sync = %d: %s", rec.Code, rec.Body.String())
	}
	result := decode(t, rec)["result"].(map[string]any)
	if result["uploaded"] != float64(1) || result["downloaded"] != float64(1) {
		t.Fatalf("result = %v", result)
	}
	if content, _, err := v.Read("Remote.md"); err != nil || content != "written there\n" {
		t.Errorf("pulled note = %q, %v", content, err)
	}
	if string(provider.tree["Local.md"]) != "written here\n" {
		t.Errorf("pushed note = %q", provider.tree["Local.md"])
	}
}

func TestCloudConflictRoutes(t *testing.T) {
	h, v, provider := newCloudServer(t)
	connected(t, h)

	if _, err := v.Write("Idea.md", "one\ntwo\n"); err != nil {
		t.Fatal(err)
	}
	do(t, h, "POST", "/api/cloud/sync/run", "{}")

	// Both sides move, on the same line, so no merge can settle it alone.
	if _, err := v.Write("Idea.md", "one mine\ntwo\n"); err != nil {
		t.Fatal(err)
	}
	provider.put("Idea.md", []byte("one theirs\ntwo\n"))
	rec := do(t, h, "POST", "/api/cloud/sync/run", "{}")
	if rec.Code != 200 {
		t.Fatalf("sync = %d: %s", rec.Code, rec.Body.String())
	}
	conflicts := decode(t, rec)["result"].(map[string]any)["conflicts"].([]any)
	if len(conflicts) != 1 {
		t.Fatalf("conflicts = %v", conflicts)
	}
	c := conflicts[0].(map[string]any)
	if c["path"] != "Idea.md" || c["mergeable"] != float64(1) {
		t.Errorf("conflict = %v", c)
	}

	// The settings payload carries them too, so the page renders in one trip.
	if got := decode(t, do(t, h, "GET", "/api/cloud/sync", ""))["conflicts"].([]any); len(got) != 1 {
		t.Errorf("settings conflicts = %v", got)
	}

	// The detail view: both versions, plus a merge with the contested region
	// marked for a human.
	rec = do(t, h, "GET", "/api/cloud/sync/conflicts/Idea.md", "")
	if rec.Code != 200 {
		t.Fatalf("detail = %d: %s", rec.Code, rec.Body.String())
	}
	detail := decode(t, rec)
	if detail["local"] != "one mine\ntwo\n" || detail["remote"] != "one theirs\ntwo\n" {
		t.Errorf("detail = %v", detail)
	}
	if detail["merge_conflicts"] != float64(1) {
		t.Errorf("same-line edits should not merge cleanly: %v", detail["merged"])
	}
	if !strings.Contains(detail["merged"].(string), "<<<<<<<") {
		t.Errorf("merged = %q", detail["merged"])
	}

	// Resolve by writing a merged text to both sides.
	rec = do(t, h, "POST", "/api/cloud/sync/resolve",
		`{"path":"Idea.md","resolution":"merge","content":"one settled\ntwo\n"}`)
	if rec.Code != 200 {
		t.Fatalf("resolve = %d: %s", rec.Code, rec.Body.String())
	}
	if got := decode(t, rec)["conflicts"].([]any); len(got) != 0 {
		t.Errorf("conflict survived resolution: %v", got)
	}
	if content, _, err := v.Read("Idea.md"); err != nil || content != "one settled\ntwo\n" {
		t.Errorf("local = %q, %v", content, err)
	}
	if string(provider.tree["Idea.md"]) != "one settled\ntwo\n" {
		t.Errorf("remote = %q", provider.tree["Idea.md"])
	}

	// A resolve with no path, and one for a path that isn't contested.
	if rec = do(t, h, "POST", "/api/cloud/sync/resolve", `{"resolution":"merge"}`); rec.Code != 400 {
		t.Errorf("resolve without a path = %d", rec.Code)
	}
	rec = do(t, h, "POST", "/api/cloud/sync/resolve", `{"path":"Nope.md","resolution":"keep_local"}`)
	if rec.Code != 404 {
		t.Errorf("resolving a path with no conflict = %d; want 404", rec.Code)
	}
}

// Restoring a pre-sync backup is the undo path for a sync that pulled down
// something unwelcome.
func TestCloudBackupRoutes(t *testing.T) {
	h, v, provider := newCloudServer(t)
	connected(t, h)

	if _, err := v.Write("Idea.md", "the version I want back\n"); err != nil {
		t.Fatal(err)
	}
	do(t, h, "POST", "/api/cloud/sync/run", "{}")

	provider.put("Idea.md", []byte("something unwelcome\n"))
	rec := do(t, h, "POST", "/api/cloud/sync/run", "{}")
	backup := decode(t, rec)["result"].(map[string]any)["backup_file"].(string)
	if backup == "" {
		t.Fatal("a run that replaced a local note should have backed it up")
	}

	rec = do(t, h, "GET", "/api/cloud/sync/backups", "")
	if backups := decode(t, rec)["backups"].([]any); len(backups) != 1 {
		t.Fatalf("backups = %v", backups)
	}
	if rec = do(t, h, "POST", "/api/cloud/sync/backups/restore", `{}`); rec.Code != 400 {
		t.Errorf("restore without a name = %d", rec.Code)
	}
	rec = do(t, h, "POST", "/api/cloud/sync/backups/restore", `{"name":"`+backup+`"}`)
	if rec.Code != 200 {
		t.Fatalf("restore = %d: %s", rec.Code, rec.Body.String())
	}
	if content, _, err := v.Read("Idea.md"); err != nil || content != "the version I want back\n" {
		t.Errorf("restored note = %q, %v", content, err)
	}
}

func TestCloudCallbackRedirects(t *testing.T) {
	h, _, _ := newCloudServer(t)
	rec := do(t, h, "GET", cloud.CallbackPath+"?error=access_denied", "")
	if rec.Code != 302 {
		t.Fatalf("callback = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc == "" || loc[:6] != "/sync?" {
		t.Errorf("callback location = %q", loc)
	}
}
