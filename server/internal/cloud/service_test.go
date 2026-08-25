package cloud

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// fakeProvider is a scriptable Provider standing in for Dropbox: it keeps the
// remote folder as an in-memory tree, with the same revision semantics the real
// one has (a conditional upload against a stale rev is refused).
//
// `builtin` stands for the credentials a real provider is constructed with (the
// startup flag); `creds` is whatever WithCredentials bound most recently.
// WithCredentials returns the same instance so the test keeps seeing the
// counters the service increments.
type fakeProvider struct {
	id        string
	builtin   Credentials
	creds     Credentials
	paste     bool
	exchanged []string // codes seen by Exchange
	refreshed int
	token     Token
	account   Account

	// trees is the account: one map of files per folder, keyed by path within
	// that folder. Folders are distinct on purpose — pointing sync somewhere
	// else has to look like an empty destination, not the same one renamed.
	trees map[string]map[string]*fakeRemote
	revs  int
	// failNext is returned by the next listing.
	failNext error
	// failUpload is returned by the next upload.
	failUpload error
	// staleRev makes the next conditional upload of this path report that the
	// remote moved — the race a two-way sync has to survive.
	staleRev  string
	uploads   []string
	downloads []string
	deletes   []string
}

type fakeRemote struct {
	data []byte
	rev  string
}

// defaultFolder is where the tests point sync, and what f.put / f.tree mean.
const defaultFolder = "/Notes"

// at returns one folder's files, creating the map on first use.
func (f *fakeProvider) at(folderID string) map[string]*fakeRemote {
	if f.trees == nil {
		f.trees = map[string]map[string]*fakeRemote{}
	}
	if f.trees[folderID] == nil {
		f.trees[folderID] = map[string]*fakeRemote{}
	}
	return f.trees[folderID]
}

// tree is the folder the tests sync into.
func (f *fakeProvider) tree() map[string]*fakeRemote { return f.at(defaultFolder) }

// remote is the bytes of one file in that folder, "" when it isn't there.
func (f *fakeProvider) remote(rel string) string {
	entry, ok := f.tree()[rel]
	if !ok {
		return ""
	}
	return string(entry.data)
}

// put writes straight into the synced folder — a change made "in Dropbox".
func (f *fakeProvider) put(rel string, data []byte) {
	f.revs++
	f.tree()[rel] = &fakeRemote{data: append([]byte(nil), data...), rev: revName(f.revs)}
}

// drop removes a file from the synced folder, as deleting it there would.
func (f *fakeProvider) drop(rel string) { delete(f.tree(), rel) }

func revName(n int) string { return "rev" + string(rune('a'+n%26)) + itoa(n) }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
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

func (f *fakeProvider) ListTree(_ context.Context, _, folderID string) ([]RemoteFile, error) {
	if err := f.take(&f.failNext); err != nil {
		return nil, err
	}
	out := []RemoteFile{}
	for rel, entry := range f.at(folderID) {
		out = append(out, RemoteFile{
			Rel:  rel,
			Hash: contentHash(entry.data),
			Rev:  entry.rev,
			Size: int64(len(entry.data)),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, nil
}

func (f *fakeProvider) UploadFile(_ context.Context, _, folderID, rel string, data []byte, rev string) (RemoteFile, error) {
	if err := f.take(&f.failUpload); err != nil {
		return RemoteFile{}, err
	}
	folder := f.at(folderID)
	if rev != "" && f.staleRev == rel {
		f.staleRev = ""
		return RemoteFile{}, ErrRevisionConflict
	}
	if rev != "" {
		if current, ok := folder[rel]; !ok || current.rev != rev {
			return RemoteFile{}, ErrRevisionConflict
		}
	}
	f.revs++
	folder[rel] = &fakeRemote{data: append([]byte(nil), data...), rev: revName(f.revs)}
	f.uploads = append(f.uploads, rel)
	return RemoteFile{Rel: rel, Hash: contentHash(data), Rev: folder[rel].rev, Size: int64(len(data))}, nil
}

func (f *fakeProvider) DownloadFile(_ context.Context, _, folderID, rel string) ([]byte, error) {
	f.downloads = append(f.downloads, rel)
	entry, ok := f.at(folderID)[rel]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), entry.data...), nil
}

func (f *fakeProvider) DeleteFile(_ context.Context, _, folderID, rel string) error {
	f.deletes = append(f.deletes, rel)
	delete(f.at(folderID), rel)
	return nil
}

// take consumes a scripted one-shot failure.
func (f *fakeProvider) take(slot *error) error {
	if *slot == nil {
		return nil
	}
	err := *slot
	*slot = nil
	return err
}

// newTestService wires a service around the fake provider, a real vault in a
// temporary directory, and a pinned, advanceable clock. Using the real vault
// rather than a stub is deliberate: the sync engine's whole job is deciding
// what to do with files on disk.
func newTestService(t *testing.T, f *fakeProvider) (*Service, *time.Time) {
	svc, now, _ := newTestServiceVault(t, f)
	return svc, now
}

func newTestServiceVault(t *testing.T, f *fakeProvider) (*Service, *time.Time, *vault.Vault) {
	t.Helper()
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	v, err := vault.Open(filepath.Join(dir, "vault"))
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	settings := filepath.Join(dir, "thoughtmesh-cloud.json")
	svc := NewService(
		NewStore(settings),
		NewStateStore(settings),
		v,
		Registry{f},
		func() time.Time { return now },
		"")
	return svc, &now, v
}

// writeNote puts a file in the vault the way the app would.
func writeNote(t *testing.T, v *vault.Vault, rel, content string) {
	t.Helper()
	if _, err := v.WriteFile(rel, []byte(content)); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

// readNote reads one back, failing the test if it is gone.
func readNote(t *testing.T, v *vault.Vault, rel string) string {
	t.Helper()
	data, err := v.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// connect walks the paste flow to a connected account.
func connect(t *testing.T, svc *Service) {
	t.Helper()
	start, err := svc.StartConnect("fake", "http://192.168.1.10:8881", true)
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
	svc, now, v := newTestServiceVault(t, f)
	connect(t, svc)
	writeNote(t, v, "Idea.md", "a thought\n")

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
	if len(f.uploads) != 1 || f.uploads[0] != "Idea.md" {
		t.Fatalf("uploads = %+v", f.uploads)
	}

	set, _ = svc.Settings()
	if *set.LastStatus != StatusOK || set.LastError != nil {
		t.Errorf("after run: %+v", set)
	}
	if set.LastResult == nil || set.LastResult.Uploaded != 1 {
		t.Errorf("last result = %+v", set.LastResult)
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

	f.failNext = context.DeadlineExceeded
	if _, err := svc.Sync(context.Background()); err == nil {
		t.Fatal("Sync should surface the failure")
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
		"http://localhost:8881":    true,
		"http://127.0.0.1:8881":    true,
		"http://192.168.1.10:8881": false,
		"http://mesh.local":        false,
	}
	for origin, want := range cases {
		if got := svc.RedirectSupported(origin); got != want {
			t.Errorf("RedirectSupported(%s) = %v; want %v", origin, got, want)
		}
	}
	svc.PublicURL = "https://pinned.example.com"
	if !svc.RedirectSupported("http://192.168.1.10:8881") {
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
