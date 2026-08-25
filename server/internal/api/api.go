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
	"github.com/chinmay28/thought-mesh/server/internal/history"
	"github.com/chinmay28/thought-mesh/server/internal/mesh"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
	"github.com/chinmay28/thought-mesh/server/internal/version"
)

// AppVersion is what /api/health and the CLI report.
var AppVersion = version.String()

// noteInfoJSON is the wire shape of a note without content. `categories` is
// always an array — the wire contract prefers an empty list to a null, and a
// note with no categories is the ordinary case, not a missing value.
type noteInfoJSON struct {
	Path       string   `json:"path"`
	Name       string   `json:"name"`
	Dir        string   `json:"dir"`
	Size       int64    `json:"size"`
	MtimeMs    int64    `json:"mtime_ms"`
	Categories []string `json:"categories"`
}

func infoJSON(n *vault.NoteInfo, categories []string) noteInfoJSON {
	if categories == nil {
		categories = []string{}
	}
	return noteInfoJSON{
		Path: n.Path, Name: n.Name, Dir: n.Dir,
		Size: n.Size, MtimeMs: n.MtimeMs, Categories: categories,
	}
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
// `h` may be nil — a machine without git, or a deployment that turned history
// off — in which case the routes are still registered and answer `available:
// 0` rather than 404, so the client can tell "no history here" from "a server
// too old to have the feature".
func New(v *vault.Vault, m *mesh.Mesh, cl *cloud.Service, h *history.Repo) http.Handler {
	s := &server{v: v, m: m, cloud: cl, history: h}
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
	mux.HandleFunc("POST /api/merge", s.mergeText)
	mux.HandleFunc("GET /api/categories", s.listCategories)
	mux.HandleFunc("POST /api/categories/rename", s.renameCategory)
	mux.HandleFunc("POST /api/categories/delete", s.deleteCategory)
	mux.HandleFunc("POST /api/categories/assign", s.setNoteCategories)
	mux.HandleFunc("GET /api/history", s.listHistory)
	mux.HandleFunc("POST /api/history/checkpoint", s.checkpointHistory)
	mux.HandleFunc("POST /api/history/rollback", s.rollbackHistory)
	mux.HandleFunc("POST /api/history/restore", s.restoreNoteVersion)
	mux.HandleFunc("GET /api/history/notes/{path...}", s.noteHistory)
	mux.HandleFunc("GET /api/history/version/{path...}", s.showNoteVersion)
	if s.cloud != nil {
		mux.HandleFunc("GET /api/cloud/sync", s.cloudSyncSettings)
		mux.HandleFunc("PATCH /api/cloud/sync", s.cloudSyncUpdate)
		mux.HandleFunc("POST /api/cloud/sync/connect", s.cloudSyncConnect)
		mux.HandleFunc("GET "+cloud.CallbackPath, s.cloudSyncCallback)
		mux.HandleFunc("POST /api/cloud/sync/complete", s.cloudSyncComplete)
		mux.HandleFunc("POST /api/cloud/sync/disconnect", s.cloudSyncDisconnect)
		mux.HandleFunc("GET /api/cloud/sync/folders", s.cloudSyncFolders)
		mux.HandleFunc("POST /api/cloud/sync/run", s.cloudSyncRun)
		mux.HandleFunc("GET /api/cloud/sync/conflicts", s.cloudSyncConflicts)
		mux.HandleFunc("GET /api/cloud/sync/conflicts/{path...}", s.cloudSyncConflictDetail)
		mux.HandleFunc("POST /api/cloud/sync/resolve", s.cloudSyncResolve)
		mux.HandleFunc("GET /api/cloud/sync/backups", s.cloudSyncBackups)
		mux.HandleFunc("POST /api/cloud/sync/backups/restore", s.cloudSyncRestoreBackup)
		mux.HandleFunc("PUT /api/cloud/sync/providers/{provider}", s.cloudSyncSetCredentials)
		mux.HandleFunc("DELETE /api/cloud/sync/providers/{provider}", s.cloudSyncClearCredentials)
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "no such endpoint")
	})
	return mux
}

type server struct {
	v       *vault.Vault
	m       *mesh.Mesh
	cloud   *cloud.Service
	history *history.Repo
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
	var hv *history.ValidationError
	var nf *vault.NotFoundError
	var ex *vault.ExistsError
	var st *vault.StaleError
	var ce *cloud.ConfigError
	var pe *cloud.ProviderError
	switch {
	case errors.As(err, &ve):
		writeErr(w, http.StatusBadRequest, ve.Error())
	case errors.As(err, &hv):
		writeErr(w, http.StatusBadRequest, hv.Error())
	case errors.As(err, &nf):
		writeErr(w, http.StatusNotFound, nf.Error())
	case errors.As(err, &ex):
		writeErr(w, http.StatusConflict, ex.Error())
	case errors.As(err, &st):
		writeErr(w, http.StatusConflict, st.Error())
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

// listNotes goes through the mesh rather than the vault directly, because the
// list carries each note's categories and those come from the snapshot's
// cached parse. On an unchanged vault that costs a stat-walk and no reads.
//
// `?category=` narrows the list to the notes carrying that category, matched
// case-insensitively. Filtering server-side keeps the client a transport: it
// is the same reason search and backlinks live here.
func (s *server) listNotes(w http.ResponseWriter, r *http.Request) {
	snap, err := s.m.Snapshot()
	if err != nil {
		handleErr(w, err)
		return
	}
	filter := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("category")))
	out := make([]noteInfoJSON, 0, len(snap.Notes))
	for i := range snap.Notes {
		note := &snap.Notes[i]
		cats := snap.Categories(note.Path)
		if filter != "" && !hasCategory(cats, filter) {
			continue
		}
		out = append(out, infoJSON(note, cats))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notes": out})
}

// hasCategory reports whether cats holds lowerName, compared case-insensitively.
func hasCategory(cats []string, lowerName string) bool {
	for _, cat := range cats {
		if strings.ToLower(cat) == lowerName {
			return true
		}
	}
	return false
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
		noteInfoJSON: infoJSON(info, snap.Categories(info.Path)),
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
		Path       string   `json:"path"`
		Name       string   `json:"name"`
		Dir        string   `json:"dir"`
		Content    string   `json:"content"`
		Categories []string `json:"categories"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	content := body.Content
	if len(body.Categories) > 0 {
		cats, err := vault.NormalizeCategories(body.Categories)
		if err != nil {
			handleErr(w, err)
			return
		}
		content = vault.WithCategories(content, cats)
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
	info, err := s.v.Create(path, content)
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
		handleErr(w, &vault.StaleError{Path: info.Path})
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
