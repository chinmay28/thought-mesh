package cloud

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// Frequencies the schedule accepts. "off" is a real, storable value — it's
// what a connected-but-paused account looks like, and it's the default.
const (
	FrequencyOff     = "off"
	FrequencyHourly  = "hourly"
	FrequencyDaily   = "daily"
	FrequencyWeekly  = "weekly"
	FrequencyMonthly = "monthly"
)

// Outcome of the most recent run.
const (
	StatusOK    = "ok"
	StatusError = "error"
)

// Where a provider's active credentials came from. The UI shows this so a
// user can tell an entry they can edit from one the operator pinned at
// startup.
const (
	// SourceSettings — entered on the Sync page, stored in the settings file.
	SourceSettings = "settings"
	// SourceServer — a --dropbox-client-id flag (or its env var), which the
	// settings file overrides.
	SourceServer = "server"
)

// Settings is the sync configuration. Pointer fields are the ones that are
// genuinely nullable — "no account connected", "never run". Timestamps are
// ISO 8601 strings with a local offset, matching the wire convention.
type Settings struct {
	Provider       *string `json:"provider"`
	AccountLabel   *string `json:"account_label"`
	AccessToken    *string `json:"access_token"`
	RefreshToken   *string `json:"refresh_token"`
	TokenExpiresAt *string `json:"token_expires_at"`
	FolderID       *string `json:"folder_id"`
	FolderPath     *string `json:"folder_path"`
	Frequency      string  `json:"frequency"`
	NextRunAt      *string `json:"next_run_at"`
	LastRunAt      *string `json:"last_run_at"`
	LastStatus     *string `json:"last_status"`
	LastError      *string `json:"last_error"`
	// LastResult is what the most recent run moved. Nil before the first run.
	LastResult *RunSummary `json:"last_result"`
	UpdatedAt  *string     `json:"updated_at"`
}

// RunSummary is what one sync did, in the shape both the settings file and the
// wire carry. A sync's outcome is a handful of counts rather than a file name,
// which is the whole difference between mirroring a tree and dropping an
// archive in a folder.
type RunSummary struct {
	Uploaded      int `json:"uploaded"`
	Downloaded    int `json:"downloaded"`
	DeletedLocal  int `json:"deleted_local"`
	DeletedRemote int `json:"deleted_remote"`
	Unchanged     int `json:"unchanged"`
	Conflicts     int `json:"conflicts"`
	Failed        int `json:"failed"`
}

// Connected reports whether an account is linked and usable.
func (s *Settings) Connected() bool {
	return s != nil && s.Provider != nil && *s.Provider != "" &&
		s.AccessToken != nil && *s.AccessToken != ""
}

// Scheduled reports whether the scheduler should be running this config: an
// account, a destination folder, and a frequency other than "off".
func (s *Settings) Scheduled() bool {
	return s.Connected() && s.Frequency != FrequencyOff && s.FolderID != nil
}

// PublicSettings is the wire shape. It follows the API's existing
// conventions — snake_case names, 0|1 integer flags, explicit nulls — and
// deliberately omits every token: the browser never needs one, and the
// settings screen is reachable by anyone on the network the server trusts.
type PublicSettings struct {
	Provider     *string     `json:"provider"`
	AccountLabel *string     `json:"account_label"`
	Connected    int         `json:"connected"`
	FolderID     *string     `json:"folder_id"`
	FolderPath   *string     `json:"folder_path"`
	Frequency    string      `json:"frequency"`
	NextRunAt    *string     `json:"next_run_at"`
	LastRunAt    *string     `json:"last_run_at"`
	LastStatus   *string     `json:"last_status"`
	LastError    *string     `json:"last_error"`
	LastResult   *RunSummary `json:"last_result"`
}

// Public projects the settings onto the wire shape.
func (s *Settings) Public() PublicSettings {
	connected := 0
	if s.Connected() {
		connected = 1
	}
	return PublicSettings{
		Provider:     s.Provider,
		AccountLabel: s.AccountLabel,
		Connected:    connected,
		FolderID:     s.FolderID,
		FolderPath:   s.FolderPath,
		Frequency:    s.Frequency,
		NextRunAt:    s.NextRunAt,
		LastRunAt:    s.LastRunAt,
		LastStatus:   s.LastStatus,
		LastError:    s.LastError,
		LastResult:   s.LastResult,
	}
}

