package vault

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// (zip/bytes are shared by the snapshot and restore tests below)

func TestZipSnapshotsWholeVault(t *testing.T) {
	v := newVault(t)
	mustCreate(t, v, "A.md", "# Alpha")
	mustCreate(t, v, "deep/nested/B.md", "beta")
	// Non-markdown data rides along (attachments etc.); hidden state doesn't.
	os.WriteFile(filepath.Join(v.Root, "attachment.txt"), []byte("extra"), 0o644)
	os.MkdirAll(filepath.Join(v.Root, ".obsidian"), 0o755)
	os.WriteFile(filepath.Join(v.Root, ".obsidian", "workspace.json"), []byte("{}"), 0o644)

	data, err := v.Zip()
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader: %v", err)
	}
	got := map[string]string{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		content, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = string(content)
	}
	want := map[string]string{
		"A.md":             "# Alpha",
		"deep/nested/B.md": "beta",
		"attachment.txt":   "extra",
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %v", got)
	}
	for name, content := range want {
		if got[name] != content {
			t.Errorf("%s = %q; want %q", name, got[name], content)
		}
	}
}

func TestRestoreZipRoundTrip(t *testing.T) {
	v := newVault(t)
	mustCreate(t, v, "Keep.md", "original")
	mustCreate(t, v, "sub/Deep.md", "nested")
	os.WriteFile(filepath.Join(v.Root, "img.txt"), []byte("bin"), 0o644)
	snapshot, err := v.Zip()
	if err != nil {
		t.Fatalf("Zip: %v", err)
	}

	// Mutate the vault: edit one note, add another, drop one, plant hidden
	// state the restore must leave alone.
	v.Write("Keep.md", "changed since the snapshot")
	mustCreate(t, v, "Added Later.md", "should disappear")
	v.Delete("sub/Deep.md")
	os.MkdirAll(filepath.Join(v.Root, ".git"), 0o755)
	os.WriteFile(filepath.Join(v.Root, ".git", "HEAD"), []byte("ref"), 0o644)

	count, err := v.RestoreZip(snapshot)
	if err != nil {
		t.Fatalf("RestoreZip: %v", err)
	}
	if count != 3 {
		t.Errorf("count = %d; want 3", count)
	}
	if content, _, _ := v.Read("Keep.md"); content != "original" {
		t.Errorf("Keep.md = %q", content)
	}
	if content, _, _ := v.Read("sub/Deep.md"); content != "nested" {
		t.Errorf("sub/Deep.md = %q", content)
	}
	if _, _, err := v.Read("Added Later.md"); !IsNotFound(err) {
		t.Error("a restore is a replace — later notes must not survive")
	}
	if data, _ := os.ReadFile(filepath.Join(v.Root, "img.txt")); string(data) != "bin" {
		t.Errorf("img.txt = %q", data)
	}
	if data, _ := os.ReadFile(filepath.Join(v.Root, ".git", "HEAD")); string(data) != "ref" {
		t.Error("hidden entries must survive a restore untouched")
	}
}

func TestRestoreZipRejectsHostileArchives(t *testing.T) {
	v := newVault(t)
	mustCreate(t, v, "Precious.md", "must survive a rejected restore")

	build := func(name string) []byte {
		var buf bytes.Buffer
		w := zip.NewWriter(&buf)
		f, _ := w.Create(name)
		f.Write([]byte("evil"))
		w.Close()
		return buf.Bytes()
	}
	for _, name := range []string{
		"../escape.md", "/abs.md", "a/../../b.md", ".hidden/x.md", "back\\slash.md",
	} {
		if _, err := v.RestoreZip(build(name)); err == nil {
			t.Errorf("RestoreZip accepted %q", name)
		}
	}
	if _, err := v.RestoreZip([]byte("not a zip")); err == nil {
		t.Error("RestoreZip accepted garbage")
	}
	// Every rejection above must have left the vault untouched.
	if content, _, err := v.Read("Precious.md"); err != nil || content != "must survive a rejected restore" {
		t.Errorf("vault damaged by rejected restore: %q, %v", content, err)
	}
}
