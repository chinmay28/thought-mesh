package merge

import (
	"strings"
	"testing"
)

func TestMergeIdenticalSides(t *testing.T) {
	r := Merge("a\n", "b\n", "b\n", true)
	if r.Text != "b\n" || !r.Clean() {
		t.Fatalf("merge = %q, %d conflicts", r.Text, r.Conflicts)
	}
}

func TestMergeOneSideUnchanged(t *testing.T) {
	base := "one\ntwo\n"
	if r := Merge(base, base, "one\ntwo\nthree\n", true); r.Text != "one\ntwo\nthree\n" || !r.Clean() {
		t.Errorf("theirs-only = %q, %d conflicts", r.Text, r.Conflicts)
	}
	if r := Merge(base, "zero\none\ntwo\n", base, true); r.Text != "zero\none\ntwo\n" || !r.Clean() {
		t.Errorf("mine-only = %q, %d conflicts", r.Text, r.Conflicts)
	}
}

// The whole point of a three-way merge: edits in different places both land,
// with nobody asked to choose.
func TestMergeDisjointEditsCombine(t *testing.T) {
	base := "title\n\nbody\n\nfooter\n"
	mine := "title (mine)\n\nbody\n\nfooter\n"
	theirs := "title\n\nbody\n\nfooter (theirs)\n"
	r := Merge(base, mine, theirs, true)
	if !r.Clean() {
		t.Fatalf("expected a clean merge, got %d conflicts:\n%s", r.Conflicts, r.Text)
	}
	if r.Text != "title (mine)\n\nbody\n\nfooter (theirs)\n" {
		t.Fatalf("merge = %q", r.Text)
	}
}

// Every line of the base is rewritten — line 1 here, line 2 there — so the
// three versions share no line at all. That is still two people editing
// different things, and a merge that looked for a common anchor would wrongly
// call it a conflict.
func TestMergeAdjacentLinesEditedByDifferentSides(t *testing.T) {
	base := "line one\nline two\n"
	mine := "line one edited here\nline two\n"
	theirs := "line one\nline two edited there\n"
	r := Merge(base, mine, theirs, true)
	if !r.Clean() {
		t.Fatalf("expected clean, got %d conflicts:\n%s", r.Conflicts, r.Text)
	}
	if want := "line one edited here\nline two edited there\n"; r.Text != want {
		t.Fatalf("merge = %q; want %q", r.Text, want)
	}
}

// Both sides rewrote the *same* line, so there is nothing to reconcile.
func TestMergeSameLineEditedByBothSidesConflicts(t *testing.T) {
	r := Merge("one\ntwo\n", "one mine\ntwo\n", "one theirs\ntwo\n", true)
	if r.Conflicts != 1 {
		t.Fatalf("expected one conflict, got %d:\n%s", r.Conflicts, r.Text)
	}
	if !strings.HasSuffix(r.Text, "two\n") {
		t.Errorf("the untouched line should survive outside the hunk:\n%s", r.Text)
	}
}

// A deletion on one side and an untouched region on the other still applies.
func TestMergeDeletionOnOneSide(t *testing.T) {
	r := Merge("a\nb\nc\n", "a\nc\n", "a\nb\nc\nd\n", true)
	if !r.Clean() {
		t.Fatalf("expected clean, got %d conflicts:\n%s", r.Conflicts, r.Text)
	}
	if r.Text != "a\nc\nd\n" {
		t.Fatalf("merge = %q", r.Text)
	}
}

func TestMergeBothAddDifferentLinesAtTheEnd(t *testing.T) {
	r := Merge("a\n", "a\nmine\n", "a\ntheirs\n", true)
	if r.Conflicts != 1 {
		t.Fatalf("expected one conflict, got %d:\n%s", r.Conflicts, r.Text)
	}
	want := "a\n<<<<<<< mine\nmine\n=======\ntheirs\n>>>>>>> theirs\n"
	if r.Text != want {
		t.Fatalf("merge =\n%q\nwant\n%q", r.Text, want)
	}
}

// A region only one side rewrote is taken from that side even when the other
// side changed elsewhere in the same document.
func TestMergeOverlappingButSeparableEdits(t *testing.T) {
	base := "1\n2\n3\n4\n5\n"
	mine := "1\n2 mine\n3\n4\n5\n"
	theirs := "1\n2\n3\n4 theirs\n5\n"
	r := Merge(base, mine, theirs, true)
	if !r.Clean() {
		t.Fatalf("expected clean, got %d conflicts:\n%s", r.Conflicts, r.Text)
	}
	if r.Text != "1\n2 mine\n3\n4 theirs\n5\n" {
		t.Fatalf("merge = %q", r.Text)
	}
}

// Without a base nothing can distinguish an addition from a deletion, so the
// shared ends survive and the middle comes back for a human.
func TestMergeWithoutBaseKeepsSharedEnds(t *testing.T) {
	r := Merge("", "head\nmine\ntail\n", "head\ntheirs\ntail\n", false)
	if r.Conflicts != 1 {
		t.Fatalf("expected one conflict, got %d:\n%s", r.Conflicts, r.Text)
	}
	if !strings.HasPrefix(r.Text, "head\n<<<<<<< mine\n") || !strings.HasSuffix(r.Text, ">>>>>>> theirs\ntail\n") {
		t.Fatalf("merge =\n%s", r.Text)
	}
}

// One side empty is the "it only exists over there" case: take the other side
// whole rather than wrapping it in markers.
func TestMergeWithoutBaseOneSideEmpty(t *testing.T) {
	r := Merge("", "", "theirs\n", false)
	if r.Text != "theirs\n" || !r.Clean() {
		t.Fatalf("merge = %q, %d conflicts", r.Text, r.Conflicts)
	}
}

func TestHasConflictMarkers(t *testing.T) {
	clean := "no markers here\n"
	if HasConflictMarkers(clean) {
		t.Error("clean text reported as conflicted")
	}
	r := Merge("a\n", "a\nmine\n", "a\ntheirs\n", true)
	if !HasConflictMarkers(r.Text) {
		t.Error("merged text with a hunk reported as clean")
	}
}

// A trailing newline is the markdown convention; a merge must not silently
// drop it, nor invent one where neither side had it.
func TestMergePreservesTrailingNewline(t *testing.T) {
	with := Merge("a\n", "a\nmine\n", "a\ntheirs\n", true)
	if !strings.HasSuffix(with.Text, "\n") {
		t.Errorf("trailing newline lost: %q", with.Text)
	}
	without := Merge("a", "a\nmine", "a\ntheirs", true)
	if strings.HasSuffix(without.Text, "\n") {
		t.Errorf("trailing newline invented: %q", without.Text)
	}
}
