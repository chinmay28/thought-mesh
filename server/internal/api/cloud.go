package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/cloud"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// Automatic cloud sync endpoints. They follow the same conventions as the
// rest of the API — snake_case JSON, 0|1 integer flags, explicit nulls,
// `{"error": …}` bodies — with two statuses the older routes never needed:
// 400 for a setup gap (`cloud.ConfigError`) and 502 for a failure that came
// from the provider (`cloud.ProviderError`). Both are mapped in handleErr.
//
// The whole surface is unauthenticated, like every other route: the server is
// meant for a trusted network. What it will *not* do is hand tokens back out
// — settings responses are redacted (see cloud.PublicSettings), so a stored
// grant can be used by this server and read by nobody.

// cloudSettingsBody is the GET payload: the current configuration plus the
// destinations this build can offer, so the Sync page renders in one round
// trip.
type cloudSettingsBody struct {
	Settings  cloud.PublicSettings   `json:"settings"`
	Providers []cloud.PublicProvider `json:"providers"`
	// Conflicts are the paths waiting on a decision. They ride along with the
	// settings because the Sync page has to lead with them — an unresolved
	// conflict is the one thing on that screen that stops being in step until
	// somebody acts.
	Conflicts []cloud.Conflict `json:"conflicts"`
	// RedirectURI is the exact string the user must register with their
	// provider. It's derived from the origin the request arrived on, so the
	// setup form can show what to paste rather than asking them to assemble
	// it from a hostname and a path.
	RedirectURI string `json:"redirect_uri"`
	// RedirectSupported is 0 when this origin can't be a registered redirect
	// URI at all (plain http on something other than localhost). The UI then
	// leads with the paste flow instead of a button that cannot work.
	RedirectSupported int `json:"redirect_supported"`
}

// decodeLoose reads a JSON object without insisting on a fixed shape — the
// cloud routes take patch-style bodies where absent keys mean "leave alone".
func decodeLoose(r *http.Request) (map[string]any, error) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, &vault.ValidationError{Msg: "invalid JSON body: " + err.Error()}
	}
	return body, nil
}

func (s *server) writeCloudSettings(w http.ResponseWriter, r *http.Request, status int) {
	set, err := s.cloud.Settings()
	if err != nil {
		handleErr(w, err)
		return
	}
	redirectSupported := 0
	if s.cloud.RedirectSupported(requestOrigin(r)) {
		redirectSupported = 1
	}
	conflicts, err := s.cloud.Conflicts()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, status, cloudSettingsBody{
		Settings:          set.Public(),
		Providers:         s.cloud.PublicProviders(),
		Conflicts:         conflicts,
		RedirectURI:       s.cloud.RedirectURI(requestOrigin(r)),
		RedirectSupported: redirectSupported,
	})
}

func (s *server) cloudSyncSettings(w http.ResponseWriter, r *http.Request) {
	s.writeCloudSettings(w, r, http.StatusOK)
}

// cloudSyncSetCredentials stores the OAuth client for one provider — the
// client id (and secret, where the provider needs one) from an app the user
// registered. This is what makes the whole feature reachable from a phone:
// the alternative is a startup flag, and a phone has no command line.
func (s *server) cloudSyncSetCredentials(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLoose(r)
	if err != nil {
		handleErr(w, err)
		return
	}
	clientID, _ := body["client_id"].(string)
	clientSecret, _ := body["client_secret"].(string)
	if err := s.cloud.SetCredentials(r.PathValue("provider"), clientID, clientSecret); err != nil {
		handleErr(w, err)
		return
	}
	s.writeCloudSettings(w, r, http.StatusOK)
}

// cloudSyncClearCredentials forgets a stored OAuth client, falling back to
// whatever the startup flags carry.
func (s *server) cloudSyncClearCredentials(w http.ResponseWriter, r *http.Request) {
	if err := s.cloud.ClearCredentials(r.PathValue("provider")); err != nil {
		handleErr(w, err)
		return
	}
	s.writeCloudSettings(w, r, http.StatusOK)
}

