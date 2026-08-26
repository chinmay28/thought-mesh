package mesh

import (
	"strings"
	"testing"
)

// folderPaths is the vault's folder tree as a comparable slice.
func folderPaths(t *testing.T, m *Mesh) []string {
	t.Helper()
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var out []string
	for _, f := range snap.FolderList() {
		out = append(out, f.Path)
	}
	return out
}

func notePaths(t *testing.T, m *Mesh) []string {
	t.Helper()
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	var out []string
	for i := range snap.Notes {
		out = append(out, snap.Notes[i].Path)
	}
	return out
}

func TestFolderListIncludesAncestorsAndRoot(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Unfiled.md", "at the root")
	put(t, m, "Reading/2026/Deep.md", "nested two down")
	put(t, m, "Money/One.md", "a")
	put(t, m, "Money/Two.md", "b")

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	got := snap.FolderList()
	want := []string{"", "Money", "Reading", "Reading/2026"}
	if len(got) != len(want) {
		t.Fatalf("folders = %+v; want %v", got, want)
	}
	for i, w := range want {
		if got[i].Path != w {
			t.Fatalf("folder[%d] = %q; want %q", i, got[i].Path, w)
		}
	}
	// "Reading" holds no note directly but must still exist, or a browser
	// could not show a path down to "Reading/2026".
	byPath := map[string]Folder{}
	for _, f := range got {
		byPath[f.Path] = f
	}
	if c := byPath["Reading"].Count; c != 0 {
		t.Errorf("Reading count = %d; want 0", c)
	}
	if tot := byPath["Reading"].Total; tot != 1 {
		t.Errorf("Reading total = %d; want 1", tot)
	}
	if c := byPath["Money"].Count; c != 2 {
		t.Errorf("Money count = %d; want 2", c)
	}
	if tot := byPath[""].Total; tot != 4 {
		t.Errorf("root total = %d; want 4 (every note)", tot)
	}
	if c := byPath[""].Count; c != 1 {
		t.Errorf("root count = %d; want 1 (only the unfiled note)", c)
	}
	if d := byPath["Reading/2026"].Depth; d != 2 {
		t.Errorf("Reading/2026 depth = %d; want 2", d)
	}
}

func TestRenameFolderMovesNotesAndRewritesLinks(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Money/Stocks.md", "about stocks")
	put(t, m, "Money/Sub/Deep.md", "nested")
	put(t, m, "Outside.md", "see [[Money/Stocks|the note]] and [[Deep]]")

	moved, _, err := m.RenameFolder("Money", "Finance")
	if err != nil {
		t.Fatalf("RenameFolder: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved = %d; want 2", moved)
	}
	got := notePaths(t, m)
	want := []string{"Finance/Stocks.md", "Finance/Sub/Deep.md", "Outside.md"}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("notes = %v; missing %q", got, w)
		}
	}
	// The path-form link has to follow the move, alias intact — a rename that
	// left it dangling would be the folder silently breaking the vault.
	content, _, err := m.V.Read("Outside.md")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if strings.Contains(content, "[[Money/Stocks") {
		t.Errorf("link still points at the old folder: %q", content)
	}
	if !strings.Contains(content, "|the note]]") {
		t.Errorf("alias was lost: %q", content)
	}
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	for _, l := range snap.Links("Outside.md") {
		if l.Path == "" {
			t.Errorf("link %q stopped resolving after the folder rename", l.Target)
		}
	}
}

func TestRenameFolderMergesIntoExistingAndKeepsBlockedNotes(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Work/Same.md", "mine")
	put(t, m, "Work/Only.md", "moves fine")
	put(t, m, "work/Same.md", "theirs — the name is taken")

	// Renaming onto an existing folder merges the two; the note whose name is
	// already taken there stays where it is rather than overwriting.
	if _, _, err := m.RenameFolder("Work", "Archive"); err != nil {
		t.Fatalf("RenameFolder: %v", err)
	}
	got := notePaths(t, m)
	if len(got) != 3 {
		t.Fatalf("notes = %v; want all 3 to survive", got)
	}
}

func TestRenameFolderRejectsRootAndSelfNesting(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Money/A.md", "a")

	if _, _, err := m.RenameFolder("", "Money"); err == nil {
		t.Error("renaming the vault root should be refused")
	}
	if _, _, err := m.RenameFolder("Money", "Money/Inner"); err == nil {
		t.Error("moving a folder inside itself should be refused")
	}
	if _, _, err := m.RenameFolder("Nope", "Other"); err == nil {
		t.Error("renaming a folder that holds no notes should be a not-found")
	}
}

func TestDeleteFolderUnfilesRatherThanDeleting(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Money/A.md", "a")
	put(t, m, "Money/Sub/B.md", "b")

	moved, _, err := m.DeleteFolder("Money")
	if err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved = %d; want 2", moved)
	}
	got := notePaths(t, m)
	// Notes move up one level and keep their own substructure — deleting a
	// folder is unfiling, never data loss.
	want := map[string]bool{"A.md": true, "Sub/B.md": true}
	if len(got) != 2 {
		t.Fatalf("notes = %v; want 2", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected note path %q", g)
		}
	}
}

func TestDeleteNestedFolderKeepsTheParent(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Reading/2026/A.md", "a")

	if _, _, err := m.DeleteFolder("Reading/2026"); err != nil {
		t.Fatalf("DeleteFolder: %v", err)
	}
	got := notePaths(t, m)
	if len(got) != 1 || got[0] != "Reading/A.md" {
		t.Fatalf("notes = %v; want [Reading/A.md]", got)
	}
}

func TestMoveNoteFilesAndUnfiles(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Loose.md", "a note with no folder")

	if _, _, err := m.MoveNote("Loose.md", "Money"); err != nil {
		t.Fatalf("MoveNote: %v", err)
	}
	if got := notePaths(t, m); len(got) != 1 || got[0] != "Money/Loose.md" {
		t.Fatalf("notes = %v; want [Money/Loose.md]", got)
	}
	// "" is the vault root, which is how a note stops being filed at all.
	if _, _, err := m.MoveNote("Money/Loose.md", ""); err != nil {
		t.Fatalf("MoveNote to root: %v", err)
	}
	if got := notePaths(t, m); len(got) != 1 || got[0] != "Loose.md" {
		t.Fatalf("notes = %v; want [Loose.md]", got)
	}
	if got := folderPaths(t, m); len(got) != 1 || got[0] != "" {
		t.Fatalf("folders = %v; want just the root — an emptied folder stops existing", got)
	}
}

func TestCleanFolderRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"../escape", "a/../../b", "a/./b", ".hidden", "a/.git"} {
		if _, err := CleanFolder(bad); err == nil {
			t.Errorf("CleanFolder(%q) should be refused", bad)
		}
	}
	for _, ok := range []string{"", "/", "Money", "Reading/2026", "a/b/c"} {
		if _, err := CleanFolder(ok); err != nil {
			t.Errorf("CleanFolder(%q) = %v; want accepted", ok, err)
		}
	}
}
