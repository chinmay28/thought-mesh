package vault

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// File-level access to the vault, for the callers that care about the folder
// rather than the notes in it — cloud sync mirrors the whole tree, attachments
// included, exactly as Zip does.
//
// This sits deliberately beside the note API rather than replacing it. A note
// is a `.md` file with a validated name; a *file* is anything non-hidden the
// user put in the folder, and sync has to carry it whether or not it would
// pass CleanPath (an image called "Screenshot 2026-08-25 at 14.02.31.png" would
// not). What both share is the hard boundary: nothing escapes the root, and
// nothing hidden is touched.

// FileInfo describes one file in the vault, note or not.
type FileInfo struct {
	Path    string // vault-relative, "/" separated
	Size    int64
	MtimeMs int64
}

// CleanFilePath validates a vault-relative path to any file. It is looser than
// CleanPath — no ".md" is added and the wikilink-hostile characters are allowed,
// because an attachment's name is not ours to choose — and just as strict about
// the things that would let a path escape the vault or collide with the state
// other tools keep in it: absolute paths, traversal, backslashes, hidden
// segments, control characters.
func CleanFilePath(p string) (string, error) {
	p = strings.TrimSpace(strings.TrimPrefix(p, "./"))
	if p == "" {
		return "", &ValidationError{"file path is empty"}
	}
	if len(p) > maxPath {
		return "", &ValidationError{fmt.Sprintf("file path longer than %d characters", maxPath)}
	}
	if strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return "", &ValidationError{fmt.Sprintf("unusable file path: %q", p)}
	}
	segs := strings.Split(p, "/")
	for _, seg := range segs {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", &ValidationError{fmt.Sprintf("unusable file path: %q", p)}
		}
		if len(seg) > maxSegment {
			return "", &ValidationError{fmt.Sprintf("path segment longer than %d characters", maxSegment)}
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return "", &ValidationError{fmt.Sprintf("unusable file path: %q", p)}
			}
		}
	}
	return strings.Join(segs, "/"), nil
}

// Files walks the vault and returns every non-hidden file, sorted by path —
// the same set Zip captures, and the set cloud sync mirrors. Hidden entries
// (.git, .obsidian, .trash) are skipped: they are other tools' state, not the
// user's notes, and a sync that carried them would fight those tools.
func (v *Vault) Files() ([]FileInfo, error) {
	var files []FileInfo
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
		// Write-then-rename leaves one of these behind only if the server died
		// mid-save; either way it is not a file anyone meant to keep in sync.
		if strings.HasSuffix(d.Name(), ".tmp~") {
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
		files = append(files, FileInfo{
			Path:    filepath.ToSlash(rel),
			Size:    info.Size(),
			MtimeMs: info.ModTime().UnixMilli(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// StatFile returns one file's info, or *NotFoundError.
func (v *Vault) StatFile(path string) (*FileInfo, error) {
	rel, err := CleanFilePath(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(v.abs(rel))
	if err != nil || fi.IsDir() {
		return nil, &NotFoundError{rel}
	}
	return &FileInfo{Path: rel, Size: fi.Size(), MtimeMs: fi.ModTime().UnixMilli()}, nil
}

// ReadFile returns one file's bytes.
func (v *Vault) ReadFile(path string) ([]byte, error) {
	rel, err := CleanFilePath(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(v.abs(rel))
	if err != nil {
		return nil, &NotFoundError{rel}
	}
	return data, nil
}

// WriteFile saves bytes to a file, creating parent folders as needed. Like
// Write, it goes through a temporary file and a rename, so a sync interrupted
// mid-download can never leave a half-written note behind.
func (v *Vault) WriteFile(path string, data []byte) (*FileInfo, error) {
	rel, err := CleanFilePath(path)
	if err != nil {
		return nil, err
	}
	abs := v.abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	tmp := abs + ".tmp~"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return v.StatFile(rel)
}

// RemoveFile deletes a file and prunes the folders it emptied, so a sync that
// mirrors a deletion doesn't leave husk directories behind. A file that is
// already gone is not an error — the caller wanted it absent, and it is.
func (v *Vault) RemoveFile(path string) error {
	rel, err := CleanFilePath(path)
	if err != nil {
		return err
	}
	if err := os.Remove(v.abs(rel)); err != nil && !os.IsNotExist(err) {
		return err
	}
	dir := ""
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dir = rel[:i]
	}
	v.pruneEmptyDirs(dir)
	return nil
}
