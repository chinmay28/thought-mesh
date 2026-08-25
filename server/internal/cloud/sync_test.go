package cloud

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sync runs one pass and fails the test if it errors — the shape almost every
// case here starts from.
func runSyncNow(t *testing.T, svc *Service) *SyncResult {
	t.Helper()
	result, err := svc.Sync(context.Background())
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return result
}

// A first sync is a merge of two trees that have never met: everything local
// goes up, everything remote comes down, and nothing is lost either way.
func TestSyncFirstRunMirrorsBothWays(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))

	writeNote(t, v, "Idea.md", "a local thought\n")
	writeNote(t, v, "journal/2026-08-23.md", "today\n")
	f.put("Remote.md", []byte("written on the phone\n"))

	result := runSyncNow(t, svc)
	if result.Uploaded != 2 || result.Downloaded != 1 || len(result.Conflicts) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if f.remote("journal/2026-08-23.md") == "" {
		t.Errorf("nested note not uploaded: %v", remoteKeys(f))
	}
	if got := readNote(t, v, "Remote.md"); got != "written on the phone\n" {
		t.Errorf("pulled note = %q", got)
	}

	// A second run has nothing to do — the point of recording the agreement.
	again := runSyncNow(t, svc)
	if again.Uploaded != 0 || again.Downloaded != 0 || again.Unchanged != 3 {
		t.Errorf("second run = %+v", again)
	}
}

// Only one side changed, so there is nothing to ask: the change propagates.
func TestSyncPropagatesOneSidedEdits(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Idea.md", "first\n")
	runSyncNow(t, svc)

	writeNote(t, v, "Idea.md", "edited here\n")
	result := runSyncNow(t, svc)
	if result.Uploaded != 1 || len(result.Conflicts) != 0 {
		t.Fatalf("local edit = %+v", result)
	}
	if f.remote("Idea.md") != "edited here\n" {
		t.Errorf("remote = %q", f.remote("Idea.md"))
	}

	f.put("Idea.md", []byte("edited over there\n"))
	result = runSyncNow(t, svc)
	if result.Downloaded != 1 || len(result.Conflicts) != 0 {
		t.Fatalf("remote edit = %+v", result)
	}
	if got := readNote(t, v, "Idea.md"); got != "edited over there\n" {
		t.Errorf("local = %q", got)
	}
}

