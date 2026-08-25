// Package history is the vault's version history, kept as an ordinary git
// repository in the vault folder itself.
//
// Git rather than a scheme of our own, for the same reason the notes are plain
// markdown: the vault stays a folder other tools understand. Whatever Thought
// Mesh records here, `git log`, `git diff` and `git checkout` read — and a user
// who stops using this app keeps every version of every note.
//
// It shells out to the `git` binary instead of linking a git implementation.
// That keeps the single static build (no cgo, no large dependency tree), and it
// guarantees the repository is exactly what the git on the machine considers
// valid rather than approximately so. Where git is missing, history is simply
// off — `Available` reports it and every call degrades to a no-op, so the rest
// of the server never branches on whether it happened to be installed.
//
// Two rules the rest of the package is built on:
//
//   - **The repository is in the vault, and only there.** `.git` is a hidden
//     entry, which every vault walk and the cloud sync tree listing already
//     skip — so history is local to this server and never rides along to
//     Dropbox. Syncing a `.git` directory through a file-sync service is a
//     well-known way to corrupt it, and with one server owning the vault there
//     is nothing to gain.
//   - **History is append-only.** Rolling back writes the old tree as a *new*
//     commit rather than rewriting or discarding anything, so a rollback is
//     itself undoable and the log is a truthful record of what happened. This
//     package never rewrites history, never touches branches or remotes, and
//     never pushes.
package history

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Kinds of commit Thought Mesh makes. They ride in a trailer so the UI can
// label an entry without parsing (or translating) the subject line.
const (
	// KindEdit — notes changed and settled; the periodic capture.
	KindEdit = "edit"
	// KindLocal — uncommitted local edits captured just before a sync, so the
	// pre-sync state is recoverable whatever the sync does next.
	KindLocal = "local"
	// KindSync — what a cloud sync brought into the vault.
	KindSync = "sync"
	// KindConflict — a sync conflict the user settled.
	KindConflict = "conflict"
	// KindRollback — the vault put back to an earlier commit.
	KindRollback = "rollback"
	// KindCheckpoint — a moment the user marked deliberately.
	KindCheckpoint = "checkpoint"
	// KindRestore — the vault replaced from a pre-sync backup archive.
	KindRestore = "restore"
)

// kindTrailer is the commit trailer carrying the kind. A trailer is invisible
// in ordinary use and survives cherry-picks, which a subject-line prefix would
// not.
const kindTrailer = "Thought-Mesh-Kind"

// author is who commits made by the server are attributed to. Deliberately not
// the person using it: the server made these commits, and a vault that is also
// the user's own repository should say so.
const (
	authorName  = "Thought Mesh"
	authorEmail = "thoughtmesh@localhost"
)

// commandTimeout bounds one git invocation. Generous for a large vault, present
// so a wedged git can never hold the scheduler goroutine.
const commandTimeout = 2 * time.Minute

// Commit is one entry in the log.
type Commit struct {
	// Ref is the full commit hash; Short is what the UI shows.
	Ref   string `json:"ref"`
	Short string `json:"short"`
	// Subject is the first line, Body the rest with the trailer stripped.
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// Kind is one of the Kind* constants, or "" for a commit made by
	// something other than Thought Mesh — a user's own `git commit` is a
	// first-class part of this history too.
	Kind   string `json:"kind"`
	Author string `json:"author"`
	// AtMs is the commit time in unix milliseconds.
	AtMs int64 `json:"at_ms"`
}

