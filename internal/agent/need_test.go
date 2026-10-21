package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// A text answer that declares a NEED gets archive excerpts and another
// call; the final answer can then quote the archived value.
func TestNeedLinesAreFaultedAgainstTheArchive(t *testing.T) {
	ar := NewArchive(t.TempDir())
	_, _ = ar.PutSubject("c1", "bash", "go test ./...", "--- FAIL: TestParse (0.00s)\n    parse_test.go:17: got 3 want 4\n[exit code 1]\n")
	f := &fake{responses: []*llm.Response{
		text("I no longer have that output.\nNEED: go test failure line\nNEED: TestParse"),
		text("It said: parse_test.go:17: got 3 want 4"),
	}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Archive: ar}
	var notices []string
	err := a.Run(context.Background(), "what did the failing test print?", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("%d model calls, want the retry", len(f.requests))
	}
	retry := f.requests[1].Messages
	last := retry[len(retry)-1]
	if last.Role != llm.RoleUser || !strings.Contains(last.Text(), "[Archive excerpts") || !strings.Contains(last.Text(), "got 3 want 4") {
		t.Fatalf("retry did not carry the excerpt:\n%s", last.Text())
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "archive fault (1/2)") {
		t.Fatalf("notices = %v", notices)
	}
	if got := a.Messages[len(a.Messages)-1].Text(); !strings.Contains(got, "got 3 want 4") {
		t.Fatalf("final = %q", got)
	}
	// Bounded: a model that keeps asking is not looped forever.
	f2 := &fake{responses: []*llm.Response{text("NEED: x"), text("NEED: y"), text("NEED: z"), text("UNKNOWN")}}
	b := &Agent{Provider: f2, Tools: tool.NewRegistry(), Model: "m", Archive: ar}
	if err := b.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	if len(f2.requests) != 3 {
		t.Fatalf("%d calls, want 1 + %d rounds", len(f2.requests), maxNeedRounds)
	}
	// Without an archive, or switched off, NEED lines are just text.
	c := &Agent{Provider: &fake{responses: []*llm.Response{text("NEED: x")}}, Tools: tool.NewRegistry(), Model: "m"}
	if err := c.Run(context.Background(), "q", nil); err != nil || len(c.Messages) != 2 {
		t.Fatalf("no archive: err=%v messages=%d", err, len(c.Messages))
	}
}

// A model writing in Chinese ends the line with a full-width colon; the
// pattern's class was meant to hold it and held the ASCII one twice.
func TestNeedLineTakesAFullWidthColon(t *testing.T) {
	m := needRe.FindStringSubmatch("NEED：go test failure line")
	if m == nil || m[1] != "go test failure line" {
		t.Fatalf("got %q", m)
	}
}
