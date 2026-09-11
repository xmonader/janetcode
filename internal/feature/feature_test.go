package feature

import (
	"encoding/json"
	"strings"
	"testing"
)

func sample() *Catalog {
	c := New()
	c.AddAll([]Feature{
		{Name: "/undo", Kind: KindCommand, What: "revert everything the last turn changed", When: "the model changed the wrong thing", Topics: []string{"revert", "mistake"}},
		{Name: "/compact", Kind: KindCommand, What: "summarise the conversation to free context", When: "the gauge is high", Topics: []string{"context", "full", "long", "output"}},
		{Name: "/resume", Kind: KindCommand, What: "reopen an earlier session", When: "you closed the terminal"},
		{Name: "ctrl+g", Kind: KindKey, What: "background the running command", When: "a build is holding the turn"},
		{Name: "compact_at", Kind: KindConfig, What: "token count that triggers compaction", When: "you want it earlier or later"},
	})
	return c
}

func TestFindPrefersTheNameOverTheProse(t *testing.T) {
	c := sample()
	hits := c.Find("undo", 5)
	if len(hits) == 0 || hits[0].Name != "/undo" {
		t.Fatalf("undo ranked %v first", hits)
	}
	// A leading slash is how a user types a command; it must not change
	// the answer.
	if h := c.Find("/undo", 1); len(h) == 0 || h[0].Name != "/undo" {
		t.Fatalf("/undo did not find itself")
	}
}

func TestFindMatchesTheProblemNotOnlyTheName(t *testing.T) {
	c := sample()
	for _, tc := range []struct{ q, want string }{
		{"the context is full", "/compact"},
		{"a long output", "/compact"},
		{"I made a mistake", "/undo"},
		{"cmpct", "/compact"}, // letters in order, like the command menu
	} {
		hits := c.Find(tc.q, 4)
		found := false
		for _, h := range hits {
			if h.Name == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q did not find %s (got %d hits)", tc.q, tc.want, len(hits))
		}
	}
}

func TestFindIsNotAWordFragmentSearch(t *testing.T) {
	c := New()
	c.Add(Feature{Name: "/x", Kind: KindCommand, What: "an undocumented thing", When: "never"})
	if hits := c.Find("undo", 5); len(hits) != 0 {
		t.Errorf("undo matched %q through a fragment of a longer word", hits[0].What)
	}
}

func TestEmptyQueryFindsNothing(t *testing.T) {
	if hits := sample().Find("   ", 5); hits != nil {
		t.Errorf("a blank query returned %d hits", len(hits))
	}
}

func TestAddReplacesRatherThanDuplicates(t *testing.T) {
	c := sample()
	n := c.Len()
	c.Add(Feature{Name: "/undo", Kind: KindCommand, What: "changed"})
	if c.Len() != n {
		t.Fatalf("catalogue grew from %d to %d on a replacement", n, c.Len())
	}
	f, _ := c.Get(KindCommand, "/undo")
	if f.What != "changed" {
		t.Fatalf("replacement did not take: %q", f.What)
	}
}

func TestSameNameInTwoKindsCoexist(t *testing.T) {
	c := New()
	c.Add(Feature{Name: "model", Kind: KindFlag, What: "the flag"})
	c.Add(Feature{Name: "model", Kind: KindConfig, What: "the config key"})
	if c.Len() != 2 {
		t.Fatalf("kinds collided: %d entries", c.Len())
	}
}

func TestIntrospectRefusesAnEmptyQuery(t *testing.T) {
	tl := Introspect{Catalog: sample()}
	if _, err := tl.Run(t.Context(), json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Fatal("an empty query was accepted")
	}
}

