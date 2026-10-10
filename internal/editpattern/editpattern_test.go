package editpattern

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func TestTokenize(t *testing.T) {
	got := Tokenize(`foo(a_1, "x, y", 3.5) // é`)
	var texts []string
	for _, tok := range got {
		texts = append(texts, tok.Text)
	}
	want := []string{"foo", "(", "a_1", ",", `"x, y"`, ",", "3.5", ")", "/", "/", "é"}
	if strings.Join(texts, "|") != strings.Join(want, "|") {
		t.Fatalf("tokens = %q, want %q", texts, want)
	}
	if got[4].Kind != String || got[6].Kind != Number || got[0].Kind != Ident {
		t.Fatalf("kinds wrong: %+v", got)
	}
}

func TestTokenizeUnterminatedStringStopsAtLineEnd(t *testing.T) {
	got := Tokenize("x = \"abc\ny")
	if got[len(got)-1].Text != "y" {
		t.Fatalf("unterminated string swallowed the next line: %+v", got)
	}
}

// The motivating case: a call gains the same new argument at every site.
func TestInduceAddedArgumentFindsRemainingSites(t *testing.T) {
	a := Edit{Path: "x.go", Old: "\tv := load(a)\n", New: "\tv := load(a, ctx)\n"}
	b := Edit{Path: "x.go", Old: "\tw := load(b)\n", New: "\tw := load(b, ctx)\n"}
	r, ok := Induce(a, b)
	if !ok {
		t.Fatal("no rule induced")
	}
	src := "func f() {\n\tx := load(c)\n\ty := load(d, ctx)\n\tz := unload(e)\n\tq := load(c + 1)\n}\n"
	sites := r.Find(src)
	if len(sites) != 1 || sites[0].Line != 2 {
		t.Fatalf("sites = %+v, want one at line 2 (the done site, another callee and a non-word argument excluded)", sites)
	}
	got := Apply(src, sites)
	if !strings.Contains(got, "x := load(c, ctx)") || !strings.Contains(got, "y := load(d, ctx)") {
		t.Fatalf("applied:\n%s", got)
	}
}

func TestInduceRenameKeepsSpacing(t *testing.T) {
	a := Edit{Old: "return item(sku)", New: "return lookup(sku)"}
	b := Edit{Old: "if item(sku) {", New: "if lookup(sku) {"}
	r, ok := Induce(a, b)
	if !ok {
		t.Fatal("no rule")
	}
	if got := Apply("x = item(sku);", r.Find("x = item(sku);")); got != "x = lookup(sku);" {
		t.Fatalf("got %q", got)
	}
}

func TestInduceRefusesDifferentShapes(t *testing.T) {
	cases := map[string][2]Edit{
		"same site twice": {
			{Path: "a", Old: "f(x)", New: "f(x, y)"},
			{Path: "a", Old: "f(x)", New: "f(x, y)"},
		},
		"different inserted text": {
			{Old: "f(a)", New: "f(a, ctx)"},
			{Old: "f(b)", New: "f(b, log)"},
		},
		"core lengths differ": {
			{Old: "f(a)", New: "f(a, ctx)"},
			{Old: "f(b)", New: "f(b, ctx, 1)"},
		},
		"punctuation differs in core": {
			{Old: "a + b", New: "a + c"},
			{Old: "a - b", New: "a - c"},
		},
		"only holes and punctuation": {
			{Old: "x = 1", New: "x = 2"},
			{Old: "y = 3", New: "y = 4"},
		},
		"new hole the pattern cannot bind": {
			{Old: "g(a)", New: "g(a, p)"},
			{Old: "g(b)", New: "g(b, q)"},
		},
		"whitespace only": {
			{Old: "f(a)", New: "f( a )"},
			{Old: "f(b)", New: "f( b )"},
		},
	}
	for name, c := range cases {
		if r, ok := Induce(c[0], c[1]); ok {
			t.Errorf("%s: induced %s", name, r)
		}
	}
}

// One token mapping to two different tokens is not a consistent
// generalisation: f(a, a) and f(b, c) do not share a rule.
func TestInduceInconsistentHoleRefused(t *testing.T) {
	a := Edit{Old: "f(a, a)", New: "f(a, a, ctx)"}
	b := Edit{Old: "f(b, c)", New: "f(b, c, ctx)"}
	if r, ok := Induce(a, b); ok {
		t.Fatalf("induced %s", r)
	}
}

