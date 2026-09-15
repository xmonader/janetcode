package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractTakesThePlanAndNothingElse(t *testing.T) {
	body, ok := Extract("I looked at the parser.\n\n## Plan\n1. rename it\n2. test it\n")
	if !ok || !strings.HasPrefix(body, "## Plan") || !strings.Contains(body, "2. test it") {
		t.Fatalf("body = %q ok = %v", body, ok)
	}
	if strings.Contains(body, "I looked at") {
		t.Fatalf("the prose before the plan came with it: %q", body)
	}
	for _, s := range []string{
		"which file did you mean?",
		"Here is what I found, but no plan yet.",
		"#### Plan\ntoo deep to be the plan heading",
		"## Planning notes\nnot the plan",
	} {
		if _, ok := Extract(s); ok {
			t.Errorf("a reply with no plan was written as one: %q", s)
		}
	}
	if _, ok := Extract("# Plan: rewrite the parser\n- do it\n"); !ok {
		t.Error("a titled plan heading is still a plan heading")
	}
}

func TestWriteKeepsOneVersionBackAndRewritesNothingUnchanged(t *testing.T) {
	root := t.TempDir()
	if changed, err := Write(root, "## Plan\nfirst"); err != nil || !changed {
		t.Fatalf("changed = %v err = %v", changed, err)
	}
	if _, err := os.Stat(PrevFile(root)); err == nil {
		t.Fatal("a first plan has no previous version")
	}
	if changed, err := Write(root, "## Plan\nfirst"); err != nil || changed {
		t.Fatalf("an unchanged plan was rewritten: changed = %v err = %v", changed, err)
	}
	if changed, err := Write(root, "## Plan\nsecond"); err != nil || !changed {
		t.Fatalf("changed = %v err = %v", changed, err)
	}
	got, err := Read(root)
	if err != nil || got != "## Plan\nsecond" {
		t.Fatalf("plan = %q err = %v", got, err)
	}
	prev, err := os.ReadFile(PrevFile(root))
	if err != nil || !strings.Contains(string(prev), "first") {
		t.Fatalf("previous version = %q err = %v", prev, err)
	}
	if want := filepath.Join(root, ".janetcode", "plan.md"); File(root) != want {
		t.Fatalf("plan file = %s, want %s", File(root), want)
	}
}

func TestNumberedShowsEveryLineAndItsComments(t *testing.T) {
	got := Numbered("## Plan\n1. rename it\n2. test it", []Comment{{Line: 2, Text: "rename it to what?"}})
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %q", lines)
	}
	for i, want := range []string{"1 │ ## Plan", "2 │ 1. rename it", "↳ rename it to what?", "3 │ 2. test it"} {
		if !strings.Contains(lines[i], want) {
			t.Errorf("line %d = %q, want %q in it", i, lines[i], want)
		}
	}
}

func TestParseRefAcceptsALineOrAPlanReference(t *testing.T) {
	for _, arg := range []string{"12 make it smaller", "plan.md:12 make it smaller", ".janetcode/plan.md:12 make it smaller"} {
		line, text, ok := ParseRef(arg)
		if !ok || line != 12 || text != "make it smaller" {
			t.Errorf("%q → %d %q %v", arg, line, text, ok)
		}
	}
	if _, _, ok := ParseRef("nowhere in particular"); ok {
		t.Error("a comment with no line was accepted")
	}
	if _, _, ok := ParseRef("0 zero is not a line"); ok {
		t.Error("line 0 was accepted")
	}
}

func TestMessageIsOneMessageQuotingEachLine(t *testing.T) {
	body := "## Plan\n1. rename it\n2. test it"
	got := Message(body, []Comment{{Line: 2, Text: "to what?"}, {Line: 3, Text: "which tests?"}})
	for _, want := range []string{"plan.md:2", "1. rename it", "to what?", "plan.md:3", "2. test it", "which tests?", "Stay in plan mode"} {
		if !strings.Contains(got, want) {
			t.Errorf("message is missing %q:\n%s", want, got)
		}
	}
}

func TestCommentsCollectAndEmpty(t *testing.T) {
	var c Comments
	c.Add(1, "one")
	c.Add(2, "  ")
	c.Add(3, "three")
	if c.Len() != 2 {
		t.Fatalf("collected %d", c.Len())
	}
	if got := c.Take(); len(got) != 2 || got[0].Text != "one" || got[1].Line != 3 {
		t.Fatalf("taken = %+v", got)
	}
	if c.Len() != 0 {
		t.Fatal("taking the comments left some behind")
	}
}