// Repo is the vault's git repository.
//
// A nil *Repo is a working, disabled history: every method is safe to call and
// does nothing. That is what lets the callers stay free of "if history != nil"
// at each site.
type Repo struct {
	// Root is the vault directory — the work tree.
	Root string
	// git is the resolved binary, "" when git isn't installed.
	git string
	// Now is the clock commit times are taken from; nil means time.Now.
	Now func() time.Time

	// mu serializes everything. Three callers reach a repository at once — the
	// watcher goroutine, the sync scheduler, and any HTTP handler — and they
	// all stage into the same index. Git would refuse the collision rather than
	// corrupt anything (it takes index.lock), but a refusal is a lost commit
	// and an error somebody has to read, so they queue instead.
	//
	// Every exported method takes this and delegates to an unexported one;
	// nothing below the lock may call back through an exported method.
	mu sync.Mutex
}

// Open prepares the vault's history, initializing a repository if the folder
// is not one yet.
//
// Only `<root>/.git` counts. Deliberately not `git rev-parse --show-toplevel`:
// a vault sitting inside somebody's dotfiles repository would resolve to *that*
// repository, and Thought Mesh would start committing a tree it knows nothing
// about. A vault either has its own repository or gets one.
//
// A missing git binary is not an error — it returns a disabled Repo, and the
// server runs exactly as it did before history existed.
func Open(root string, now func() time.Time) (*Repo, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		return &Repo{Root: abs, Now: now}, nil
	}
	r := &Repo{Root: abs, git: bin, Now: now}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
		return r, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := r.init(); err != nil {
		return nil, fmt.Errorf("initialize vault history: %w", err)
	}
	return r, nil
}

// Available reports whether history is working. Everything else is a no-op
// when it isn't.
func (r *Repo) Available() bool { return r != nil && r.git != "" }

func (r *Repo) now() time.Time {
	if r == nil || r.Now == nil {
		return time.Now()
	}
	return r.Now()
}

// init creates the repository and takes the first commit, so a vault that
// already had notes in it starts with them rather than with an empty tree.
func (r *Repo) init() error {
	// `main` explicitly: git's default branch name varies by version and
	// config, and the rest of this package should not have to wonder.
	if _, err := r.run("init", "--quiet", "--initial-branch=main"); err != nil {
		return err
	}
	if err := r.writeGitignore(); err != nil {
		return err
	}
	// Open hasn't returned yet, so nothing else can be holding the lock; take
	// it anyway rather than leaving one path that assumes so.
	r.mu.Lock()
	defer r.mu.Unlock()
	_, err := r.commit("Start tracking this vault", "", KindCheckpoint, false)
	return err
}

// gitignore is written only into a repository this package created — never
// over one the user already had. It excludes the two things that would
// otherwise churn: the temp files a write-then-rename save passes through (a
// commit could catch one mid-write) and the state other tools keep in the
// vault, which is not the user's notes and changes constantly.
const gitignore = `# Written by Thought Mesh when it started tracking this vault.
# The notes are the point; everything below is other tools' state or ours.
*.tmp~
.obsidian/
.trash/
.DS_Store
`

func (r *Repo) writeGitignore() error {
	path := filepath.Join(r.Root, ".gitignore")
	if _, err := os.Stat(path); err == nil {
		return nil // the user's own; leave it alone
	}
	return os.WriteFile(path, []byte(gitignore), 0o644)
}

