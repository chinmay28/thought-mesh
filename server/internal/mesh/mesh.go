// Package mesh derives the link structure of a vault: which notes link where,
// what links back, and the graph over all of it.
//
// Nothing here is persisted — the markdown files are the only source of truth
// (see internal/vault). The index is rebuilt from a stat-walk on every
// Snapshot() call, with per-file parses cached by (mtime, size), so it is
// always correct even when the vault is edited behind the server's back
// (git pull, Syncthing, a stray text editor) while costing only the reparse
// of files that actually changed.
//
// Link syntax is Obsidian's: [[Target]], [[Target|shown text]],
// [[Target#heading]]. A target with a "/" is resolved as a vault path;
// a bare name matches any note with that file name, case-insensitively,
// preferring the shortest path on a tie. Links inside fenced code blocks
// don't count.
package mesh

import (
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/chinmay28/thought-mesh/server/internal/vault"
)

// RawLink is one wikilink occurrence in a note's markdown.
type RawLink struct {
	Target  string // link text with alias/heading stripped, trimmed
	Line    int    // 1-based line number
	Snippet string // the line of text it occurred on, trimmed and capped
}

// Link is a raw link with its resolution: Path is "" when no note matches.
type Link struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Name   string `json:"name"`
}

// Backlink is one referring note, with the first mention's context.
type Backlink struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Snippet string `json:"snippet"`
	Count   int    `json:"count"`
}

// SearchResult is one note matched by a query.
type SearchResult struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Snippet string `json:"snippet"`
	Line    int    `json:"line"`
}

// GraphNode / GraphEdge form the whole-vault graph. Missing is 1 for a link
// target no note satisfies yet — the ghost nodes Obsidian also shows.
type GraphNode struct {
	ID      string `json:"id"` // note path, or the normalized target of a missing note
	Name    string `json:"name"`
	Missing int    `json:"missing"`
	LinksIn int    `json:"links_in"`
	Out     int    `json:"links_out"`
}

type GraphEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Mesh caches parsed links per file.
type Mesh struct {
	V *vault.Vault

	mu    sync.Mutex
	cache map[string]parsedFile
}

type parsedFile struct {
	mtimeMs int64
	size    int64
	links   []RawLink
}

func New(v *vault.Vault) *Mesh {
	return &Mesh{V: v, cache: map[string]parsedFile{}}
}

// wikiRe matches [[...]] with no nested brackets; the inner text is split on
// '#' (heading) and '|' (alias) by parseTarget.
var wikiRe = regexp.MustCompile(`\[\[([^\[\]]+)\]\]`)

// parseTarget reduces the inside of a [[...]] to the note it names.
// "Note#Heading|shown" → "Note". Empty when only a heading/alias was given.
func parseTarget(inner string) string {
	if i := strings.IndexByte(inner, '|'); i >= 0 {
		inner = inner[:i]
	}
	if i := strings.IndexByte(inner, '#'); i >= 0 {
		inner = inner[:i]
	}
	return strings.TrimSpace(inner)
}

const snippetMax = 200

// ParseLinks extracts the wikilinks from markdown, skipping fenced code
// blocks (``` / ~~~), whose contents are literal text.
func ParseLinks(content string) []RawLink {
	var out []RawLink
	inFence := false
	fence := ""
	for lineNo, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = true
			fence = trimmed[:3]
			continue
		}
		for _, m := range wikiRe.FindAllStringSubmatch(line, -1) {
			target := parseTarget(m[1])
			if target == "" {
				continue
			}
			snippet := trimmed
			if len(snippet) > snippetMax {
				snippet = snippet[:snippetMax]
			}
			out = append(out, RawLink{Target: target, Line: lineNo + 1, Snippet: snippet})
		}
	}
	return out
}

// Snapshot is one consistent view of the vault's link structure.
type Snapshot struct {
	Notes []vault.NoteInfo

	byPath  map[string]*vault.NoteInfo
	byLower map[string][]string // lower(name) -> paths, shortest first
	links   map[string][]RawLink
}

