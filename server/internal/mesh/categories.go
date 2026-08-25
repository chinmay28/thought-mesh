package mesh

import (
	"sort"
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// Categories are the second thing derived from a vault walk, alongside links:
// each note declares its own in frontmatter (see vault.WithCategories), and the
// vault-wide vocabulary is whatever those declarations add up to.
//
// There is deliberately no list of categories anywhere — no registry file, no
// "create a category" step. A category exists exactly as long as some note
// claims it, which is what keeps the folder of markdown self-describing: copy
// one note somewhere else and its categories travel with it.

// Category is one name in the vault's vocabulary, with how many notes use it.
type Category struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Categories returns one note's categories, in the order the note lists them.
func (s *Snapshot) Categories(path string) []string {
	return s.cats[path]
}

// CategoryList is the vault's whole vocabulary, sorted by name
// (case-insensitively — "work" and "Work" are the same category, and the
// spelling that wins is the one on the shortest path, then the first
// alphabetically, so the answer doesn't depend on walk order).
func (s *Snapshot) CategoryList() []Category {
	type entry struct {
		name  string
		count int
	}
	byKey := map[string]*entry{}
	for _, note := range s.Notes {
		for _, cat := range s.cats[note.Path] {
			key := strings.ToLower(cat)
			if e, ok := byKey[key]; ok {
				e.count++
				if cat < e.name {
					e.name = cat
				}
				continue
			}
			byKey[key] = &entry{name: cat, count: 1}
		}
	}
	out := make([]Category, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, Category{Name: e.name, Count: e.count})
	}
	sort.Slice(out, func(i, j int) bool {
		li, lj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if li != lj {
			return li < lj
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// SetCategories replaces a note's categories, rewriting just the frontmatter
// key and leaving the body — and any other frontmatter — untouched.
//
// baseMtimeMs, when non-zero, is the version the caller was looking at: a
// mismatch is a *vault.StaleError, the same optimistic-concurrency path a
// content save takes, so tapping a category chip on a stale screen can't
// quietly undo an edit made somewhere else.
func (m *Mesh) SetCategories(path string, cats []string, baseMtimeMs int64) (*vault.NoteInfo, error) {
	clean, err := vault.NormalizeCategories(cats)
	if err != nil {
		return nil, err
	}
	content, info, err := m.V.Read(path)
	if err != nil {
		return nil, err
	}
	if baseMtimeMs != 0 && baseMtimeMs != info.MtimeMs {
		return nil, &vault.StaleError{Path: info.Path}
	}
	next := vault.WithCategories(content, clean)
	if next == content {
		return info, nil
	}
	return m.V.Write(info.Path, next)
}

// RenameCategory renames one category across the whole vault, the same way
// Rename rewrites wikilinks: a name the user is changing means the same thing
// before and after, so leaving half the notes on the old spelling would just be
// a silent split. Returns how many notes were rewritten.
//
// Renaming onto an existing category merges the two, which is the useful
// reading of "Work" → "work-stuff" when both already exist. Matching is
// case-insensitive, so fixing only the capitalization works.
func (m *Mesh) RenameCategory(from, to string) (int, error) {
	fromName, err := vault.NormalizeCategory(from)
	if err != nil {
		return 0, err
	}
	toName, err := vault.NormalizeCategory(to)
	if err != nil {
		return 0, err
	}
	snap, err := m.Snapshot()
	if err != nil {
		return 0, err
	}
	fromKey := strings.ToLower(fromName)
	updated := 0
	for _, note := range snap.Notes {
		cats := snap.cats[note.Path]
		if !containsFold(cats, fromKey) {
			continue
		}
		next := make([]string, 0, len(cats))
		for _, cat := range cats {
			if strings.EqualFold(cat, fromName) {
				next = append(next, toName)
				continue
			}
			next = append(next, cat)
		}
		// A note already carrying both names collapses to one entry.
		next, err := vault.NormalizeCategories(next)
		if err != nil {
			return updated, err
		}
		content, _, err := m.V.Read(note.Path)
		if err != nil {
			continue // deleted underneath us; the next snapshot heals
		}
		rewritten := vault.WithCategories(content, next)
		if rewritten == content {
			continue
		}
		if _, err := m.V.Write(note.Path, rewritten); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// DeleteCategory removes one category from every note that carries it.
func (m *Mesh) DeleteCategory(name string) (int, error) {
	clean, err := vault.NormalizeCategory(name)
	if err != nil {
		return 0, err
	}
	snap, err := m.Snapshot()
	if err != nil {
		return 0, err
	}
	key := strings.ToLower(clean)
	updated := 0
	for _, note := range snap.Notes {
		cats := snap.cats[note.Path]
		if !containsFold(cats, key) {
			continue
		}
		next := make([]string, 0, len(cats))
		for _, cat := range cats {
			if strings.ToLower(cat) != key {
				next = append(next, cat)
			}
		}
		content, _, err := m.V.Read(note.Path)
		if err != nil {
			continue
		}
		rewritten := vault.WithCategories(content, next)
		if rewritten == content {
			continue
		}
		if _, err := m.V.Write(note.Path, rewritten); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// containsFold reports whether cats holds lowerKey, compared case-insensitively.
func containsFold(cats []string, lowerKey string) bool {
	for _, cat := range cats {
		if strings.ToLower(cat) == lowerKey {
			return true
		}
	}
	return false
}
