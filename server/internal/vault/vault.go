// Package vault is the note store: a directory of plain markdown files.
//
// The files ARE the data. There is no database and no sidecar index on disk —
// anything else (backlinks, graph, search) is derived in memory by
// internal/mesh, so a vault stays a perfectly ordinary folder you can sync,
// grep, back up, or open in any other markdown editor (Obsidian included)
// without Thought Mesh in the loop.
//
// A note is identified by its vault-relative path with forward slashes,
// always ending in ".md" (e.g. "journal/2026-08-23.md"). Paths are validated
// hard at this boundary: no escaping the root, no hidden segments, no
// characters that would break wikilink syntax or a filesystem the vault might
// be synced to.
package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ValidationError reports input the caller can fix (HTTP 400).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// NotFoundError reports a note that does not exist (HTTP 404).
type NotFoundError struct{ Path string }

func (e *NotFoundError) Error() string { return fmt.Sprintf("note not found: %s", e.Path) }

// ExistsError reports a create/rename target already taken (HTTP 409).
type ExistsError struct{ Path string }

func (e *ExistsError) Error() string { return fmt.Sprintf("note already exists: %s", e.Path) }

// StaleError reports a write made against a version of a note that has since
// changed — another device, another editor, a sync pulling something down. It
// is the optimistic-concurrency half of every write that takes a base mtime,
// and maps to HTTP 409 so the caller can offer the choice rather than one
// side silently winning.
type StaleError struct{ Path string }

func (e *StaleError) Error() string {
	return "note changed on disk since it was loaded: " + e.Path
}

// NoteInfo describes a note without its content.
type NoteInfo struct {
	Path    string // vault-relative, "/" separated, ends ".md"
	Name    string // file name without the ".md"
	Dir     string // parent folder ("" at the vault root)
	Size    int64
	MtimeMs int64 // modification time, unix milliseconds
}

// Vault is a note store rooted at one directory.
type Vault struct {
	Root string // absolute
}

// Open resolves root to an absolute path and creates it if missing.
func Open(root string) (*Vault, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return nil, err
	}
	return &Vault{Root: abs}, nil
}

// Characters a note path may never contain: wikilink syntax (# | [ ]), and
// what Windows/most sync targets refuse (\ : * ? " < >). Newlines and other
// control characters are rejected separately.
const forbiddenChars = `#|[]\:*?"<>`

// maxSegment and maxPath keep names practical across filesystems.
const (
	maxSegment = 200
	maxPath    = 512
)

// CleanPath validates and normalizes a vault-relative note path. It returns
// the canonical form ("/"-separated, ".md" suffix) or a *ValidationError.
func CleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", &ValidationError{"note path is empty"}
	}
	if len(p) > maxPath {
		return "", &ValidationError{fmt.Sprintf("note path longer than %d characters", maxPath)}
	}
	if !strings.HasSuffix(strings.ToLower(p), ".md") {
		p += ".md"
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if seg == "" || seg == "." || seg == ".." {
			return "", &ValidationError{"note path has an empty, '.' or '..' segment"}
		}
		if strings.HasPrefix(seg, ".") {
			return "", &ValidationError{"note path segments must not start with '.'"}
		}
		if len(seg) > maxSegment {
			return "", &ValidationError{fmt.Sprintf("path segment longer than %d characters", maxSegment)}
		}
		if strings.ContainsAny(seg, forbiddenChars) {
			return "", &ValidationError{fmt.Sprintf("note path must not contain any of %s", forbiddenChars)}
		}
		for _, r := range seg {
			if r < 0x20 || r == 0x7f {
				return "", &ValidationError{"note path contains a control character"}
			}
		}
		// A name that is only ".md" (empty stem) is not a note.
		if i == len(segs)-1 && strings.EqualFold(seg, ".md") {
			return "", &ValidationError{"note name is empty"}
		}
	}
	return strings.Join(segs, "/"), nil
}

// SanitizeName turns a free-form title into a usable file stem, replacing
// forbidden characters with "-". Returns a *ValidationError when nothing
// usable remains.
func SanitizeName(name string) (string, error) {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		if strings.ContainsRune(forbiddenChars+"/", r) || r < 0x20 || r == 0x7f {
			b.WriteRune('-')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), " .-")
	if out == "" {
		return "", &ValidationError{"note name is empty"}
	}
	if len(out) > maxSegment-3 { // leave room for ".md"
		out = out[:maxSegment-3]
	}
	return out, nil
}

