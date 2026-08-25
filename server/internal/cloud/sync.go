package cloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/chinmay28/thought-mesh/server/internal/merge"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// The sync engine.
//
// One run compares three descriptions of the same tree — the vault on disk, the
// folder in Dropbox, and the state both were in when they last agreed (see
// state.go) — and derives, per path, which side moved:
//
//	local == base, remote != base   →  pull (the change happened over there)
//	local != base, remote == base   →  push (the change happened here)
//	local == remote                 →  nothing; just record that they agree
//	both differ, differently        →  conflict; touch neither side
//
// Absence is a value in that comparison, which is what makes deletions work.
// A file that is gone locally but still matches the base remotely was deleted
// here, and the deletion is pushed; a file that has never been seen before on
// either side has no base, so whichever side has it is the one that added it.
//
// Nothing is ever resolved by picking a winner behind the user's back. Where
// both sides genuinely moved, the run parks a conflict, leaves both versions
// exactly where they are, and carries on with the rest of the tree — one
// contested note never blocks the other nine hundred.

// LocalStore is the vault as cloud sync needs it: the whole folder, not just
// the notes. *vault.Vault implements it; a test can substitute its own.
type LocalStore interface {
	Files() ([]vault.FileInfo, error)
	ReadFile(rel string) ([]byte, error)
	WriteFile(rel string, data []byte) (*vault.FileInfo, error)
	RemoveFile(rel string) error
	Zip() ([]byte, error)
	RestoreZip(data []byte) (int, error)
}

// SyncResult reports what one run did.
type SyncResult struct {
	Uploaded      int `json:"uploaded"`
	Downloaded    int `json:"downloaded"`
	DeletedLocal  int `json:"deleted_local"`
	DeletedRemote int `json:"deleted_remote"`
	// Unchanged is how many files were already in step — the number that
	// should dominate on a healthy schedule.
	Unchanged int `json:"unchanged"`
	// Conflicts are the paths left for the user to settle, including any
	// carried over from an earlier run.
	Conflicts []Conflict `json:"conflicts"`
	// BackupFile is the local pre-sync backup, written only when the run was
	// about to overwrite or delete something in the vault. Empty otherwise.
	BackupFile string `json:"backup_file"`
	// Failed is how many individual files errored; the run still applied
	// everything it could, and Error names the first thing that went wrong.
	Failed int    `json:"failed"`
	Error  string `json:"error"`
}

// localFile is one vault file with its content hash resolved.
type localFile struct {
	rel     string
	size    int64
	mtimeMs int64
	hash    string
}

// planKind is what a run decided to do with one path.
type planKind int

const (
	planNothing planKind = iota
	planPush
	planPull
	planDeleteRemote
	planDeleteLocal
	planConflict
)

// planItem is one path's decision, with the three versions it was made from.
type planItem struct {
	rel    string
	kind   planKind
	local  *localFile
	remote *RemoteFile
	base   *FileState
}

// Sync brings the vault and the connected folder into step, and records the
// outcome in the settings the way every run does — success or failure, plus
// the next deadline.
func (s *Service) Sync(ctx context.Context) (*SyncResult, error) {
	set, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if !set.Connected() {
		return nil, &ConfigError{Message: "No cloud account is connected."}
	}
	if set.FolderID == nil {
		return nil, &ConfigError{Message: "Choose a folder in the connected account first."}
	}

	result, runErr := s.runSync(ctx, *set.FolderID)
	if err := s.recordRun(result, runErr); err != nil {
		return nil, err
	}
	if runErr != nil {
		return nil, runErr
	}
	return result, nil
}

