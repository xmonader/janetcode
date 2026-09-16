package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// An attachment the model already has word for word is not sent again:
// the repository map is re-ranked every user turn and comes out
// identical often enough to matter, and skills re-attach on a timer.
// The check is against the live history, so anything compaction takes
// out comes back.

func TestIdenticalAttachmentIsNotSentTwice(t *testing.T) {
	mapText := "# repository map\n" + strings.Repeat("internal/server/handler.go: ServeHTTP, newRouter\n", 40)
	f := &fake{responses: []*llm.Response{text("one"), text("two"), text("three")}}
	a, _ := newAgent(t, f, nil)
	a.Prepare = func(_ context.Context, b []llm.Block) ([]llm.Block, string) {
		return append(b, llm.Block{Type: llm.BlockText, Text: mapText}), "map attached"
	}
	for _, p := range []string{"first", "second", "third"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := countText(a.Messages, mapText); n != 1 {
		t.Fatalf("the same map is in the history %d times, want 1", n)
	}
	// The user's own text is never what gets dropped.
	for _, p := range []string{"first", "second", "third"} {
		if countText(a.Messages, p) != 1 {
			t.Fatalf("the prompt %q is not in the history exactly once", p)
		}
	}
}

func TestAnAttachmentComesBackWhenTheHistoryLosesIt(t *testing.T) {
	mapText := strings.Repeat("internal/server/handler.go: ServeHTTP\n", 40)
	f := &fake{responses: []*llm.Response{text("one"), text("two")}}
	a, _ := newAgent(t, f, nil)
	a.Prepare = func(_ context.Context, b []llm.Block) ([]llm.Block, string) {
		return append(b, llm.Block{Type: llm.BlockText, Text: mapText}), ""
	}
	if err := a.Run(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	a.Messages = nil // what a compaction leaves behind
	if err := a.Run(context.Background(), "second", nil); err != nil {
		t.Fatal(err)
	}
	if n := countText(a.Messages, mapText); n != 1 {
		t.Fatalf("the map was not re-attached after the history lost it (%d copies)", n)
	}
}

func TestShortAttachmentsAreLeftAlone(t *testing.T) {
	short := "one line of context"
	f := &fake{responses: []*llm.Response{text("one"), text("two")}}
	a, _ := newAgent(t, f, nil)
	a.Prepare = func(_ context.Context, b []llm.Block) ([]llm.Block, string) {
		return append(b, llm.Block{Type: llm.BlockText, Text: short}), ""
	}
	for _, p := range []string{"first", "second"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := countText(a.Messages, short); n != 2 {
		t.Fatalf("a short attachment was deduplicated: %d copies, want 2", n)
	}
}

func countText(msgs []llm.Message, s string) int {
	n := 0
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && b.Text == s {
				n++
			}
		}
	}
	return n
}
