package agent

import (
	"strings"
	"testing"
)

const wrapBrief = `# Task

Build the catalog pager in pager.py. It lists items in pages and is pleasant to use.

- An argument whose value is ` + "`None`" + ` is treated as absent.
- ` + "`prev`" + ` returns the latest datetime strictly earlier than before.
- Every function has the shape ` + "`render(items, width)`" + ` and nothing else.
- The pager walks through the shelves slowly.
- ` + "`total`" + ` must be an integer.
`

func TestWrapRulesNoteListsUncheckedRules(t *testing.T) {
	checks := []string{"python -c 'assert total([1]) == 1'"}
	note := wrapRulesNote("pytest", 9, wrapBrief, checks)
	for _, want := range []string{"None", "strictly earlier", "render(items, width)", "Before you finish"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, "walks through the shelves") {
		t.Errorf("a descriptive sentence was listed:\n%s", note)
	}
	if strings.Contains(note, "must be an integer") {
		t.Errorf("a rule whose identifier a check uses was listed:\n%s", note)
	}
}

func TestWrapRulesNoteEmptyWhenNothingRule(t *testing.T) {
	if n := wrapRulesNote("x", 9, "Build a thing that walks around.\n", nil); n != "" {
		t.Fatalf("unexpected note %q", n)
	}
}
