package mesh

import (
	"strings"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// Rename moves a note and rewrites the wikilinks that pointed at it, so a
// rename never orphans the mesh — the reason renames go through the server
// rather than being a bare file move.
//
// Returns the renamed note's info and how many other notes were rewritten.
func (m *Mesh) Rename(oldPath, newPath string) (*vault.NoteInfo, int, error) {
	pre, err := m.Snapshot()
	if err != nil {
		return nil, 0, err
	}
	oldInfo, err := m.V.Stat(oldPath)
	if err != nil {
		return nil, 0, err
	}

	// Referrers are decided against the pre-rename snapshot: every note with
	// at least one link that resolved to the old path.
	var referrers []string
	for _, n := range pre.Notes {
		for _, raw := range pre.links[n.Path] {
			if pre.Resolve(raw.Target) == oldInfo.Path {
				referrers = append(referrers, n.Path)
				break
			}
		}
	}

	newInfo, err := m.V.Rename(oldInfo.Path, newPath)
	if err != nil {
		return nil, 0, err
	}

	// Pick what rewritten links should say: the bare name when it now
	// unambiguously means this note, otherwise the full path (without .md).
	post, err := m.Snapshot()
	if err != nil {
		return newInfo, 0, err
	}
	newTarget := newInfo.Name
	if post.Resolve(newInfo.Name) != newInfo.Path {
		newTarget = strings.TrimSuffix(newInfo.Path, ".md")
	}

	resolvesToOld := func(target string) bool { return pre.Resolve(target) == oldInfo.Path }
	updated := 0
	for _, ref := range referrers {
		// A self-link lives in the file that just moved.
		if ref == oldInfo.Path {
			ref = newInfo.Path
		}
		content, _, err := m.V.Read(ref)
		if err != nil {
			continue
		}
		rewritten, changed := RewriteLinks(content, resolvesToOld, newTarget)
		// A link can be rewritten to exactly what it already said — moving a
		// note whose bare name still resolves. Writing that back would bump
		// the mtime and earn a history commit for a file nobody changed, and
		// a folder rename does this once per note it moves.
		if !changed || rewritten == content {
			continue
		}
		if _, err := m.V.Write(ref, rewritten); err != nil {
			return newInfo, updated, err
		}
		updated++
	}
	return newInfo, updated, nil
}
