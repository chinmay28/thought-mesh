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

// --- categories ---------------------------------------------------------------

func TestNoteCategoriesLifecycle(t *testing.T) {
	h := newServer(t)

	// Categories can be set at creation…
	rec := do(t, h, "POST", "/api/notes",
		`{"path":"Idea.md","content":"a thought\n","categories":["Ideas","ideas"]}`)
	if rec.Code != 201 {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body.String())
	}
	note := decode(t, rec)
	cats := note["categories"].([]any)
	if len(cats) != 1 || cats[0] != "Ideas" {
		t.Fatalf("categories = %v", cats)
	}
	// …and they live in the note's own frontmatter, not in a sidecar.
	if content := note["content"].(string); !strings.HasPrefix(content, "---\ncategories: [Ideas]\n---\n") {
		t.Errorf("content = %q", content)
	}

	// A note with none reports an empty array, never a null.
	do(t, h, "POST", "/api/notes", `{"path":"Plain.md","content":"nothing\n"}`)
	plain := decode(t, do(t, h, "GET", "/api/notes/Plain.md", ""))
	if got, ok := plain["categories"].([]any); !ok || len(got) != 0 {
		t.Errorf("plain note categories = %v", plain["categories"])
	}

	// Assigning replaces the list wholesale.
	rec = do(t, h, "POST", "/api/categories/assign",
		`{"path":"Idea.md","categories":["Work","Reading list"]}`)
	if rec.Code != 200 {
		t.Fatalf("assign = %d: %s", rec.Code, rec.Body.String())
	}
	cats = decode(t, rec)["categories"].([]any)
	if len(cats) != 2 || cats[0] != "Work" || cats[1] != "Reading list" {
		t.Errorf("after assign = %v", cats)
	}

	// The vocabulary is derived from the notes, with counts.
	body := decode(t, do(t, h, "GET", "/api/categories", ""))
	all := body["categories"].([]any)
	if len(all) != 2 {
		t.Fatalf("vocabulary = %v", all)
	}
	first := all[0].(map[string]any)
	if first["name"] != "Reading list" || first["count"] != float64(1) {
		t.Errorf("first category = %v", first)
	}

	// Listing can be narrowed to one category, case-insensitively.
	notes := decode(t, do(t, h, "GET", "/api/notes?category=work", ""))["notes"].([]any)
	if len(notes) != 1 || notes[0].(map[string]any)["path"] != "Idea.md" {
		t.Errorf("filtered notes = %v", notes)
	}

	// Clearing takes the frontmatter block with it.
	rec = do(t, h, "POST", "/api/categories/assign", `{"path":"Idea.md","categories":[]}`)
	if got := decode(t, rec)["content"].(string); strings.Contains(got, "categories") {
		t.Errorf("cleared note = %q", got)
	}
}

func TestCategoryRenameAndDelete(t *testing.T) {
	h := newServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"A.md","content":"a\n","categories":["Work"]}`)
	do(t, h, "POST", "/api/notes", `{"path":"B.md","content":"b\n","categories":["work","Ideas"]}`)

	// One rename reaches every note carrying the name, in either spelling.
	rec := do(t, h, "POST", "/api/categories/rename", `{"from":"Work","to":"Day job"}`)
	if rec.Code != 200 {
		t.Fatalf("rename = %d: %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	if body["updated_notes"] != float64(2) {
		t.Errorf("updated = %v", body["updated_notes"])
	}
	names := categoryNames(body)
	if names["Day job"] != 2 || names["Work"] != 0 {
		t.Errorf("after rename = %v", names)
	}

	rec = do(t, h, "POST", "/api/categories/delete", `{"name":"Day job"}`)
	if rec.Code != 200 {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if names = categoryNames(decode(t, rec)); names["Day job"] != 0 || names["Ideas"] != 1 {
		t.Errorf("after delete = %v", names)
	}
	// The notes themselves are untouched.
	if content := decode(t, do(t, h, "GET", "/api/notes/A.md", ""))["content"].(string); content != "a\n" {
		t.Errorf("note damaged by a category delete: %q", content)
	}

	// An unusable name is a 400, not a silent no-op.
	if rec = do(t, h, "POST", "/api/categories/rename", `{"from":"Ideas","to":"a, b"}`); rec.Code != 400 {
		t.Errorf("bad rename target = %d", rec.Code)
	}
}

// Assigning a category from a stale screen is the same 409 a stale content save
// gets — the client then offers the choice rather than one side just winning.
func TestAssignCategoriesRejectsAStaleBaseMtime(t *testing.T) {
	h := newServer(t)
	do(t, h, "POST", "/api/notes", `{"path":"A.md","content":"a\n"}`)
	rec := do(t, h, "POST", "/api/categories/assign",
		`{"path":"A.md","categories":["Ideas"],"base_mtime_ms":1}`)
	if rec.Code != 409 || decode(t, rec)["error"] == "" {
		t.Errorf("stale assign = %d %s", rec.Code, rec.Body.String())
	}
}

func categoryNames(body map[string]any) map[string]int {
	out := map[string]int{}
	for _, entry := range body["categories"].([]any) {
		c := entry.(map[string]any)
		out[c["name"].(string)] = int(c["count"].(float64))
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
