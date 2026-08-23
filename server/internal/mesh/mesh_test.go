package mesh

import (
	"testing"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

func newMesh(t *testing.T) *Mesh {
	t.Helper()
	v, err := vault.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return New(v)
}

func put(t *testing.T, m *Mesh, path, content string) {
	t.Helper()
	if _, err := m.V.Write(path, content); err != nil {
		t.Fatalf("Write(%q): %v", path, err)
	}
}

func TestParseLinks(t *testing.T) {
	content := "# Title\n" +
		"See [[Alpha]] and [[beta|the beta note]] plus [[Gamma#Section]].\n" +
		"```\nnot a link: [[Ignored]]\n```\n" +
		"[[Alpha]] again, and [[#just-a-heading]] which names no note.\n"
	links := ParseLinks(content)
	var targets []string
	for _, l := range links {
		targets = append(targets, l.Target)
	}
	want := []string{"Alpha", "beta", "Gamma", "Alpha"}
	if len(targets) != len(want) {
		t.Fatalf("targets = %v; want %v", targets, want)
	}
	for i := range want {
		if targets[i] != want[i] {
			t.Fatalf("targets = %v; want %v", targets, want)
		}
	}
	if links[0].Line != 2 || links[0].Snippet == "" {
		t.Errorf("first link = %+v", links[0])
	}
}

func TestResolveAndBacklinks(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Alpha.md", "links to [[Beta]] and [[deep/Gamma]] and [[Nowhere]]")
	put(t, m, "Beta.md", "back to [[alpha]]") // case-insensitive
	put(t, m, "deep/Gamma.md", "mentions [[Beta]] twice: [[Beta|again]]")
	put(t, m, "other/Beta.md", "a second Beta — the shorter path should win")

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	if got := snap.Resolve("beta"); got != "Beta.md" {
		t.Errorf("Resolve(beta) = %q; want Beta.md (shortest path wins)", got)
	}
	if got := snap.Resolve("other/Beta"); got != "other/Beta.md" {
		t.Errorf("Resolve(other/Beta) = %q", got)
	}
	if got := snap.Resolve("deep/Gamma"); got != "deep/Gamma.md" {
		t.Errorf("Resolve(deep/Gamma) = %q", got)
	}
	if got := snap.Resolve("Nowhere"); got != "" {
		t.Errorf("Resolve(Nowhere) = %q; want unresolved", got)
	}

	links := snap.Links("Alpha.md")
	if len(links) != 3 {
		t.Fatalf("Links(Alpha) = %+v", links)
	}
	if links[0].Path != "Beta.md" || links[1].Path != "deep/Gamma.md" || links[2].Path != "" {
		t.Errorf("Links(Alpha) = %+v", links)
	}

	backs := snap.Backlinks("Beta.md")
	if len(backs) != 2 {
		t.Fatalf("Backlinks(Beta) = %+v", backs)
	}
	// Sorted by note path: Alpha.md then deep/Gamma.md.
	if backs[0].Path != "Alpha.md" || backs[0].Count != 1 {
		t.Errorf("backlink[0] = %+v", backs[0])
	}
	if backs[1].Path != "deep/Gamma.md" || backs[1].Count != 2 {
		t.Errorf("backlink[1] = %+v", backs[1])
	}
}

func TestSnapshotCacheInvalidation(t *testing.T) {
	m := newMesh(t)
	put(t, m, "A.md", "[[B]]")
	put(t, m, "B.md", "")
	if _, err := m.Snapshot(); err != nil {
		t.Fatal(err)
	}
	// Rewrite A so it no longer links; the next snapshot must see it.
	put(t, m, "A.md", "no links now")
	snap, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got := snap.Backlinks("B.md"); len(got) != 0 {
		t.Errorf("stale backlinks after rewrite: %+v", got)
	}
}

func TestGraph(t *testing.T) {
	m := newMesh(t)
	put(t, m, "A.md", "[[B]] [[B]] [[Ghost]]")
	put(t, m, "B.md", "[[A]]")

	snap, err := m.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	nodes, edges := snap.Graph()
	if len(nodes) != 3 {
		t.Fatalf("nodes = %+v", nodes)
	}
	if len(edges) != 3 { // A→B (deduped), A→Ghost, B→A
		t.Fatalf("edges = %+v", edges)
	}
	var ghost *GraphNode
	for i := range nodes {
		if nodes[i].Missing == 1 {
			ghost = &nodes[i]
		}
	}
	if ghost == nil || ghost.Name != "Ghost" || ghost.LinksIn != 1 {
		t.Errorf("ghost node = %+v", ghost)
	}
}

func TestSearch(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Recipes.md", "# Recipes\nchocolate cake\n")
	put(t, m, "journal/Monday.md", "ate too much chocolate today")

	results, err := m.Search("chocolate")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v", results)
	}
	// Name match would rank first, but neither name matches here; both are
	// content hits with snippets.
	for _, r := range results {
		if r.Snippet == "" || r.Line == 0 {
			t.Errorf("content hit missing snippet/line: %+v", r)
		}
	}

	byName, err := m.Search("recip")
	if err != nil {
		t.Fatal(err)
	}
	if len(byName) != 1 || byName[0].Path != "Recipes.md" || byName[0].Snippet != "" {
		t.Errorf("name search = %+v", byName)
	}

	empty, err := m.Search("   ")
	if err != nil || len(empty) != 0 {
		t.Errorf("blank search = %+v, %v", empty, err)
	}
}

func TestRenameRewritesLinks(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Hub.md", "see [[Old Name]] and [[Old Name|alias]] and [[Old Name#part]]\n```\n[[Old Name]] in code stays\n```\n")
	put(t, m, "Other.md", "unrelated [[Hub]]")
	put(t, m, "Old Name.md", "self ref: [[Old Name]]")

	info, updated, err := m.Rename("Old Name.md", "New Name.md")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if info.Path != "New Name.md" {
		t.Errorf("renamed to %q", info.Path)
	}
	if updated != 2 { // Hub.md and the note's own self-link
		t.Errorf("updated = %d; want 2", updated)
	}

	hub, _, _ := m.V.Read("Hub.md")
	want := "see [[New Name]] and [[New Name|alias]] and [[New Name#part]]\n```\n[[Old Name]] in code stays\n```\n"
	if hub != want {
		t.Errorf("Hub.md = %q\nwant     %q", hub, want)
	}
	self, _, _ := m.V.Read("New Name.md")
	if self != "self ref: [[New Name]]" {
		t.Errorf("self link = %q", self)
	}
	other, _, _ := m.V.Read("Other.md")
	if other != "unrelated [[Hub]]" {
		t.Errorf("Other.md was touched: %q", other)
	}
}

func TestRenameIntoFolderUsesPathWhenAmbiguous(t *testing.T) {
	m := newMesh(t)
	put(t, m, "Ref.md", "[[Target]]")
	put(t, m, "Target.md", "")
	put(t, m, "s/Clash.md", "")

	// Rename Target → clash with an existing name elsewhere: links must use
	// the full path so they still resolve to the moved note.
	if _, _, err := m.Rename("Target.md", "deep/Clash.md"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	ref, _, _ := m.V.Read("Ref.md")
	if ref != "[[deep/Clash]]" {
		t.Errorf("Ref.md = %q; want [[deep/Clash]]", ref)
	}
}
