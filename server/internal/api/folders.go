package api

import (
	"net/http"

	"github.com/chinmay28/thought-mesh/server/internal/mesh"
)

// Folder endpoints.
//
// A folder IS a category — the same one thing, stored where every other tool
// can see it (see internal/mesh/folders.go for why the folder won). So there is
// nothing to create: a folder exists exactly as long as a note is in it, which
// is the same rule categories had, now enforced by the filesystem rather than
// by us.
//
// Every write here moves files, so every write goes through Mesh.Rename and
// reports how many wikilinks it rewrote. A caller that renames a folder is not
// told "done" until the links that pointed into it point at the new place.
//
// The folder a change targets travels in the body rather than the path: a
// `{path...}` wildcard has to be the last segment of a route pattern, so
// `/api/folders/{path...}/rename` isn't expressible.

// foldersResponse is what every folder write answers with: the whole tree as it
// now stands, so the client never has to guess how a rename landed, plus what
// the move touched.
type foldersResponse struct {
	Folders     []mesh.Folder `json:"folders"`
	MovedNotes  int           `json:"moved_notes"`
	UpdatedLink int           `json:"updated_notes"`
}

func (s *server) folderList() ([]mesh.Folder, error) {
	snap, err := s.m.Snapshot()
	if err != nil {
		return nil, err
	}
	folders := snap.FolderList()
	if folders == nil {
		folders = []mesh.Folder{}
	}
	return folders, nil
}

// listFolders reports every folder in the vault with its note counts.
func (s *server) listFolders(w http.ResponseWriter, _ *http.Request) {
	folders, err := s.folderList()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": folders})
}

// renameFolder moves a folder and everything under it.
func (s *server) renameFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	moved, updated, err := s.m.RenameFolder(body.From, body.To)
	if err != nil {
		handleErr(w, err)
		return
	}
	s.writeFolders(w, moved, updated)
}

// deleteFolder unfiles a folder's notes — they move up one level, and the
// folder stops existing because nothing is in it. No note is deleted.
func (s *server) deleteFolder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path string `json:"path"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	moved, updated, err := s.m.DeleteFolder(body.Path)
	if err != nil {
		handleErr(w, err)
		return
	}
	s.writeFolders(w, moved, updated)
}

// moveNote files one note under a folder, keeping its name.
func (s *server) moveNote(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Path   string `json:"path"`
		Folder string `json:"folder"`
	}
	if err := decodeBody(r, &body); err != nil {
		handleErr(w, err)
		return
	}
	info, updated, err := s.m.MoveNote(body.Path, body.Folder)
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"note":          infoJSON(info),
		"updated_notes": updated,
	})
}

func (s *server) writeFolders(w http.ResponseWriter, moved, updated int) {
	folders, err := s.folderList()
	if err != nil {
		handleErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, foldersResponse{
		Folders: folders, MovedNotes: moved, UpdatedLink: updated,
	})
}
