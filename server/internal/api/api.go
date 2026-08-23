// Package api exposes the vault and its mesh over REST.
//
// Wire conventions (shared with the PWA in apps/web/src/api/client.ts):
// snake_case JSON field names, integer flags, `{"error": …}` error bodies,
// statuses 200/201/204/400/404/409. apps/web is compiled against these exact
// shapes — change them only together.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/cloud"
	"github.com/chinmay28/thought-mesh/server/internal/mesh"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
	"github.com/chinmay28/thought-mesh/server/internal/version"
)

// AppVersion is what /api/health and the CLI report.
var AppVersion = version.String()

// noteInfoJSON is the wire shape of a note without content.
type noteInfoJSON struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Dir     string `json:"dir"`
	Size    int64  `json:"size"`
	MtimeMs int64  `json:"mtime_ms"`
}

func infoJSON(n *vault.NoteInfo) noteInfoJSON {
	return noteInfoJSON{Path: n.Path, Name: n.Name, Dir: n.Dir, Size: n.Size, MtimeMs: n.MtimeMs}
}

// noteJSON is the full note: info + content + link structure.
type noteJSON struct {
	noteInfoJSON
	Content   string          `json:"content"`
	Links     []mesh.Link     `json:"links"`
	Backlinks []mesh.Backlink `json:"backlinks"`
}

// New builds the API handler. `cl` may be nil — a server without cloud sync
// (an API-only test harness) leaves the cloud routes unregistered, and the
// web client treats a 404 there as "this server doesn't do cloud sync".
func New(v *vault.Vault, m *mesh.Mesh, cl *cloud.Service) http.Handler {
	s := &server{v: v, m: m, cloud: cl}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/notes", s.listNotes)
	mux.HandleFunc("POST /api/notes", s.createNote)
	mux.HandleFunc("GET /api/notes/{path...}", s.getNote)
	mux.HandleFunc("PUT /api/notes/{path...}", s.saveNote)
	mux.HandleFunc("DELETE /api/notes/{path...}", s.deleteNote)
	mux.HandleFunc("POST /api/rename", s.renameNote)
	mux.HandleFunc("GET /api/search", s.search)
	mux.HandleFunc("GET /api/graph", s.graph)
	if s.cloud != nil {
		mux.HandleFunc("GET /api/cloud/sync", s.cloudSyncSettings)
		mux.HandleFunc("PATCH /api/cloud/sync", s.cloudSyncUpdate)
		mux.HandleFunc("POST /api/cloud/sync/connect", s.cloudSyncConnect)
		mux.HandleFunc("GET "+cloud.CallbackPath, s.cloudSyncCallback)
		mux.HandleFunc("POST /api/cloud/sync/complete", s.cloudSyncComplete)
		mux.HandleFunc("POST /api/cloud/sync/disconnect", s.cloudSyncDisconnect)
		mux.HandleFunc("GET /api/cloud/sync/folders", s.cloudSyncFolders)
		mux.HandleFunc("POST /api/cloud/sync/run", s.cloudSyncRun)
		mux.HandleFunc("PUT /api/cloud/sync/providers/{provider}", s.cloudSyncSetCredentials)
		mux.HandleFunc("DELETE /api/cloud/sync/providers/{provider}", s.cloudSyncClearCredentials)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such endpoint")
	})
	return mux
}

type server struct {
	v     *vault.Vault
	m     *mesh.Mesh
	cloud *cloud.Service
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		log.Printf("[thoughtmesh] write response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// handleErr maps domain errors to HTTP statuses. The two cloud statuses:
// a ConfigError is a setup gap the caller can close (400), a ProviderError
// is a failure that came from Dropbox rather than from us (502).
func handleErr(w http.ResponseWriter, err error) {
	var ve *vault.ValidationError
	var nf *vault.NotFoundError
	var ex *vault.ExistsError
	var ce *cloud.ConfigError
	var pe *cloud.ProviderError
	switch {
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, ve.Error())
	case errors.As(err, &nf):
		writeErr(w, http.StatusNotFound, nf.Error())
	case errors.As(err, &ex):
		writeErr(w, http.StatusConflict, ex.Error())
	case errors.As(err, &ce):
		writeErr(w, http.StatusBadRequest, ce.Error())
	case errors.As(err, &pe):
		writeErr(w, http.StatusBadGateway, pe.Error())
	default:
		log.Printf("[thoughtmesh] internal error: %v", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
	}
}

func decodeBody(r *http.Request, into any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return &vault.ValidationError{Msg: "invalid JSON body: " + err.Error()}
	}
	return nil
}

func (s *server) health(w http.ResponseWriter, _ *http.Request) {
	notes, err := s.v.List()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": AppVersion,
		"notes":   len(notes),
	})
}