// PublicProvider describes one destination the UI can offer, and everything
// the setup form needs. `configured` is 0 when no OAuth client has been
// registered for it — the button is shown either way, because "Dropbox needs
// setup" is more useful than a screen with nothing on it.
//
// `client_id` is echoed back deliberately: it is not a secret (it travels in
// the authorize URL the browser opens), and showing it is how a user checks
// that what they pasted is what got stored. The secret only ever reports
// whether one is present.
type PublicProvider struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Configured int    `json:"configured"`
	ClientID   string `json:"client_id"`
	HasSecret  int    `json:"has_secret"`
	// SecretRequired is 1 for providers that reject a PKCE-only client.
	SecretRequired int `json:"secret_required"`
	// Source is "settings", "server", or "" — see the Source* constants.
	Source string `json:"source"`
	// SetupURL is the provider's developer console, linked from the form.
	SetupURL string `json:"setup_url"`
	// SupportsCodePaste is 1 for providers that can authorize with no redirect
	// URI, showing the user a code to copy back.
	SupportsCodePaste int `json:"supports_code_paste"`
}

// storedCredentials is one provider's OAuth client as it sits in the file.
type storedCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret,omitempty"`
	UpdatedAt    string `json:"updated_at,omitempty"`
}

// storeFile is the on-disk shape: the settings plus any per-provider OAuth
// clients entered from the Sync page.
type storeFile struct {
	Settings    *Settings                    `json:"settings"`
	Credentials map[string]storedCredentials `json:"credentials,omitempty"`
}

// Store persists the sync configuration in one JSON file.
//
// The file lives OUTSIDE the vault on purpose: it holds OAuth tokens, and the
// vault is exactly the thing users sync, copy and version by other means — a
// token that rode along in it would leak with the first `git push`. It is
// written 0600, atomically (write-then-rename), and serialized by a mutex so
// the scheduler and a request can't interleave read-modify-write cycles.
type Store struct {
	Path string

	mu sync.Mutex
}

// NewStore opens (or will create on first write) the settings file at path.
func NewStore(path string) *Store { return &Store{Path: path} }

// load reads the file; a missing file is the defaults, not an error.
func (st *Store) load() (*storeFile, error) {
	data, err := os.ReadFile(st.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return &storeFile{Settings: &Settings{Frequency: FrequencyOff}}, nil
		}
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	if f.Settings == nil {
		f.Settings = &Settings{Frequency: FrequencyOff}
	}
	if f.Settings.Frequency == "" {
		f.Settings.Frequency = FrequencyOff
	}
	return &f, nil
}

