package api

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinmay28/thought-mesh/server/internal/cloud"
	"github.com/chinmay28/thought-mesh/server/internal/mesh"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// stubProvider implements cloud.Provider for route-level tests.
type stubProvider struct{ creds cloud.Credentials }

func (p *stubProvider) ID() string              { return "dropbox" }
func (p *stubProvider) Name() string            { return "Dropbox" }
func (p *stubProvider) Configured() bool        { return p.creds.Set() }
func (p *stubProvider) RequiresSecret() bool    { return false }
func (p *stubProvider) SetupURL() string        { return "https://example.com/apps" }
func (p *stubProvider) SupportsCodePaste() bool { return true }

func (p *stubProvider) BuiltinCredentials() cloud.Credentials { return p.creds }

func (p *stubProvider) WithCredentials(c cloud.Credentials) cloud.Provider {
	next := *p
	next.creds = c
	return &next
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

func (p *stubProvider) Upload(context.Context, string, string, string, []byte) error { return nil }

func newCloudServer(t *testing.T) http.Handler {
	t.Helper()
	v, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	svc := cloud.NewService(
		cloud.NewStore(filepath.Join(t.TempDir(), "cloud.json")),
		cloud.Registry{&stubProvider{creds: cloud.Credentials{ClientID: "app"}}},
		v.Zip,
		func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
		"")
	return New(v, mesh.New(v), svc)
}

func TestCloudRoutesAbsentWithoutService(t *testing.T) {
	h := newServer(t) // built with a nil cloud service
	rec := do(t, h, "GET", "/api/cloud/sync", "")
	if rec.Code != 404 {
		t.Errorf("GET /api/cloud/sync without service = %d; want 404", rec.Code)
	}
}

func TestCloudSettingsShape(t *testing.T) {
	h := newCloudServer(t)
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
	h := newCloudServer(t)

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
	if run["file_name"] == "" || run["bytes"] == float64(0) {
		t.Errorf("run = %v", run)
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
	h := newCloudServer(t)

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

func TestCloudCallbackRedirects(t *testing.T) {
	h := newCloudServer(t)
	rec := do(t, h, "GET", cloud.CallbackPath+"?error=access_denied", "")
	if rec.Code != 302 {
		t.Fatalf("callback = %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if loc == "" || loc[:6] != "/sync?" {
		t.Errorf("callback location = %q", loc)
	}
}
