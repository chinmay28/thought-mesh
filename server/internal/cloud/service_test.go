package cloud

import (
	"context"
	"strings"
	"testing"
	"time"
)

// fakeProvider is a scriptable Provider for service-level tests. `builtin`
// stands for the credentials a real provider is constructed with (the startup
// flag); `creds` is whatever WithCredentials bound most recently.
// WithCredentials returns the same instance so the test keeps seeing the
// counters the service increments.
type fakeProvider struct {
	id        string
	builtin   Credentials
	creds     Credentials
	paste     bool
	exchanged []string // codes seen by Exchange
	refreshed int
	uploads   []fakeUpload
	failNext  error // returned by the next Upload
	token     Token
	account   Account
}

type fakeUpload struct {
	folderID, name string
	data           []byte
}

func (f *fakeProvider) ID() string   { return f.id }
func (f *fakeProvider) Name() string { return "Fake" }

func (f *fakeProvider) Configured() bool { return f.creds.Set() || f.builtin.Set() }

func (f *fakeProvider) BuiltinCredentials() Credentials { return f.builtin }

func (f *fakeProvider) WithCredentials(c Credentials) Provider {
	f.creds = c
	return f
}

func (f *fakeProvider) RequiresSecret() bool    { return false }
func (f *fakeProvider) SetupURL() string        { return "https://fake.example/apps" }
func (f *fakeProvider) SupportsCodePaste() bool { return f.paste }

func (f *fakeProvider) AuthorizeURL(redirectURI, state, challenge string) string {
	return "https://fake.example/authorize?state=" + state
}

func (f *fakeProvider) Exchange(_ context.Context, code, _, _ string) (Token, Account, error) {
	f.exchanged = append(f.exchanged, code)
	return f.token, f.account, nil
}

func (f *fakeProvider) Refresh(_ context.Context, refreshToken string) (Token, error) {
	f.refreshed++
	return Token{AccessToken: "refreshed-at", RefreshToken: refreshToken,
		ExpiresAt: time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)}, nil
}

func (f *fakeProvider) ListFolders(_ context.Context, _, _ string) ([]Folder, error) {
	return []Folder{{ID: "/Notes", Name: "Notes", Path: "/Notes"}}, nil
}

func (f *fakeProvider) Upload(_ context.Context, _, folderID, name string, data []byte) error {
	if f.failNext != nil {
		err := f.failNext
		f.failNext = nil
		return err
	}
	f.uploads = append(f.uploads, fakeUpload{folderID: folderID, name: name, data: data})
	return nil
}

// newTestService wires a service around the fake provider with a pinned,
// advanceable clock.
func newTestService(t *testing.T, f *fakeProvider) (*Service, *time.Time) {
	t.Helper()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	svc := NewService(
		newStore(t),
		Registry{f},
		func() ([]byte, error) { return []byte("vault-zip"), nil },
		func() time.Time { return now },
		"")
	return svc, &now
}

// connect walks the paste flow to a connected account.
func connect(t *testing.T, svc *Service) {
	t.Helper()
	start, err := svc.StartConnect("fake", "http://192.168.1.10:8788", true)
	if err != nil {
		t.Fatalf("StartConnect: %v", err)
	}
	if start.Mode != ModePaste || start.PendingID == "" {
		t.Fatalf("start = %+v", start)
	}
	if _, err := svc.CompleteConnect(context.Background(), start.PendingID, "code-1"); err != nil {
		t.Fatalf("CompleteConnect: %v", err)
	}
}

func stdFake() *fakeProvider {
	return &fakeProvider{
		id:      "fake",
		builtin: Credentials{ClientID: "app"},
		paste:   true,
		token: Token{AccessToken: "at", RefreshToken: "rt",
			ExpiresAt: time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)},
		account: Account{Label: "user@example.com"},
	}
}

func TestConnectChooseFolderScheduleAndRun(t *testing.T) {
	f := stdFake()
	svc, now := newTestService(t, f)
	connect(t, svc)

	set, _ := svc.Settings()
	if !set.Connected() || *set.AccountLabel != "user@example.com" || set.Frequency != FrequencyOff {
		t.Fatalf("after connect: %+v", set)
	}
	// Tokens never appear in the public projection.
	pub := set.Public()
	if pub.Connected != 1 || pub.AccountLabel == nil {
		t.Errorf("public = %+v", pub)
	}

	// Scheduling before a folder exists is refused... after a folder, allowed.
	if _, err := svc.Update(ptr(FrequencyDaily), nil, nil); err != nil {
		t.Fatalf("frequency with account: %v", err)
	}
	set, _ = svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	if *set.FolderID != "/Notes" {
		t.Fatalf("folder = %+v", set)
	}
	if set.NextRunAt == nil {
		t.Fatal("schedule has no next_run_at")
	}
	next, _ := parseISO(*set.NextRunAt)
	if want := now.AddDate(0, 0, 1); !next.Equal(want) {
		t.Errorf("next run %v; want %v", next, want)
	}

	// Not due yet; due after a day passes.
	if ran, _ := svc.RunIfDue(context.Background()); ran {
		t.Error("ran before the deadline")
	}
	*now = now.AddDate(0, 0, 1)
	ran, err := svc.RunIfDue(context.Background())
	if !ran || err != nil {
		t.Fatalf("RunIfDue = %v, %v", ran, err)
	}
	if len(f.uploads) != 1 {
		t.Fatalf("uploads = %+v", f.uploads)
	}
	up := f.uploads[0]
	if up.folderID != "/Notes" || string(up.data) != "vault-zip" ||
		!strings.HasPrefix(up.name, "thoughtmesh-") || !strings.HasSuffix(up.name, ".vault.zip") {
		t.Errorf("upload = %+v", up)
	}

	set, _ = svc.Settings()
	if *set.LastStatus != StatusOK || *set.LastFileName != up.name || set.LastError != nil {
		t.Errorf("after run: %+v", set)
	}
	next, _ = parseISO(*set.NextRunAt)
	if want := now.AddDate(0, 0, 1); !next.Equal(want) {
		t.Errorf("rebased next run %v; want %v", next, want)
	}
}

