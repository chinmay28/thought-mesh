package mesh

import (
	"strings"
	"testing"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

func newCategoryMesh(t *testing.T, notes map[string]string) (*Mesh, *vault.Vault) {
	t.Helper()
	v, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for path, content := range notes {
		if _, err := v.Write(path, content); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return New(v), v
}

func TestCategoryListCountsAcrossTheVault(t *testing.T) {
	m, _ := newCategoryMesh(t, map[string]string{
		"A.md":        "---\ncategories: [Work, Ideas]\n---\na\n",
		"B.md":        "---\ncategories: [work]\n---\nb\n", // same category, other spelling
		"sub/C.md":    "---\ncategories: [Ideas]\n---\nc\n",
		"Untagged.md": "nothing here\n",
	})
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	cats := snap.CategoryList()
	if len(cats) != 2 {
		t.Fatalf("categories = %+v", cats)
	}
	// Sorted case-insensitively by name; "work" and "Work" are one category.
	if cats[0].Name != "Ideas" || cats[0].Count != 2 {
		t.Errorf("first = %+v", cats[0])
	}
	if cats[1].Count != 2 || !strings.EqualFold(cats[1].Name, "Work") {
		t.Errorf("second = %+v", cats[1])
	}
	if got := snap.Categories("A.md"); len(got) != 2 || got[0] != "Work" {
		t.Errorf("A.md categories = %v", got)
	}
	if got := snap.Categories("Untagged.md"); len(got) != 0 {
		t.Errorf("untagged note = %v", got)
	}
}

func TestSetCategoriesRewritesOnlyTheFrontmatter(t *testing.T) {
	m, v := newCategoryMesh(t, map[string]string{
		"A.md": "# Heading\n\nbody with a [[Link]]\n",
	})
	if _, err := m.SetCategories("A.md", []string{"Ideas", "ideas", " "}, 0); err != nil {
		t.Fatalf("SetCategories: %v", err)
	}
	content, _, err := v.Read("A.md")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(content, "---\ncategories: [Ideas]\n---\n") {
		t.Errorf("content = %q", content)
	}
	if !strings.Contains(content, "body with a [[Link]]") {
		t.Errorf("body lost: %q", content)
	}
	// Duplicates collapsed, blanks dropped.
	snap, _ := m.Snapshot()
	if got := snap.Categories("A.md"); len(got) != 1 || got[0] != "Ideas" {
		t.Errorf("categories = %v", got)
	}
	// Frontmatter is not a wikilink source, and the link still resolves.
	if links := snap.Links("A.md"); len(links) != 1 || links[0].Target != "Link" {
		t.Errorf("links = %+v", links)
	}
}

// Assigning from a stale screen must not quietly revert an edit made elsewhere
// — the same optimistic concurrency a content save gets.
func TestSetCategoriesRejectsAStaleBaseMtime(t *testing.T) {
	m, _ := newCategoryMesh(t, map[string]string{"A.md": "body\n"})
	_, err := m.SetCategories("A.md", []string{"Ideas"}, 1)
	var stale *vault.StaleError
	if err == nil || !asStale(err, &stale) {
		t.Fatalf("stale write = %v; want a StaleError", err)
	}
	// The matching mtime is accepted.
	info, err := m.V.Stat("A.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetCategories("A.md", []string{"Ideas"}, info.MtimeMs); err != nil {
		t.Errorf("current mtime = %v", err)
	}
}

func asStale(err error, target **vault.StaleError) bool {
	s, ok := err.(*vault.StaleError)
	if ok {
		*target = s
	}
	return ok
}

// Renaming a category is the same promise as renaming a note: the name means
// the same thing afterwards, everywhere.
func TestRenameCategoryAcrossTheVault(t *testing.T) {
	m, _ := newCategoryMesh(t, map[string]string{
		"A.md": "---\ncategories: [Work, Ideas]\n---\na\n",
		"B.md": "---\ncategories: [work]\n---\nb\n",
		"C.md": "---\ncategories: [Ideas]\n---\nc\n",
	})
	updated, err := m.RenameCategory("Work", "Day job")
	if err != nil {
		t.Fatalf("RenameCategory: %v", err)
	}
	if updated != 2 {
		t.Errorf("updated %d notes; want 2 (matching is case-insensitive)", updated)
	}
	snap, _ := m.Snapshot()
	names := map[string]int{}
	for _, c := range snap.CategoryList() {
		names[c.Name] = c.Count
	}
	if names["Day job"] != 2 || names["Ideas"] != 2 {
		t.Errorf("after rename = %v", names)
	}
	if _, gone := names["Work"]; gone {
		t.Errorf("the old name survived: %v", names)
	}
}

// Renaming onto a name already in use merges the two rather than leaving a note
// carrying it twice.
func TestRenameCategoryOntoAnExistingOneMerges(t *testing.T) {
	m, _ := newCategoryMesh(t, map[string]string{
		"A.md": "---\ncategories: [Work, Job]\n---\na\n",
	})
	if _, err := m.RenameCategory("Job", "Work"); err != nil {
		t.Fatalf("RenameCategory: %v", err)
	}
	snap, _ := m.Snapshot()
	if got := snap.Categories("A.md"); len(got) != 1 || got[0] != "Work" {
		t.Errorf("categories = %v", got)
	}
}

func TestDeleteCategoryLeavesTheNotesAlone(t *testing.T) {
	m, v := newCategoryMesh(t, map[string]string{
		"A.md": "---\ncategories: [Work, Ideas]\n---\nthe note itself\n",
	})
	updated, err := m.DeleteCategory("Work")
	if err != nil {
		t.Fatalf("DeleteCategory: %v", err)
	}
	if updated != 1 {
		t.Errorf("updated = %d", updated)
	}
	content, _, _ := v.Read("A.md")
	if !strings.Contains(content, "the note itself") {
		t.Errorf("the note was damaged: %q", content)
	}
	snap, _ := m.Snapshot()
	if got := snap.Categories("A.md"); len(got) != 1 || got[0] != "Ideas" {
		t.Errorf("categories = %v", got)
	}
}

// A rename that moves a note must carry its categories with it — they live in
// the file, so this is really a check that nothing rewrites the frontmatter.
func TestRenamingANoteKeepsItsCategories(t *testing.T) {
	m, _ := newCategoryMesh(t, map[string]string{
		"A.md":     "---\ncategories: [Ideas]\n---\na\n",
		"Other.md": "points at [[A]]\n",
	})
	info, updated, err := m.Rename("A.md", "sub/Renamed")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if updated != 1 {
		t.Errorf("updated links in %d notes; want 1", updated)
	}
	snap, _ := m.Snapshot()
	if got := snap.Categories(info.Path); len(got) != 1 || got[0] != "Ideas" {
		t.Errorf("categories after rename = %v", got)
	}
}