// Commit stages everything and commits, when there is anything to commit.
//
// It reports whether a commit was made: an unchanged vault produces none, which
// is what keeps a once-a-minute watcher from filling the log with noise. A body
// is optional; `kind` rides in a trailer.
func (r *Repo) Commit(subject, body, kind string) (bool, error) {
	if !r.Available() {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commit(subject, body, kind, false)
}

// CommitAlways is Commit for a moment worth recording even when nothing
// changed — a checkpoint, or a manual sync the user annotated. The annotation
// is the point; losing it because the tree happened to be clean would be
// surprising.
func (r *Repo) CommitAlways(subject, body, kind string) (bool, error) {
	if !r.Available() {
		return false, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commit(subject, body, kind, true)
}

// commit is Commit without the lock; callers already hold it.
func (r *Repo) commit(subject, body, kind string, allowEmpty bool) (bool, error) {
	if _, err := r.run("add", "-A", "--", "."); err != nil {
		return false, err
	}
	if !allowEmpty {
		dirty, err := r.staged()
		if err != nil {
			return false, err
		}
		if !dirty {
			return false, nil
		}
	}
	args := []string{"commit", "--quiet", "--no-verify", "-m", message(subject, body, kind)}
	if allowEmpty {
		args = append(args, "--allow-empty")
	}
	if _, err := r.run(args...); err != nil {
		return false, err
	}
	return true, nil
}

// message assembles subject, optional body and the kind trailer.
func message(subject, body, kind string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(subject))
	if trimmed := strings.TrimSpace(body); trimmed != "" {
		b.WriteString("\n\n")
		b.WriteString(trimmed)
	}
	if kind != "" {
		b.WriteString("\n\n" + kindTrailer + ": " + kind)
	}
	return b.String()
}

// staged reports whether the index differs from HEAD — "is there anything to
// commit", asked in the one way that also works before the first commit.
func (r *Repo) staged() (bool, error) {
	if err := r.hasCommits(); err != nil {
		// No HEAD yet: anything in the index at all is a change.
		out, err := r.run("diff", "--cached", "--name-only")
		if err != nil {
			return false, err
		}
		return strings.TrimSpace(out) != "", nil
	}
	_, err := r.run("diff", "--cached", "--quiet")
	if err == nil {
		return false, nil // exit 0: index matches HEAD
	}
	if code, ok := exitCode(err); ok && code == 1 {
		return true, nil // exit 1: there are staged changes
	}
	return false, err
}

// Fingerprint is the hash of the tree the vault would commit to right now.
//
// The watcher uses it to tell "still being typed into" from "settled": two
// identical readings a tick apart mean the writing has stopped. That makes
// content-sensitivity the whole requirement, and it is why this is a tree hash
// rather than `git status` output — status reports *which* files are dirty, so
// it stops moving after the first keystroke and would have the watcher commit
// in the middle of a sentence.
//
// Staging as a side effect is harmless: the index is a cache of what a commit
// would contain, and that is exactly the question being asked.
func (r *Repo) Fingerprint() (string, error) {
	if !r.Available() {
		return "", nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.run("add", "-A", "--", "."); err != nil {
		return "", err
	}
	out, err := r.run("write-tree")
	return strings.TrimSpace(out), err
}

// Log returns the most recent commits, newest first.
func (r *Repo) Log(limit int) ([]Commit, error) {
	return r.log(limit, nil)
}

// FileLog returns the commits that touched one path, newest first — a note's
// own history. `--follow` is deliberately not used: a rename in this app goes
// through Mesh.Rename, which rewrites other notes in the same commit, and
// following through that guesses more than it knows.
func (r *Repo) FileLog(path string, limit int) ([]Commit, error) {
	clean, err := relPath(path)
	if err != nil {
		return nil, err
	}
	return r.log(limit, []string{"--", clean})
}

// logFormat packs one commit into NUL-separated fields, records separated by
// 0x1e — bytes that cannot appear in a hash, a name or a subject, so the parse
// needs no escaping.
const logFormat = "%H%x00%h%x00%an%x00%at%x00%s%x00%b%x1e"

func (r *Repo) log(limit int, extra []string) ([]Commit, error) {
	if !r.Available() {
		return []Commit{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.hasCommits(); err != nil {
		return []Commit{}, nil // a repository with no commits has no history
	}
	if limit <= 0 || limit > maxLogEntries {
		limit = maxLogEntries
	}
	args := append([]string{
		"log", fmt.Sprintf("-n%d", limit), "--format=" + logFormat, "--no-color",
	}, extra...)
	out, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	return parseLog(out), nil
}

// maxLogEntries bounds a listing. History can be years long; a screen is not.
const maxLogEntries = 500

func parseLog(out string) []Commit {
	commits := []Commit{}
	for _, record := range strings.Split(out, "\x1e") {
		record = strings.TrimLeft(record, "\n")
		if strings.TrimSpace(record) == "" {
			continue
		}
		fields := strings.Split(record, "\x00")
		if len(fields) < 6 {
			continue
		}
		body, kind := splitTrailer(fields[5])
		commits = append(commits, Commit{
			Ref:     fields[0],
			Short:   fields[1],
			Author:  fields[2],
			AtMs:    parseUnix(fields[3]),
			Subject: fields[4],
			Body:    body,
			Kind:    kind,
		})
	}
	return commits
}

// splitTrailer pulls the kind out of a commit body and returns what remains.
func splitTrailer(body string) (rest, kind string) {
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		if value, ok := strings.CutPrefix(line, kindTrailer+":"); ok {
			kind = strings.TrimSpace(value)
			continue
		}
		kept = append(kept, line)
	}
	return strings.TrimSpace(strings.Join(kept, "\n")), kind
}

func parseUnix(s string) int64 {
	var seconds int64
	for _, c := range strings.TrimSpace(s) {
		if c < '0' || c > '9' {
			return 0
		}
		seconds = seconds*10 + int64(c-'0')
	}
	return seconds * 1000
}

// Show returns one file's contents as of a commit. A path that did not exist
// then is a *NotFoundError from the caller's point of view; here it is an
// ordinary git failure, which the API layer maps.
func (r *Repo) Show(ref, path string) ([]byte, error) {
	if !r.Available() {
		return nil, ErrUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cleanRef, err := validateRef(ref)
	if err != nil {
		return nil, err
	}
	cleanPath, err := relPath(path)
	if err != nil {
		return nil, err
	}
	return r.runRaw("show", cleanRef+":"+cleanPath)
}

// Rollback puts the whole vault back to a commit, as a NEW commit.
//
// Nothing is rewritten or discarded: whatever is in the vault now is committed
// first (so it can be reached again), then the old tree is written on top. That
// is what makes a rollback safe to try — it is just another entry in the log,
// and rolling back the rollback is the same operation again.
func (r *Repo) Rollback(ref string) (Commit, error) {
	if !r.Available() {
		return Commit{}, ErrUnavailable
	}
	// Held across the whole sequence: a commit landing between the capture and
	// the read-tree would be silently rolled away with everything else.
	r.mu.Lock()
	defer r.mu.Unlock()
	cleanRef, err := validateRef(ref)
	if err != nil {
		return Commit{}, err
	}
	target, err := r.commitInfo(cleanRef)
	if err != nil {
		return Commit{}, err
	}
	if _, err := r.commit(
		"Vault before rolling back to "+target.Short+", at "+r.stamp(), "", KindLocal, false,
	); err != nil {
		return Commit{}, err
	}
	// read-tree writes both the index and the working tree, removing tracked
	// files the old tree did not have — which a `checkout <ref> -- .` would
	// leave behind, quietly turning a rollback into a merge.
	if _, err := r.run("read-tree", "-u", "--reset", target.Ref); err != nil {
		return Commit{}, err
	}
	// No body: the subject already says what this restores, and the italic
	// body line in the UI is reserved for what a person typed.
	if _, err := r.commit(
		"Roll back to "+target.Short+" — "+target.Subject, "", KindRollback, true,
	); err != nil {
		return Commit{}, err
	}
	return r.commitInfo("HEAD")
}

// commitInfo reads one commit's fields.
func (r *Repo) commitInfo(ref string) (Commit, error) {
	out, err := r.run("log", "-n1", "--format="+logFormat, "--no-color", ref)
	if err != nil {
		return Commit{}, err
	}
	commits := parseLog(out)
	if len(commits) == 0 {
		return Commit{}, fmt.Errorf("no such commit: %s", ref)
	}
	return commits[0], nil
}

// stampLayout is how a time reads inside a commit message: unambiguous,
// sortable, and the same shape the Sync page shows.
const stampLayout = "2006-01-02 15:04"

// Stamp renders the current time the way commit subjects carry it.
func (r *Repo) Stamp() string { return r.stamp() }

func (r *Repo) stamp() string { return r.now().Format(stampLayout) }

// hasCommits reports (as an error) whether HEAD resolves yet.
func (r *Repo) hasCommits() error {
	_, err := r.run("rev-parse", "--verify", "--quiet", "HEAD")
	return err
}

// --- running git ---------------------------------------------------------------

// ErrUnavailable is returned by the calls that cannot degrade to a no-op when
// git is missing. The API layer turns it into a plain "history is off here".
var ErrUnavailable = fmt.Errorf("this server has no version history: git is not installed")

func (r *Repo) run(args ...string) (string, error) {
	out, err := r.runRaw(args...)
	return string(out), err
}

func (r *Repo) runRaw(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	// The git directory and work tree are passed explicitly rather than
	// discovered: discovery walks upwards, and a vault inside another
	// repository would silently resolve to that one.
	full := append([]string{
		"--git-dir=" + filepath.Join(r.Root, ".git"),
		"--work-tree=" + r.Root,
		// A server running as a different user from the vault's owner would
		// otherwise be refused for "dubious ownership". The path is ours by
		// construction, so the check buys nothing here.
		"-c", "safe.directory=" + r.Root,
	}, args...)
	cmd := exec.CommandContext(ctx, r.git, full...)
	cmd.Dir = r.Root
	// A fixed identity, and an environment with nothing of the user's in it:
	// a stray GIT_DIR or an interactive credential prompt in the server's
	// environment must not reach these invocations.
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+authorName,
		"GIT_AUTHOR_EMAIL="+authorEmail,
		"GIT_COMMITTER_NAME="+authorName,
		"GIT_COMMITTER_EMAIL="+authorEmail,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_OPTIONAL_LOCKS=0",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &CommandError{
			Args:   args,
			Stderr: strings.TrimSpace(stderr.String()),
			Err:    err,
		}
	}
	return stdout.Bytes(), nil
}

// CommandError carries what git said, which is the only useful thing to show.
type CommandError struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), e.Stderr)
}