// cloudSyncUpdate patches the schedule and the destination folder. Absent
// keys are left alone; `folder_id` and `folder_path` move together, since a
// path without its handle is just a label.
func (s *server) cloudSyncUpdate(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLoose(r)
	if err != nil {
		handleErr(w, err)
		return
	}
	var frequency, folderID, folderPath *string
	if v, ok := body["frequency"].(string); ok {
		frequency = &v
	}
	if v, ok := body["folder_id"].(string); ok {
		folderID = &v
		path := v
		if p, ok := body["folder_path"].(string); ok {
			path = p
		}
		folderPath = &path
	}
	if _, err := s.cloud.Update(frequency, folderID, folderPath); err != nil {
		handleErr(w, err)
		return
	}
	s.writeCloudSettings(w, r, http.StatusOK)
}

// cloudSyncConnect starts an OAuth authorization and returns where to send
// the browser. The client navigates there itself rather than being redirected
// — it's a cross-origin hop out of a single-page app, and a fetch that
// followed a 302 would land the consent page in an XHR.
//
// `mode: "paste"` asks for the no-redirect flow, where the provider shows the
// user a code instead of redirecting. The response then carries a pending_id
// the client holds until it posts the code back to /complete.
func (s *server) cloudSyncConnect(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLoose(r)
	if err != nil {
		handleErr(w, err)
		return
	}
	provider, _ := body["provider"].(string)
	mode, _ := body["mode"].(string)
	start, err := s.cloud.StartConnect(provider, requestOrigin(r), mode == cloud.ModePaste)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"authorize_url": start.AuthorizeURL,
		"mode":          start.Mode,
		"pending_id":    start.PendingID,
	})
}

// cloudSyncComplete finishes a paste-mode authorization: the code the
// provider displayed, plus the pending_id from /connect. (Redirect mode
// finishes at the callback instead, which is a browser navigation.)
func (s *server) cloudSyncComplete(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLoose(r)
	if err != nil {
		handleErr(w, err)
		return
	}
	pendingID, _ := body["pending_id"].(string)
	code, _ := body["code"].(string)
	// Dropbox renders the code in a box people select by hand, so a stray
	// space or newline on the way back is ordinary, not a malformed request.
	code = strings.TrimSpace(code)
	if code == "" {
		writeErr(w, http.StatusBadRequest, "Paste the code the provider showed you.")
		return
	}
	if _, err := s.cloud.CompleteConnect(r.Context(), pendingID, code); err != nil {
		handleErr(w, err)
		return
	}
	s.writeCloudSettings(w, r, http.StatusOK)
}

// cloudSyncCallback is where the provider returns the browser. It is a
// *navigation*, not an API call, so it answers with a redirect back into the
// app rather than JSON — the outcome rides along in the query string and the
// Sync page reports it.
func (s *server) cloudSyncCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if desc := q.Get("error"); desc != "" {
		// The user pressed "Cancel" on the consent screen, or the provider
		// refused. Either way there's nothing to exchange.
		cloudCallbackRedirect(w, r, "error", desc)
		return
	}
	code, state := q.Get("code"), q.Get("state")
	if code == "" || state == "" {
		cloudCallbackRedirect(w, r, "error", "The sign-in response was incomplete.")
		return
	}
	if _, err := s.cloud.CompleteConnect(r.Context(), state, code); err != nil {
		cloudCallbackRedirect(w, r, "error", err.Error())
		return
	}
	cloudCallbackRedirect(w, r, "connected", "")
}

// cloudCallbackRedirect sends the browser back to the Sync page carrying the
// outcome. `cloud=connected` or `cloud=error&cloud_error=…`.
func cloudCallbackRedirect(w http.ResponseWriter, r *http.Request, status, message string) {
	q := url.Values{"cloud": {status}}
	if message != "" {
		q.Set("cloud_error", message)
	}
	http.Redirect(w, r, "/sync?"+q.Encode(), http.StatusFound)
}

func (s *server) cloudSyncDisconnect(w http.ResponseWriter, r *http.Request) {
	if _, err := s.cloud.Disconnect(); err != nil {
		handleErr(w, err)
		return
	}
	s.writeCloudSettings(w, r, http.StatusOK)
}

