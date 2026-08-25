package vault

import (
	"os"
	"path/filepath"
	"testing"
)

// Files is the set cloud sync mirrors: everything the user put in the folder,
// not just the notes — and nothing that belongs to another tool.
func TestFilesIncludesAttachmentsAndSkipsHiddenState(t *testing.T) {
	v := newVault(t)
	if _, err := v.Write("Idea.md", "a thought\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := v.WriteFile("attachments/Screenshot 2026-08-25 at 14.02.31.png", []byte{0x89, 'P'}); err != nil {
		t.Fatal(err)
	}
	// Hidden state from other tools, and a half-written temp file.
	os.MkdirAll(filepath.Join(v.Root, ".obsidian"), 0o755)
	os.WriteFile(filepath.Join(v.Root, ".obsidian", "config"), []byte("{}"), 0o644)
	os.WriteFile(filepath.Join(v.Root, "Idea.md.tmp~"), []byte("half"), 0o644)

	files, err := v.Files()
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	got := map[string]bool{}
	for _, f := range files {
		got[f.Path] = true
	}
	if !got["Idea.md"] || !got["attachments/Screenshot 2026-08-25 at 14.02.31.png"] {
		t.Errorf("files = %v", got)
	}
	if got[".obsidian/config"] || got["Idea.md.tmp~"] {
		t.Errorf("files should not include other tools' state or temp files: %v", got)
	}
}

// CleanFilePath is looser than CleanPath — an attachment's name isn't ours to
// choose — and just as strict about escaping the vault.
func TestCleanFilePath(t *testing.T) {
	ok := []string{
		"Idea.md",
		"attachments/photo (1).png",
		"journal/2026-08-23.md",
		"./Idea.md",
		"notes/a note: with a colon.md", // fine as a file, unlike a wikilink target
	}
	for _, p := range ok {
		if _, err := CleanFilePath(p); err != nil {
			t.Errorf("CleanFilePath(%q) = %v; want accepted", p, err)
		}
	}
	bad := []string{"", "/etc/passwd", "../escape.md", "a/../../b.md", ".git/config", `a\b.md`, "a/\x00b"}
	for _, p := range bad {
		if _, err := CleanFilePath(p); err == nil {
			t.Errorf("CleanFilePath(%q) should have been refused", p)
		}
	}
}

func TestWriteReadRemoveFile(t *testing.T) {
	v := newVault(t)
	if _, err := v.WriteFile("journal/2026-08-23.md", []byte("today\n")); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	data, err := v.ReadFile("journal/2026-08-23.md")
	if err != nil || string(data) != "today\n" {
		t.Fatalf("ReadFile = %q, %v", data, err)
	}
	if err := v.RemoveFile("journal/2026-08-23.md"); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}
	// The emptied folder goes too — a sync mirroring a deletion shouldn't
	// leave husk directories behind.
	if _, err := os.Stat(filepath.Join(v.Root, "journal")); !os.IsNotExist(err) {
		t.Errorf("empty folder survived: %v", err)
	}
	// Removing what is already gone is what the caller wanted.
	if err := v.RemoveFile("journal/2026-08-23.md"); err != nil {
		t.Errorf("removing a missing file = %v; want nil", err)
	}
}
