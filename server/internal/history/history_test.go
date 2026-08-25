package history

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests drive the real `git` binary against a real vault, which is the
// only way to check the thing this package actually promises: that what it
// writes is a repository git itself agrees with.
func newRepo(t *testing.T) (*Repo, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; history is off on such a machine by design")
	}
	root := t.TempDir()
	now := time.Date(2026, 8, 25, 9, 0, 0, 0, time.UTC)
	r, err := Open(root, func() time.Time { return now })
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !r.Available() {
		t.Fatal("git is installed but history reports unavailable")
	}
	return r, root
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

// Opening a plain folder turns it into a repository whose first commit already
// holds the notes that were there — a vault does not start its history empty
// just because Thought Mesh arrived late.
func TestOpenInitializesAndCapturesWhatIsAlreadyThere(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Idea.md"), []byte("a thought\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Open(root, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	commits, err := r.Log(10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 1 || commits[0].Kind != KindCheckpoint {
		t.Fatalf("commits = %+v", commits)
	}
	if got, err := r.Show(commits[0].Ref, "Idea.md"); err != nil || string(got) != "a thought\n" {
		t.Errorf("first commit = %q, %v", got, err)
	}
	// Opening again must not re-initialize or add a commit.
	again, err := Open(root, nil)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	if commits, _ := again.Log(10); len(commits) != 1 {
		t.Errorf("re-opening changed the history: %+v", commits)
	}
}

// A vault that is already the user's own repository is used as it is — its
// history is not restarted and its .gitignore is not overwritten.
func TestOpenAdoptsAnExistingRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("mine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "init", "--quiet", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if _, err := Open(root, nil); err != nil {
		t.Fatalf("Open: %v", err)
	}
	if got := read(t, root, ".gitignore"); got != "mine\n" {
		t.Errorf("the user's .gitignore was overwritten: %q", got)
	}
}

func TestCommitOnlyWhenSomethingChanged(t *testing.T) {
	r, root := newRepo(t)

	write(t, root, "Idea.md", "first\n")
	made, err := r.Commit("Notes edited", "", KindEdit)
	if err != nil || !made {
		t.Fatalf("Commit = %v, %v", made, err)
	}
	// Nothing changed since: no commit, and no error.
	made, err = r.Commit("Notes edited", "", KindEdit)
	if err != nil || made {
		t.Fatalf("second Commit = %v, %v; want no commit", made, err)
	}
	// A moment worth marking is recorded even so.
	made, err = r.CommitAlways("Checkpoint", "before the trip", KindCheckpoint)
	if err != nil || !made {
		t.Fatalf("CommitAlways = %v, %v", made, err)
	}
	commits, err := r.Log(10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if len(commits) != 3 {
		t.Fatalf("commits = %d: %+v", len(commits), commits)
	}
	if commits[0].Subject != "Checkpoint" || commits[0].Body != "before the trip" ||
		commits[0].Kind != KindCheckpoint {
		t.Errorf("newest = %+v", commits[0])
	}
	// The trailer is bookkeeping, not something the user should read back.
	if strings.Contains(commits[0].Body, kindTrailer) {
		t.Errorf("the kind trailer leaked into the body: %q", commits[0].Body)
	}
	if commits[0].Author != authorName {
		t.Errorf("author = %q; want the server, not the user", commits[0].Author)
	}
}

// The temp files a write-then-rename save passes through must never be
// committed: a commit could otherwise catch one mid-write.
func TestGitignoreKeepsTempAndToolStateOut(t *testing.T) {
	r, root := newRepo(t)
	write(t, root, "Idea.md", "kept\n")
	write(t, root, "Idea.md.tmp~", "half written")
	write(t, root, ".obsidian/workspace.json", "{}")
	if _, err := r.Commit("Notes edited", "", KindEdit); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	out, err := r.run("ls-files")
	if err != nil {
		t.Fatalf("ls-files: %v", err)
	}
	tracked := strings.Fields(out)
	for _, name := range tracked {
		if strings.HasSuffix(name, ".tmp~") || strings.HasPrefix(name, ".obsidian/") {
			t.Errorf("tracked %q; want it ignored (tracked: %v)", name, tracked)
		}
	}
	if !containsString(tracked, "Idea.md") {
		t.Errorf("the note itself is not tracked: %v", tracked)
	}
}

func TestFileLogAndShowGiveOneNotesHistory(t *testing.T) {
	r, root := newRepo(t)
	write(t, root, "Idea.md", "version one\n")
	write(t, root, "Other.md", "unrelated\n")
	r.Commit("First", "", KindEdit)
	write(t, root, "Idea.md", "version two\n")
	r.Commit("Second", "", KindEdit)
	write(t, root, "Other.md", "unrelated, edited\n")
	r.Commit("Third", "", KindEdit)

	commits, err := r.FileLog("Idea.md", 10)
	if err != nil {
		t.Fatalf("FileLog: %v", err)
	}
	// Only the commits that touched this note — the third one didn't.
	if len(commits) != 2 || commits[0].Subject != "Second" || commits[1].Subject != "First" {
		t.Fatalf("file log = %+v", commits)
	}
	older, err := r.Show(commits[1].Ref, "Idea.md")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if string(older) != "version one\n" {
		t.Errorf("older version = %q", older)
	}
	// The note as it is now is untouched by having looked at an old one.
	if got := read(t, root, "Idea.md"); got != "version two\n" {
		t.Errorf("working copy = %q", got)
	}
}

// Rolling back must put the vault back exactly — including removing notes that
// did not exist at the target — and must do it as a new commit, so the state
// being replaced is still reachable afterwards.
func TestRollbackRestoresTheTreeAsANewCommit(t *testing.T) {
	r, root := newRepo(t)
	write(t, root, "Keep.md", "original\n")
	r.Commit("First", "", KindEdit)
	target, _ := r.Log(1)

	write(t, root, "Keep.md", "changed later\n")
	write(t, root, "Added.md", "written after the target\n")
	r.Commit("Second", "", KindEdit)

	rolled, err := r.Rollback(target[0].Ref)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if rolled.Kind != KindRollback {
		t.Errorf("rollback commit = %+v", rolled)
	}
	if got := read(t, root, "Keep.md"); got != "original\n" {
		t.Errorf("Keep.md = %q; want the target's version", got)
	}
	// The file added afterwards is gone — a rollback that left it behind would
	// quietly be a merge.
	if _, err := os.Stat(filepath.Join(root, "Added.md")); !os.IsNotExist(err) {
		t.Errorf("Added.md survived the rollback: %v", err)
	}
	// Nothing was rewritten: the state we rolled away from is still in the log,
	// so the rollback is itself undoable.
	commits, _ := r.Log(10)
	if len(commits) < 3 {
		t.Fatalf("history was rewritten: %+v", commits)
	}
	if !hasSubject(commits, "Second") {
		t.Errorf("the replaced state left the log: %+v", commits)
	}

	// And rolling back the rollback brings it back.
	var second Commit
	for _, c := range commits {
		if c.Subject == "Second" {
			second = c
		}
	}
	if _, err := r.Rollback(second.Ref); err != nil {
		t.Fatalf("second Rollback: %v", err)
	}
	if got := read(t, root, "Keep.md"); got != "changed later\n" {
		t.Errorf("after undoing the rollback: %q", got)
	}
	if got := read(t, root, "Added.md"); got != "written after the target\n" {
		t.Errorf("Added.md not restored: %q", got)
	}
}

// Uncommitted work must survive a rollback — it is committed first, so it can
// be reached again.
func TestRollbackCapturesUncommittedWorkFirst(t *testing.T) {
	r, root := newRepo(t)
	write(t, root, "Idea.md", "committed\n")
	r.Commit("First", "", KindEdit)
	target, _ := r.Log(1)

	write(t, root, "Idea.md", "typed but never committed\n")
	if _, err := r.Rollback(target[0].Ref); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	commits, _ := r.Log(10)
	var saved *Commit
	for i := range commits {
		if commits[i].Kind == KindLocal {
			saved = &commits[i]
		}
	}
	if saved == nil {
		t.Fatalf("uncommitted work was discarded: %+v", commits)
	}
	got, err := r.Show(saved.Ref, "Idea.md")
	if err != nil || string(got) != "typed but never committed\n" {
		t.Errorf("rescued version = %q, %v", got, err)
	}
}

// Refs and paths reach a command line, so the boundary is narrow on purpose.
func TestRefsAndPathsAreValidated(t *testing.T) {
	r, _ := newRepo(t)
	for _, ref := range []string{"", "  ", "--upload-pack=touch /tmp/x", "HEAD~1", "main", "zzz", "0a"} {
		if _, err := r.Show(ref, "Idea.md"); err == nil {
			t.Errorf("Show(%q) should have been refused", ref)
		}
		if _, err := r.Rollback(ref); err == nil {
			t.Errorf("Rollback(%q) should have been refused", ref)
		}
	}
	for _, path := range []string{"", "../escape.md", "/etc/passwd", "-x", `a\b.md`, "a/../b"} {
		if _, err := r.Show("HEAD", path); err == nil {
			t.Errorf("Show(HEAD, %q) should have been refused", path)
		}
	}
}

// The watcher tells "still being typed into" from "settled" by comparing two
// readings a tick apart, so the fingerprint has to move with the vault's
// *contents* — not merely with which files are dirty, which stops moving after
// the first keystroke and would have it commit mid-sentence.
func TestFingerprintTracksContentNotJustWhichFilesChanged(t *testing.T) {
	r, root := newRepo(t)
	clean, err := r.Fingerprint()
	if err != nil {
		t.Fatalf("Fingerprint: %v", err)
	}
	write(t, root, "Idea.md", "typing\n")
	dirty, _ := r.Fingerprint()
	if dirty == clean || dirty == "" {
		t.Fatalf("fingerprint did not move on a new note: %q → %q", clean, dirty)
	}
	if same, _ := r.Fingerprint(); same != dirty {
		t.Errorf("fingerprint is not stable across reads: %q vs %q", dirty, same)
	}
	// The file was already dirty; the *content* changing is what has to show.
	write(t, root, "Idea.md", "typing more\n")
	stillTyping, _ := r.Fingerprint()
	if stillTyping == dirty {
		t.Fatal("fingerprint did not notice an edit to an already-dirty file")
	}
	// Settled: two readings agree, which is the watcher's signal to commit.
	if again, _ := r.Fingerprint(); again != stillTyping {
		t.Errorf("fingerprint moved with no edit: %q vs %q", stillTyping, again)
	}
	r.Commit("Notes edited", "", KindEdit)
	after, _ := r.Fingerprint()
	if after != stillTyping {
		t.Errorf("committing changed the tree hash: %q vs %q", after, stillTyping)
	}
	// Deleting a note moves it too — a deletion is a change like any other.
	if err := os.Remove(filepath.Join(root, "Idea.md")); err != nil {
		t.Fatal(err)
	}
	if removed, _ := r.Fingerprint(); removed == after {
		t.Error("fingerprint did not notice a deletion")
	}
}

// A commit made by the user themselves belongs in this history as much as ours.
func TestUserCommitsAppearWithNoKind(t *testing.T) {
	r, root := newRepo(t)
	write(t, root, "Idea.md", "written by hand\n")
	if out, err := exec.Command("git", "-C", root, "add", "-A").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd := exec.Command("git", "-C", root, "commit", "-m", "By hand")
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Someone", "GIT_AUTHOR_EMAIL=s@example.com",
		"GIT_COMMITTER_NAME=Someone", "GIT_COMMITTER_EMAIL=s@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	commits, err := r.Log(5)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if commits[0].Subject != "By hand" || commits[0].Kind != "" {
		t.Errorf("hand-made commit = %+v", commits[0])
	}
}

// Where git is missing the whole feature is off, and every call has to be safe
// rather than every caller having to check.
func TestDisabledRepoIsSafeToUse(t *testing.T) {
	r := &Repo{Root: t.TempDir()}
	if r.Available() {
		t.Fatal("a repo with no git binary reports available")
	}
	if made, err := r.Commit("x", "", KindEdit); made || err != nil {
		t.Errorf("Commit = %v, %v; want a silent no-op", made, err)
	}
	if commits, err := r.Log(10); err != nil || len(commits) != 0 {
		t.Errorf("Log = %+v, %v", commits, err)
	}
	if _, err := r.Show("HEAD", "Idea.md"); err == nil {
		t.Error("Show should say history is off rather than pretend")
	}
	var nilRepo *Repo
	if nilRepo.Available() {
		t.Error("a nil repo reports available")
	}
	if made, err := nilRepo.Commit("x", "", KindEdit); made || err != nil {
		t.Errorf("nil Commit = %v, %v", made, err)
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func hasSubject(commits []Commit, subject string) bool {
	for _, c := range commits {
		if c.Subject == subject {
			return true
		}
	}
	return false
}