// Deletions are the case a one-way upload can never get right: without a
// record of what both sides last agreed on, a deleted note is indistinguishable
// from one that has not arrived yet, and would simply be restored.
func TestSyncPropagatesDeletionsBothWays(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Gone.md", "bye\n")
	writeNote(t, v, "Stays.md", "here\n")
	runSyncNow(t, svc)

	if err := v.RemoveFile("Gone.md"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	result := runSyncNow(t, svc)
	if result.DeletedRemote != 1 {
		t.Fatalf("local delete = %+v", result)
	}
	if f.remote("Gone.md") != "" {
		t.Error("remote copy survived a local deletion")
	}

	f.drop("Stays.md")
	result = runSyncNow(t, svc)
	if result.DeletedLocal != 1 {
		t.Fatalf("remote delete = %+v", result)
	}
	if _, err := v.ReadFile("Stays.md"); err == nil {
		t.Error("local copy survived a remote deletion")
	}
}

// The heart of it: both sides moved, so neither is touched and the user is
// asked. A conflict must also survive the next run untouched — it is not a
// transient state that resolves itself by picking a winner later.
func TestSyncBothSidesEditedIsAConflictAndStaysOne(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Idea.md", "shared start\n")
	runSyncNow(t, svc)

	writeNote(t, v, "Idea.md", "my version\n")
	f.put("Idea.md", []byte("their version\n"))

	result := runSyncNow(t, svc)
	if len(result.Conflicts) != 1 || result.Conflicts[0].Path != "Idea.md" {
		t.Fatalf("result = %+v", result)
	}
	if result.Conflicts[0].Mergeable != 1 || result.Conflicts[0].HasBase != 1 {
		t.Errorf("conflict = %+v", result.Conflicts[0])
	}
	if readNote(t, v, "Idea.md") != "my version\n" {
		t.Error("local version was overwritten by a conflicting sync")
	}
	if f.remote("Idea.md") != "their version\n" {
		t.Error("remote version was overwritten by a conflicting sync")
	}

	again := runSyncNow(t, svc)
	if len(again.Conflicts) != 1 {
		t.Fatalf("conflict did not persist: %+v", again)
	}
	if again.Uploaded != 0 || again.Downloaded != 0 {
		t.Errorf("a contested path must not be transferred: %+v", again)
	}
}

// The three ways out, one per sub-test, each ending with both sides holding the
// same bytes and nothing left to do.
func TestResolveConflict(t *testing.T) {
	setup := func(t *testing.T) (*Service, *fakeProvider, interface {
		ReadFile(string) ([]byte, error)
	}) {
		t.Helper()
		f := stdFake()
		svc, _, v := newTestServiceVault(t, f)
		connect(t, svc)
		svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
		writeNote(t, v, "Idea.md", "line one\nline two\n")
		runSyncNow(t, svc)
		writeNote(t, v, "Idea.md", "line one edited here\nline two\n")
		f.put("Idea.md", []byte("line one\nline two edited there\n"))
		if len(runSyncNow(t, svc).Conflicts) != 1 {
			t.Fatal("expected a conflict to resolve")
		}
		return svc, f, v
	}

	t.Run("keep local", func(t *testing.T) {
		svc, f, v := setup(t)
		if _, err := svc.ResolveConflict(context.Background(), "Idea.md", ResolveKeepLocal, ""); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if f.remote("Idea.md") != "line one edited here\nline two\n" {
			t.Errorf("remote = %q", f.remote("Idea.md"))
		}
		assertSettled(t, svc, v, "line one edited here\nline two\n")
	})

	t.Run("keep remote", func(t *testing.T) {
		svc, f, v := setup(t)
		if _, err := svc.ResolveConflict(context.Background(), "Idea.md", ResolveKeepRemote, ""); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if f.remote("Idea.md") != "line one\nline two edited there\n" {
			t.Errorf("remote changed: %q", f.remote("Idea.md"))
		}
		assertSettled(t, svc, v, "line one\nline two edited there\n")
	})

	t.Run("merge writes to both sides", func(t *testing.T) {
		svc, f, v := setup(t)
		// Edits on different lines: the offered merge is already clean.
		detail, err := svc.ConflictDetail(context.Background(), "Idea.md")
		if err != nil {
			t.Fatalf("detail: %v", err)
		}
		if detail.MergeConflicts != 0 {
			t.Fatalf("disjoint edits should merge cleanly: %q", detail.Merged)
		}
		want := "line one edited here\nline two edited there\n"
		if detail.Merged != want {
			t.Fatalf("merged = %q; want %q", detail.Merged, want)
		}
		if _, err := svc.ResolveConflict(context.Background(), "Idea.md", ResolveMerge, detail.Merged); err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if f.remote("Idea.md") != want {
			t.Errorf("remote = %q", f.remote("Idea.md"))
		}
		assertSettled(t, svc, v, want)
	})
}

// assertSettled checks that a resolution left both sides in step: the local
// file holds `want`, the conflict is gone, and the next run has nothing to do.
func assertSettled(t *testing.T, svc *Service, v interface {
	ReadFile(string) ([]byte, error)
}, want string) {
	t.Helper()
	data, err := v.ReadFile("Idea.md")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(data) != want {
		t.Errorf("local = %q; want %q", data, want)
	}
	conflicts, err := svc.Conflicts()
	if err != nil {
		t.Fatalf("Conflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("conflict survived resolution: %+v", conflicts)
	}
	result := runSyncNow(t, svc)
	if result.Uploaded != 0 || result.Downloaded != 0 || len(result.Conflicts) != 0 {
		t.Errorf("resolution left work behind: %+v", result)
	}
}

// A deletion on one side has no third version to weave in, so merging must not
// be offered for it.
func TestDeleteVersusEditConflictIsNotMergeable(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Idea.md", "start\n")
	runSyncNow(t, svc)

	if err := v.RemoveFile("Idea.md"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	f.put("Idea.md", []byte("edited over there\n"))

	result := runSyncNow(t, svc)
	if len(result.Conflicts) != 1 {
		t.Fatalf("result = %+v", result)
	}
	c := result.Conflicts[0]
	if c.LocalMissing != 1 || c.Mergeable != 0 {
		t.Fatalf("conflict = %+v", c)
	}
	if _, err := svc.ResolveConflict(context.Background(), "Idea.md", ResolveMerge, "x"); !IsConfigError(err) {
		t.Errorf("merging a deletion = %v; want a ConfigError explaining why not", err)
	}
	// Keeping the remote version undoes the local deletion.
	if _, err := svc.ResolveConflict(context.Background(), "Idea.md", ResolveKeepRemote, ""); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := readNote(t, v, "Idea.md"); got != "edited over there\n" {
		t.Errorf("local = %q", got)
	}
}

// The last race a two-way sync has to survive: somebody writes to Dropbox
// between our listing and our upload. The conditional write is refused, and
// that is a conflict — not a failed run.
func TestUploadRefusedByStaleRevisionBecomesAConflict(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Idea.md", "start\n")
	runSyncNow(t, svc)

	writeNote(t, v, "Idea.md", "edited here\n")
	f.staleRev = "Idea.md"

	result := runSyncNow(t, svc)
	if len(result.Conflicts) != 1 || result.Conflicts[0].Path != "Idea.md" {
		t.Fatalf("result = %+v", result)
	}
	if result.Failed != 0 {
		t.Errorf("a refused conditional write is not a failure: %+v", result)
	}
}

// One unreachable file must not stop the rest of the tree from syncing.
func TestSyncContinuesPastAFileLevelFailure(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "A.md", "one\n")
	writeNote(t, v, "B.md", "two\n")

	f.failUpload = os.ErrDeadlineExceeded // the first upload of the run
	if _, err := svc.Sync(context.Background()); err == nil {
		t.Fatal("Sync should surface the failure")
	}
	if len(f.uploads) != 1 {
		t.Errorf("the other file should still have uploaded: %v", f.uploads)
	}
	set, _ := svc.Settings()
	if set.LastResult == nil || set.LastResult.Failed != 1 || set.LastResult.Uploaded != 1 {
		t.Errorf("last result = %+v", set.LastResult)
	}
}

// A sync that overwrites or deletes something in the vault takes a local copy
// first — the undo path, and the one thing that makes a two-way sync safe to
// leave on a schedule.
func TestSyncWritesPreSyncBackupOnlyWhenItTouchesLocalFiles(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Idea.md", "start\n")

	// Pushing up changes nothing locally, so nothing needs backing up.
	if result := runSyncNow(t, svc); result.BackupFile != "" {
		t.Errorf("upload-only run wrote a backup: %s", result.BackupFile)
	}

	f.put("Idea.md", []byte("replaced from the cloud\n"))
	result := runSyncNow(t, svc)
	if result.BackupFile == "" {
		t.Fatal("a run that replaces a local note must back it up first")
	}
	backups, err := svc.Backups()
	if err != nil {
		t.Fatalf("Backups: %v", err)
	}
	if len(backups) != 1 || backups[0].Name != result.BackupFile {
		t.Fatalf("backups = %+v", backups)
	}
	// Beside the settings file, never inside the vault — a backup swept into
	// the next sync would upload the vault into itself.
	if _, err := os.Stat(filepath.Join(filepath.Dir(svc.Store.Path), result.BackupFile)); err != nil {
		t.Errorf("backup not beside the settings file: %v", err)
	}
	if !strings.HasPrefix(result.BackupFile, "thoughtmesh-pre-sync-") {
		t.Errorf("backup name = %s", result.BackupFile)
	}
}

// Pointing sync at a different folder makes every recorded hash meaningless:
// the same path there is a different file, and keeping the old state would read
// as "everything was deleted remotely".
func TestChangingFolderResetsSyncState(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Idea.md", "start\n")
	runSyncNow(t, svc)

	state, _ := svc.State.State()
	if len(state.Files) != 1 {
		t.Fatalf("state after sync = %+v", state.Files)
	}
	if _, err := svc.Update(nil, ptr("/Other"), ptr("/Other")); err != nil {
		t.Fatalf("change folder: %v", err)
	}
	state, _ = svc.State.State()
	if len(state.Files) != 0 || state.FolderID != "/Other" {
		t.Errorf("state not reset: %+v", state)
	}
	// The note is pushed to the new folder rather than deleted from the vault.
	result := runSyncNow(t, svc)
	if result.Uploaded != 1 || result.DeletedLocal != 0 {
		t.Errorf("first run in a new folder = %+v", result)
	}
}

// Restoring a pre-sync backup is the undo button, and it has to clear the sync
// bookkeeping: the vault now holds something the cloud has never seen.
func TestRestoreBackupReplacesVaultAndResetsState(t *testing.T) {
	f := stdFake()
	svc, _, v := newTestServiceVault(t, f)
	connect(t, svc)
	svc.Update(nil, ptr("/Notes"), ptr("/Notes"))
	writeNote(t, v, "Idea.md", "the version I want back\n")
	runSyncNow(t, svc)

	f.put("Idea.md", []byte("something unwelcome\n"))
	result := runSyncNow(t, svc)
	if result.BackupFile == "" {
		t.Fatal("expected a pre-sync backup")
	}
	if readNote(t, v, "Idea.md") != "something unwelcome\n" {
		t.Fatal("the unwelcome version should have landed")
	}

	files, err := svc.RestoreBackup(result.BackupFile)
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if files != 1 {
		t.Errorf("restored %d files; want 1", files)
	}
	if got := readNote(t, v, "Idea.md"); got != "the version I want back\n" {
		t.Errorf("restored note = %q", got)
	}
	state, _ := svc.State.State()
	if len(state.Files) != 0 {
		t.Errorf("state survived a restore: %+v", state.Files)
	}
}

func TestRestoreBackupRefusesAnythingElse(t *testing.T) {
	svc, _ := newTestService(t, stdFake())
	for _, name := range []string{"../escape.vault.zip", "notes.md", "sub/x.vault.zip"} {
		if _, err := svc.RestoreBackup(name); err == nil {
			t.Errorf("RestoreBackup(%q) should have been refused", name)
		}
	}
}

func TestSyncNeedsAccountAndFolder(t *testing.T) {
	svc, _ := newTestService(t, stdFake())
	if _, err := svc.Sync(context.Background()); !IsConfigError(err) {
		t.Errorf("sync unconnected = %v; want ConfigError", err)
	}
	connect(t, svc)
	if _, err := svc.Sync(context.Background()); !IsConfigError(err) {
		t.Errorf("sync without a folder = %v; want ConfigError", err)
	}
}

// The comparison table, exercised directly — every case, no I/O.
func TestPlanDecisions(t *testing.T) {
	local := func(hash string) *localFile { return &localFile{hash: hash} }
	remote := func(hash string) *RemoteFile { return &RemoteFile{Hash: hash} }
	base := func(hash string) *FileState { return &FileState{Hash: hash} }

	cases := []struct {
		name string
		item planItem
		want planKind
	}{
		{"in step", planItem{local: local("a"), remote: remote("a"), base: base("a")}, planNothing},
		{"converged independently", planItem{local: local("a"), remote: remote("a")}, planNothing},
		{"edited here", planItem{local: local("b"), remote: remote("a"), base: base("a")}, planPush},
		{"edited there", planItem{local: local("a"), remote: remote("b"), base: base("a")}, planPull},
		{"new here", planItem{local: local("a")}, planPush},
		{"new there", planItem{remote: remote("a")}, planPull},
		{"deleted here", planItem{remote: remote("a"), base: base("a")}, planDeleteRemote},
		{"deleted there", planItem{local: local("a"), base: base("a")}, planDeleteLocal},
		{"gone from both", planItem{base: base("a")}, planNothing},
		{"both edited", planItem{local: local("b"), remote: remote("c"), base: base("a")}, planConflict},
		{"deleted here, edited there", planItem{remote: remote("b"), base: base("a")}, planConflict},
		{"edited here, deleted there", planItem{local: local("b"), base: base("a")}, planConflict},
		{"added on both sides, differently", planItem{local: local("a"), remote: remote("b")}, planConflict},
	}
	for _, c := range cases {
		if got := decide(c.item); got != c.want {
			t.Errorf("%s: decide = %v; want %v", c.name, got, c.want)
		}
	}
}

func TestContentHashMatchesDropboxDefinition(t *testing.T) {
	// Dropbox's published example: the hash of an empty file is the SHA-256 of
	// an empty byte string.
	if got := contentHash(nil); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Errorf("empty hash = %s", got)
	}
	// Identical content hashes identically; different content doesn't.
	if contentHash([]byte("a note\n")) != contentHash([]byte("a note\n")) {
		t.Error("hash is not stable")
	}
	if contentHash([]byte("a")) == contentHash([]byte("b")) {
		t.Error("distinct content collided")
	}
}

func remoteKeys(f *fakeProvider) []string {
	var out []string
	for rel := range f.tree() {
		out = append(out, rel)
	}
	return out
}
