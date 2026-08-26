package mesh

import (
	"errors"
	"sort"
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// A note's folder is its category. There is exactly one, it is the directory
// the file is actually in, and it needs no metadata to exist: `ls` shows it,
// Obsidian shows it, and a note carries it to any machine it is copied to.
//
// This replaces an earlier `categories:` frontmatter key. Two ways to say the
// same thing meant a note filed under Money/ and *also* labelled "Money" showed
// the name twice with nothing to distinguish the two, and "rename" meant
// different operations depending on which one you touched. The folder won
// because it was already there — the files ARE the data, and a directory is
// the one grouping every other tool already understands.
//
// The cost, accepted deliberately: a note has one folder, so it has one
// category. Many-per-note went away with the frontmatter key.

// Folder is one directory in the vault, with how many notes it holds.
type Folder struct {
	// Path is vault-relative and "/" separated. "" is the vault root, where
	// notes that aren't filed anywhere live.
	Path string `json:"path"`
	// Name is the last segment — what to show in a list.
	Name string `json:"name"`
	// Depth is how many folders are above this one; 0 at the root.
	Depth int `json:"depth"`
	// Count is the notes directly inside. Total includes every folder below,
	// so a parent that only holds subfolders still reports a useful size.
	Count int `json:"count"`
	Total int `json:"total"`
}

// Folder returns the folder one note is in ("" at the vault root).
func (s *Snapshot) Folder(path string) string {
	if n, ok := s.byPath[path]; ok {
		return n.Dir
	}
	return ""
}

// FolderList is every folder in the vault, sorted by path so a client can
// render the tree by walking the slice in order.
//
// Folders that hold only other folders are included: `a/b/note.md` means `a`
// exists as surely as `a/b` does, and a browser that skipped it could not show
// a path to `a/b` at all. The root is always first, so "everything unfiled" is
// never a special case the caller has to synthesize.
func (s *Snapshot) FolderList() []Folder {
	byPath := map[string]*Folder{"": {Path: "", Name: "", Depth: 0}}
	for i := range s.Notes {
		dir := s.Notes[i].Dir
		if f, ok := byPath[dir]; ok {
			f.Count++
		} else {
			byPath[dir] = &Folder{
				Path:  dir,
				Name:  dir[strings.LastIndex(dir, "/")+1:],
				Depth: strings.Count(dir, "/") + 1,
				Count: 1,
			}
		}
		// Every ancestor holds this note in its total, and exists whether or
		// not a note sits directly in it.
		for p := dir; ; {
			f, ok := byPath[p]
			if !ok {
				f = &Folder{
					Path:  p,
					Name:  p[strings.LastIndex(p, "/")+1:],
					Depth: strings.Count(p, "/") + 1,
				}
				byPath[p] = f
			}
			f.Total++
			if p == "" {
				break
			}
			cut := strings.LastIndex(p, "/")
			if cut < 0 {
				p = ""
				continue
			}
			p = p[:cut]
		}
	}
	out := make([]Folder, 0, len(byPath))
	for _, f := range byPath {
		out = append(out, *f)
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Path), strings.ToLower(out[j].Path)
		if li != lj {
			return li < lj
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// CleanFolder validates a folder path the same way CleanPath validates a note
// path, minus the ".md". "" is the vault root and always valid.
func CleanFolder(p string) (string, error) {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return "", nil
	}
	// CleanPath does the real work — segment rules, traversal, forbidden
	// characters — so a folder can never be somewhere a note could not be.
	cleaned, err := vault.CleanPath(p + "/x.md")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(cleaned, "/x.md"), nil
}

// notesUnder returns the notes in a folder and every folder below it, together
// with the part of their path that follows the folder.
func notesUnder(snap *Snapshot, folder string) map[string]string {
	out := map[string]string{}
	for i := range snap.Notes {
		p := snap.Notes[i].Path
		switch {
		case folder == "":
			out[p] = p
		case strings.EqualFold(p[:min(len(p), len(folder)+1)], folder+"/"):
			out[p] = p[len(folder)+1:]
		}
	}
	return out
}

// RenameFolder moves a folder and everything under it, rewriting the wikilinks
// that pointed into it — the same guarantee Rename gives one note, for the same
// reason: the notes mean the same thing after the move, so a link that stopped
// resolving would be the rename silently breaking the vault.
//
// Renaming onto an existing folder merges the two, which is the useful reading
// of "Work" → "work" when both exist. A note whose name is already taken in the
// destination is left where it is and reported, rather than overwriting it.
func (m *Mesh) RenameFolder(from, to string) (moved int, updated int, err error) {
	fromPath, err := CleanFolder(from)
	if err != nil {
		return 0, 0, err
	}
	toPath, err := CleanFolder(to)
	if err != nil {
		return 0, 0, err
	}
	if fromPath == "" {
		return 0, 0, &vault.ValidationError{Msg: "the vault root is not a folder that can be renamed"}
	}
	if fromPath == toPath {
		return 0, 0, nil
	}
	// Moving a folder into itself would walk forever ("a" → "a/b").
	if toPath != "" && strings.HasPrefix(strings.ToLower(toPath)+"/", strings.ToLower(fromPath)+"/") {
		return 0, 0, &vault.ValidationError{Msg: "a folder cannot be moved inside itself"}
	}
	snap, err := m.Snapshot()
	if err != nil {
		return 0, 0, err
	}
	under := notesUnder(snap, fromPath)
	if len(under) == 0 {
		return 0, 0, &vault.NotFoundError{Path: fromPath}
	}
	return m.moveAll(under, toPath)
}

// DeleteFolder removes one level of filing: the notes inside move up to the
// folder above it, keeping any substructure of their own. It never deletes a
// note — "delete this category" only ever meant "stop filing things under it".
func (m *Mesh) DeleteFolder(path string) (moved int, updated int, err error) {
	folder, err := CleanFolder(path)
	if err != nil {
		return 0, 0, err
	}
	if folder == "" {
		return 0, 0, &vault.ValidationError{Msg: "the vault root is not a folder that can be removed"}
	}
	parent := ""
	if cut := strings.LastIndex(folder, "/"); cut >= 0 {
		parent = folder[:cut]
	}
	snap, err := m.Snapshot()
	if err != nil {
		return 0, 0, err
	}
	under := notesUnder(snap, folder)
	if len(under) == 0 {
		return 0, 0, &vault.NotFoundError{Path: folder}
	}
	return m.moveAll(under, parent)
}

// MoveNote files one note under a folder, keeping its name. "" moves it to the
// vault root.
func (m *Mesh) MoveNote(path, folder string) (*vault.NoteInfo, int, error) {
	dest, err := CleanFolder(folder)
	if err != nil {
		return nil, 0, err
	}
	info, err := m.V.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	return m.Rename(info.Path, join(dest, info.Name))
}

// moveAll relocates a set of notes under dest, keeping the relative path each
// one had inside the folder being moved. Each note goes through Rename, so the
// links that pointed at it follow.
//
// A destination that is already taken doesn't abort the move: the rest of the
// folder still relocates, and the caller reports what stayed behind. That is
// the same rule cloud sync applies to one file that wouldn't upload.
func (m *Mesh) moveAll(under map[string]string, dest string) (moved int, updated int, err error) {
	// Deepest first, so moving `a/b/c.md` never has to pass through a path
	// that a shallower move has already claimed.
	paths := make([]string, 0, len(under))
	for p := range under {
		paths = append(paths, p)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(paths)))
	for _, p := range paths {
		target := join(dest, under[p])
		if target == p {
			continue
		}
		_, n, err := m.Rename(p, target)
		if err != nil {
			var exists *vault.ExistsError
			if errors.As(err, &exists) {
				continue // name already taken there; leave it where it is
			}
			return moved, updated, err
		}
		moved++
		updated += n
	}
	return moved, updated, nil
}

// join builds a vault path from a folder and the rest, treating "" as the root.
func join(folder, rest string) string {
	if folder == "" {
		return rest
	}
	return folder + "/" + rest
}
