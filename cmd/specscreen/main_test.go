package main

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func say(t, role, text string) event {
	m := llm.TextMessage(llm.Role(role), text)
	return event{T: t, Surface: true, Message: &m}
}

func TestScreenCountsSuggestionsAndOffers(t *testing.T) {
	var s screen
	s.session([]event{
		say("user/message", "user", "fix the parser [repository map: 2 files]"),
		say("assistant/message", "assistant", "Fixed. Want me to add tests for the parser?"),
		{T: session.TypeFollowup, Output: "add tests for the parser"},
		say("user/message", "user", "Add tests for the parser."),
		say("assistant/message", "assistant", "Done."),
		{T: session.TypeFollowup, Output: "run the benchmarks"},
		say("user/message", "user", "[harness] you wrote verified"),
		say("user/message", "user", "what time is it?"),
		say("assistant/message", "assistant", "Noon. Shall I commit the change?"),
		say("user/message", "user", "yes"),
	})
	if s.pairs != 3 || s.harness != 1 {
		t.Fatalf("pairs %d harness %d", s.pairs, s.harness)
	}
	// The second suggestion is compared with the next human prompt, the
	// harness's own message in between skipped: one exact, one miss.
	if s.suggested != 2 || s.exact != 1 || s.near != 0 {
		t.Fatalf("suggested %d exact %d near %d", s.suggested, s.exact, s.near)
	}
	if s.offered != 2 || s.tookOffer != 2 {
		t.Fatalf("offered %d took %d", s.offered, s.tookOffer)
	}
	if !strings.Contains(s.text(), "exact match 1 (50%)") {
		t.Fatal(s.text())
	}
}

func TestTakesOffer(t *testing.T) {
	for _, c := range []struct {
		offer, prompt string
		want          bool
	}{
		{"add tests for the parser", "yes please", true},
		{"add tests for the parser", "add parser tests", true},
		{"add tests for the parser", "what time is it?", false},
		{"", "yes", false},
		{"add tests", "yesterday's build broke", false},
	} {
		if got := takesOffer(c.offer, c.prompt); got != c.want {
			t.Errorf("%q/%q: %v", c.offer, c.prompt, got)
		}
	}
}