func (e *CommandError) Unwrap() error { return e.Err }

// exitCode pulls the process exit status out of a run failure. Several git
// commands answer a question with their exit code — `diff --quiet` says
// "there are changes" with a 1 — so a non-zero status is not always a failure.
func exitCode(err error) (int, bool) {
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 0, false
	}
	return ee.ExitCode(), true
}

// --- input validation ----------------------------------------------------------

// validateRef accepts only what a commit reference can look like coming from a
// request. Refs reach a command line, so nothing that could be read as an
// option or a path is allowed through — this is the boundary, and it is narrow
// on purpose.
func validateRef(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", &ValidationError{"a commit reference is required"}
	}
	if ref == "HEAD" {
		return ref, nil
	}
	if len(ref) < 4 || len(ref) > 64 {
		return "", &ValidationError{"that is not a commit reference"}
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return "", &ValidationError{"a commit reference is hexadecimal"}
		}
	}
	return ref, nil
}

// relPath vets a vault-relative path on its way to git. Same boundary as the
// ref: no leading dash (an option), no traversal, no absolute paths.
func relPath(path string) (string, error) {
	path = strings.TrimSpace(strings.TrimPrefix(path, "./"))
	if path == "" {
		return "", &ValidationError{"a path is required"}
	}
	if strings.HasPrefix(path, "-") || strings.HasPrefix(path, "/") || strings.Contains(path, `\`) {
		return "", &ValidationError{fmt.Sprintf("unusable path: %q", path)}
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", &ValidationError{fmt.Sprintf("unusable path: %q", path)}
		}
	}
	return path, nil
}

// ValidationError reports input the caller can fix. The API maps it to 400,
// the same way it maps the vault's.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }
