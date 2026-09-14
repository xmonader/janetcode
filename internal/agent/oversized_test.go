package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestOversizedRefusesWithAPrice: truncating bills the caller for a prefix
// that is rarely the answer, and the damage is done by the time they read
// the warning. Refusing costs a few dozen tokens.
func TestOversizedRefusesWithAPrice(t *testing.T) {
	big := strings.Repeat("x", 400_000) // ~100k tokens
	small := strings.Repeat("x", 400)

	// Plenty of room, small result: nothing to say.
	if msg := oversized("grep", "TODO", small, 1000, 200_000, false); msg != "" {
		t.Fatalf("a small result must pass: %q", msg)
	}
	// Plenty of room, enormous result: withheld, with the cost named.
	msg := oversized("grep", "TODO", big, 1000, 200_000, false)
	if msg == "" {
		t.Fatal("a result larger than the cap must be withheld")
	}
	for _, want := range []string{"grep TODO", "100k tokens", "%", AcceptLargeOutput} {
		if !strings.Contains(msg, want) {
			t.Fatalf("the refusal must state %q: %s", want, msg)
		}
	}
	// Having said yes, it comes through.
	if msg := oversized("grep", "TODO", big, 1000, 200_000, true); msg != "" {
		t.Fatalf("an accepted result must not be withheld: %q", msg)
	}
	// No room at all: say so, and say the opt-in cannot help.
	msg = oversized("grep", "", big, 200_000, 200_000, false)
	if !strings.Contains(msg, "no room left") || !strings.Contains(msg, "cannot help") {
		t.Fatalf("got %q", msg)
	}
	// An unknown window disables the whole check rather than guessing.
	if msg := oversized("grep", "", big, 0, 0, false); msg != "" {
		t.Fatalf("with no known window nothing should be withheld: %q", msg)
	}
	// A small window still allows a workable floor.
	if msg := oversized("read_file", "a.go", strings.Repeat("x", 8_000), 100, 12_000, false); msg != "" {
		t.Fatalf("2k tokens must fit under the floor: %q", msg)
	}
}

func TestLargeOutputOptIn(t *testing.T) {
	if !tookLargeOutput(json.RawMessage(`{"accept_large_output":true,"pattern":"x"}`)) {
		t.Fatal("the opt-in was not read")
	}
	for _, in := range []string{`{"pattern":"x"}`, `{"accept_large_output":false}`, `{"accept_large_output":"yes"}`, `nope`} {
		if tookLargeOutput(json.RawMessage(in)) {
			t.Fatalf("%s must not count as consent", in)
		}
	}
}

// TestTakeLargeOutput: the opt-in is not declared on any schema, so it has
// to be stripped before a tool that disallows unknown fields sees it.
func TestTakeLargeOutput(t *testing.T) {
	got := takeLargeOutput(json.RawMessage(`{"accept_large_output":true,"pattern":"x"}`))
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj[AcceptLargeOutput]; ok {
		t.Fatal("the opt-in must be removed")
	}
	if obj["pattern"] != "x" {
		t.Fatal("the rest of the input must survive")
	}
	// Untouched when absent, and unparseable input passes through.
	for _, in := range []string{`{"pattern":"x"}`, `nope`} {
		if got := string(takeLargeOutput(json.RawMessage(in))); got != in {
			t.Fatalf("%s became %s", in, got)
		}
	}
}