func (st *Store) save(f *storeFile) error {
	if err := os.MkdirAll(filepath.Dir(st.Path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.Path + ".tmp~"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Settings reads the current configuration.
func (st *Store) Settings() (*Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := st.load()
	if err != nil {
		return nil, err
	}
	return f.Settings, nil
}

// SaveSettings replaces the settings, stamping updated_at.
func (st *Store) SaveSettings(s *Settings, nowISO string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := st.load()
	if err != nil {
		return err
	}
	s.UpdatedAt = &nowISO
	f.Settings = s
	return st.save(f)
}

// UpdateSettings applies fn to the current settings under the store's lock —
// the read-modify-write primitive the service builds on.
func (st *Store) UpdateSettings(nowISO string, fn func(*Settings)) (*Settings, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := st.load()
	if err != nil {
		return nil, err
	}
	fn(f.Settings)
	f.Settings.UpdatedAt = &nowISO
	if err := st.save(f); err != nil {
		return nil, err
	}
	return f.Settings, nil
}

// Credentials reads every stored OAuth client, keyed by provider id.
func (st *Store) Credentials() (map[string]Credentials, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := st.load()
	if err != nil {
		return nil, err
	}
	out := make(map[string]Credentials, len(f.Credentials))
	for id, c := range f.Credentials {
		out[id] = Credentials{ClientID: c.ClientID, ClientSecret: c.ClientSecret}
	}
	return out, nil
}

// SaveCredentials upserts one provider's OAuth client.
func (st *Store) SaveCredentials(provider string, creds Credentials, nowISO string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := st.load()
	if err != nil {
		return err
	}
	if f.Credentials == nil {
		f.Credentials = map[string]storedCredentials{}
	}
	f.Credentials[provider] = storedCredentials{
		ClientID:     creds.ClientID,
		ClientSecret: creds.ClientSecret,
		UpdatedAt:    nowISO,
	}
	return st.save(f)
}

// DeleteCredentials forgets one provider's stored OAuth client.
func (st *Store) DeleteCredentials(provider string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := st.load()
	if err != nil {
		return err
	}
	delete(f.Credentials, provider)
	return st.save(f)
}

// --- time representation ------------------------------------------------------

// isoFormat is ISO 8601 with milliseconds and the local offset — the same
// rendering CountRoster stores, kept for familiarity on the wire.
const isoFormat = "2006-01-02T15:04:05.000-07:00"

// toISO renders an instant in the stored representation.
func toISO(t time.Time) string { return t.Format(isoFormat) }

// parseISO reads a stored timestamp back. Forgiving about the exact shape —
// anything RFC3339-compatible parses.
func parseISO(s string) (time.Time, bool) {
	for _, layout := range []string{isoFormat, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// --- scheduling math ----------------------------------------------------------

var frequencies = map[string]bool{
	FrequencyOff:     true,
	FrequencyHourly:  true,
	FrequencyDaily:   true,
	FrequencyWeekly:  true,
	FrequencyMonthly: true,
}

// validateFrequency mirrors the domain's parser style: a bad value is a
// ValidationError, which api.handleErr already turns into a 400.
func validateFrequency(v string) error {
	if frequencies[v] {
		return nil
	}
	return &vault.ValidationError{
		Msg: `invalid frequency "` + v + `"; expected off, hourly, daily, weekly, or monthly`,
	}
}

// nextRun is when a sync on this frequency should follow one taken at
// `from`. Intervals run from the last attempt rather than snapping to a wall
// clock boundary: the point is "a snapshot at least this often", and an
// interval schedule survives a server that was asleep at midnight.
//
// Months are added calendar-wise (AddDate), so a monthly schedule keeps its
// day-of-month instead of drifting by two or three days a year.
func nextRun(from time.Time, frequency string) (time.Time, bool) {
	switch frequency {
	case FrequencyHourly:
		return from.Add(time.Hour), true
	case FrequencyDaily:
		return from.AddDate(0, 0, 1), true
	case FrequencyWeekly:
		return from.AddDate(0, 0, 7), true
	case FrequencyMonthly:
		return from.AddDate(0, 1, 0), true
	}
	return time.Time{}, false
}

// nextRunISO is nextRun in the stored representation, or nil when the
// schedule is off.
func nextRunISO(from time.Time, frequency string) *string {
	at, ok := nextRun(from, frequency)
	if !ok {
		return nil
	}
	iso := toISO(at)
	return &iso
}

// due reports whether a run is owed at `now`. A schedule with no next_run_at
// — freshly connected, or a file written by hand — is owed one immediately,
// which is also the friendliest first impression.
func (s *Settings) due(now time.Time) bool {
	if !s.Scheduled() {
		return false
	}
	if s.NextRunAt == nil {
		return true
	}
	at, ok := parseISO(*s.NextRunAt)
	if !ok {
		return true
	}
	return !at.After(now)
}

// --- credential validation ----------------------------------------------------

// maxCredentialLen bounds what the setup form will store. Real client ids and
// secrets are well under this; the limit is here so a paste accident can't put
// a megabyte in the file.
const maxCredentialLen = 512

// validateCredentials checks what a user pasted into the setup form. It is
// deliberately forgiving about *shape* — client ids are not ours to
// second-guess — and strict only about the things that would produce a broken
// authorize URL: emptiness, embedded whitespace (the classic "copied the
// surrounding line too" mistake), and control characters.
func validateCredentials(provider Provider, clientID, clientSecret string) (Credentials, error) {
	clientID = strings.TrimSpace(clientID)
	clientSecret = strings.TrimSpace(clientSecret)

	switch {
	case clientID == "":
		return Credentials{}, &vault.ValidationError{Msg: "a client id is required"}
	case len(clientID) > maxCredentialLen:
		return Credentials{}, &vault.ValidationError{
			Msg: "that client id is implausibly long — check what was pasted"}
	case hasSpaceOrControl(clientID):
		return Credentials{}, &vault.ValidationError{
			Msg: "the client id contains a space or line break; paste just the id itself"}
	}
	switch {
	case len(clientSecret) > maxCredentialLen:
		return Credentials{}, &vault.ValidationError{
			Msg: "that client secret is implausibly long — check what was pasted"}
	case clientSecret != "" && hasSpaceOrControl(clientSecret):
		return Credentials{}, &vault.ValidationError{
			Msg: "the client secret contains a space or line break; paste just the secret itself"}
	// Catching this here turns a confusing failure at the provider's token
	// endpoint — after the user has already been through a consent screen —
	// into a message on the form they're already looking at.
	case clientSecret == "" && provider.RequiresSecret():
		return Credentials{}, &vault.ValidationError{
			Msg: provider.Name() + " requires a client secret as well as a client id"}
	}
	return Credentials{ClientID: clientID, ClientSecret: clientSecret}, nil
}

// hasSpaceOrControl reports whether s carries anything that can't legitimately
// be part of a pasted credential.
func hasSpaceOrControl(s string) bool {
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func ptr(s string) *string { return &s }
