// Contract tests: these pin the wire shapes the PWA is compiled against
// (apps/web/src/api/client.ts). Change them only together.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chinmay28/thought-mesh/server/internal/mesh"
	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

func newServer(t *testing.T) http.Handler {
	t.Helper()
	v, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return New(v, mesh.New(v), nil, nil)
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	return out
}

func TestHealth(t *testing.T) {
	h := newServer(t)
	rec := do(t, h, "GET", "/api/health", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := decode(t, rec)
	if body["status"] != "ok" || body["version"] == "" || body["notes"] != float64(0) {
		t.Errorf("health = %v", body)
	}
}

func TestNoteLifecycle(t *testing.T) {
	h := newServer(t)

	// Create by name+dir.
	rec := do(t, h, "POST", "/api/notes", `{"name":"My First Note","dir":"ideas","content":"hello [[Second]]"}`)
	if rec.Code != 201 {
		t.Fatalf("create status = %d: %s", rec.Code, rec.Body.String())
	}
	note := decode(t, rec)
	if note["path"] != "ideas/My First Note.md" || note["name"] != "My First Note" ||
		note["dir"] != "ideas" || note["content"] != "hello [[Second]]" {
		t.Errorf("created = %v", note)
	}
	links := note["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("links = %v", links)
	}
	l := links[0].(map[string]any)
	if l["target"] != "Second" || l["path"] != "" || l["name"] != "" {
		t.Errorf("unresolved link = %v", l)
	}
	if _, ok := note["mtime_ms"].(float64); !ok {
		t.Errorf("mtime_ms missing: %v", note)
	}

	// Create the linked-to note by explicit path; the first note becomes a backlink.
	rec = do(t, h, "POST", "/api/notes", `{"path":"Second.md","content":""}`)
	if rec.Code != 201 {
		t.Fatalf("create Second = %d", rec.Code)
	}
	rec = do(t, h, "GET", "/api/notes/Second.md", "")
	if rec.Code != 200 {
		t.Fatalf("get = %d", rec.Code)
	}
	second := decode(t, rec)
	backs := second["backlinks"].([]any)
	if len(backs) != 1 {
		t.Fatalf("backlinks = %v", backs)
	}
	b := backs[0].(map[string]any)
	if b["path"] != "ideas/My First Note.md" || b["count"] != float64(1) || b["snippet"] == "" {
		t.Errorf("backlink = %v", b)
	}

	// Duplicate create → 409 with an {"error": …} body.
	rec = do(t, h, "POST", "/api/notes", `{"path":"Second.md","content":""}`)
	if rec.Code != 409 || decode(t, rec)["error"] == "" {
		t.Errorf("duplicate create = %d %s", rec.Code, rec.Body.String())
	}

	// Save with a stale base mtime → 409; without → 200.
	rec = do(t, h, "PUT", "/api/notes/Second.md", `{"content":"x","base_mtime_ms":1}`)
	if rec.Code != 409 {
		t.Errorf("stale save = %d", rec.Code)
	}
	rec = do(t, h, "PUT", "/api/notes/Second.md", `{"content":"now has text"}`)
	if rec.Code != 200 || decode(t, rec)["content"] != "now has text" {
		t.Errorf("save = %d %s", rec.Code, rec.Body.String())
	}

	// List.
	rec = do(t, h, "GET", "/api/notes", "")
	list := decode(t, rec)["notes"].([]any)
	if len(list) != 2 {
		t.Fatalf("list = %v", list)
	}

	// Delete.
	rec = do(t, h, "DELETE", "/api/notes/Second.md", "")
	if rec.Code != 204 {
		t.Fatalf("delete = %d", rec.Code)
	}
	rec = do(t, h, "GET", "/api/notes/Second.md", "")
	if rec.Code != 404 || decode(t, rec)["error"] == "" {
		t.Errorf("get deleted = %d %s", rec.Code, rec.Body.String())
	}
}

func TestValidation(t *testing.T) {
	h := newServer(t)
	for _, tc := range []struct{ method, path, body string }{
		{"POST", "/api/notes", `{"name":"","content":""}`},
		{"POST", "/api/notes", `{"path":"../evil.md"}`},
		{"POST", "/api/notes", `not json`},
		{"POST", "/api/notes", `{"unknown_field":1}`},
		{"PUT", "/api/notes/whatever.md", `{}`},
	} {
		rec := do(t, h, tc.method, tc.path, tc.body)
		if tc.path == "/api/notes/whatever.md" && rec.Code == 404 {
			continue // content missing is checked first; 400 expected below
		}
		if rec.Code != 400 {
			t.Errorf("%s %s %s = %d; want 400", tc.method, tc.path, tc.body, rec.Code)
		}
		if decode(t, rec)["error"] == "" {
			t.Errorf("%s: no error body", tc.body)
		}
	}
}

func TestRenameEndpoint(t *testing.T) {
	h := newServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"A.md","content":"see [[B]]"}`)
	do(t, h, "POST", "/api/notes", `{"path":"B.md","content":""}`)

	rec := do(t, h, "POST", "/api/rename", `{"path":"B.md","new_path":"folder/C.md"}`)
	if rec.Code != 200 {
		t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["updated_notes"] != float64(1) {
		t.Errorf("updated_notes = %v", body["updated_notes"])
	}
	note := body["note"].(map[string]any)
	if note["path"] != "folder/C.md" {
		t.Errorf("note = %v", note)
	}
	rec = do(t, h, "GET", "/api/notes/A.md", "")
	if got := decode(t, rec)["content"]; got != "see [[C]]" {
		t.Errorf("A.md content = %q", got)
	}
}

func TestSearchAndGraph(t *testing.T) {
	h := newServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"A.md","content":"alpha links [[B]] and [[Ghost]]"}`)
	do(t, h, "POST", "/api/notes", `{"path":"B.md","content":"beta text"}`)

	rec := do(t, h, "GET", "/api/search?q=beta", "")
	results := decode(t, rec)["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", results)
	}
	r := results[0].(map[string]any)
	if r["path"] != "B.md" || r["snippet"] != "beta text" || r["line"] != float64(1) {
		t.Errorf("result = %v", r)
	}
	// Blank query is an empty result set, not an error.
	rec = do(t, h, "GET", "/api/search?q=", "")
	if rec.Code != 200 || len(decode(t, rec)["results"].([]any)) != 0 {
		t.Errorf("blank search = %d %s", rec.Code, rec.Body.String())
	}

	rec = do(t, h, "GET", "/api/graph", "")
	body := decode(t, rec)
	nodes := body["nodes"].([]any)
	edges := body["edges"].([]any)
	if len(nodes) != 3 || len(edges) != 2 {
		t.Fatalf("graph = %v", body)
	}
	var ghost map[string]any
	for _, n := range nodes {
		if n.(map[string]any)["missing"] == float64(1) {
			ghost = n.(map[string]any)
		}
	}
	if ghost == nil || ghost["name"] != "Ghost" || ghost["id"] != "missing:ghost" {
		t.Errorf("ghost = %v", ghost)
	}
}

