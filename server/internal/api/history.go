package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/history"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// Version history endpoints, over the vault's git repository.
//
// Two shapes of question, and both matter: "what did this note say before?"
// (per-note, the one people actually ask) and "put the whole vault back to
// Tuesday" (the recovery case). The first is served by a note's own log plus
// reading it at a revision; the second by the vault log plus a rollback.
//
// Nothing here rewrites history. A rollback is a new commit whose tree is the
// old one, so the state being replaced stays reachable and rolling back the
// rollback is the same operation again — which is what makes it safe to try.
//
// Where git isn't installed the routes are still registered and answer
// honestly (`available: 0`, and 400 on the write paths) rather than 404, so
// the client can tell "this server has no history" from "this server is too
// old to have the feature".

// historyStatus is the wire shape of "is there history here at all".
type historyStatus struct {
	Available int `json:"available"`
	// Commits is the recent log, newest first. Empty when unavailable.
	Commits []history.Commit `json:"commits"`
}

// defaultHistoryLimit is how much log a request gets without asking. A screen's
// worth; `?limit=` raises it up to the package's own cap.
const defaultHistoryLimit = 50

func historyLimit(r *http.Request) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultHistoryLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultHistoryLimit
	}
	return n
}

// handleHistoryErr maps this package's errors on top of the shared ones: a bad
// ref or path is a 400, an unavailable history is a 400 that says why, and a
// commit or path git doesn't know is a 404 rather than an internal error —
// asking for a note at a revision that predates it is an ordinary thing to do.
func handleHistoryErr(w http.ResponseWriter, err error) {
	var ve *history.ValidationError
	var ce *history.CommandError
	switch {
	case errors.Is(err, history.ErrUnavailable):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, ve.Error())
	case errors.As(err, &ce):
		if isUnknownToGit(ce.Stderr) {
			writeErr(w, http.StatusNotFound, "no such version of that note")
			return
		}
		handleErr(w, err)
	default:
		handleErr(w, err)
	}
}

// isUnknownToGit recognizes "that object doesn't exist" among git's failures.
func isUnknownToGit(stderr string) bool {
	lower := strings.ToLower(stderr)
	for _, phrase := range []string{
		"exists on disk, but not in",
		"unknown revision",
		"does not exist",
		"invalid object name",
		"bad revision",
		"no such path",
	} {
		if strings.Contains(lower, phrase) {
			return true
		}
	}
	return false
}

// listHistory returns the vault's recent history.
func (s *server) listHistory(w http.ResponseWriter, r *http.Request) {
	body := historyStatus{Commits: []history.Commit{}}
	if !s.history.Available() {
		writeJSON(w, http.StatusOK, body)
		return
	}
	commits, err := s.history.Log(historyLimit(r))
	if err != nil {
		handleHistoryErr(w, err)
		return
	}
	body.Available, body.Commits = 1, commits
	writeJSON(w, http.StatusOK, body)
}

// noteHistory returns the commits that touched one note — the question people
// actually ask of a history.
func (s *server) noteHistory(w http.ResponseWriter, r *http.Request) {
	body := historyStatus{Commits: []history.Commit{}}
	if !s.history.Available() {
		writeJSON(w, http.StatusOK, body)
		return
	}
	commits, err := s.history.FileLog(r.PathValue("path"), historyLimit(r))
	if err != nil {
		handleHistoryErr(w, err)
		return
	}
	body.Available, body.Commits = 1, commits
	writeJSON(w, http.StatusOK, body)
}

// showNoteVersion returns a note's content as of one commit. Reading only —
// the working copy is untouched, so comparing costs nothing.
func (s *server) showNoteVersion(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("ref")
	content, err := s.history.Show(ref, r.PathValue("path"))
	if err != nil {
		handleHistoryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    r.PathValue("path"),
		"ref":     ref,
		"content": string(content),
	})
}

// restoreNoteVersion puts one note back to an older version, leaving the rest
// of the vault alone.
//
// It goes through the ordinary note save rather than through git, so everything
// a save does still happens — the link index sees it, and the next cloud sync
// pushes it like any other edit. The old version becomes the current one; it
// does not erase what was in between, which is still in the log.
func (s *server) restoreNoteVersion(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
		Ref  string `json:"ref"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	content, err := s.history.Show(body.Ref, body.Path)
	if err != nil {
		handleHistoryErr(w, err)
		return
	}
	info, err := s.v.Write(body.Path, string(content))
	if err != nil {
		handleErr(w, err)
		return
	}
	short := body.Ref
	if len(short) > 7 {
		short = short[:7]
	}
	if _, err := s.history.Commit(
		"Restore "+info.Path+" to "+short+", at "+s.history.Stamp(), "", history.KindRollback,
	); err != nil {
		handleHistoryErr(w, err)
		return
	}
	note, err := s.fullNote(info.Path)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

// checkpointHistory marks a moment deliberately, with an optional message.
//
// It commits even when nothing has changed: the message *is* the point, and
// dropping it because the vault happened to be clean would be the surprising
// outcome.
func (s *server) checkpointHistory(w http.ResponseWriter, r *http.Request) {
	if !s.history.Available() {
		writeErr(w, http.StatusBadRequest, history.ErrUnavailable.Error())
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	message := strings.TrimSpace(body.Message)
	subject := "Checkpoint at " + s.history.Stamp()
	if _, err := s.history.CommitAlways(subject, message, history.KindCheckpoint); err != nil {
		handleHistoryErr(w, err)
		return
	}
	s.writeHistory(w, r)
}

// rollbackHistory puts the whole vault back to a commit — the recovery case.
func (s *server) rollbackHistory(w http.ResponseWriter, r *http.Request) {
	if !s.history.Available() {
		writeErr(w, http.StatusBadRequest, history.ErrUnavailable.Error())
		return
	}
	var body struct {
		Ref string `json:"ref"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	if strings.TrimSpace(body.Ref) == "" {
		handleErr(w, &vault.ValidationError{Msg: "Pick a version to roll back to."})
		return
	}
	if _, err := s.history.Rollback(body.Ref); err != nil {
		handleHistoryErr(w, err)
		return
	}
	// A rollback rewrites the vault wholesale, so the sync bookkeeping now
	// describes files that are no longer there. Clearing it makes the next run
	// work the difference out from scratch instead of trusting a stale base.
	if s.cloud != nil {
		if err := s.cloud.ForgetSyncState(); err != nil {
			handleErr(w, err)
			return
		}
	}
	s.writeHistory(w, r)
}

// writeHistory answers a write with the log as it now stands, so the client
// never has to guess how its change landed.
func (s *server) writeHistory(w http.ResponseWriter, r *http.Request) {
	commits, err := s.history.Log(historyLimit(r))
	if err != nil {
		handleHistoryErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, historyStatus{Available: 1, Commits: commits})
}
