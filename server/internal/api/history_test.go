package api

import (
	"net/http"
	"os/exec"
	"testing"

	"github.com/chinmay28/thought-mesh/server/internal/history"
	"github.com/chinmay28/thought-mesh/server/internal/mesh"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// newHistoryServer wires an API over a vault that is a real git repository.
func newHistoryServer(t *testing.T) (http.Handler, *vault.Vault, *history.Repo) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed; history is off on such a machine by design")
	}
	v, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	h, err := history.Open(v.Root, nil)
	if err != nil {
		t.Fatalf("history.Open: %v", err)
	}
	return New(v, mesh.New(v), nil, h), v, h
}

// A server without git answers honestly rather than 404 — the client has to be
// able to tell "no history here" from "too old to have the feature".
func TestHistoryRoutesExistButReportUnavailableWithoutGit(t *testing.T) {
	h := newServer(t) // built with a nil history
	rec := do(t, h, "GET", "/api/history", "")
	if rec.Code != 200 {
		t.Fatalf("GET /api/history = %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["available"] != float64(0) {
		t.Errorf("available = %v; want 0", body["available"])
	}
	if got, ok := body["commits"].([]any); !ok || len(got) != 0 {
		t.Errorf("commits = %v; want an empty array, never null", body["commits"])
	}
	// The write paths say why rather than pretending to work.
	for _, route := range []string{"/api/history/checkpoint", "/api/history/rollback"} {
		if rec := do(t, h, "POST", route, `{}`); rec.Code != 400 {
			t.Errorf("POST %s without git = %d; want 400", route, rec.Code)
		}
	}
}

func TestHistoryRecordsNotesAndRollsTheVaultBack(t *testing.T) {
	h, v, _ := newHistoryServer(t)

	do(t, h, "POST", "/api/notes", `{"path":"Idea.md","content":"version one\n"}`)
	rec := do(t, h, "POST", "/api/history/checkpoint", `{"message":"the good version"}`)
	if rec.Code != 200 {
		t.Fatalf("checkpoint = %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["available"] != float64(1) {
		t.Fatalf("available = %v", body["available"])
	}
	commits := body["commits"].([]any)
	first := commits[0].(map[string]any)
	if first["kind"] != "checkpoint" || first["body"] != "the good version" {
		t.Fatalf("checkpoint commit = %v", first)
	}
	good := first["ref"].(string)

	// Change the note, and add another that didn't exist at the checkpoint.
	do(t, h, "PUT", "/api/notes/Idea.md", `{"content":"version two\n"}`)
	do(t, h, "POST", "/api/notes", `{"path":"Later.md","content":"written after\n"}`)
	do(t, h, "POST", "/api/history/checkpoint", `{"message":"after"}`)

	// A note's own history is the question people actually ask.
	rec = do(t, h, "GET", "/api/history/notes/Idea.md", "")
	if rec.Code != 200 {
		t.Fatalf("note history = %d: %s", rec.Code, rec.Body.String())
	}
	noteCommits := decode(t, rec)["commits"].([]any)
	if len(noteCommits) < 2 {
		t.Fatalf("note history = %v", noteCommits)
	}

	// Reading an old version leaves the working copy alone.
	rec = do(t, h, "GET", "/api/history/version/Idea.md?ref="+good, "")
	if rec.Code != 200 {
		t.Fatalf("show = %d: %s", rec.Code, rec.Body.String())
	}
	if got := decode(t, rec)["content"]; got != "version one\n" {
		t.Errorf("old version = %q", got)
	}
	if content, _, _ := v.Read("Idea.md"); content != "version two\n" {
		t.Errorf("reading history changed the note: %q", content)
	}

	// Rolling the whole vault back removes what came after.
	rec = do(t, h, "POST", "/api/history/rollback", `{"ref":"`+good+`"}`)
	if rec.Code != 200 {
		t.Fatalf("rollback = %d: %s", rec.Code, rec.Body.String())
	}
	if content, _, _ := v.Read("Idea.md"); content != "version one\n" {
		t.Errorf("after rollback = %q", content)
	}
	if _, _, err := v.Read("Later.md"); !vault.IsNotFound(err) {
		t.Errorf("a note written after the target survived the rollback: %v", err)
	}
	// Nothing was rewritten: the rolled-away state is still in the log, so the
	// rollback is itself undoable.
	after := decode(t, rec)["commits"].([]any)
	if !hasSubjectContaining(after, "Roll back to") {
		t.Errorf("no rollback entry: %v", after)
	}
	if !hasBodyEqual(after, "after") {
		t.Errorf("the replaced state left the log: %v", after)
	}
}

// Restoring one note leaves the rest of the vault alone, and goes through the
// ordinary save so the link index sees it.
func TestRestoreOneNoteFromHistory(t *testing.T) {
	h, v, _ := newHistoryServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"Idea.md","content":"links to [[Other]]\n"}`)
	do(t, h, "POST", "/api/notes", `{"path":"Other.md","content":"other\n"}`)
	rec := do(t, h, "POST", "/api/history/checkpoint", `{"message":"before"}`)
	good := decode(t, rec)["commits"].([]any)[0].(map[string]any)["ref"].(string)

	do(t, h, "PUT", "/api/notes/Idea.md", `{"content":"links to nothing\n"}`)
	do(t, h, "PUT", "/api/notes/Other.md", `{"content":"other, edited\n"}`)

	rec = do(t, h, "POST", "/api/history/restore", `{"path":"Idea.md","ref":"`+good+`"}`)
	if rec.Code != 200 {
		t.Fatalf("restore = %d: %s", rec.Code, rec.Body.String())
	}
	note := decode(t, rec)
	if note["content"] != "links to [[Other]]\n" {
		t.Errorf("restored note = %q", note["content"])
	}
	// The link index followed the restore, which is why this goes through the
	// note save rather than straight through git.
	links := note["links"].([]any)
	if len(links) != 1 || links[0].(map[string]any)["path"] != "Other.md" {
		t.Errorf("links = %v", links)
	}
	// The other note is untouched.
	if content, _, _ := v.Read("Other.md"); content != "other, edited\n" {
		t.Errorf("restoring one note changed another: %q", content)
	}
}

// Refs and paths reach a command line, and asking for a version that predates a
// note is an ordinary thing to do — a 404, not an internal error.
func TestHistoryRejectsBadRefsAndUnknownVersions(t *testing.T) {
	h, _, _ := newHistoryServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"Idea.md","content":"one\n"}`)
	rec := do(t, h, "POST", "/api/history/checkpoint", `{}`)
	ref := decode(t, rec)["commits"].([]any)[0].(map[string]any)["ref"].(string)

	for _, bad := range []string{"", "HEAD~1", "--upload-pack=x", "main"} {
		if rec := do(t, h, "GET", "/api/history/version/Idea.md?ref="+bad, ""); rec.Code != 400 {
			t.Errorf("ref %q = %d; want 400", bad, rec.Code)
		}
	}
	if rec := do(t, h, "POST", "/api/history/rollback", `{"ref":""}`); rec.Code != 400 {
		t.Errorf("empty rollback ref = %d; want 400", rec.Code)
	}
	// A note that did not exist at that commit.
	rec = do(t, h, "GET", "/api/history/version/Missing.md?ref="+ref, "")
	if rec.Code != 404 {
		t.Errorf("unknown version = %d: %s", rec.Code, rec.Body.String())
	}
}

func hasSubjectContaining(commits []any, want string) bool {
	for _, entry := range commits {
		if subject, ok := entry.(map[string]any)["subject"].(string); ok &&
			len(subject) >= len(want) && contains(subject, want) {
			return true
		}
	}
	return false
}

func hasBodyEqual(commits []any, want string) bool {
	for _, entry := range commits {
		if entry.(map[string]any)["body"] == want {
			return true
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
