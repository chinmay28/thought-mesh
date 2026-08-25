// Package merge is the third answer to a conflict.
//
// Everywhere two versions of a note collide — a save against a file that moved
// underneath the editor, or a cloud sync where both sides changed since they
// last agreed — the user is offered three ways out: keep mine, take theirs, or
// merge. The first two are trivial. This package is the third.
//
// It is a line-level diff3: given the common ancestor both sides started from,
// changes that touch different regions are applied together silently, and only
// regions both sides rewrote come back as conflict hunks marked the way every
// merge tool marks them:
//
//	<<<<<<< mine
//	the local version
//	=======
//	the version from the other side
//	>>>>>>> theirs
//
// Markers rather than a silent pick, because a merge that guesses is worse than
// one that asks: the result lands in the editor, where the person who wrote
// both halves can see them side by side and delete the half they don't want.
//
// With no ancestor available (a file that appeared on both sides
// independently, or a base version we no longer hold) the merge degrades to a
// two-way one: shared leading and trailing lines are kept, and the middle is
// one conflict hunk. That is honest — without a base, nothing can tell an
// addition from a deletion.
package merge

import "strings"

// Labels name the two sides in the conflict markers. They are part of what the
// user reads, so they say whose text it is rather than "ours"/"theirs".
const (
	LabelMine   = "mine"
	LabelTheirs = "theirs"
)

// Result is a merged document and whether anything in it needs a human.
type Result struct {
	// Text is the merged content, conflict markers included.
	Text string
	// Conflicts is how many regions both sides rewrote.
	Conflicts int
}

// Clean reports whether the merge came out without a conflict hunk.
func (r Result) Clean() bool { return r.Conflicts == 0 }

// Merge combines two versions of a text that both descend from base.
//
// Pass hasBase = false when there is no common ancestor to reason from; base is
// then ignored and the two versions are reconciled on their shared prefix and
// suffix alone.
func Merge(base, mine, theirs string, hasBase bool) Result {
	if mine == theirs {
		return Result{Text: mine}
	}
	if hasBase {
		if base == mine {
			return Result{Text: theirs} // only the other side moved
		}
		if base == theirs {
			return Result{Text: mine} // only we moved
		}
	}

	// Merging is line-oriented, and the trailing newline every markdown file
	// carries would otherwise show up as a final empty line in every hunk.
	// Split it off, merge the lines, and put back whichever ending survived.
	baseLines, mineLines, theirLines := lines(base), lines(mine), lines(theirs)
	var merged []string
	var conflicts int
	if hasBase {
		merged, conflicts = mergeLines(baseLines, mineLines, theirLines)
	} else {
		merged, conflicts = mergeWithoutBase(mineLines, theirLines)
	}

	text := strings.Join(merged, "\n")
	// A merged note keeps the trailing newline if either side had one — the
	// convention for a markdown file, and a merge is not the place to drop it.
	if strings.HasSuffix(mine, "\n") || strings.HasSuffix(theirs, "\n") {
		text += "\n"
	}
	return Result{Text: text, Conflicts: conflicts}
}

// lines splits a document for merging, dropping the trailing newline's empty
// final element so it doesn't participate as a line of its own.
func lines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{""}
	}
	return strings.Split(s, "\n")
}