func (s *server) listNotes(w http.ResponseWriter, _ *http.Request) {
	notes, err := s.v.List()
	if err != nil {
		handleErr(w, err)
		return
	}
	out := make([]noteInfoJSON, 0, len(notes))
	for i := range notes {
		out = append(out, infoJSON(&notes[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": out})
}

// fullNote assembles the note + links + backlinks response body.
func (s *server) fullNote(path string) (*noteJSON, error) {
	content, info, err := s.v.Read(path)
	if err != nil {
		return nil, err
	}
	snap, err := s.m.Snapshot()
	if err != nil {
		return nil, err
	}
	links := snap.Links(info.Path)
	if links == nil {
		links = []mesh.Link{}
	}
	backlinks := snap.Backlinks(info.Path)
	if backlinks == nil {
		backlinks = []mesh.Backlink{}
	}
	return &noteJSON{
		noteInfoJSON: infoJSON(info),
		Content:      content,
		Links:        links,
		Backlinks:    backlinks,
	}, nil
}

func (s *server) getNote(w http.ResponseWriter, r *http.Request) {
	note, err := s.fullNote(r.PathValue("path"))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

func (s *server) createNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		Name    string `json:"name"`
		Dir     string `json:"dir"`
		Content string `json:"content"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	path := body.Path
	if path == "" {
		name, err := vault.SanitizeName(body.Name)
		if err != nil {
			handleErr(w, err)
			return
		}
		path = name + ".md"
		if dir := strings.Trim(body.Dir, "/"); dir != "" {
			path = dir + "/" + path
		}
	}
	info, err := s.v.Create(path, body.Content)
	if err != nil {
		handleErr(w, err)
		return
	}
	note, err := s.fullNote(info.Path)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, note)
}

func (s *server) saveNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content     *string `json:"content"`
		BaseMtimeMs *int64  `json:"base_mtime_ms"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	if body.Content == nil {
		handleErr(w, &vault.ValidationError{Msg: "content is required"})
		return
	}
	info, err := s.v.Stat(r.PathValue("path"))
	if err != nil {
		handleErr(w, err)
		return
	}
	// Optimistic concurrency: a client that says which version it edited is
	// told when the file moved beneath it (another device, another editor),
	// instead of silently overwriting that work.
	if body.BaseMtimeMs != nil && *body.BaseMtimeMs != info.MtimeMs {
		writeErr(w, http.StatusConflict, "note changed on disk since it was loaded")
		return
	}
	if _, err := s.v.Write(info.Path, *body.Content); err != nil {
		handleErr(w, err)
		return
	}
	note, err := s.fullNote(info.Path)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, note)
}

func (s *server) deleteNote(w http.ResponseWriter, r *http.Request) {
	if err := s.v.Delete(r.PathValue("path")); err != nil {
		handleErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) renameNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path    string `json:"path"`
		NewPath string `json:"new_path"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	info, updated, err := s.m.Rename(body.Path, body.NewPath)
	if err != nil {
		handleErr(w, err)
		return
	}
	note, err := s.fullNote(info.Path)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"note":          note,
		"updated_notes": updated,
	})
}

func (s *server) search(w http.ResponseWriter, r *http.Request) {
	results, err := s.m.Search(r.URL.Query().Get("q"))
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

func (s *server) graph(w http.ResponseWriter, _ *http.Request) {
	snap, err := s.m.Snapshot()
	if err != nil {
		handleErr(w, err)
		return
	}
	nodes, edges := snap.Graph()
	if nodes == nil {
		nodes = []mesh.GraphNode{}
	}
	if edges == nil {
		edges = []mesh.GraphEdge{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes, "edges": edges})
}