// abs maps a validated vault-relative path to the absolute file path.
func (v *Vault) abs(rel string) string {
	return filepath.Join(v.Root, filepath.FromSlash(rel))
}

func infoFrom(rel string, fi fs.FileInfo) *NoteInfo {
	name := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	dir := ""
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		dir = rel[:i]
	}
	return &NoteInfo{
		Path:    rel,
		Name:    name,
		Dir:     dir,
		Size:    fi.Size(),
		MtimeMs: fi.ModTime().UnixMilli(),
	}
}

// List walks the vault and returns every markdown note, sorted by path.
// Hidden files and directories (dot-prefixed — .git, .obsidian, .trash) are
// skipped, so a synced or Obsidian-managed vault lists cleanly.
func (v *Vault) List() ([]NoteInfo, error) {
	var notes []NoteInfo
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
		if d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		rel, err := filepath.Rel(v.Root, p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		notes = append(notes, *infoFrom(filepath.ToSlash(rel), fi))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Path < notes[j].Path })
	return notes, nil
}

// Stat returns a note's info, or *NotFoundError.
func (v *Vault) Stat(path string) (*NoteInfo, error) {
	rel, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(v.abs(rel))
	if err != nil || fi.IsDir() {
		return nil, &NotFoundError{rel}
	}
	return infoFrom(rel, fi), nil
}

// Read returns a note's content and info.
func (v *Vault) Read(path string) (string, *NoteInfo, error) {
	info, err := v.Stat(path)
	if err != nil {
		return "", nil, err
	}
	data, err := os.ReadFile(v.abs(info.Path))
	if err != nil {
		return "", nil, &NotFoundError{info.Path}
	}
	return string(data), info, nil
}

// Write saves content to a note, creating it (and parent folders) if needed.
func (v *Vault) Write(path, content string) (*NoteInfo, error) {
	rel, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	abs := v.abs(rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	// Write-then-rename so a crash mid-write never truncates a note.
	tmp := abs + ".tmp~"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, abs); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return v.Stat(rel)
}

// Create is Write that refuses to overwrite an existing note.
func (v *Vault) Create(path, content string) (*NoteInfo, error) {
	rel, err := CleanPath(path)
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(v.abs(rel)); err == nil {
		return nil, &ExistsError{rel}
	}
	return v.Write(rel, content)
}

// Delete removes a note. Emptied parent folders are pruned so renames and
// deletions don't strand husk directories in the vault.
func (v *Vault) Delete(path string) error {
	info, err := v.Stat(path)
	if err != nil {
		return err
	}
	if err := os.Remove(v.abs(info.Path)); err != nil {
		return err
	}
	v.pruneEmptyDirs(info.Dir)
	return nil
}

// Rename moves a note to a new path (folders included), refusing to clobber.
func (v *Vault) Rename(oldPath, newPath string) (*NoteInfo, error) {
	oldInfo, err := v.Stat(oldPath)
	if err != nil {
		return nil, err
	}
	newRel, err := CleanPath(newPath)
	if err != nil {
		return nil, err
	}
	if newRel == oldInfo.Path {
		return oldInfo, nil
	}
	// Same path, different case is a rename too (macOS FS is case-insensitive,
	// so os.Stat would report the target "taken" by the source itself).
	if !strings.EqualFold(newRel, oldInfo.Path) {
		if _, err := os.Stat(v.abs(newRel)); err == nil {
			return nil, &ExistsError{newRel}
		}
	}
	abs := v.abs(newRel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, err
	}
	if err := os.Rename(v.abs(oldInfo.Path), abs); err != nil {
		return nil, err
	}
	v.pruneEmptyDirs(oldInfo.Dir)
	return v.Stat(newRel)
}

// pruneEmptyDirs removes now-empty folders from dir up to the vault root.
func (v *Vault) pruneEmptyDirs(dir string) {
	for dir != "" {
		abs := v.abs(dir)
		entries, err := os.ReadDir(abs)
		if err != nil || len(entries) > 0 {
			return
		}
		if os.Remove(abs) != nil {
			return
		}
		if i := strings.LastIndex(dir, "/"); i >= 0 {
			dir = dir[:i]
		} else {
			dir = ""
		}
	}
}

// IsNotFound reports whether err is a NotFoundError.
func IsNotFound(err error) bool {
	var nf *NotFoundError
	return errors.As(err, &nf)
}
