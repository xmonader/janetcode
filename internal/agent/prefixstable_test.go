package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// requestPrefix is the part of a request a provider's prompt cache keys
// on before the conversation: the system prompt and the tool array, in
// order, rendered so a difference of one byte shows up as a difference
// here.
func requestPrefix(r llm.Request) string {
	var b strings.Builder
	b.WriteString("system\x00" + r.System + "\x00tools")
	for _, d := range r.Tools {
		b.WriteString("\x00" + d.Name + "\x00" + d.Description + "\x00" + string(d.InputSchema))
	}
	return b.String()
}

// TestRequestPrefixIsByteStableAcrossTurns is the caching invariant the
// whole design leans on: everything before the conversation — the system
// prompt, the tool definitions and their order — is byte for byte the
// same on every call of a session, and each request's messages extend the
// last one's rather than editing them. Anything volatile that creeps into
// the prefix (a timestamp in the system prompt, a tool registering
// mid-turn, a map iteration deciding the tool order) re-bills the whole
// prompt on every call, and nothing else in the suite would notice.
func TestRequestPrefixIsByteStableAcrossTurns(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		// Turn one takes two model calls, so the invariant is checked
		// within a turn as well as across turns.
		toolCall("t1", "read_file", `{"path":"a.txt"}`),
		text("that is alpha"),
		// Turn two.
		toolCall("t2", "read_file", `{"path":"a.txt"}`),
		text("still alpha"),
	}}
	a, _ := newAgent(t, f, nil)
	for _, prompt := range []string{"read a.txt", "read it again"} {
		if err := a.Run(context.Background(), prompt, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.requests) != 4 {
		t.Fatalf("%d requests, want 4", len(f.requests))
	}
	if len(f.requests[0].Tools) == 0 || f.requests[0].System == "" {
		t.Fatal("the first request carried no prefix; the comparison would be vacuous")
	}

	want := requestPrefix(f.requests[0])
	for i, r := range f.requests[1:] {
		if got := requestPrefix(r); got != want {
			t.Fatalf("request %d has a different cached prefix from the first:\n got %q\nwant %q", i+2, got, want)
		}
	}

	// The messages must only ever grow: an edit to one already sent
	// throws the cache away from that point on, however stable the
	// prefix above it is.
	for i := 1; i < len(f.requests); i++ {
		prev, cur := f.requests[i-1].Messages, f.requests[i].Messages
		if len(cur) < len(prev) {
			t.Fatalf("request %d sent %d messages, fewer than the %d before it", i+1, len(cur), len(prev))
		}
		for j := range prev {
			if hashMessage(cur[j]) != hashMessage(prev[j]) {
				t.Fatalf("request %d rewrote message %d instead of appending", i+1, j)
			}
		}
	}

	// The agent's own watch has to agree: no drift at all this session.
	if report := a.PrefixReport(); !strings.Contains(report, "only appended") {
		t.Fatalf("the prefix watch saw drift:\n%s", report)
	}
}