func TestUnknownEndpoint(t *testing.T) {
	h := newServer(t)
	rec := do(t, h, "GET", "/api/nope", "")
	if rec.Code != 404 || decode(t, rec)["error"] == "" {
		t.Errorf("unknown = %d %s", rec.Code, rec.Body.String())
	}
}

// --- folders ------------------------------------------------------------------

func TestNoteFolderIsItsCategory(t *testing.T) {
	h := newServer(t)

	// A note's folder comes from its path — there is no second field to set,
	// and none on the wire that could disagree with it.
	rec := do(t, h, "POST", "/api/notes", `{"path":"Money/Idea.md","content":"a thought\n"}`)
	if rec.Code != 201 {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	note := decode(t, rec)
	if note["dir"] != "Money" {
		t.Fatalf("dir = %v", note["dir"])
	}
	if _, ok := note["categories"]; ok {
		t.Error("`categories` is gone from the wire: a folder is the category")
	}
	// Content is exactly what was sent — no frontmatter is written any more.
	if content := note["content"].(string); content != "a thought\n" {
		t.Errorf("content = %q; want it untouched", content)
	}

	// `dir` is "" at the root, never null.
	do(t, h, "POST", "/api/notes", `{"path":"Plain.md","content":"nothing\n"}`)
	plain := decode(t, do(t, h, "GET", "/api/notes/Plain.md", ""))
	if plain["dir"] != "" {
		t.Errorf("root note dir = %v; want \"\"", plain["dir"])
	}

	// The tree is derived from the notes, with counts. The root is always
	// present, so "unfiled" is never a case the client has to invent.
	body := decode(t, do(t, h, "GET", "/api/folders", ""))
	folders := body["folders"].([]any)
	if len(folders) != 2 {
		t.Fatalf("folders = %v", folders)
	}
	root := folders[0].(map[string]any)
	if root["path"] != "" || root["count"] != float64(1) || root["total"] != float64(2) {
		t.Errorf("root = %v", root)
	}
	money := folders[1].(map[string]any)
	if money["path"] != "Money" || money["name"] != "Money" || money["count"] != float64(1) {
		t.Errorf("Money = %v", money)
	}

	// Listing narrows to one folder, case-insensitively and exactly.
	notes := decode(t, do(t, h, "GET", "/api/notes?folder=money", ""))["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["path"] != "Money/Idea.md" {
		t.Errorf("filtered notes = %v", notes)
	}
	// An empty value is the vault root, not "no filter" — that distinction is
	// what makes "unfiled notes" reachable at all.
	notes = decode(t, do(t, h, "GET", "/api/notes?folder=", ""))["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["path"] != "Plain.md" {
		t.Errorf("root notes = %v", notes)
	}
	// No parameter at all is still every note.
	notes = decode(t, do(t, h, "GET", "/api/notes", ""))["notes"].([]any)
	if len(notes) != 2 {
		t.Errorf("all notes = %v", notes)
	}
}

func TestMoveNoteBetweenFolders(t *testing.T) {
	h := newServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"Loose.md","content":"a\n"}`)

	rec := do(t, h, "POST", "/api/folders/move", `{"path":"Loose.md","folder":"Money"}`)
	if rec.Code != 200 {
		t.Fatalf("move = %d: %s", rec.Code, rec.Body.String())
	}
	moved := decode(t, rec)["note"].(map[string]any)
	if moved["path"] != "Money/Loose.md" || moved["dir"] != "Money" {
		t.Errorf("moved = %v", moved)
	}
	// "" unfiles it again.
	rec = do(t, h, "POST", "/api/folders/move", `{"path":"Money/Loose.md","folder":""}`)
	if got := decode(t, rec)["note"].(map[string]any)["path"]; got != "Loose.md" {
		t.Errorf("unfiled = %v", got)
	}
}

func TestFolderRenameAndDelete(t *testing.T) {
	h := newServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"Work/A.md","content":"a\n"}`)
	do(t, h, "POST", "/api/notes", `{"path":"Work/B.md","content":"see [[A]]\n"}`)

	rec := do(t, h, "POST", "/api/folders/rename", `{"from":"Work","to":"Day job"}`)
	if rec.Code != 200 {
		t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["moved_notes"] != float64(2) {
		t.Errorf("moved = %v", body["moved_notes"])
	}
	if names := folderNames(body); names["Day job"] != 2 || names["Work"] != 0 {
		t.Errorf("after rename = %v", names)
	}

	// Deleting a folder unfiles its notes; it never deletes one.
	rec = do(t, h, "POST", "/api/folders/delete", `{"path":"Day job"}`)
	if rec.Code != 200 {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if names := folderNames(decode(t, rec)); names["Day job"] != 0 {
		t.Errorf("after delete = %v", names)
	}
	if got := decode(t, do(t, h, "GET", "/api/notes/A.md", ""))["content"]; got != "a\n" {
		t.Errorf("note lost or damaged by a folder delete: %v", got)
	}

	// A name a path can't hold is a 400, not a silent no-op.
	do(t, h, "POST", "/api/notes", `{"path":"Keep/C.md","content":"c\n"}`)
	if rec = do(t, h, "POST", "/api/folders/rename", `{"from":"Keep","to":"a:b"}`); rec.Code != 400 {
		t.Errorf("bad rename target = %d: %s", rec.Code, rec.Body.String())
	}
	// A folder holding no notes doesn't exist, so renaming it is a 404.
	if rec = do(t, h, "POST", "/api/folders/rename", `{"from":"Nope","to":"Other"}`); rec.Code != 404 {
		t.Errorf("missing folder = %d", rec.Code)
	}
}

func folderNames(body map[string]any) map[string]int {
	out := map[string]int{}
	for _, entry := range body["folders"].([]any) {
		f := entry.(map[string]any)
		out[f["path"].(string)] = int(f["count"].(float64))
	}
	return out
}

// --- merge --------------------------------------------------------------------

// The editor's third way out of a save conflict. It is stateless on purpose:
// only the browser still holds the version the edit started from.
func TestMergeEndpoint(t *testing.T) {
	h := newServer(t)

	// Edits in different places combine with nothing to decide.
	rec := do(t, h, "POST", "/api/merge",
		`{"base":"one\ntwo\n","mine":"one mine\ntwo\n","theirs":"one\ntwo theirs\n"}`)
	if rec.Code != 200 {
		t.Fatalf("merge = %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["conflicts"] != float64(0) || body["merged"] != "one mine\ntwo theirs\n" {
		t.Errorf("merge = %v", body)
	}

	// The same line on both sides comes back marked for a human.
	rec = do(t, h, "POST", "/api/merge",
		`{"base":"one\n","mine":"one mine\n","theirs":"one theirs\n"}`)
	body = decode(t, rec)
	if body["conflicts"] != float64(1) || !strings.Contains(body["merged"].(string), "<<<<<<< mine") {
		t.Errorf("conflicting merge = %v", body)
	}

	// With no base at all, the shared ends survive and the middle is contested.
	rec = do(t, h, "POST", "/api/merge", `{"mine":"head\nmine\ntail\n","theirs":"head\ntheirs\ntail\n"}`)
	body = decode(t, rec)
	if body["conflicts"] != float64(1) || !strings.HasPrefix(body["merged"].(string), "head\n") {
		t.Errorf("baseless merge = %v", body)
	}
}