// runSync is one pass: read both sides, plan, apply.
func (s *Service) runSync(ctx context.Context, folderID string) (*SyncResult, error) {
	provider, token, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	state, err := s.State.State()
	if err != nil {
		return nil, err
	}
	// A folder change invalidates every hash we hold: the same path in a
	// different folder is a different file, and treating the old state as a
	// base there would read as "everything was deleted remotely".
	if state.FolderID != folderID {
		if err := s.State.Reset(folderID, toISO(s.Now())); err != nil {
			return nil, err
		}
		if state, err = s.State.State(); err != nil {
			return nil, err
		}
	}

	remoteFiles, err := provider.ListTree(ctx, token, folderID)
	if err != nil {
		return nil, &ProviderError{Provider: provider.Name(), Err: err}
	}
	remote := map[string]*RemoteFile{}
	for i := range remoteFiles {
		rel, err := vault.CleanFilePath(remoteFiles[i].Rel)
		if err != nil {
			// Something in the folder can't live in a vault (an absolute or
			// traversing path). Skipping is the only safe answer, and it is not
			// a reason to fail the run.
			continue
		}
		remoteFiles[i].Rel = rel
		remote[rel] = &remoteFiles[i]
	}

	local, err := s.localFiles(state)
	if err != nil {
		return nil, err
	}

	plan := buildPlan(local, remote, state)
	return s.applyPlan(ctx, provider, token, folderID, plan, state)
}

// localFiles reads the vault side, hashing what changed. A file whose size and
// mtime still match the recorded state is taken at its word rather than reread:
// on a healthy schedule that is every file, and the run costs one stat-walk.
func (s *Service) localFiles(state *syncState) (map[string]*localFile, error) {
	files, err := s.Vault.Files()
	if err != nil {
		return nil, err
	}
	out := make(map[string]*localFile, len(files))
	for _, f := range files {
		rel, err := vault.CleanFilePath(f.Path)
		if err != nil {
			continue // not something that can be named remotely either
		}
		lf := &localFile{rel: rel, size: f.Size, mtimeMs: f.MtimeMs}
		if known, ok := state.Files[rel]; ok && known.Size == f.Size && known.MtimeMs == f.MtimeMs {
			lf.hash = known.Hash
		} else {
			data, err := s.Vault.ReadFile(rel)
			if err != nil {
				continue // vanished between the walk and the read
			}
			lf.hash = contentHash(data)
		}
		out[rel] = lf
	}
	return out, nil
}

// buildPlan is the three-way comparison, one entry per path either side knows
// about. It reads and writes nothing — every decision is derivable from the
// hashes, which is what makes it testable on its own.
func buildPlan(local map[string]*localFile, remote map[string]*RemoteFile, state *syncState) []planItem {
	paths := map[string]bool{}
	for rel := range local {
		paths[rel] = true
	}
	for rel := range remote {
		paths[rel] = true
	}
	for rel := range state.Files {
		paths[rel] = true
	}
	// Contested paths too. A conflict clears the path's file state, so without
	// this a note deleted on *both* sides while contested would drop out of the
	// comparison entirely and its conflict would sit there for ever, with
	// nothing left on either side to settle.
	for rel := range state.Conflicts {
		paths[rel] = true
	}
	ordered := make([]string, 0, len(paths))
	for rel := range paths {
		ordered = append(ordered, rel)
	}
	sort.Strings(ordered)

	plan := make([]planItem, 0, len(ordered))
	for _, rel := range ordered {
		item := planItem{rel: rel, local: local[rel], remote: remote[rel]}
		if base, ok := state.Files[rel]; ok {
			item.base = &base
		}
		item.kind = decide(item)
		plan = append(plan, item)
	}
	return plan
}

// decide is the table at the top of this file, written out.
func decide(item planItem) planKind {
	localHash, remoteHash := "", ""
	if item.local != nil {
		localHash = item.local.hash
	}
	if item.remote != nil {
		remoteHash = item.remote.Hash
	}
	// Identical on both sides — including "absent on both" — needs nothing
	// done, only recording.
	if localHash == remoteHash {
		return planNothing
	}

	baseHash := ""
	hasBase := item.base != nil
	if hasBase {
		baseHash = item.base.Hash
	}
	if !hasBase {
		// Neither side has seen this before. One side having it is an
		// addition; both having it, differently, is a conflict — there is no
		// base that could say which is the newer thought.
		if localHash == "" {
			return planPull
		}
		if remoteHash == "" {
			return planPush
		}
		return planConflict
	}

	localChanged := localHash != baseHash
	remoteChanged := remoteHash != baseHash
	switch {
	case localChanged && remoteChanged:
		return planConflict
	case localChanged:
		if localHash == "" {
			return planDeleteRemote
		}
		return planPush
	case remoteChanged:
		if remoteHash == "" {
			return planDeleteLocal
		}
		return planPull
	}
	// Neither side changed but the hashes differ, which can't happen — take
	// the safe reading rather than guessing.
	return planConflict
}

