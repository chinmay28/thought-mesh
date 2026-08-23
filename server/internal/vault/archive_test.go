package vault

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

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
