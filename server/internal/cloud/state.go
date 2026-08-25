package cloud

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Sync state: what the two sides looked like the last time they agreed.
//
// A two-way sync cannot work from the two current versions alone. "This file
// differs from the one in Dropbox" says nothing about who should win — it is
// the same picture whether the note was edited here, edited there, or edited in
// both places. What tells them apart is a third version: the one both sides
// held at the end of the previous sync. Against that base, a side that still
// matches it did not change, and a side that does not, did.
//
// So each successful transfer records the content hash both sides then carried.
// That record is what makes a deletion propagate instead of being undone by the
// next pull, and what makes a genuine two-sided edit surface as a conflict
// rather than silently overwriting somebody.
//
// It lives OUTSIDE the vault, beside the settings file, for the same reason the
// tokens do: the vault is exactly what users copy, sync and version by other
// means, and bookkeeping that rode along in it would be restored onto machines
// it does not describe. Losing this file is survivable — the next sync simply
// has no base, so anything that differs on the two sides comes back as a
// conflict for the user to settle.

// FileState is one file as of the last successful sync of it.
type FileState struct {
	// Hash is the content hash both sides carried (Dropbox's own algorithm —
	// see contentHash — so the remote value never has to be recomputed).
	Hash string `json:"hash"`
	// Rev is Dropbox's revision handle, used to make an upload conditional:
	// "replace what I last saw, and fail if that is not what is there".
	Rev string `json:"rev"`
	// Size is the agreed content's size — the same on both sides, since after
	// a sync both sides hold the same bytes.
	Size int64 `json:"size"`
	// MtimeMs is the local file's modification time when this was recorded.
	// It is not part of the comparison, only a shortcut for it: a file whose
	// size and mtime still match is taken to be unchanged rather than reread
	// and rehashed, the same (mtime, size) trick the link index uses.
	MtimeMs int64 `json:"mtime_ms"`
}

// Conflict is one path both sides changed since they last agreed, parked until
// the user says which version wins. A conflicted path is skipped entirely by
// subsequent syncs — neither side is touched — so a conflict can sit unresolved
// without either version being lost.
type Conflict struct {
	Path string `json:"path"`
	// LocalHash/RemoteHash are the two versions in play; either is "" when
	// that side no longer has the file.
	LocalHash  string `json:"local_hash"`
	RemoteHash string `json:"remote_hash"`
	RemoteRev  string `json:"remote_rev"`
	// BaseHash is the version the two sides last agreed on. Detecting a
	// conflict clears the path's file state, so the hash is carried here —
	// it is what a three-way merge is taken against.
	BaseHash   string `json:"base_hash"`
	LocalSize  int64  `json:"local_size"`
	RemoteSize int64  `json:"remote_size"`
	// LocalMissing/RemoteMissing are 1 for the delete-versus-edit case: one
	// side removed the file while the other was still writing to it. There is
	// nothing to merge there, only a keep-or-drop decision.
	LocalMissing  int `json:"local_missing"`
	RemoteMissing int `json:"remote_missing"`
	// Mergeable is 1 when both versions are text of a workable size, so a
	// three-way (or, with no base, a two-way) merge can actually be offered.
	Mergeable int `json:"mergeable"`
	// HasBase is 1 when the version the two sides last agreed on is still
	// cached, which is what makes the merge a real three-way one.
	HasBase    int    `json:"has_base"`
	DetectedAt string `json:"detected_at"`
}

// Resolutions a user can pick for a conflict. They are the wire values.
const (
	// ResolveKeepLocal — this server's version wins; it is pushed over the
	// remote one (or the remote file is deleted, if this side deleted it).
	ResolveKeepLocal = "keep_local"
	// ResolveKeepRemote — the cloud's version wins; it replaces the local one
	// (or the local file is deleted, if the cloud side deleted it).
	ResolveKeepRemote = "keep_remote"
	// ResolveMerge — one text combining both, written to BOTH sides. The
	// client sends the merged content it let the user edit.
	ResolveMerge = "merge"
)

// syncState is the whole bookkeeping file.
type syncState struct {
	// FolderID is the destination the rest of this file describes. Pointing
	// sync at a different folder invalidates all of it: the same path in a
	// different folder is a different file, and treating the old hashes as a
	// base there would push a stale "deletion" of everything.
	FolderID  string               `json:"folder_id"`
	Files     map[string]FileState `json:"files"`
	Conflicts map[string]Conflict  `json:"conflicts"`
	UpdatedAt string               `json:"updated_at"`
}

func (s *syncState) ensure() {
	if s.Files == nil {
		s.Files = map[string]FileState{}
	}
	if s.Conflicts == nil {
		s.Conflicts = map[string]Conflict{}
	}
}

// StateStore persists the sync state in its own JSON file next to the settings
// — separate on purpose: it is rewritten on every sync and can hold a line per
// file, while the settings file is small, hand-editable, and holds the tokens.
type StateStore struct {
	// Path is the state file.
	Path string
	// BaseDir holds the last-agreed content of synced files, keyed by hash, so
	// a conflict can be merged three-way instead of two-way. It is a cache:
	// entries can be evicted or lost and sync still works.
	BaseDir string

	mu sync.Mutex
}