// applyPlan carries out the decisions, one file at a time.
//
// A failure on one path is recorded and the run continues: a note that couldn't
// upload is no reason to leave the other nine hundred out of step, and the
// failed one is simply still out of step at the next run.
func (s *Service) applyPlan(ctx context.Context, provider Provider, token, folderID string,
	plan []planItem, state *syncState) (*SyncResult, error) {

	result := &SyncResult{Conflicts: []Conflict{}}
	nowISO := toISO(s.Now())
	updates := map[string]FileState{}
	removals := map[string]bool{}
	conflicts := map[string]Conflict{}
	var firstErr error

	// Anything that would overwrite or delete a file in the vault gets a local
	// safety copy first. Cloud sync is the one thing here that changes notes
	// the user didn't just edit, so it keeps the undo path the old snapshot
	// restore had: a zip of the vault as it was, beside the settings file.
	if s.planTouchesLocalContent(plan) {
		backup, err := s.writePreSyncBackup()
		if err != nil {
			return nil, fmt.Errorf("pre-sync backup: %w", err)
		}
		result.BackupFile = backup
	}

	for _, item := range plan {
		// A path already contested stays untouched until the user settles it —
		// unless the two sides have since converged on their own, in which case
		// the argument is over.
		if existing, contested := state.Conflicts[item.rel]; contested {
			if item.kind == planNothing {
				removals[item.rel] = false
			} else {
				conflicts[item.rel] = existing
				result.Conflicts = append(result.Conflicts, existing)
				continue
			}
		}

		switch item.kind {
		case planNothing:
			if item.local == nil && item.remote == nil {
				removals[item.rel] = true // gone from both sides; forget it
				continue
			}
			result.Unchanged++
			updates[item.rel] = fileStateFrom(item)

		case planPush:
			data, err := s.Vault.ReadFile(item.rel)
			if err != nil {
				result.Failed, firstErr = result.Failed+1, orFirst(firstErr, err)
				continue
			}
			rev := ""
			if item.remote != nil {
				rev = item.remote.Rev
			}
			uploaded, err := provider.UploadFile(ctx, token, folderID, item.rel, data, rev)
			if errors.Is(err, ErrRevisionConflict) {
				// Somebody wrote to Dropbox between the listing and now. That
				// is exactly a conflict, not a failure.
				c := conflictFrom(item, nowISO, s.State)
				conflicts[item.rel], result.Conflicts = c, append(result.Conflicts, c)
				continue
			}
			if err != nil {
				result.Failed, firstErr = result.Failed+1, orFirst(firstErr,
					&ProviderError{Provider: provider.Name(), Err: err})
				continue
			}
			s.State.PutBase(uploaded.Hash, data)
			// The mtime recorded is the walk's, not a fresh stat: if the note
			// was edited between the walk and the read, a fresh stat would
			// describe content we did not upload, and the next run's
			// "unchanged since (mtime, size)" shortcut would skip the edit
			// entirely. A stale mtime only ever costs one redundant rehash.
			updates[item.rel] = FileState{
				Hash: uploaded.Hash, Rev: uploaded.Rev,
				Size: item.local.size, MtimeMs: item.local.mtimeMs,
			}
			result.Uploaded++

		case planPull:
			data, err := provider.DownloadFile(ctx, token, folderID, item.rel)
			if err != nil {
				result.Failed, firstErr = result.Failed+1, orFirst(firstErr,
					&ProviderError{Provider: provider.Name(), Err: err})
				continue
			}
			// Trust the bytes, not the listing: hashing what actually arrived
			// keeps a truncated download from being recorded as agreed.
			hash := contentHash(data)
			info, err := s.Vault.WriteFile(item.rel, data)
			if err != nil {
				result.Failed, firstErr = result.Failed+1, orFirst(firstErr, err)
				continue
			}
			s.State.PutBase(hash, data)
			updates[item.rel] = FileState{
				Hash: hash, Rev: item.remote.Rev,
				Size: info.Size, MtimeMs: info.MtimeMs,
			}
			result.Downloaded++

		case planDeleteRemote:
			if err := provider.DeleteFile(ctx, token, folderID, item.rel); err != nil {
				result.Failed, firstErr = result.Failed+1, orFirst(firstErr,
					&ProviderError{Provider: provider.Name(), Err: err})
				continue
			}
			removals[item.rel] = true
			result.DeletedRemote++

		case planDeleteLocal:
			if err := s.Vault.RemoveFile(item.rel); err != nil {
				result.Failed, firstErr = result.Failed+1, orFirst(firstErr, err)
				continue
			}
			removals[item.rel] = true
			result.DeletedLocal++

		case planConflict:
			c := conflictFrom(item, nowISO, s.State)
			conflicts[item.rel], result.Conflicts = c, append(result.Conflicts, c)
		}
	}

	if _, err := s.State.Update(nowISO, func(st *syncState) {
		st.FolderID = folderID
		for rel, fs := range updates {
			st.Files[rel] = fs
		}
		for rel, drop := range removals {
			if drop {
				delete(st.Files, rel)
			}
			delete(st.Conflicts, rel)
		}
		for rel, c := range conflicts {
			st.Conflicts[rel] = c
			// A contested path has no agreed version, so it has no base state
			// either: whatever we last knew about it is now history.
			delete(st.Files, rel)
		}
		s.pruneBaseCache(st)
	}); err != nil {
		return nil, err
	}

	sort.Slice(result.Conflicts, func(i, j int) bool {
		return result.Conflicts[i].Path < result.Conflicts[j].Path
	})
	if firstErr != nil {
		result.Error = firstErr.Error()
	}
	return result, firstErr
}

