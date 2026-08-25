package vault

import (
	"fmt"
	"strings"
)

// Categories are the one piece of note metadata Thought Mesh writes, and they
// live where every other markdown tool expects them: a YAML frontmatter block
// at the top of the file.
//
//	---
//	categories: [Ideas, Work]
//	---
//
//	# The note itself
//
// That placement is not a detail — it is the whole "the files ARE the data"
// rule applied to metadata. A sidecar index would make the vault stop being an
// ordinary folder the moment it left this server, and a category invisible in
// Obsidian (or in `grep`) would not really be the note's.
//
// The parser here is deliberately tiny rather than a YAML dependency: it reads
// and rewrites exactly one key and copies every other line through untouched,
// so frontmatter written by Obsidian, Jekyll or a person keeps working and
// keeps its formatting. What it will not do is reformat, reorder, or attempt
// to understand the rest of the block.

// Frontmatter limits. Generous for real use, present so a paste accident
// can't put something unmanageable at the top of a note.
const (
	// MaxCategoryLen bounds one category name.
	MaxCategoryLen = 80
	// MaxCategories bounds how many one note may carry.
	MaxCategories = 24
	// maxFrontmatterLines bounds how far we look for the closing fence
	// before deciding a leading "---" was a horizontal rule after all.
	maxFrontmatterLines = 200
)

// forbiddenCategoryChars would break the one-line flow sequence categories are
// written as (`[a, b]`), or the `key: value` line itself. Rejecting them at the
// boundary keeps the writer honest instead of producing frontmatter that only
// this parser can read back.
const forbiddenCategoryChars = `[]{},:#"'|` + "`"

// categoriesKey is the frontmatter key Thought Mesh owns. `category` is
// accepted on read as a courtesy to vaults that already use the singular, and
// is rewritten to the plural on the first change.
const categoriesKey = "categories"

// SplitFrontmatter separates a leading YAML frontmatter block from the note
// body. `ok` is false when there is no block, in which case `body` is the
// whole content and `fm` is empty.
//
// A block must open on the very first line with `---` and close with `---` or
// `...` on a line of its own. A leading `---` with no closing fence is a
// horizontal rule, not frontmatter, and is left in the body.
func SplitFrontmatter(content string) (fm []string, body string, ok bool) {
	// Tolerate a UTF-8 BOM: an editor may have added one, and it would
	// otherwise hide the opening fence.
	rest := strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(rest, "---") {
		return nil, content, false
	}
	lines := strings.Split(rest, "\n")
	if strings.TrimRight(lines[0], " \t\r") != "---" {
		return nil, content, false
	}
	limit := len(lines)
	if limit > maxFrontmatterLines+1 {
		limit = maxFrontmatterLines + 1
	}
	for i := 1; i < limit; i++ {
		switch strings.TrimRight(lines[i], " \t\r") {
		case "---", "...":
			return lines[1:i], strings.Join(lines[i+1:], "\n"), true
		}
	}
	return nil, content, false
}

// Categories reads a note's categories from its frontmatter, in the order they
// appear. Both the flow form (`categories: [a, b]`) and the block form
// (`categories:` then `- a`) are understood, as is the singular `category:`.
// A note without frontmatter has no categories, which is not an error.
func Categories(content string) []string {
	fm, _, ok := SplitFrontmatter(content)
	if !ok {
		return nil
	}
	start, end, inline := findCategoriesEntry(fm)
	if start < 0 {
		return nil
	}
	var raw []string
	if inline != "" {
		raw = splitFlowSequence(inline)
	}
	for _, line := range fm[start+1 : end] {
		item := strings.TrimSpace(line)
		if !strings.HasPrefix(item, "-") {
			continue
		}
		raw = append(raw, strings.TrimSpace(strings.TrimPrefix(item, "-")))
	}
	return dedupeCategories(raw)
}

// findCategoriesEntry locates the categories key in a frontmatter block. It
// returns the index of the key line, the index one past the entry's last line
// (its block-list items included), and whatever followed the colon on the key
// line. A missing key reports start = -1.
func findCategoriesEntry(fm []string) (start, end int, inline string) {
	for i, line := range fm {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		// Only a top-level key counts: an indented `categories:` belongs to
		// some other tool's nested structure, and is none of our business.
		if key != strings.TrimLeft(key, " \t") {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(key))
		if name != categoriesKey && name != "category" {
			continue
		}
		end = i + 1
		for end < len(fm) {
			item := strings.TrimSpace(fm[end])
			// Blank lines and comments inside a block list are still part of
			// the entry; a new key ends it.
			if strings.HasPrefix(item, "-") || item == "" || strings.HasPrefix(item, "#") {
				end++
				continue
			}
			break
		}
		// Trailing blank/comment lines belong to whatever follows, not to us.
		for end > i+1 && !strings.HasPrefix(strings.TrimSpace(fm[end-1]), "-") {
			end--
		}
		return i, end, strings.TrimSpace(value)
	}
	return -1, 0, ""
}

