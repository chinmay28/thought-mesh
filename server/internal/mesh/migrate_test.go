package mesh

import (
	"strings"
	"testing"
)

func read(t *testing.T, m *Mesh, path string) string {
	t.Helper()
	content, _, err := m.V.Read(path)
	if err != nil {
		t.Fatalf("Read(%q): %v", path, err)
	}
	return content
}

func TestMigrateFilesNotesByTheirFirstCategory(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Stocks.md", "---\ncategories: [Money, Investing]\n---\nbody\n")

	report, err := m.MigrateCategoriesToFolders()
	if err != nil {
		t.Fatalf("MigrateCategoriesToFolders: %v", err)
	}
	if report.Filed != 1 || report.Stripped != 1 {
		t.Fatalf("report = %+v; want 1 filed, 1 stripped", report)
	}
	// A file lives in one folder, so only the first category can survive —
	// and what was dropped has to be said out loud, not silently discarded.
	if len(report.Dropped) != 1 || !strings.Contains(report.Dropped[0], "Investing") {
		t.Fatalf("dropped = %v; want the Investing category named", report.Dropped)
	}
	if got := notePaths(t, m); len(got) != 1 || got[0] != "Money/Stocks.md" {
		t.Fatalf("notes = %v; want [Money/Stocks.md]", got)
	}
	if c := read(t, m, "Money/Stocks.md"); strings.Contains(c, "categories") {
		t.Errorf("frontmatter key survived: %q", c)
	}
}

func TestMigrateKeepsOtherFrontmatterByteForByte(t *testing.T) {
	m := newMesh(t)
	put(t, m, "N.md", "---\ntitle: Keep me\ncategories: [Money]\nobsidian-prop: 'x'\n---\nbody\n")

	if _, err := m.MigrateCategoriesToFolders(); err != nil {
		t.Fatalf("MigrateCategoriesToFolders: %v", err)
	}
	got := read(t, m, "Money/N.md")
	for _, want := range []string{"title: Keep me", "obsidian-prop: 'x'"} {
		if !strings.Contains(got, want) {
			t.Errorf("frontmatter written by another tool was lost (%q): %q", want, got)
		}
	}
}

func TestMigrateLeavesNotesAlreadyInTheRightFolder(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Money/N.md", "---\ncategories: [money]\n---\nbody\n")

	report, err := m.MigrateCategoriesToFolders()
	if err != nil {
		t.Fatalf("MigrateCategoriesToFolders: %v", err)
	}
	// Matching is case-insensitive: "money" and Money/ are the same place, so
	// this is a strip, not a move.
	if report.Filed != 0 || report.Stripped != 1 {
		t.Fatalf("report = %+v; want 0 filed, 1 stripped", report)
	}
	if got := notePaths(t, m); len(got) != 1 || got[0] != "Money/N.md" {
		t.Fatalf("notes = %v; want the note left where it was", got)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	m := newMesh(t)
	put(t, m, "A.md", "---\ncategories: [Money]\n---\nbody\n")

	if _, err := m.MigrateCategoriesToFolders(); err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := m.MigrateCategoriesToFolders()
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// Running on every start has to be free once the vault has converted.
	if !second.Empty() {
		t.Fatalf("second run = %+v; want nothing to do", second)
	}
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.HasFrontmatterCategories() {
		t.Error("a migrated vault should report no frontmatter categories left")
	}
}

func TestMigrateTurnsASlashedCategoryIntoNestedFolders(t *testing.T) {
	m := newMesh(t)
	put(t, m, "A.md", "---\ncategories: [Reading/2026]\n---\nbody\n")

	if _, err := m.MigrateCategoriesToFolders(); err != nil {
		t.Fatalf("MigrateCategoriesToFolders: %v", err)
	}
	if got := notePaths(t, m); len(got) != 1 || got[0] != "Reading/2026/A.md" {
		t.Fatalf("notes = %v; want [Reading/2026/A.md]", got)
	}
}

func TestMigrateSanitizesACategoryAPathCannotHold(t *testing.T) {
	m := newMesh(t)
	// Categories allowed characters a path cannot carry, so the folder name
	// has to be sanitized rather than the migration failing.
	put(t, m, "A.md", "---\ncategories: [what?*now]\n---\nbody\n")

	if _, err := m.MigrateCategoriesToFolders(); err != nil {
		t.Fatalf("MigrateCategoriesToFolders: %v", err)
	}
	got := notePaths(t, m)
	if len(got) != 1 || strings.ContainsAny(got[0], `?*`) {
		t.Fatalf("notes = %v; want a sanitized folder name", got)
	}
	if !strings.HasSuffix(got[0], "/A.md") {
		t.Fatalf("notes = %v; want the note filed under a folder", got)
	}
}

func TestMigrateReportsANoteItCouldNotFile(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Money/Same.md", "already here")
	put(t, m, "Same.md", "---\ncategories: [Money]\n---\nbody\n")

	report, err := m.MigrateCategoriesToFolders()
	if err != nil {
		t.Fatalf("MigrateCategoriesToFolders: %v", err)
	}
	// The destination name is taken. Overwriting would lose a note, so it
	// stays put and gets named in the report.
	if len(report.Blocked) != 1 {
		t.Fatalf("report = %+v; want 1 blocked", report)
	}
	if got := notePaths(t, m); len(got) != 2 {
		t.Fatalf("notes = %v; want both to survive", got)
	}
}

func TestMigrateLeavesNotesWithoutCategoriesAlone(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Plain.md", "no frontmatter at all\n")
	put(t, m, "Ruled.md", "---\nnot frontmatter, just a rule\n")

	report, err := m.MigrateCategoriesToFolders()
	if err != nil {
		t.Fatalf("MigrateCategoriesToFolders: %v", err)
	}
	if !report.Empty() {
		t.Fatalf("report = %+v; want nothing to do", report)
	}
	if got := read(t, m, "Ruled.md"); got != "---\nnot frontmatter, just a rule\n" {
		t.Errorf("a leading rule was treated as frontmatter: %q", got)
	}
}