// mergeLines is the diff3 walk.
//
// Each side is diffed against the base, giving a list of changes in *base*
// coordinates. Two changes collide only when the stretches of base they rewrite
// actually overlap; anything else is two people editing different parts of the
// same note, and both edits are kept.
//
// Working in base coordinates is the whole trick. Looking instead for lines all
// three versions still share would call this a conflict —
//
//	base    line one            / line two
//	mine    line one EDITED     / line two
//	theirs  line one            / line two EDITED
//
// — because between them the two sides have rewritten every line, leaving no
// common anchor, even though neither touched what the other did.
func mergeLines(base, mine, theirs []string) ([]string, int) {
	ours, theirsChanges := changes(base, mine), changes(base, theirs)
	var out []string
	conflicts := 0
	pos, i, j := 0, 0, 0

	for i < len(ours) || j < len(theirsChanges) {
		// Open a region at whichever side's next change starts earliest…
		startI, startJ := i, j
		var from, to int
		if j >= len(theirsChanges) ||
			(i < len(ours) && ours[i].start <= theirsChanges[j].start) {
			from, to = ours[i].start, ours[i].end
			i++
		} else {
			from, to = theirsChanges[j].start, theirsChanges[j].end
			j++
		}
		// …then pull in every further change that overlaps it, from either
		// side, until the region stops growing. What comes out is the smallest
		// stretch of base that can be decided on its own.
		for {
			grew := false
			if i < len(ours) && overlaps(from, to, ours[i].start, ours[i].end) {
				if ours[i].end > to {
					to = ours[i].end
				}
				i++
				grew = true
			}
			if j < len(theirsChanges) &&
				overlaps(from, to, theirsChanges[j].start, theirsChanges[j].end) {
				if theirsChanges[j].end > to {
					to = theirsChanges[j].end
				}
				j++
				grew = true
			}
			if !grew {
				break
			}
		}

		out = append(out, base[pos:from]...)
		untouched := base[from:to]
		mineRegion := applyChanges(base, ours[startI:i], from, to)
		theirRegion := applyChanges(base, theirsChanges[startJ:j], from, to)
		switch {
		case equal(mineRegion, theirRegion):
			out = append(out, mineRegion...) // both sides made the same edit
		case equal(mineRegion, untouched):
			out = append(out, theirRegion...) // only the other side touched this
		case equal(theirRegion, untouched):
			out = append(out, mineRegion...) // only we touched this
		default:
			out = append(out, conflictHunk(mineRegion, theirRegion)...)
			conflicts++
		}
		pos = to
	}
	return append(out, base[pos:]...), conflicts
}

// change is one stretch of base a side rewrote: base[start:end) became `lines`.
// An empty range is an insertion, an empty `lines` a deletion.
type change struct {
	start, end int
	lines      []string
}

// changes diffs a version against the base, in base coordinates.
func changes(base, other []string) []change {
	var out []change
	b, o := 0, 0
	for _, m := range lcs(base, other) {
		if m.i > b || m.j > o {
			out = append(out, change{start: b, end: m.i, lines: other[o:m.j]})
		}
		b, o = m.i+1, m.j+1
	}
	if b < len(base) || o < len(other) {
		out = append(out, change{start: b, end: len(base), lines: other[o:]})
	}
	return out
}

// applyChanges reconstructs one side's text over base[from:to).
func applyChanges(base []string, group []change, from, to int) []string {
	out := make([]string, 0, to-from)
	pos := from
	for _, c := range group {
		out = append(out, base[pos:c.start]...)
		out = append(out, c.lines...)
		pos = c.end
	}
	return append(out, base[pos:to]...)
}

// overlaps reports whether two rewritten stretches of base collide.
//
// Empty ranges are insertions and need their own reading: two insertions at the
// same point collide (both sides added something there, and only a human can
// say in which order), but an insertion at the edge of a replacement does not —
// it lands cleanly before or after it.
func overlaps(s1, e1, s2, e2 int) bool {
	if s1 == e1 && s2 == e2 {
		return s1 == s2
	}
	if s1 == e1 {
		return s1 > s2 && s1 < e2
	}
	if s2 == e2 {
		return s2 > s1 && s2 < e1
	}
	return s1 < e2 && s2 < e1
}

