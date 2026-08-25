package api

import (
	"net/http"

	"github.com/chinmay28/thought-mesh/server/internal/mesh"
)

// Category endpoints.
//
// Categories are frontmatter on the note itself (see internal/vault), so there
// is nothing to create and nothing to delete-if-unused: the vocabulary is
// whatever the notes currently declare. That shapes the surface — one route to
// read the vocabulary, one to set a note's own list, and two vault-wide edits
// that exist for the same reason renaming a note rewrites its wikilinks:
// leaving half the notes on the old spelling would silently split one category
// into two.
//
// The note a change targets travels in the body rather than the path, the way
// /api/rename does — a `{path...}` wildcard has to be the last segment of a
// route pattern, so `/api/notes/{path...}/categories` isn't expressible.

// categoriesResponse is what every category write answers with: the whole
// vocabulary as it now stands, so the client never has to guess how a rename
// landed, plus how many notes were rewritten.
type categoriesResponse struct {
	Categories   []mesh.Category `json:"categories"`
	UpdatedNotes int             `json:"updated_notes"`
}

func (s *server) categoryList() ([]mesh.Category, error) {
	snap, err := s.m.Snapshot()
	if err != nil {
		return nil, err
	}
	cats := snap.CategoryList()
	if cats == nil {
		cats = []mesh.Category{}
	}
	return cats, nil
}

// listCategories reports every category in the vault with its note count.
func (s *server) listCategories(w http.ResponseWriter, _ *http.Request) {
	cats, err := s.categoryList()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"categories": cats})
}

// setNoteCategories replaces one note's categories. `base_mtime_ms` is
// optional and behaves exactly as it does on a content save: the version the
// caller was looking at, and a 409 when the file has moved on since — assigning
// a category from a stale screen shouldn't quietly revert someone's edit.
func (s *server) setNoteCategories(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path        string   `json:"path"`
		Categories  []string `json:"categories"`
		BaseMtimeMs *int64   `json:"base_mtime_ms"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	var base int64
	if body.BaseMtimeMs != nil {
		base = *body.BaseMtimeMs
	}
	info, err := s.m.SetCategories(body.Path, body.Categories, base)
	if err != nil {
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

// renameCategory renames one category everywhere it appears. Renaming onto a
// name already in use merges the two, which is what someone consolidating
// "Work" and "work" is asking for.
func (s *server) renameCategory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	updated, err := s.m.RenameCategory(body.From, body.To)
	if err != nil {
		handleErr(w, err)
		return
	}
	s.writeCategories(w, updated)
}

// deleteCategory strips one category from every note carrying it. The notes
// themselves are untouched otherwise — a category is a label, and dropping the
// label is not dropping what it was on.
func (s *server) deleteCategory(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	updated, err := s.m.DeleteCategory(body.Name)
	if err != nil {
		handleErr(w, err)
		return
	}
	s.writeCategories(w, updated)
}

func (s *server) writeCategories(w http.ResponseWriter, updated int) {
	cats, err := s.categoryList()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, categoriesResponse{Categories: cats, UpdatedNotes: updated})
}
