package cloud

import (
	"context"
	"strings"
	"testing"

	"github.com/chinmay28/thought-mesh/server/internal/history"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// syncingWithHistory wires a connected service whose vault is a git repository.
func syncingWithHistory(t *testing.T, f *fakeProvider) (*Service, *history.Repo, *vault.Vault) {
	t.Helper()
	svc, _, v, hist := newTestServiceHistory(t, f, true)
	connect(t, svc)
	if _, err := svc.Update(nil, ptr("/Notes"), ptr("/Notes")); err != nil {
		t.Fatalf("choose folder: %v", err)
	}
	return svc, hist, v
}

// A run's commit subject carries the time and what moved, which is what makes
// the log readable six months later.
func TestSyncCommitsCarryTheTimeAndWhatMoved(t *testing.T) {
	f := stdFake()
	svc, hist, v := syncingWithHistory(t, f)
	writeNote(t, v, "Idea.md", "a thought\n")

	if _, err := svc.Sync(context.Background(), ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	commits, err := hist.Log(20)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	// Newest first: the sync's own commit, then the capture of what was in the
	// vault before the run touched anything.
	sync := findKind(t, commits, history.KindSync)
	if !strings.HasPrefix(sync.Subject, "Sync 2026-08-23 12:00 — ") {
		t.Errorf("sync subject = %q; want the time in it", sync.Subject)
	}
	if !strings.Contains(sync.Subject, "1 up") {
		t.Errorf("sync subject = %q; want what moved in it", sync.Subject)
	}
	if pre := findKind(t, commits, history.KindLocal); !strings.Contains(pre.Subject, "before sync") {
		t.Errorf("pre-sync subject = %q", pre.Subject)
	}
}

// A manual sync can carry a note. It is the only thing that will tell one of
// these apart from the next, so it must survive even a run that moved nothing.
func TestManualSyncMessageBecomesTheCommitBody(t *testing.T) {
	f := stdFake()
	svc, hist, v := syncingWithHistory(t, f)
	writeNote(t, v, "Idea.md", "a thought\n")
	if _, err := svc.Sync(context.Background(), "before the trip"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	commits, _ := hist.Log(20)
	if got := findKind(t, commits, history.KindSync); got.Body != "before the trip" {
		t.Errorf("body = %q", got.Body)
	}

	// A second run moves nothing — but the annotation is the point of pressing
	// the button, so it is still recorded.
	before, _ := hist.Log(50)
	if _, err := svc.Sync(context.Background(), "and again, just in case"); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	after, _ := hist.Log(50)
	if len(after) != len(before)+1 {
		t.Fatalf("log grew by %d; want the annotated run recorded", len(after)-len(before))
	}
	if after[0].Body != "and again, just in case" {
		t.Errorf("newest = %+v", after[0])
	}
}

// An unannotated run that moved nothing must not write an entry — an hourly
// schedule would otherwise fill the log with a record of nothing happening.
func TestQuietSyncsLeaveNoTrace(t *testing.T) {
	f := stdFake()
	svc, hist, v := syncingWithHistory(t, f)
	writeNote(t, v, "Idea.md", "a thought\n")
	svc.Sync(context.Background(), "")

	before, _ := hist.Log(50)
	for i := 0; i < 3; i++ {
		if _, err := svc.Sync(context.Background(), ""); err != nil {
			t.Fatalf("Sync: %v", err)
		}
	}
	after, _ := hist.Log(50)
	if len(after) != len(before) {
		t.Errorf("three idle syncs wrote %d entries: %+v", len(after)-len(before), after)
	}
}

// With a history there is no zip: the commit taken before the run is the same
// safety copy, incremental and per-note rather than all-or-nothing.
func TestHistoryReplacesTheZipBackup(t *testing.T) {
	f := stdFake()
	svc, hist, v := syncingWithHistory(t, f)
	writeNote(t, v, "Idea.md", "the version I want back\n")
	svc.Sync(context.Background(), "")

	f.put("Idea.md", []byte("something unwelcome\n"))
	result, err := svc.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.BackupFile != "" {
		t.Errorf("wrote a zip backup as well as history: %s", result.BackupFile)
	}
	if backups, _ := svc.Backups(); len(backups) != 0 {
		t.Errorf("backups = %+v; want none where there is a history", backups)
	}

	// The version that was replaced is reachable, which is the whole promise.
	commits, _ := hist.Log(50)
	pre := findKind(t, commits, history.KindLocal)
	got, err := hist.Show(pre.Ref, "Idea.md")
	if err != nil || string(got) != "the version I want back\n" {
		t.Errorf("pre-sync version = %q, %v", got, err)
	}
	// And rolling back to it puts it back.
	if _, err := hist.Rollback(pre.Ref); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	data, err := v.ReadFile("Idea.md")
	if err != nil || string(data) != "the version I want back\n" {
		t.Errorf("after rollback = %q, %v", data, err)
	}
}

// Settling a conflict is exactly the moment someone might later wish they had
// chosen the other way, so it earns an entry of its own.
func TestResolvingAConflictIsRecorded(t *testing.T) {
	f := stdFake()
	svc, hist, v := syncingWithHistory(t, f)
	writeNote(t, v, "Idea.md", "start\n")
	svc.Sync(context.Background(), "")

	writeNote(t, v, "Idea.md", "my version\n")
	f.put("Idea.md", []byte("their version\n"))
	if _, err := svc.Sync(context.Background(), ""); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := svc.ResolveConflict(context.Background(), "Idea.md", ResolveKeepRemote, ""); err != nil {
		t.Fatalf("ResolveConflict: %v", err)
	}
	commits, _ := hist.Log(50)
	got := findKind(t, commits, history.KindConflict)
	if !strings.Contains(got.Subject, "Idea.md") || !strings.Contains(got.Subject, "cloud's version") {
		t.Errorf("conflict subject = %q", got.Subject)
	}
}

// A rollback rewrites the vault from outside the sync, so the recorded hashes
// now describe files that are no longer there. Left alone, the next run would
// read that as a pile of remote deletions and push them.
func TestForgetSyncStateAfterARollback(t *testing.T) {
	f := stdFake()
	svc, _, v := syncingWithHistory(t, f)
	writeNote(t, v, "Idea.md", "start\n")
	svc.Sync(context.Background(), "")

	if state, _ := svc.State.State(); len(state.Files) != 1 {
		t.Fatalf("state after sync = %+v", state.Files)
	}
	if err := svc.ForgetSyncState(); err != nil {
		t.Fatalf("ForgetSyncState: %v", err)
	}
	state, _ := svc.State.State()
	if len(state.Files) != 0 {
		t.Errorf("state not cleared: %+v", state.Files)
	}
	if state.FolderID != "/Notes" {
		t.Errorf("folder forgotten too: %q", state.FolderID)
	}
	// The next run works the difference out from scratch rather than deleting.
	result, err := svc.Sync(context.Background(), "")
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.DeletedLocal != 0 || result.DeletedRemote != 0 {
		t.Errorf("a run after forgetting deleted things: %+v", result)
	}
}

func findKind(t *testing.T, commits []history.Commit, kind string) history.Commit {
	t.Helper()
	for _, c := range commits {
		if c.Kind == kind {
			return c
		}
	}
	t.Fatalf("no %q commit in %+v", kind, commits)
	return history.Commit{}
}