// mergeWithoutBase is the two-way fallback: keep the lines the two versions
// share at the start and at the end, and hand the middle to the user as one
// conflict. Nothing else is safe — with no ancestor, a line present on one side
// only is equally likely to be an addition there or a deletion here.
func mergeWithoutBase(mine, theirs []string) ([]string, int) {
	prefix := 0
	for prefix < len(mine) && prefix < len(theirs) && mine[prefix] == theirs[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(mine)-prefix && suffix < len(theirs)-prefix &&
		mine[len(mine)-1-suffix] == theirs[len(theirs)-1-suffix] {
		suffix++
	}
	middleMine := mine[prefix : len(mine)-suffix]
	middleTheirs := theirs[prefix : len(theirs)-suffix]

	out := append([]string{}, mine[:prefix]...)
	conflicts := 0
	switch {
	case len(middleMine) == 0 && len(middleTheirs) == 0:
		// Nothing between the shared ends: the versions were identical.
	case len(middleMine) == 0:
		out = append(out, middleTheirs...)
	case len(middleTheirs) == 0:
		out = append(out, middleMine...)
	default:
		out = append(out, conflictHunk(middleMine, middleTheirs)...)
		conflicts++
	}
	out = append(out, mine[len(mine)-suffix:]...)
	return out, conflicts
}

// conflictHunk renders one unresolved region in the conventional marker form.
func conflictHunk(mine, theirs []string) []string {
	out := make([]string, 0, len(mine)+len(theirs)+3)
	out = append(out, "<<<<<<< "+LabelMine)
	out = append(out, mine...)
	out = append(out, "=======")
	out = append(out, theirs...)
	out = append(out, ">>>>>>> "+LabelTheirs)
	return out
}

// HasConflictMarkers reports whether text still carries an unresolved hunk —
// what the UI checks before letting a merged draft be saved without comment.
func HasConflictMarkers(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "<<<<<<< ") || strings.HasPrefix(line, ">>>>>>> ") {
			return true
		}
	}
	return false
}

// --- diff primitives ----------------------------------------------------------

// pair is one aligned line: index i in the first sequence, j in the second.
type pair struct{ i, j int }

// maxLCSCells bounds the dynamic-programming table. Past it the quadratic
// table would cost more memory than a note merge can justify, and the merge
// falls back to matching only the shared prefix and suffix — the same
// degradation as having no base at all, and on documents that large the diff
// would be unreadable anyway.
const maxLCSCells = 4 << 20 // ~4M cells

// lcs returns the longest common subsequence of a and b as aligned index
// pairs, in order.
func lcs(a, b []string) []pair {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	// Common ends are matched directly; only the differing middle needs the
	// table, which is what keeps an edit to one line of a long note cheap.
	head := 0
	for head < len(a) && head < len(b) && a[head] == b[head] {
		head++
	}
	tail := 0
	for tail < len(a)-head && tail < len(b)-head &&
		a[len(a)-1-tail] == b[len(b)-1-tail] {
		tail++
	}
	out := make([]pair, 0, head+tail)
	for i := 0; i < head; i++ {
		out = append(out, pair{i, i})
	}
	midA, midB := a[head:len(a)-tail], b[head:len(b)-tail]
	if len(midA) > 0 && len(midB) > 0 && len(midA)*len(midB) <= maxLCSCells {
		for _, p := range lcsTable(midA, midB) {
			out = append(out, pair{p.i + head, p.j + head})
		}
	}
	for i := 0; i < tail; i++ {
		out = append(out, pair{len(a) - tail + i, len(b) - tail + i})
	}
	return out
}

// lcsTable is the textbook O(n·m) longest-common-subsequence walk.
func lcsTable(a, b []string) []pair {
	rows, cols := len(a)+1, len(b)+1
	table := make([]int, rows*cols)
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				table[i*cols+j] = table[(i+1)*cols+j+1] + 1
				continue
			}
			if table[(i+1)*cols+j] >= table[i*cols+j+1] {
				table[i*cols+j] = table[(i+1)*cols+j]
			} else {
				table[i*cols+j] = table[i*cols+j+1]
			}
		}
	}
	var out []pair
	for i, j := 0, 0; i < len(a) && j < len(b); {
		switch {
		case a[i] == b[j]:
			out = append(out, pair{i, j})
			i, j = i+1, j+1
		case table[(i+1)*cols+j] >= table[i*cols+j+1]:
			i++
		default:
			j++
		}
	}
	return out
}

// matchMap turns aligned pairs into a lookup from the first sequence's index.
func matchMap(pairs []pair) map[int]int {
	out := make(map[int]int, len(pairs))
	for _, p := range pairs {
		out[p.i] = p.j
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