// planTouchesLocalContent reports whether the run is about to change something
// in the vault that the user didn't ask for directly.
func (s *Service) planTouchesLocalContent(plan []planItem) bool {
	for _, item := range plan {
		switch item.kind {
		case planDeleteLocal:
			return true
		case planPull:
			if item.local != nil {
				return true // an existing note is about to be replaced
			}
		}
	}
	return false
}

// fileStateFrom records two sides that already agree.
func fileStateFrom(item planItem) FileState {
	fs := FileState{}
	if item.local != nil {
		fs.Hash, fs.Size, fs.MtimeMs = item.local.hash, item.local.size, item.local.mtimeMs
	}
	if item.remote != nil {
		fs.Rev = item.remote.Rev
		if fs.Hash == "" {
			fs.Hash, fs.Size = item.remote.Hash, item.remote.Size
		}
	}
	return fs
}

// conflictFrom describes a contested path for the UI: which versions are in
// play, whether one side deleted it, and whether a merge can be offered at all.
func conflictFrom(item planItem, nowISO string, state *StateStore) Conflict {
	c := Conflict{Path: item.rel, DetectedAt: nowISO}
	if item.local != nil {
		c.LocalHash, c.LocalSize = item.local.hash, item.local.size
	} else {
		c.LocalMissing = 1
	}
	if item.remote != nil {
		c.RemoteHash, c.RemoteRev, c.RemoteSize = item.remote.Hash, item.remote.Rev, item.remote.Size
	} else {
		c.RemoteMissing = 1
	}
	// A deletion on one side can only be kept or undone; there is no third
	// version to weave together. Merging is offered only when both sides hold
	// text small enough to line up.
	if c.LocalMissing == 0 && c.RemoteMissing == 0 &&
		c.LocalSize <= maxBaseCacheBytes && c.RemoteSize <= maxBaseCacheBytes {
		c.Mergeable = 1
	}
	if item.base != nil {
		c.BaseHash = item.base.Hash
		if _, ok := state.Base(item.base.Hash); ok {
			c.HasBase = 1
		}
	}
	return c
}

// pruneBaseCache drops cached base versions nothing refers to any more.
//
// "Nothing" includes the open conflicts, not just the agreed files: recording a
// conflict clears the path's file state, and pruning on that alone would throw
// away the very version the merge needs, moments after promising it.
func (s *Service) pruneBaseCache(st *syncState) {
	keep := make(map[string]bool, len(st.Files)+len(st.Conflicts))
	for _, fs := range st.Files {
		keep[fs.Hash] = true
	}
	for _, c := range st.Conflicts {
		if c.BaseHash != "" {
			keep[c.BaseHash] = true
		}
	}
	s.State.PruneBase(keep)
}

