package vault

import (
	"reflect"
	"testing"
)

func TestCategoriesReadsBothYAMLForms(t *testing.T) {
	cases := map[string][]string{
		"no frontmatter at all\n":                                     nil,
		"---\ncategories: [Ideas, Work]\n---\nbody\n":                 {"Ideas", "Work"},
		"---\ncategories:\n  - Ideas\n  - Work\n---\nb\n":             {"Ideas", "Work"},
		"---\ncategory: Ideas\n---\nb\n":                              {"Ideas"},
		`---` + "\n" + `categories: ["Ideas", 'Work']` + "\n---\nb\n": {"Ideas", "Work"},
		// Other keys are none of our business, and neither is a nested one.
		"---\ntitle: A note\nnested:\n  categories: [No]\n---\nb\n": nil,
		// Duplicates collapse case-insensitively, first spelling winning.
		"---\ncategories: [Work, work, WORK]\n---\nb\n": {"Work"},
		// A leading "---" with no closing fence is a horizontal rule.
		"---\njust a rule\n":            nil,
		"---\ncategories: []\n---\nb\n": nil,
	}
	for content, want := range cases {
		got := Categories(content)
		if len(got) == 0 && len(want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Categories(%q) = %v; want %v", content, got, want)
		}
	}
}

func TestWithCategoriesCreatesRewritesAndRemoves(t *testing.T) {
	// A note with no frontmatter gains a block, separated from the body.
	got := WithCategories("# Title\n\nbody\n", []string{"Ideas"})
	want := "---\ncategories: [Ideas]\n---\n\n# Title\n\nbody\n"
	if got != want {
		t.Errorf("create = %q; want %q", got, want)
	}

	// Rewriting replaces just the one key.
	got = WithCategories("---\ntitle: A note\ncategories: [Old]\n---\nbody\n", []string{"New", "Second"})
	want = "---\ntitle: A note\ncategories: [New, Second]\n---\nbody\n"
	if got != want {
		t.Errorf("rewrite = %q; want %q", got, want)
	}

	// A block-form list is replaced entirely, items included.
	got = WithCategories("---\ncategories:\n  - Old\n  - Older\ntitle: x\n---\nbody\n", []string{"New"})
	want = "---\ncategories: [New]\ntitle: x\n---\nbody\n"
	if got != want {
		t.Errorf("block rewrite = %q; want %q", got, want)
	}

	// Clearing the last category takes the now-empty block with it — and the
	// blank line that separated it from the body — so the file goes back to
	// being plain markdown.
	got = WithCategories("---\ncategories: [Only]\n---\n\nbody\n", nil)
	if got != "body\n" {
		t.Errorf("remove = %q", got)
	}

	// …but a block holding anything else survives, minus the key.
	got = WithCategories("---\ntitle: x\ncategories: [Only]\n---\nbody\n", nil)
	want = "---\ntitle: x\n---\nbody\n"
	if got != want {
		t.Errorf("remove one key = %q; want %q", got, want)
	}
}

// Frontmatter this app didn't write must survive a category change untouched —
// the parser reads one key and copies the rest through byte for byte.
func TestWithCategoriesPreservesForeignFrontmatter(t *testing.T) {
	content := "---\naliases:\n  - Second name\ncssclass: wide\n# a comment\ndate: 2026-08-25\n---\n\nbody\n"
	got := WithCategories(content, []string{"Ideas"})
	for _, line := range []string{"aliases:", "  - Second name", "cssclass: wide", "# a comment", "date: 2026-08-25"} {
		if !contains(got, line) {
			t.Errorf("lost %q from:\n%s", line, got)
		}
	}
	if cats := Categories(got); len(cats) != 1 || cats[0] != "Ideas" {
		t.Errorf("categories = %v", cats)
	}
}

// Round-tripping is the property that matters: whatever the writer accepts, the
// reader has to get back.
func TestCategoriesRoundTrip(t *testing.T) {
	sets := [][]string{
		{"Ideas"},
		{"Ideas", "Work", "Reading list"},
		{"Ünicode categories", "with-dashes", "and 1 number"},
	}
	for _, set := range sets {
		content := WithCategories("body\n", set)
		if got := Categories(content); !reflect.DeepEqual(got, set) {
			t.Errorf("round trip %v = %v (from %q)", set, got, content)
		}
	}
}

func TestNormalizeCategory(t *testing.T) {
	if got, err := NormalizeCategory("  reading   list  "); err != nil || got != "reading list" {
		t.Errorf("normalize = %q, %v", got, err)
	}
	// A newline in a pasted name is collapsed like any other whitespace, not
	// treated as an error — the point is to accept what people paste.
	if got, err := NormalizeCategory("with\nnewline"); err != nil || got != "with newline" {
		t.Errorf("newline = %q, %v", got, err)
	}
	for _, bad := range []string{"", "   ", "a,b", "a[b]", "a: b", "-leading dash", "bell\x07here"} {
		if _, err := NormalizeCategory(bad); err == nil {
			t.Errorf("NormalizeCategory(%q) should have been refused", bad)
		}
	}
}

func TestNormalizeCategoriesDedupesAndDropsBlanks(t *testing.T) {
	got, err := NormalizeCategories([]string{"Work", "", "  ", "work", "Ideas"})
	if err != nil {
		t.Fatalf("NormalizeCategories: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"Work", "Ideas"}) {
		t.Errorf("normalized = %v", got)
	}
	too := make([]string, MaxCategories+1)
	for i := range too {
		too[i] = string(rune('a'+i%26)) + itoa(i)
	}
	if _, err := NormalizeCategories(too); err == nil {
		t.Error("a note with too many categories should be refused")
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
