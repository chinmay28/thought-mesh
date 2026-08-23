package vault

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
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

// Restore limits. Far beyond any plausible vault of markdown; present so a
// hostile or corrupt archive can't exhaust the disk (zip bombs advertise
// small and decompress huge).
const (
	maxRestoreEntries = 100_000
	maxRestoreBytes   = 2 << 30 // 2 GiB uncompressed, total
)

// RestoreZip replaces the vault's contents with the archive's — the inverse
// of Zip, used by cloud restore.
//
// Semantics are a true restore, not a merge: after it returns, the vault's
// non-hidden contents are exactly the archive's (hidden entries — .git,
// .obsidian — are left untouched, the same way Zip never includes them).
// Callers are expected to snapshot the current vault first; cloud.Service
// does.
//
// The archive is fully validated and unpacked to a temporary directory
// BEFORE anything existing is removed, so a corrupt or hostile archive
// (traversal paths, absolute paths, oversized contents) leaves the vault
// untouched. Returns the number of files restored.
func (v *Vault) RestoreZip(data []byte) (int, error) {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0, &ValidationError{Msg: "not a readable zip archive: " + err.Error()}
	}
	if len(r.File) > maxRestoreEntries {
		return 0, &ValidationError{Msg: fmt.Sprintf("archive has more than %d entries", maxRestoreEntries)}
	}

	// Stage next to the vault (same filesystem, so the final moves are
	// renames), hidden-named so a walk of the parent never confuses it for
	// data.
	tmp, err := os.MkdirTemp(filepath.Dir(v.Root), ".thoughtmesh-restore-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(tmp)

	count := 0
	var total int64
	for _, f := range r.File {
		name := filepath.ToSlash(f.Name)
		if f.FileInfo().IsDir() || strings.HasSuffix(name, "/") {
			continue // directories are implied by the files inside them
		}
		if err := validateArchivePath(name); err != nil {
			return 0, err
		}
		total += int64(f.UncompressedSize64)
		if total > maxRestoreBytes {
			return 0, &ValidationError{Msg: "archive decompresses past the restore size limit"}
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return 0, err
		}
		if err := extractOne(f, dst); err != nil {
			return 0, err
		}
		count++
	}

	// Everything staged and valid — now swap: clear the vault's non-hidden
	// entries, then move the staged tree in.
	entries, err := os.ReadDir(v.Root)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(v.Root, e.Name())); err != nil {
			return 0, err
		}
	}
	staged, err := os.ReadDir(tmp)
	if err != nil {
		return 0, err
	}
	for _, e := range staged {
		if err := os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(v.Root, e.Name())); err != nil {
			return 0, err
		}
	}
	return count, nil
}

// validateArchivePath vets one entry path from an untrusted archive. Looser
// than CleanPath — an archive may carry attachments whose names never went
// through note validation — but hard on everything that could escape the
// vault or collide with hidden state: absolute paths, traversal, hidden
// segments, backslashes and control characters.
func validateArchivePath(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return &ValidationError{Msg: fmt.Sprintf("archive entry has an unusable path: %q", name)}
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return &ValidationError{Msg: fmt.Sprintf("archive entry has an unusable path: %q", name)}
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return &ValidationError{Msg: fmt.Sprintf("archive entry has an unusable path: %q", name)}
			}
		}
	}
	return nil
}

// extractOne writes a single archive entry, holding the per-archive limits
// again at stream level — the zip header's size claim is untrusted.
func extractOne(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, io.LimitReader(rc, maxRestoreBytes)); err != nil {
		return err
	}
	return out.Close()
}