func TestIntrospectAnswersFromTheCatalogue(t *testing.T) {
	tl := Introspect{Catalog: sample()}
	out, err := tl.Run(t.Context(), json.RawMessage(`{"query":"context is full"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "/compact") {
		t.Fatalf("answer did not name /compact:\n%s", out)
	}
	if len(out) > MaxAnswerBytes+120 {
		t.Fatalf("answer is %d bytes", len(out))
	}
}

func TestIntrospectIsReadOnlyAndSchemaIsValid(t *testing.T) {
	var tl Introspect
	if tl.Mutating() {
		t.Error("introspect claims to mutate")
	}
	var schema map[string]any
	if err := json.Unmarshal(tl.Schema(), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if tl.Name() != "introspect" {
		t.Errorf("name is %q", tl.Name())
	}
}

func TestFindReadsTheCommandNamedInTheMiddleOfASentence(t *testing.T) {
	c := sample()
	c.Add(Feature{Name: "/remember", Kind: KindCommand, What: "add a fact to the project memory", When: "a fact you are tired of repeating"})
	hits := c.Find("remember this for next time", 5)
	if len(hits) == 0 || hits[0].Name != "/remember" {
		t.Fatalf("ranked %v first, not /remember", names(hits))
	}
}

func TestFindIgnoresFillerWords(t *testing.T) {
	c := sample()
	// "what" and "this" appear in half the catalogue's prose and must
	// not be what decides the order.
	hits := c.Find("what is this", 5)
	if len(hits) != 0 {
		t.Errorf("a query of nothing but filler matched %v", names(hits))
	}
}

func TestFindSaysNothingRatherThanGuessing(t *testing.T) {
	c := sample()
	if hits := c.Find("zzz nothing at all zzz", 5); len(hits) != 0 {
		t.Errorf("gibberish matched %v", names(hits))
	}
}

func TestKindNamedTurnsAListingQueryIntoAListing(t *testing.T) {
	for _, q := range []string{"keys", "keybindings", "commands", "config keys"} {
		if _, ok := KindNamed(q); !ok {
			t.Errorf("%q was not recognised as a kind", q)
		}
	}
	if _, ok := KindNamed("undo"); ok {
		t.Error("undo was taken for a kind")
	}
	out := Answer(sample(), "keys", "")
	if !strings.Contains(out, "ctrl+g") {
		t.Errorf("/help keys did not list the keys:\n%s", out)
	}
}

// The transcript wraps to the terminal width with no hanging indent, so
// at eighty columns a "when" line came back to column zero and the rows
// ran into each other. /help wraps its own rows.
func TestAnswerWrapsWithAHangingIndent(t *testing.T) {
	c := sample()
	out := AnswerWidth(c, "the context is full", "", 60)
	for _, line := range strings.Split(out, "\n") {
		if len(line) > 60 {
			t.Errorf("line is %d wide: %q", len(line), line)
		}
	}
	if !strings.Contains(out, "\n      when:") {
		t.Errorf("no indented when line:\n%s", out)
	}
	// Unwrapped is what the model gets: a request is not a terminal.
	if strings.Contains(Answer(c, "the context is full", ""), "\n  summar") {
		t.Error("the unwrapped answer was wrapped")
	}
}

func TestListWrapsToTheTerminalWidth(t *testing.T) {
	c := sample()
	c.Add(Feature{Name: "/verify", Kind: KindCommand,
		Invoke: "/verify [set <cmd>|mutate <test cmd>|clear|trust]",
		What:   "run the project's checks now; set adds one to .janetcode/verify.json",
		When:   "you want the harness to run the build or the tests after every change and hand failures back to the model"})
	for _, w := range []int{60, 80, 110} {
		for _, line := range strings.Split(c.ListWidth(w), "\n") {
			if len(line) > w {
				t.Errorf("at %d columns a line is %d wide: %q", w, len(line), line)
			}
		}
	}
	if !strings.Contains(c.ListWidth(80), "/verify [set <cmd>|mutate <test cmd>|clear|trust]\n") {
		t.Error("a wide invocation did not get a line of its own")
	}
}

func names(fs []Feature) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Name
	}
	return out
}

func TestHeadlineDoesNotCutInsideBracketsOrAbbreviations(t *testing.T) {
	cases := []struct{ in, want string }{
		{"revert everything the last turn changed (git snapshot; file copies outside git); preview only lists it",
			"revert everything the last turn changed (git snapshot; file copies outside git)"},
		{"comma-separated permission rules to allow for this run, e.g. 'bash(go test *),edit_file'",
			"comma-separated permission rules to allow for this run, e.g. 'bash(go test *),edit_file'"},
		{"configured provider or preset name, e.g. anthropic, openai, openrouter, ollama",
			"configured provider or preset name, e.g. anthropic, openai, openrouter, ollama"},
		{"one sentence. And a second one that is dropped.", "one sentence"},
		{"a clause long enough to be worth cutting; and the rest",
			"a clause long enough to be worth cutting"},
		{"short; keep", "short; keep"},
		{"trailing full stop.", "trailing full stop"},
	}
	for _, c := range cases {
		if got := Headline(c.in, 31); got != c.want {
			t.Errorf("Headline(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestAnswerSurvivesANilCatalogue(t *testing.T) {
	if got := Answer(nil, "zzzz no such thing zzzz", ""); got == "" {
		t.Fatal("nil catalogue produced no answer at all")
	}
}