func orFirst(first, next error) error {
	if first != nil {
		return first
	}
	return next
}

// --- conflicts ----------------------------------------------------------------

// Conflicts lists the paths currently waiting on a decision, oldest first —
// the order they happened in, which is the order they read best in.
func (s *Service) Conflicts() ([]Conflict, error) {
	state, err := s.State.State()
	if err != nil {
		return nil, err
	}
	out := make([]Conflict, 0, len(state.Conflicts))
	for _, c := range state.Conflicts {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DetectedAt != out[j].DetectedAt {
			return out[i].DetectedAt < out[j].DetectedAt
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

// ConflictDetail is everything the resolution screen needs in one request: the
// two versions in full, the one they diverged from if it is still cached, and a
// merge already computed so the common case is "read this and save it".
type ConflictDetail struct {
	Conflict
	// Local/Remote/Base are the texts, empty when that version is absent. Text
	// is only ever filled in for a mergeable conflict — showing a megabyte of
	// binary in a textarea helps nobody.
	Local  string `json:"local"`
	Remote string `json:"remote"`
	Base   string `json:"base"`
	// Merged is the proposed resolution, conflict markers and all.
	Merged string `json:"merged"`
	// MergeConflicts is how many regions the merge couldn't settle by itself.
	MergeConflicts int `json:"merge_conflicts"`
}

// ConflictDetail fetches both versions of a contested path and merges them.
//
// The remote side is downloaded here rather than at detection time on purpose:
// a run that hits twenty conflicts shouldn't pull twenty files nobody has asked
// to look at yet.
func (s *Service) ConflictDetail(ctx context.Context, rel string) (*ConflictDetail, error) {
	state, err := s.State.State()
	if err != nil {
		return nil, err
	}
	c, ok := state.Conflicts[rel]
	if !ok {
		return nil, &vault.NotFoundError{Path: rel}
	}
	detail := &ConflictDetail{Conflict: c}
	if c.Mergeable != 1 {
		return detail, nil
	}

	localText := ""
	if c.LocalMissing == 0 {
		data, err := s.Vault.ReadFile(rel)
		if err != nil {
			return nil, err
		}
		localText = string(data)
	}
	remoteText := ""
	if c.RemoteMissing == 0 {
		data, err := s.downloadConflictSide(ctx, rel)
		if err != nil {
			return nil, err
		}
		remoteText = string(data)
	}
	// A side that turns out not to be text can't be merged after all — say so
	// rather than putting a megabyte of binary in a textarea.
	if !isText([]byte(localText)) || !isText([]byte(remoteText)) {
		detail.Mergeable = 0
		return detail, nil
	}

	baseText, hasBase := "", false
	if base, ok := s.State.Base(baseHashFor(state, rel)); ok && isText(base) {
		baseText, hasBase = string(base), true
	}

	merged := merge.Merge(baseText, localText, remoteText, hasBase)
	detail.Local, detail.Remote, detail.Base = localText, remoteText, baseText
	detail.Merged, detail.MergeConflicts = merged.Text, merged.Conflicts
	// Report what the merge was actually taken against, not what was available
	// when the conflict was recorded: the cached base can have been evicted, or
	// turn out not to be text, since then.
	detail.HasBase = 0
	if hasBase {
		detail.HasBase = 1
	}
	return detail, nil
}

// baseHashFor is the hash of the version the two sides last agreed on. A
// conflict clears the file's state, so the hash is remembered on the conflict
// record itself — but only the pre-conflict state knew it, so fall back to
// whatever is still recorded.
func baseHashFor(state *syncState, rel string) string {
	if fs, ok := state.Files[rel]; ok {
		return fs.Hash
	}
	return state.Conflicts[rel].BaseHash
}

func (s *Service) downloadConflictSide(ctx context.Context, rel string) ([]byte, error) {
	set, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if !set.Connected() || set.FolderID == nil {
		return nil, &ConfigError{Message: "No cloud folder is connected."}
	}
	provider, token, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	data, err := provider.DownloadFile(ctx, token, *set.FolderID, rel)
	if err != nil {
		return nil, &ProviderError{Provider: provider.Name(), Err: err}
	}
	return data, nil
}

// ResolveConflict applies the user's decision and clears the conflict.
//
// Every resolution ends with both sides holding the same bytes and that
// agreement recorded, so the next run sees nothing to do — including the merge
// case, where the merged text is written to the vault *and* uploaded. A
// resolution that only fixed one side would come straight back as a conflict.
func (s *Service) ResolveConflict(ctx context.Context, rel, resolution, mergedContent string) (*Conflict, error) {
	state, err := s.State.State()
	if err != nil {
		return nil, err
	}
	c, ok := state.Conflicts[rel]
	if !ok {
		return nil, &vault.NotFoundError{Path: rel}
	}
	set, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if !set.Connected() || set.FolderID == nil {
		return nil, &ConfigError{Message: "No cloud folder is connected."}
	}
	provider, token, err := s.authorize(ctx)
	if err != nil {
		return nil, err
	}
	folderID := *set.FolderID

	var applied *FileState
	switch resolution {
	case ResolveKeepLocal:
		applied, err = s.resolveKeepLocal(ctx, provider, token, folderID, rel, c)
	case ResolveKeepRemote:
		applied, err = s.resolveKeepRemote(ctx, provider, token, folderID, rel, c)
	case ResolveMerge:
		if c.Mergeable != 1 {
			return nil, &ConfigError{
				Message: "That conflict can't be merged — one side is missing or isn't text. Keep one version instead."}
		}
		applied, err = s.resolveMerge(ctx, provider, token, folderID, rel, []byte(mergedContent))
	default:
		return nil, &vault.ValidationError{
			Msg: `resolution must be "` + ResolveKeepLocal + `", "` + ResolveKeepRemote +
				`" or "` + ResolveMerge + `"`}
	}
	if err != nil {
		return nil, err
	}

	if _, err := s.State.Update(toISO(s.Now()), func(st *syncState) {
		delete(st.Conflicts, rel)
		if applied == nil {
			delete(st.Files, rel)
			return
		}
		st.Files[rel] = *applied
	}); err != nil {
		return nil, err
	}
	return &c, nil
}

// resolveKeepLocal pushes this server's version over the remote one — or
// re-applies the deletion, when this side is where the file was removed.
func (s *Service) resolveKeepLocal(ctx context.Context, provider Provider, token, folderID, rel string,
	c Conflict) (*FileState, error) {

	if c.LocalMissing == 1 {
		if err := provider.DeleteFile(ctx, token, folderID, rel); err != nil {
			return nil, &ProviderError{Provider: provider.Name(), Err: err}
		}
		return nil, nil
	}
	data, err := s.Vault.ReadFile(rel)
	if err != nil {
		return nil, err
	}
	return s.pushResolved(ctx, provider, token, folderID, rel, data)
}

// resolveKeepRemote replaces the local version with the cloud's — or deletes
// the local file, when the cloud is where it was removed.
func (s *Service) resolveKeepRemote(ctx context.Context, provider Provider, token, folderID, rel string,
	c Conflict) (*FileState, error) {

	if c.RemoteMissing == 1 {
		if err := s.Vault.RemoveFile(rel); err != nil {
			return nil, err
		}
		return nil, nil
	}
	data, err := provider.DownloadFile(ctx, token, folderID, rel)
	if err != nil {
		return nil, &ProviderError{Provider: provider.Name(), Err: err}
	}
	hash := contentHash(data)
	info, err := s.Vault.WriteFile(rel, data)
	if err != nil {
		return nil, err
	}
	s.State.PutBase(hash, data)
	return &FileState{Hash: hash, Rev: c.RemoteRev, Size: info.Size, MtimeMs: info.MtimeMs}, nil
}

// resolveMerge writes the merged text to both sides.
func (s *Service) resolveMerge(ctx context.Context, provider Provider, token, folderID, rel string,
	data []byte) (*FileState, error) {

	if _, err := s.Vault.WriteFile(rel, data); err != nil {
		return nil, err
	}
	return s.pushResolved(ctx, provider, token, folderID, rel, data)
}

// pushResolved uploads a decided version unconditionally — the user has just
// said this one wins, so a stale rev is not a reason to ask them again — and
// records the agreement it creates.
func (s *Service) pushResolved(ctx context.Context, provider Provider, token, folderID, rel string,
	data []byte) (*FileState, error) {

	uploaded, err := provider.UploadFile(ctx, token, folderID, rel, data, "")
	if err != nil {
		return nil, &ProviderError{Provider: provider.Name(), Err: err}
	}
	info, err := s.Vault.WriteFile(rel, data)
	if err != nil {
		return nil, err
	}
	s.State.PutBase(uploaded.Hash, data)
	return &FileState{
		Hash: uploaded.Hash, Rev: uploaded.Rev,
		Size: info.Size, MtimeMs: info.MtimeMs,
	}, nil
}

// --- local pre-sync backups ---------------------------------------------------

// backupSuffix marks the local safety copies this package writes.
const backupSuffix = ".vault.zip"

// keepBackups is how many pre-sync backups are retained. Enough to walk back
// through a bad afternoon, few enough that a vault isn't stored a hundred times
// over on the server's disk.
const keepBackups = 5

// writePreSyncBackup zips the vault as it stands into the settings file's
// directory — outside the vault, so it can never be swept into a later sync.
func (s *Service) writePreSyncBackup() (string, error) {
	data, err := s.Vault.Zip()
	if err != nil {
		return "", err
	}
	stamp := s.Now().Format("2006-01-02-150405")
	name := "thoughtmesh-pre-sync-" + stamp + backupSuffix
	path := filepath.Join(s.backupDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	s.pruneBackups()
	return name, nil
}

func (s *Service) backupDir() string { return filepath.Dir(s.Store.Path) }

// Backup is one local pre-sync copy of the vault — the undo path for a sync
// that pulled down something unwelcome.
type Backup struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	ModifiedMs int64  `json:"modified_ms"`
}

// Backups lists the local pre-sync backups, newest first.
func (s *Service) Backups() ([]Backup, error) {
	entries, err := os.ReadDir(s.backupDir())
	if err != nil {
		if os.IsNotExist(err) {
			return []Backup{}, nil
		}
		return nil, err
	}
	out := []Backup{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), backupSuffix) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, Backup{
			Name: e.Name(), Size: info.Size(), ModifiedMs: info.ModTime().UnixMilli(),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ModifiedMs != out[j].ModifiedMs {
			return out[i].ModifiedMs > out[j].ModifiedMs
		}
		return out[i].Name > out[j].Name
	})
	return out, nil
}

func (s *Service) pruneBackups() {
	backups, err := s.Backups()
	if err != nil {
		return
	}
	for i := keepBackups; i < len(backups); i++ {
		os.Remove(filepath.Join(s.backupDir(), backups[i].Name))
	}
}

// RestoreBackup puts a local pre-sync backup back, replacing the vault's
// contents with it.
//
// It is the undo button for a sync, and it deliberately clears the sync state:
// the vault now holds something the cloud has never seen, and the next run
// should work that out from scratch rather than believing a base that describes
// the version just discarded.
func (s *Service) RestoreBackup(name string) (int, error) {
	if strings.ContainsAny(name, `/\`) || !strings.HasSuffix(name, backupSuffix) {
		return 0, &vault.ValidationError{Msg: "that is not a pre-sync backup"}
	}
	data, err := os.ReadFile(filepath.Join(s.backupDir(), name))
	if err != nil {
		return 0, &vault.NotFoundError{Path: name}
	}
	// Take a backup of what is about to be replaced, so restoring the wrong
	// one is itself undoable.
	if _, err := s.writePreSyncBackup(); err != nil {
		return 0, fmt.Errorf("pre-restore backup: %w", err)
	}
	files, err := s.Vault.RestoreZip(data)
	if err != nil {
		return 0, err
	}
	set, err := s.Settings()
	if err != nil {
		return files, err
	}
	folderID := ""
	if set.FolderID != nil {
		folderID = *set.FolderID
	}
	if err := s.State.Reset(folderID, toISO(s.Now())); err != nil {
		return files, err
	}
	return files, nil
}

// --- helpers ------------------------------------------------------------------

// isText reports whether data can be shown, diffed and merged as text. A NUL
// byte or invalid UTF-8 means it can't, and the UI offers only keep-or-keep.
func isText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	return !strings.ContainsRune(string(data), 0)
}