func TestScheduleRequiresConnection(t *testing.T) {
	svc, _ := newTestService(t, stdFake())
	_, err := svc.Update(ptr(FrequencyDaily), nil, nil)
	if !IsConfigError(err) {
		t.Errorf("scheduling unconnected = %v", err)
	}
}

func TestRunRecordsFailureAndReschedules(t *testing.T) {
	f := stdFake()
	svc, now := newTestService(t, f)
	connect(t, svc)
	svc.Update(ptr(FrequencyHourly), ptr("/Notes"), ptr("/Notes"))

	f.failNext = &ProviderError{Provider: "Fake", Err: context.DeadlineExceeded}
	if _, err := svc.Run(context.Background()); err == nil {
		t.Fatal("Run should surface the failure")
	}
	set, _ := svc.Settings()
	if *set.LastStatus != StatusError || set.LastError == nil {
		t.Errorf("failure not recorded: %+v", set)
	}
	next, _ := parseISO(*set.NextRunAt)
	if want := now.Add(time.Hour); !next.Equal(want) {
		t.Errorf("failed run must still reschedule: %v; want %v", next, want)
	}
}

func TestExpiredTokenIsRefreshedBeforeUse(t *testing.T) {
	f := stdFake()
	// Token that expires two hours from the pinned now.
	svc, now := newTestService(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))

	*now = now.Add(5 * time.Hour) // past the 16:00 expiry
	if _, err := svc.ListFolders(context.Background(), ""); err != nil {
		t.Fatalf("ListFolders: %v", err)
	}
	if f.refreshed != 1 {
		t.Errorf("refreshed = %d; want 1", f.refreshed)
	}
	set, _ := svc.Settings()
	if *set.AccessToken != "refreshed-at" || *set.RefreshToken != "rt" {
		t.Errorf("token not persisted: %+v", set)
	}
}

func TestChangingClientIDDisconnects(t *testing.T) {
	f := stdFake()
	svc, _ := newTestService(t, f)
	connect(t, svc)

	// Same id again (fixing a secret) keeps the connection.
	if err := svc.SetCredentials("fake", "app", "newsecret"); err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	if set, _ := svc.Settings(); !set.Connected() {
		t.Fatal("re-saving the same client id must keep the account")
	}
	// A different id invalidates the tokens it didn't mint.
	if err := svc.SetCredentials("fake", "other-app", ""); err != nil {
		t.Fatalf("SetCredentials: %v", err)
	}
	if set, _ := svc.Settings(); set.Connected() {
		t.Fatal("changing the client id must disconnect")
	}
}

func TestDisconnectDropsTokens(t *testing.T) {
	svc, _ := newTestService(t, stdFake())
	connect(t, svc)
	set, err := svc.Disconnect()
	if err != nil {
		t.Fatalf("Disconnect: %v", err)
	}
	if set.Connected() || set.AccessToken != nil || set.RefreshToken != nil {
		t.Errorf("after disconnect: %+v", set)
	}
}

func TestRedirectSupported(t *testing.T) {
	svc, _ := newTestService(t, stdFake())
	cases := map[string]bool{
		"https://mesh.example.com": true,
		"http://localhost:8788":    true,
		"http://127.0.0.1:8788":    true,
		"http://192.168.1.10:8788": false,
		"http://mesh.local":        false,
	}
	for origin, want := range cases {
		if got := svc.RedirectSupported(origin); got != want {
			t.Errorf("RedirectSupported(%s) = %v; want %v", origin, got, want)
		}
	}
	svc.PublicURL = "https://pinned.example.com"
	if !svc.RedirectSupported("http://192.168.1.10:8788") {
		t.Error("a pinned https public URL should enable redirects")
	}
}

func TestPendingStateIsSingleUse(t *testing.T) {
	svc, _ := newTestService(t, stdFake())
	start, err := svc.StartConnect("fake", "http://x", true)
	if err != nil {
		t.Fatalf("StartConnect: %v", err)
	}
	if _, err := svc.CompleteConnect(context.Background(), start.PendingID, "c"); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if _, err := svc.CompleteConnect(context.Background(), start.PendingID, "c"); !IsConfigError(err) {
		t.Errorf("replayed state = %v; want ConfigError", err)
	}
}
