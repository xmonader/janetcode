package perm

import (
	"encoding/json"
	"testing"
)

// A rule carries its parsed clauses, so two rules that say the same thing
// are still one rule to Add and to the ledger however each was built.
func TestEqualIgnoresHowTheRuleWasBuilt(t *testing.T) {
	parsed, err := Parse("task(model:haiku*)")
	if err != nil {
		t.Fatal(err)
	}
	literal := Rule{Tool: "task", Pattern: "model:haiku*"}
	if !parsed.Equal(literal) || !literal.Equal(parsed) {
		t.Fatalf("%v and %v should be the same rule", parsed, literal)
	}
	var rs Rules
	if !rs.Add(Allowed, parsed) {
		t.Fatal("the first add should have taken")
	}
	if rs.Add(Allowed, literal) {
		t.Fatal("the same rule was added twice")
	}
	if len(rs.Allow) != 1 {
		t.Fatalf("allow = %v", rs.Allow)
	}
}

// However it was built, a parameter rule matches a call's fields and a
// bare pattern matches the subject.
func TestCompiledAndLiteralRulesMatchAlike(t *testing.T) {
	call := Call{Tool: "task", Subject: "explore the repo", Input: json.RawMessage(`{"model":"haiku-4","prompt":"x"}`)}
	parsed, err := Parse("task( model:haiku* )")
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Pattern != "model:haiku*" {
		t.Fatalf("pattern was not canonicalised: %q", parsed.Pattern)
	}
	for _, r := range []Rule{parsed, {Tool: "task", Pattern: "model:haiku*"}} {
		if !r.matchesCall(call) {
			t.Errorf("%v should match the call", r)
		}
		// Twice: a rule is matched over and over, and the second time
		// must say what the first did.
		if !r.matchesCall(call) {
			t.Errorf("%v stopped matching on the second call", r)
		}
	}
	for _, r := range []Rule{{Tool: "task", Pattern: "model:sonnet*"}, {Tool: "task", Pattern: "agent:explore"}} {
		if r.matchesCall(call) {
			t.Errorf("%v should not match the call", r)
		}
	}
	// A bare pattern is still matched against the subject.
	if r := (Rule{Tool: "task", Pattern: "explore *"}); !r.matchesCall(call) {
		t.Errorf("%v should match the subject", r)
	}
}

func BenchmarkMatchParamRule(b *testing.B) {
	r, err := Parse("task(model:haiku*, agent:explore)")
	if err != nil {
		b.Fatal(err)
	}
	c := Call{Tool: "task", Subject: "x", Input: json.RawMessage(`{"model":"haiku-4","agent":"explore"}`)}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if !r.matchesCall(c) {
			b.Fatal("should match")
		}
	}
}