// splitFlowSequence reads `[a, b]` — or a bare `a` for the singular key — into
// its items. Quotes are stripped so frontmatter written by another tool still
// reads back sensibly.
func splitFlowSequence(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if inner, ok := strings.CutPrefix(value, "["); ok {
		value = strings.TrimSuffix(inner, "]")
	}
	var out []string
	for _, part := range strings.Split(value, ",") {
		out = append(out, strings.Trim(strings.TrimSpace(part), `"'`))
	}
	return out
}

// NormalizeCategory validates and canonicalizes one category name: whitespace
// collapsed, ends trimmed. It is deliberately liberal about the alphabet —
// categories are the user's vocabulary, not ours — and strict only about what
// would produce frontmatter that can't be read back.
func NormalizeCategory(name string) (string, error) {
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		return "", &ValidationError{"category name is empty"}
	}
	if len(name) > MaxCategoryLen {
		return "", &ValidationError{
			fmt.Sprintf("category name longer than %d characters", MaxCategoryLen)}
	}
	if strings.ContainsAny(name, forbiddenCategoryChars) {
		return "", &ValidationError{
			fmt.Sprintf("category name must not contain any of %s", forbiddenCategoryChars)}
	}
	// Whitespace (newlines included) was collapsed above; anything still in the
	// control range is a genuinely unusable byte.
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "", &ValidationError{"category name contains a control character"}
		}
	}
	// A leading "-" would read as a list item, and a leading "?" or "&" as
	// YAML syntax, once the name is written back out.
	if strings.ContainsAny(name[:1], "-?&*!%@>") {
		return "", &ValidationError{"category name must not start with " + name[:1]}
	}
	return name, nil
}

// NormalizeCategories validates a whole set, dropping blanks and duplicates
// (case-insensitively, first spelling wins) while keeping the caller's order.
func NormalizeCategories(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	seen := map[string]bool{}
	for _, raw := range names {
		if strings.TrimSpace(raw) == "" {
			continue // an empty chip is a deletion, not an error
		}
		name, err := NormalizeCategory(raw)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
	}
	if len(out) > MaxCategories {
		return nil, &ValidationError{
			fmt.Sprintf("a note may carry at most %d categories", MaxCategories)}
	}
	return out, nil
}

// dedupeCategories is NormalizeCategories for values already on disk: anything
// unusable is dropped rather than rejected, because a note someone else wrote
// must still open.
func dedupeCategories(raw []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, item := range raw {
		name, err := NormalizeCategory(item)
		if err != nil {
			continue
		}
		key := strings.ToLower(name)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, name)
		if len(out) == MaxCategories {
			break
		}
	}
	return out
}

// WithCategories returns content with its categories set to cats, creating,
// rewriting or removing the frontmatter block as needed. Every other
// frontmatter line is copied through byte for byte — this rewrites one key, it
// does not reformat someone else's metadata.
//
// An empty cats removes the key, and removes the whole block when nothing else
// was in it, so clearing the last category leaves a plain markdown file rather
// than an empty `---` sandwich.
func WithCategories(content string, cats []string) string {
	fm, body, hadBlock := SplitFrontmatter(content)
	next := make([]string, 0, len(fm)+1)
	if start, end, _ := findCategoriesEntry(fm); start >= 0 {
		next = append(next, fm[:start]...)
		if len(cats) > 0 {
			next = append(next, categoriesLine(cats))
		}
		next = append(next, fm[end:]...)
	} else {
		next = append(next, fm...)
		if len(cats) > 0 {
			// A new key goes first: it's the one this app manages, and the top
			// of the block is where a reader looks for it.
			next = append([]string{categoriesLine(cats)}, next...)
		}
	}

	if len(next) == 0 {
		if !hadBlock {
			return content
		}
		// The block existed only to hold categories — drop it, and the blank
		// line it was separated from the body by.
		return strings.TrimPrefix(body, "\n")
	}
	block := "---\n" + strings.Join(next, "\n") + "\n---\n"
	if hadBlock {
		return block + body
	}
	if strings.TrimSpace(body) == "" {
		return block
	}
	// A blank line between the block and the body, unless the body opens with
	// one already.
	if strings.HasPrefix(body, "\n") {
		return block + body
	}
	return block + "\n" + body
}

// categoriesLine renders the key in the flow form — one line, which is what
// keeps a note's frontmatter from growing a screen tall in the editor.
func categoriesLine(cats []string) string {
	return categoriesKey + ": [" + strings.Join(cats, ", ") + "]"
}
