package vault

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Zip snapshots the whole vault — every non-hidden file, notes and anything
// else, with the folder structure preserved — as a zip archive in memory.
//
// This is the artifact cloud sync uploads. It deliberately includes files
// beyond `*.md` (unlike List): the promise is "your data, all of it", and a
// vault may carry attachments or exports the notes refer to. Hidden entries
// (.git, .obsidian, .trash) are skipped for the same reason List skips them —
// they're other tools' state, not the user's notes.
//
// Entries use vault-relative forward-slash paths and Deflate compression;
// markdown compresses to a fraction of its size, which matters for an upload
// that runs on a schedule.
func (v *Vault) Zip() ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	err := filepath.WalkDir(v.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasPrefix(d.Name(), ".") && p != v.Root {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(v.Root, p)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		header.Method = zip.Deflate
		out, err := w.CreateHeader(header)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		_, err = out.Write(data)
		return err
	})
	if err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
