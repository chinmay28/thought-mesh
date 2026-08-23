package cloud

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "thoughtmesh-cloud.json"))
}

func TestStoreDefaultsWhenMissing(t *testing.T) {
	st := newStore(t)
	set, err := st.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if set.Frequency != FrequencyOff || set.Connected() {
		t.Errorf("defaults = %+v", set)
	}
	creds, err := st.Credentials()
	if err != nil || len(creds) != 0 {
		t.Errorf("credentials = %v, %v", creds, err)
	}
}

func TestStoreRoundTripAndPermissions(t *testing.T) {
	st := newStore(t)
	now := toISO(time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC))
	set := &Settings{
		Provider:     ptr(ProviderDropbox),
		AccountLabel: ptr("user@example.com"),
		AccessToken:  ptr("tok"),
		RefreshToken: ptr("refresh"),
		FolderID:     ptr("/Apps/ThoughtMesh"),
		FolderPath:   ptr("/Apps/ThoughtMesh"),
		Frequency:    FrequencyDaily,
	}
	if err := st.SaveSettings(set, now); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	back, err := st.Settings()
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if !back.Connected() || *back.RefreshToken != "refresh" ||
		back.Frequency != FrequencyDaily || *back.UpdatedAt != now {
		t.Errorf("round trip = %+v", back)
	}
	// The file holds tokens, so it must not be group/world readable.
	info, err := os.Stat(st.Path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("settings file mode = %o; want 600", perm)
	}
}

func TestStoreCredentials(t *testing.T) {
	st := newStore(t)
	if err := st.SaveCredentials(ProviderDropbox,
		Credentials{ClientID: "abc", ClientSecret: "s3cret"}, "2026-08-23T12:00:00.000+00:00"); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	creds, err := st.Credentials()
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if got := creds[ProviderDropbox]; got.ClientID != "abc" || got.ClientSecret != "s3cret" {
		t.Errorf("credentials = %+v", got)
	}
	// Credentials and settings live in one file; writing one keeps the other.
	if set, _ := st.Settings(); set.Frequency != FrequencyOff {
		t.Errorf("settings disturbed: %+v", set)
	}
	if err := st.DeleteCredentials(ProviderDropbox); err != nil {
		t.Fatalf("DeleteCredentials: %v", err)
	}
	if creds, _ := st.Credentials(); len(creds) != 0 {
		t.Errorf("after delete = %v", creds)
	}
}

func TestNextRunAndDue(t *testing.T) {
	from := time.Date(2026, 8, 23, 9, 30, 0, 0, time.UTC)
	cases := map[string]time.Time{
		FrequencyHourly:  from.Add(time.Hour),
		FrequencyDaily:   from.AddDate(0, 0, 1),
		FrequencyWeekly:  from.AddDate(0, 0, 7),
		FrequencyMonthly: from.AddDate(0, 1, 0),
	}
	for freq, want := range cases {
		got, ok := nextRun(from, freq)
		if !ok || !got.Equal(want) {
			t.Errorf("nextRun(%s) = %v, %v; want %v", freq, got, ok, want)
		}
	}
	if _, ok := nextRun(from, FrequencyOff); ok {
		t.Error("nextRun(off) should not schedule")
	}

	set := &Settings{
		Provider:    ptr(ProviderDropbox),
		AccessToken: ptr("tok"),
		FolderID:    ptr("/f"),
		Frequency:   FrequencyDaily,
	}
	// No next_run_at yet → owed immediately.
	if !set.due(from) {
		t.Error("fresh schedule should be due")
	}
	set.NextRunAt = nextRunISO(from, FrequencyDaily)
	if set.due(from.Add(time.Hour)) {
		t.Error("not yet due")
	}
	if !set.due(from.AddDate(0, 0, 1)) {
		t.Error("due at the deadline")
	}
	set.Frequency = FrequencyOff
	if set.due(from.AddDate(0, 0, 2)) {
		t.Error("an off schedule is never due")
	}
}

func TestValidateFrequency(t *testing.T) {
	for _, ok := range []string{"off", "hourly", "daily", "weekly", "monthly"} {
		if err := validateFrequency(ok); err != nil {
			t.Errorf("validateFrequency(%s) = %v", ok, err)
		}
	}
	err := validateFrequency("fortnightly")
	var ve *vault.ValidationError
	if !errors.As(err, &ve) {
		t.Errorf("bad frequency error = %T %v", err, err)
	}
}

func TestValidateCredentials(t *testing.T) {
	d := NewDropbox(Credentials{}, nil, nil)
	if _, err := validateCredentials(d, "  abc123  ", ""); err != nil {
		t.Errorf("trimmed id should pass: %v", err)
	}
	for _, bad := range [][2]string{
		{"", ""},
		{"has space", ""},
		{"tab\tid", ""},
		{"ok", "secret with space"},
	} {
		if _, err := validateCredentials(d, bad[0], bad[1]); err == nil {
			t.Errorf("validateCredentials(%q, %q) should fail", bad[0], bad[1])
		}
	}
}