// cloudSyncFolders lists the sub-folders of `folder_id` (absent = the
// account root) so the client can walk down to a destination. The client
// keeps its own breadcrumb trail on the way down, which is why there's no
// parent in the response.
func (s *server) cloudSyncFolders(w http.ResponseWriter, r *http.Request) {
	folders, err := s.cloud.ListFolders(r.Context(), r.URL.Query().Get("folder_id"))
	if err != nil {
		handleErr(w, err)
		return
	}
	if folders == nil {
		folders = []cloud.Folder{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders})
}

// cloudSyncRun syncs right now, outside the schedule. A failure is recorded in
// the settings *and* returned, so the button reports it instead of quietly
// looking successful.
func (s *server) cloudSyncRun(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLoose(r)
	if err != nil {
		handleErr(w, err)
		return
	}
	// An optional note from whoever pressed the button. It becomes the body of
	// the commit this run produces — six months later it is the only thing
	// that will tell one sync apart from the next.
	note, _ := body["message"].(string)
	result, err := s.cloud.Sync(r.Context(), note)
	if err != nil {
		handleErr(w, err)
		return
	}
	set, err := s.cloud.Settings()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"result":   result,
		"settings": set.Public(),
	})
}

// cloudSyncConflicts lists the paths a sync left contested.
func (s *server) cloudSyncConflicts(w http.ResponseWriter, _ *http.Request) {
	conflicts, err := s.cloud.Conflicts()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": conflicts})
}

// cloudSyncConflictDetail returns both versions of one contested path plus a
// merge of them, ready to be shown side by side and edited.
//
// The remote side is fetched here rather than when the conflict was detected: a
// run that finds twenty conflicts shouldn't download twenty files nobody has
// opened yet.
func (s *server) cloudSyncConflictDetail(w http.ResponseWriter, r *http.Request) {
	detail, err := s.cloud.ConflictDetail(r.Context(), r.PathValue("path"))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// cloudSyncResolve applies the user's decision to one conflict: keep this
// server's version, take the cloud's, or write a merged text to both.
func (s *server) cloudSyncResolve(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLoose(r)
	if err != nil {
		handleErr(w, err)
		return
	}
	path, _ := body["path"].(string)
	resolution, _ := body["resolution"].(string)
	content, _ := body["content"].(string)
	if strings.TrimSpace(path) == "" {
		writeErr(w, http.StatusBadRequest, "Which conflict? A path is required.")
		return
	}
	if _, err := s.cloud.ResolveConflict(r.Context(), path, resolution, content); err != nil {
		handleErr(w, err)
		return
	}
	s.writeCloudSettings(w, r, http.StatusOK)
}

// cloudSyncBackups lists the local pre-sync copies of the vault — the undo path
// for a sync that pulled down something unwelcome.
func (s *server) cloudSyncBackups(w http.ResponseWriter, _ *http.Request) {
	backups, err := s.cloud.Backups()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": backups})
}

// cloudSyncRestoreBackup replaces the vault with one of those backups. The
// server takes a fresh backup of the current vault first, so restoring the
// wrong one is itself undoable.
func (s *server) cloudSyncRestoreBackup(w http.ResponseWriter, r *http.Request) {
	body, err := decodeLoose(r)
	if err != nil {
		handleErr(w, err)
		return
	}
	name, _ := body["name"].(string)
	if strings.TrimSpace(name) == "" {
		writeErr(w, http.StatusBadRequest, "Pick a backup to restore.")
		return
	}
	files, err := s.cloud.RestoreBackup(name)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"backup": name, "files": files})
}

// requestOrigin reconstructs the origin the browser used, which is what the
// OAuth redirect URI has to be built from. Forwarded headers win: behind a
// TLS-terminating proxy the request itself looks like plain HTTP on an
// internal name, and a redirect URI built from that would never match the one
// registered with the provider. (An operator whose proxy sets neither header
// can pin the origin with --public-url.)
func requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if proto := forwardedFirst(r.Header.Get("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	}
	host := r.Host
	if fwd := forwardedFirst(r.Header.Get("X-Forwarded-Host")); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

// forwardedFirst takes the left-most value of a comma-separated X-Forwarded-*
// header — the one the original client reached.
func forwardedFirst(v string) string {
	first, _, _ := strings.Cut(v, ",")
	return strings.TrimSpace(first)
}
