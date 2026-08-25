package history

import (
	"os"
	"path/filepath"
	"testing"
)

// The watcher's whole contract: commit once writing has stopped, and not
// before. A commit per keystroke would bury the log; a commit per tick would
// cut sentences in half.
func TestWatcherCommitsOnlyOnceEditingSettles(t *testing.T) {
	r, root := newRepo(t)
	w := &Watcher{Repo: r}

	// The first tick only takes a baseline — a server that starts while a note
	// is half-written waits rather than committing the half.
	write(t, root, "Idea.md", "half a sen")
	if w.Tick() {
		t.Fatal("committed on the first tick, with nothing to compare against")
	}
	// Still being written: the tree moved, so hold off.
	write(t, root, "Idea.md", "half a sentence, then the rest\n")
	if w.Tick() {
		t.Fatal("committed while the note was still changing")
	}
	// Settled: two readings agree.
	if !w.Tick() {
		t.Fatal("did not commit after the writing stopped")
	}
	commits, err := r.Log(10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if commits[0].Kind != KindEdit {
		t.Fatalf("newest = %+v", commits[0])
	}
	// The committed version is the finished sentence, never the half.
	got, err := r.Show(commits[0].Ref, "Idea.md")
	if err != nil || string(got) != "half a sentence, then the rest\n" {
		t.Errorf("committed version = %q, %v", got, err)
	}

	// Nothing changed since: no second commit, however many ticks pass.
	if w.Tick() || w.Tick() {
		t.Error("committed again with an unchanged vault")
	}
	if after, _ := r.Log(10); len(after) != len(commits) {
		t.Errorf("the log grew while the vault stood still: %+v", after)
	}
}

func TestWatcherCommitsADeletion(t *testing.T) {
	r, root := newRepo(t)
	w := &Watcher{Repo: r}
	write(t, root, "Idea.md", "written\n")
	w.Tick()
	w.Tick() // committed

	if err := os.Remove(filepath.Join(root, "Idea.md")); err != nil {
		t.Fatal(err)
	}
	w.Tick() // notices the change
	if !w.Tick() {
		t.Fatal("a deletion was never committed")
	}
	commits, _ := r.Log(1)
	if _, err := r.Show(commits[0].Ref, "Idea.md"); err == nil {
		t.Error("the note is still in the newest commit")
	}
}

// A sync or a rollback commits the vault itself. The watcher must not follow
// that with a duplicate — it sees a moved tree, skips a tick, and then finds
// nothing staged.
func TestWatcherDoesNotDuplicateSomebodyElsesCommit(t *testing.T) {
	r, root := newRepo(t)
	w := &Watcher{Repo: r}
	write(t, root, "Idea.md", "one\n")
	w.Tick()
	w.Tick() // committed
	before, _ := r.Log(50)

	// Something else — a sync — changes and commits the vault.
	write(t, root, "Idea.md", "brought down by a sync\n")
	if _, err := r.Commit("Sync", "", KindSync); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if w.Tick() || w.Tick() {
		t.Error("the watcher committed after a sync had already done so")
	}
	after, _ := r.Log(50)
	if len(after) != len(before)+1 {
		t.Errorf("log grew by %d; want just the sync's own commit", len(after)-len(before))
	}
}

// Where git is missing the watcher is inert rather than noisy.
func TestWatcherIsInertWithoutGit(t *testing.T) {
	w := &Watcher{Repo: &Repo{Root: t.TempDir()}}
	if w.Tick() {
		t.Error("committed with no git available")
	}
	var none *Watcher = &Watcher{}
	if none.Tick() {
		t.Error("committed with no repo at all")
	}
}