func TestFindBindsHolesConsistently(t *testing.T) {
	a := Edit{Old: "eq(a, a)", New: "same(a, a)"}
	b := Edit{Old: "eq(b, b)", New: "same(b, b)"}
	r, ok := Induce(a, b)
	if !ok {
		t.Fatal("no rule")
	}
	sites := r.Find("eq(c, d); eq(e, e)")
	if len(sites) != 1 {
		t.Fatalf("sites = %+v, want only eq(e, e)", sites)
	}
}

func TestPredictsOwnExamplesAndAThird(t *testing.T) {
	a := Edit{Old: "HEADER_LEN + 4", New: "HEADER_LEN + CHECKSUM_LEN + 4"}
	b := Edit{Old: "HEADER_LEN + n", New: "HEADER_LEN + CHECKSUM_LEN + n"}
	r, ok := Induce(a, b)
	if !ok {
		t.Fatal("no rule")
	}
	for _, e := range []Edit{a, b, {Old: "off = HEADER_LEN + size", New: "off = HEADER_LEN + CHECKSUM_LEN + size"}} {
		if !r.Predicts(e) {
			t.Errorf("rule %s does not predict %q -> %q", r, e.Old, e.New)
		}
	}
	if r.Predicts(Edit{Old: "HEADER_LEN + n", New: "HEADER_LEN + 2*n"}) {
		t.Error("predicted an edit the rule does not make")
	}
}

func TestScreenCountsOnlyPredictedThirds(t *testing.T) {
	a := Edit{Path: "x.py", Old: "item(sku)", New: "lookup(sku)"}
	b := Edit{Path: "x.py", Old: "y = item(sku) + 1", New: "y = lookup(sku) + 1"}
	c := Edit{Path: "x.py", Old: "return item(sku)", New: "return lookup(sku)"}
	// Same shape as the class but a different new name: not predicted.
	d := Edit{Path: "x.py", Old: "z = item(sku)", New: "z = view(sku)"}
	unrelated := Edit{Path: "x.py", Old: "def f():\n    pass", New: "def f():\n    return 1"}
	res := Screen([]Run{
		{Name: "hit", Edits: []Edit{a, unrelated, b, c, c}}, // c retried counts once
		{Name: "pair", Edits: []Edit{a, b, d}},
		{Name: "none", Edits: []Edit{unrelated}},
		{Name: "empty"},
	})
	editing, pairs, threes := res.Counts()
	if editing != 3 || pairs != 2 || threes != 1 {
		t.Fatalf("editing, pairs, threes = %d, %d, %d; want 3, 2, 1", editing, pairs, threes)
	}
	if got := res.Runs[0].Best(); got.Hunks != 3 || got.Bytes != len(c.New) {
		t.Fatalf("best class = %+v", got)
	}
	text := res.Text()
	if !strings.Contains(text, "verdict: keep") || !strings.Contains(text, "hit ") {
		t.Fatalf("text:\n%s", text)
	}
	if !strings.Contains(Screen([]Run{{Name: "x", Edits: []Edit{a}}}).Text(), "verdict: drop") {
		t.Fatal("a run with no class should drop")
	}
}

func TestEditsFromEvents(t *testing.T) {
	in := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	events := []session.Event{
		{Type: "assistant/message", Message: &llm.Message{Blocks: []llm.Block{
			{Type: llm.BlockToolUse, Name: "edit_file", Input: in(map[string]any{"path": "a.go", "old_string": "x", "new_string": "y"})},
			{Type: llm.BlockToolUse, Name: "bash", Input: in(map[string]any{"command": "ls"})},
			{Type: llm.BlockToolUse, Name: "edit_file", Input: in(map[string]any{"path": "b.go",
				"edits": []map[string]any{{"old_string": "p", "new_string": "q"}, {"from_line": 3, "to_line": 4, "new_string": "z"}}})},
		}}},
		{Type: "tool/result", Message: &llm.Message{Blocks: []llm.Block{{Type: llm.BlockToolUse, Name: "edit_file", Input: in(map[string]any{"old_string": "no"})}}}},
	}
	got := EditsFromEvents(events)
	want := []Edit{{Path: "a.go", Old: "x", New: "y"}, {Path: "b.go", Old: "p", New: "q"}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("edits = %+v, want %+v", got, want)
	}
}
