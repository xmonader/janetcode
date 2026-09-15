package session

import (
	"strings"
	"testing"
)

// info makes the one thing Lineage reads: metadata, in listing order.
func info(id, forkOf, title string) Info {
	return Info{Meta: Meta{ID: id, ForkOf: forkOf, Title: title}, Messages: 2}
}

// shape renders a lineage the way a listing draws it, so a test can state
// the whole tree in one string.
func shape(nodes []Node) string {
	var sb strings.Builder
	for _, n := range nodes {
		sb.WriteString(n.Prefix() + n.Info.Meta.ID)
		if m := n.Mark(); m != "" {
			sb.WriteString(" [" + m + "]")
		}
		if n.Note() != "" {
			sb.WriteString(" (parent elsewhere)")
		}
		if n.Context {
			sb.WriteString(" (context)")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestLineagePutsForksUnderTheirParent(t *testing.T) {
	// Listing order: b is newest, then its parent a, then a's other fork
	// c and an unrelated root d. Roots keep that order; forks follow the
	// parent in the order they came in.
	nodes := Lineage([]Info{
		info("b", "a", "fork one"),
		info("a", "", "original"),
		info("c", "a", "fork two"),
		info("d", "", "unrelated"),
	}, "c")
	want := "a\n└ b\n└ c [current]\nd\n"
	if got := shape(nodes); got != want {
		t.Fatalf("lineage:\n%s\nwant:\n%s", got, want)
	}
}

func TestLineageMarksSettledAndCurrent(t *testing.T) {
	a := info("a", "", "root")
	a.Meta.Settled = true
	nodes := Lineage([]Info{a, info("b", "a", "fork")}, "b")
	if got := shape(nodes); got != "a [settled]\n└ b [current]\n" {
		t.Fatalf("marks:\n%s", got)
	}
}

func TestLineageListsAForkWhoseParentIsElsewhereAtTopLevel(t *testing.T) {
	nodes := Lineage([]Info{info("b", "gone", "fork of a deleted session")}, "")
	if got := shape(nodes); got != "b (parent elsewhere)\n" {
		t.Fatalf("orphan:\n%s", got)
	}
	if note := nodes[0].Note(); !strings.Contains(note, "gone") {
		t.Fatalf("note does not name the parent: %q", note)
	}
}

func TestLineageCapsHowDeepAChainIsDrawn(t *testing.T) {
	var infos []Info
	prev := ""
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		infos = append(infos, info(id, prev, id))
		prev = id
	}
	nodes := Lineage(infos, "")
	want := "a\n└ b\n  └ c\n    └ d\n    … e\n    … f\n"
	if got := shape(nodes); got != want {
		t.Fatalf("capped chain:\n%s\nwant:\n%s", got, want)
	}
	for _, n := range nodes {
		if n.Depth > MaxLineageDepth {
			t.Fatalf("%s drawn at depth %d past the cap", n.Info.Meta.ID, n.Depth)
		}
	}
	if !nodes[4].Deeper || nodes[3].Deeper {
		t.Fatal("the cap did not land on the first node past it")
	}
}

func TestLineageKeepsASessionWhoseForkLinksLoop(t *testing.T) {
	// A hand-edited pair pointing at each other must still be listed.
	nodes := Lineage([]Info{info("a", "b", "one"), info("b", "a", "two")}, "")
	if len(nodes) != 2 {
		t.Fatalf("a cycle lost a session: %s", shape(nodes))
	}
}

func TestFilterLineageKeepsTheAncestorsOfAMatch(t *testing.T) {
	nodes := Lineage([]Info{
		info("a", "", "parser work"),
		info("b", "a", "renaming the lexer"),
		info("c", "b", "deep in the lexer"),
		info("d", "", "unrelated docs"),
	}, "")
	got := shape(FilterLineage(nodes, "lexer"))
	if got != "a (context)\n└ b\n  └ c\n" {
		t.Fatalf("filtered:\n%s", got)
	}
	// Every word has to match, and an empty query keeps the whole tree.
	if out := FilterLineage(nodes, "lexer docs"); len(out) != 0 {
		t.Fatalf("both words should match one row: %s", shape(out))
	}
	if out := FilterLineage(nodes, "  "); len(out) != len(nodes) {
		t.Fatalf("an empty query dropped rows: %s", shape(out))
	}
	// Matching a root keeps the root alone: descendants are not context.
	if got := shape(FilterLineage(nodes, "docs")); got != "d\n" {
		t.Fatalf("root match:\n%s", got)
	}
}
