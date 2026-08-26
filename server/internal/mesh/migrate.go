package mesh

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// Migration is what one pass of MigrateCategoriesToFolders did, so the startup
// log can say it plainly rather than leaving the user to diff their vault.
type Migration struct {
	// Filed is notes moved into a folder named for their first category.
	Filed int
	// Stripped is notes whose `categories:` key was removed — every note the
	// migration touched, including those already in the right folder.
	Stripped int
	// Dropped names second and later categories that had nowhere to go: a
	// file lives in one folder, so only the first survives.
	Dropped []string
	// Blocked names notes whose destination was already taken. They keep both
	// their place and their frontmatter, so a later run can retry.
	Blocked []string
}

// Empty reports whether there was nothing to do — the ordinary case on every
// start after the first.
func (m Migration) Empty() bool { return m.Filed == 0 && m.Stripped == 0 && len(m.Blocked) == 0 }

// Summary is a one-line description for the log.
func (m Migration) Summary() string {
	s := fmt.Sprintf("filed %d note(s) into folders, cleared frontmatter on %d", m.Filed, m.Stripped)
	if len(m.Dropped) > 0 {
		s += fmt.Sprintf(", dropped %d extra categor%s", len(m.Dropped),
			map[bool]string{true: "y", false: "ies"}[len(m.Dropped) == 1])
	}
	if len(m.Blocked) > 0 {
		s += fmt.Sprintf(", %d blocked by a name already taken", len(m.Blocked))
	}
	return s
}

// MigrateCategoriesToFolders converts the vault from the old `categories:`
// frontmatter to folders, which are now the same thing.
//
// Each note carrying categories moves into a folder named for its *first* one
// and loses the key. The first is the only one that can survive: a file lives
// in exactly one directory, and inventing `Money-and-Investing/` or copying the
// note would both be worse than saying plainly what was dropped.
//
// It is safe to run on every start. A vault with no `categories:` left is a
// vault already migrated, so the second run does nothing and reports nothing.
// Notes already sitting in the right folder are only stripped, never moved.
func (m *Mesh) MigrateCategoriesToFolders() (Migration, error) {
	var out Migration
	snap, err := m.Snapshot()
	if err != nil {
		return out, err
	}
	// Deterministic order: the log should read the same twice, and a blocked
	// destination should be decided by path, not by walk order.
	paths := make([]string, 0, len(snap.Notes))
	for i := range snap.Notes {
		if len(snap.cats[snap.Notes[i].Path]) > 0 {
			paths = append(paths, snap.Notes[i].Path)
		}
	}
	sort.Strings(paths)

	for _, path := range paths {
		cats := snap.cats[path]
		info, err := m.V.Stat(path)
		if err != nil {
			continue // deleted underneath us; the next start heals
		}
		folder, err := folderForCategory(cats[0])
		if err != nil {
			// A category that can't be a folder name (all punctuation, say)
			// leaves the note unfiled rather than failing the whole start.
			folder = ""
		}
		for _, extra := range cats[1:] {
			out.Dropped = append(out.Dropped, fmt.Sprintf("%s: %s", path, extra))
		}

		// Strip first, move second. Both orders leave the vault consistent,
		// but this one means a crash between them loses the key rather than
		// leaving a note filed under a category it still claims in text.
		content, _, err := m.V.Read(path)
		if err != nil {
			continue
		}
		stripped := vault.WithCategories(content, nil)
		if stripped != content {
			if _, err := m.V.Write(path, stripped); err != nil {
				return out, err
			}
		}
		out.Stripped++

		if folder == "" || strings.EqualFold(info.Dir, folder) {
			continue // already where it belongs, or nowhere to put it
		}
		if _, _, err := m.Rename(path, join(folder, info.Name)); err != nil {
			var exists *vault.ExistsError
			if errors.As(err, &exists) {
				out.Blocked = append(out.Blocked, path)
				continue
			}
			return out, err
		}
		out.Filed++
	}
	return out, nil
}

// folderForCategory turns a category name into a folder name. Categories
// allowed characters a path cannot carry (`*`, `?`, `<`), so this is
// SanitizeName's job, one segment at a time — a category that reads "a/b"
// becomes a nested folder rather than a file called "a/b".
func folderForCategory(name string) (string, error) {
	segs := strings.Split(name, "/")
	out := make([]string, 0, len(segs))
	for _, seg := range segs {
		if strings.TrimSpace(seg) == "" {
			continue
		}
		clean, err := vault.SanitizeName(seg)
		if err != nil {
			return "", err
		}
		out = append(out, clean)
	}
	if len(out) == 0 {
		return "", &vault.ValidationError{Msg: "category has no usable folder name"}
	}
	return CleanFolder(strings.Join(out, "/"))
}

// HasFrontmatterCategories reports whether any note still carries the old
// `categories:` key. It is how a start decides whether a migration is worth a
// checkpoint — the answer is false on every vault that has already converted.
func (s *Snapshot) HasFrontmatterCategories() bool {
	for i := range s.Notes {
		if len(s.cats[s.Notes[i].Path]) > 0 {
			return true
		}
	}
	return false
}