// NewStateStore derives the state file and the base cache from the settings
// file's path, keeping all three together and all three outside the vault.
func NewStateStore(settingsPath string) *StateStore {
	dir := filepath.Dir(settingsPath)
	stem := strings.TrimSuffix(filepath.Base(settingsPath), filepath.Ext(settingsPath))
	return &StateStore{
		Path:    filepath.Join(dir, stem+"-sync-state.json"),
		BaseDir: filepath.Join(dir, stem+"-sync-base"),
	}
}

func (st *StateStore) load() (*syncState, error) {
	data, err := os.ReadFile(st.Path)
	if err != nil {
		if os.IsNotExist(err) {
			s := &syncState{}
			s.ensure()
			return s, nil
		}
		return nil, err
	}
	var s syncState
	if err := json.Unmarshal(data, &s); err != nil {
		// A corrupt state file is not worth failing a sync over: with no base,
		// every difference becomes a conflict the user can settle, which is
		// the same safe position as a first-ever sync.
		s = syncState{}
	}
	s.ensure()
	return &s, nil
}

func (st *StateStore) save(s *syncState, nowISO string) error {
	if err := os.MkdirAll(filepath.Dir(st.Path), 0o755); err != nil {
		return err
	}
	s.UpdatedAt = nowISO
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := st.Path + ".tmp~"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.Path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// State reads the current bookkeeping.
func (st *StateStore) State() (*syncState, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.load()
}

// Update applies fn to the state under the store's lock — the read-modify-write
// primitive every sync step goes through, so a scheduled run and a manual one
// can't interleave halfway.
func (st *StateStore) Update(nowISO string, fn func(*syncState)) (*syncState, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	s, err := st.load()
	if err != nil {
		return nil, err
	}
	fn(s)
	s.ensure()
	if err := st.save(s, nowISO); err != nil {
		return nil, err
	}
	return s, nil
}

// Reset forgets everything known about a folder — used when the destination
// changes, and when an account is disconnected.
func (st *StateStore) Reset(folderID, nowISO string) error {
	st.mu.Lock()
	defer st.mu.Unlock()
	s := &syncState{FolderID: folderID}
	s.ensure()
	if err := st.save(s, nowISO); err != nil {
		return err
	}
	return os.RemoveAll(st.BaseDir)
}

// --- the base-version cache ---------------------------------------------------

// maxBaseCacheBytes bounds what is worth keeping a base copy of. A merge is a
// line-level operation on text; a 20 MB attachment can only ever be resolved by
// picking a side, so caching it would buy nothing and cost the disk twice.
const maxBaseCacheBytes = 1 << 20 // 1 MiB

// basePath is where the blob with this content hash lives. Hashes are hex, so
// the name needs no validation, and the two-character shard keeps the directory
// listable on a large vault.
func (st *StateStore) basePath(hash string) string {
	if len(hash) < 4 {
		return ""
	}
	return filepath.Join(st.BaseDir, hash[:2], hash)
}

// PutBase remembers the content both sides agreed on, so a later conflict on
// this path can be merged against it. Failures are silent: this is a cache, and
// a sync that worked should not fail because a cache write did not.
func (st *StateStore) PutBase(hash string, data []byte) {
	if len(data) > maxBaseCacheBytes || hash == "" {
		return
	}
	p := st.basePath(hash)
	if p == "" {
		return
	}
	if _, err := os.Stat(p); err == nil {
		return // content-addressed: identical bytes are already there
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	tmp := p + ".tmp~"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	if os.Rename(tmp, p) != nil {
		os.Remove(tmp)
	}
}

// Base reads back a cached version by its hash.
func (st *StateStore) Base(hash string) ([]byte, bool) {
	p := st.basePath(hash)
	if p == "" {
		return nil, false
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	return data, true
}

// PruneBase drops cached versions nothing refers to any more. Called after a
// sync settles, so the cache tracks the current base set rather than growing
// with every edit ever made.
func (st *StateStore) PruneBase(keep map[string]bool) {
	shards, err := os.ReadDir(st.BaseDir)
	if err != nil {
		return
	}
	for _, shard := range shards {
		if !shard.IsDir() {
			continue
		}
		dir := filepath.Join(st.BaseDir, shard.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		remaining := 0
		for _, e := range entries {
			if keep[e.Name()] {
				remaining++
				continue
			}
			os.Remove(filepath.Join(dir, e.Name()))
		}
		if remaining == 0 {
			os.Remove(dir)
		}
	}
}

// --- content hashing ----------------------------------------------------------

// dropboxBlockSize is fixed by Dropbox's content-hash definition.
const dropboxBlockSize = 4 * 1024 * 1024

// contentHash computes Dropbox's content hash: SHA-256 of each 4 MiB block,
// concatenated, then SHA-256 of that, in lowercase hex.
//
// Using the provider's own algorithm rather than a plain SHA-256 is what lets a
// listing answer "did this file change?" without downloading anything —
// Dropbox reports the hash for every entry, and it can be compared directly
// with one computed here.
func contentHash(data []byte) string {
	digest := sha256.New()
	for start := 0; start < len(data); start += dropboxBlockSize {
		end := start + dropboxBlockSize
		if end > len(data) {
			end = len(data)
		}
		block := sha256.Sum256(data[start:end])
		digest.Write(block[:])
	}
	// An empty file has no blocks at all — the outer hash is then taken over an
	// empty concatenation, which is what this loop already produces.
	return hex.EncodeToString(digest.Sum(nil))
}
