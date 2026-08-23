package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func newVault(t *testing.T) *Vault {
	t.Helper()
	v, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return v
}

func TestCleanPath(t *testing.T) {
	good := map[string]string{
		"Foo":                   "Foo.md",
		"Foo.md":                "Foo.md",
		"a/b/Note":              "a/b/Note.md",
		"  Spaced  ":            "Spaced.md",
		"Ünïcode näme":          "Ünïcode näme.md",
		"journal/2026-08-23.md": "journal/2026-08-23.md",
		"With (parens) & +.md":  "With (parens) & +.md",
	}
	for in, want := range good {
		got, err := CleanPath(in)
		if err != nil || got != want {
			t.Errorf("CleanPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	bad := []string{
		"", "   ", "../evil", "a/../b", "a//b", "/abs", ".hidden", "dir/.hidden",
		"has#hash", "has|pipe", "has[bracket", "back\\slash", "colon:name",
		"quest?ion", "star*", "quote\"", "less<", "more>", "ctrl\x01char", ".md",
	}
	for _, in := range bad {
		if got, err := CleanPath(in); err == nil {
			t.Errorf("CleanPath(%q) = %q; want error", in, got)
		} else {
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Errorf("CleanPath(%q) error is %T; want *ValidationError", in, err)
			}
		}
	}
}

func TestSanitizeName(t *testing.T) {
	got, err := SanitizeName("  What is a [[mesh]]? #ideas  ")
	if err != nil {
		t.Fatalf("SanitizeName: %v", err)
	}
	if want := "What is a --mesh--- -ideas"; got != want {
		t.Errorf("SanitizeName = %q; want %q", got, want)
	}
	if _, err := SanitizeName("###"); err == nil {
		t.Error("SanitizeName(all-forbidden) should fail")
	}
}

func TestCreateReadWriteDelete(t *testing.T) {
	v := newVault(t)
	info, err := v.Create("ideas/First Note", "# First\n")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if info.Path != "ideas/First Note.md" || info.Name != "First Note" || info.Dir != "ideas" {
		t.Errorf("info = %+v", info)
	}
	if _, err := v.Create("ideas/First Note.md", "again"); err == nil {
		t.Fatal("Create over existing note should fail")
	} else {
		var ex *ExistsError
		if !errors.As(err, &ex) {
			t.Fatalf("error is %T; want *ExistsError", err)
		}
	}

	content, _, err := v.Read("ideas/First Note.md")
	if err != nil || content != "# First\n" {
		t.Fatalf("Read = %q, %v", content, err)
	}

	if _, err := v.Write("ideas/First Note.md", "updated"); err != nil {
		t.Fatalf("Write: %v", err)
	}
	content, _, _ = v.Read("ideas/First Note.md")
	if content != "updated" {
		t.Errorf("after Write, content = %q", content)
	}

	if err := v.Delete("ideas/First Note.md"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, err := v.Read("ideas/First Note.md"); !IsNotFound(err) {
		t.Errorf("Read after delete: %v; want not-found", err)
	}
	// The emptied folder is pruned.
	if _, err := os.Stat(filepath.Join(v.Root, "ideas")); !os.IsNotExist(err) {
		t.Error("empty parent folder was not pruned")
	}
}

func TestListSkipsHiddenAndNonMarkdown(t *testing.T) {
	v := newVault(t)
	mustCreate(t, v, "a.md", "")
	mustCreate(t, v, "dir/b.md", "")
	// Non-markdown and hidden entries, planted directly on disk.
	os.WriteFile(filepath.Join(v.Root, "image.png"), []byte("x"), 0o644)
	os.MkdirAll(filepath.Join(v.Root, ".obsidian"), 0o755)
	os.WriteFile(filepath.Join(v.Root, ".obsidian", "c.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(v.Root, ".hidden.md"), []byte("x"), 0o644)

	notes, err := v.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(notes) != 2 || notes[0].Path != "a.md" || notes[1].Path != "dir/b.md" {
		t.Errorf("List = %+v", notes)
	}
}

func TestRename(t *testing.T) {
	v := newVault(t)
	mustCreate(t, v, "old/Name.md", "body")
	mustCreate(t, v, "taken.md", "")

	if _, err := v.Rename("old/Name.md", "taken.md"); err == nil {
		t.Fatal("Rename onto an existing note should fail")
	}
	info, err := v.Rename("old/Name.md", "new/Renamed.md")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if info.Path != "new/Renamed.md" {
		t.Errorf("renamed to %q", info.Path)
	}
	if content, _, _ := v.Read("new/Renamed.md"); content != "body" {
		t.Errorf("content after rename = %q", content)
	}
	if _, err := os.Stat(filepath.Join(v.Root, "old")); !os.IsNotExist(err) {
		t.Error("old folder was not pruned")
	}
}

func mustCreate(t *testing.T, v *Vault, path, content string) {
	t.Helper()
	if _, err := v.Create(path, content); err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
}