// Snapshot walks the vault and returns the current link structure.
func (m *Mesh) Snapshot() (*Snapshot, error) {
	notes, err := m.V.List()
	if err != nil {
		return nil, err
	}
	s := &Snapshot{
		Notes:   notes,
		byPath:  make(map[string]*vault.NoteInfo, len(notes)),
		byLower: make(map[string][]string, len(notes)),
		links:   make(map[string][]RawLink, len(notes)),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := make(map[string]bool, len(notes))
	for i := range notes {
		n := &notes[i]
		seen[n.Path] = true
		s.byPath[n.Path] = n
		lower := strings.ToLower(n.Name)
		s.byLower[lower] = append(s.byLower[lower], n.Path)

		if c, ok := m.cache[n.Path]; ok && c.mtimeMs == n.MtimeMs && c.size == n.Size {
			s.links[n.Path] = c.links
			continue
		}
		content, info, err := m.V.Read(n.Path)
		if err != nil {
			continue // deleted between walk and read — the next snapshot heals
		}
		links := ParseLinks(content)
		m.cache[n.Path] = parsedFile{mtimeMs: info.MtimeMs, size: info.Size, links: links}
		s.links[n.Path] = links
	}
	// Drop cache entries for notes that no longer exist.
	for p := range m.cache {
		if !seen[p] {
			delete(m.cache, p)
		}
	}
	// Shortest path wins a name tie; break remaining ties lexicographically.
	for _, paths := range s.byLower {
		sort.Slice(paths, func(i, j int) bool {
			if len(paths[i]) != len(paths[j]) {
				return len(paths[i]) < len(paths[j])
			}
			return paths[i] < paths[j]
		})
	}
	return s, nil
}

// Resolve maps a wikilink target to the path of the note it names, or "".
func (s *Snapshot) Resolve(target string) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	lower := strings.ToLower(strings.TrimSuffix(t, ".md"))
	lower = strings.TrimSuffix(lower, ".MD")
	if strings.Contains(lower, "/") {
		full := lower + ".md"
		for p := range s.byPath {
			if strings.ToLower(p) == full {
				return p
			}
		}
		// Fall through: Obsidian also resolves "dir/Name" by name when the
		// exact path misses. Use just the final segment.
		lower = lower[strings.LastIndex(lower, "/")+1:]
	}
	if paths := s.byLower[lower]; len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// Links returns a note's outgoing links, resolved and deduplicated (one entry
// per distinct target, in order of first appearance).
func (s *Snapshot) Links(path string) []Link {
	var out []Link
	seen := map[string]bool{}
	for _, raw := range s.links[path] {
		key := strings.ToLower(raw.Target)
		if seen[key] {
			continue
		}
		seen[key] = true
		link := Link{Target: raw.Target}
		if p := s.Resolve(raw.Target); p != "" {
			link.Path = p
			link.Name = s.byPath[p].Name
		}
		out = append(out, link)
	}
	return out
}

// Backlinks returns the notes that link to path, one entry per referrer with
// the first mention's snippet and the total mention count.
func (s *Snapshot) Backlinks(path string) []Backlink {
	var out []Backlink
	for _, src := range s.Notes {
		if src.Path == path {
			continue
		}
		var first *RawLink
		count := 0
		for i, raw := range s.links[src.Path] {
			if s.Resolve(raw.Target) == path {
				if first == nil {
					first = &s.links[src.Path][i]
				}
				count++
			}
		}
		if first != nil {
			out = append(out, Backlink{Path: src.Path, Name: src.Name, Snippet: first.Snippet, Count: count})
		}
	}
	return out
}

// Graph builds the whole-vault node/edge set, ghost nodes included.
func (s *Snapshot) Graph() ([]GraphNode, []GraphEdge) {
	nodes := make([]GraphNode, 0, len(s.Notes))
	index := map[string]int{} // node id -> index in nodes
	for _, n := range s.Notes {
		index[n.Path] = len(nodes)
		nodes = append(nodes, GraphNode{ID: n.Path, Name: n.Name})
	}
	var edges []GraphEdge
	edgeSeen := map[string]bool{}
	for _, src := range s.Notes {
		for _, raw := range s.links[src.Path] {
			to := s.Resolve(raw.Target)
			if to == "" {
				// Ghost node for a note that doesn't exist yet, keyed by the
				// normalized target so [[Idea]] and [[idea]] are one node.
				ghost := "missing:" + strings.ToLower(raw.Target)
				if _, ok := index[ghost]; !ok {
					index[ghost] = len(nodes)
					nodes = append(nodes, GraphNode{ID: ghost, Name: strings.TrimSpace(raw.Target), Missing: 1})
				}
				to = ghost
			}
			if to == src.Path {
				continue // self-links don't draw
			}
			key := src.Path + "\x00" + to
			if edgeSeen[key] {
				continue
			}
			edgeSeen[key] = true
			edges = append(edges, GraphEdge{From: src.Path, To: to})
			nodes[index[src.Path]].Out++
			nodes[index[to]].LinksIn++
		}
	}
	return nodes, edges
}

const maxSearchResults = 100

// Search finds notes whose name or content contains q (case-insensitive).
// Name matches rank first; content matches carry the first matching line.
func (m *Mesh) Search(q string) ([]SearchResult, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return []SearchResult{}, nil
	}
	lower := strings.ToLower(q)
	notes, err := m.V.List()
	if err != nil {
		return nil, err
	}
	var nameHits, contentHits []SearchResult
	for _, n := range notes {
		if strings.Contains(strings.ToLower(n.Name), lower) {
			nameHits = append(nameHits, SearchResult{Path: n.Path, Name: n.Name})
			continue
		}
		content, _, err := m.V.Read(n.Path)
		if err != nil {
			continue
		}
		if idx := strings.Index(strings.ToLower(content), lower); idx >= 0 {
			lineNo := strings.Count(content[:idx], "\n") + 1
			lineStart := strings.LastIndexByte(content[:idx], '\n') + 1
			lineEnd := len(content)
			if i := strings.IndexByte(content[idx:], '\n'); i >= 0 {
				lineEnd = idx + i
			}
			snippet := strings.TrimSpace(content[lineStart:lineEnd])
			if len(snippet) > snippetMax {
				snippet = snippet[:snippetMax]
			}
			contentHits = append(contentHits, SearchResult{Path: n.Path, Name: n.Name, Snippet: snippet, Line: lineNo})
		}
	}
	out := append(nameHits, contentHits...)
	if len(out) > maxSearchResults {
		out = out[:maxSearchResults]
	}
	return out, nil
}

// RewriteLinks updates every wikilink in content that resolves (under s) to
// oldPath so it names newName instead, preserving aliases and headings.
// Fenced code blocks are left alone, mirroring ParseLinks. Returns the new
// content and whether anything changed.
//
// The replacement target is the note's plain new name when that name is
// unambiguous after the rename; callers pass newTarget accordingly (name or
// full path without .md).
func RewriteLinks(content string, resolves func(target string) bool, newTarget string) (string, bool) {
	changed := false
	lines := strings.Split(content, "\n")
	inFence := false
	fence := ""
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inFence {
			if strings.HasPrefix(trimmed, fence) {
				inFence = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = true
			fence = trimmed[:3]
			continue
		}
		lines[i] = wikiRe.ReplaceAllStringFunc(line, func(match string) string {
			inner := match[2 : len(match)-2]
			target := parseTarget(inner)
			if target == "" || !resolves(target) {
				return match
			}
			// Keep everything after the target (heading, alias) intact.
			rest := inner[strings.Index(inner, target)+len(target):]
			changed = true
			return "[[" + newTarget + rest + "]]"
		})
	}
	if !changed {
		return content, false
	}
	return strings.Join(lines, "\n"), true
}
