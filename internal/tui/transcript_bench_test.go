package tui

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
)

// bigTranscript builds a transcript of about n lines of finished
// entries, the shape a long session leaves behind: answers, tool calls
// and notices, all done and cached.
func bigTranscript(n int) *transcript {
	t := newTranscript()
	for i := 0; len(t.entries)*4 < n; i++ {
		t.user(fmt.Sprintf("change the %dth handler", i))
		t.add(&entry{kind: entryAssistant, text: fmt.Sprintf(
			"Looking at handler %d.\n\n```go\nfunc handler%d(w http.ResponseWriter) {\n\tfmt.Fprintln(w, \"ok\")\n}\n```\n\nThat is the one.", i, i), done: true})
		t.add(&entry{kind: entryTool, call: agent.ToolCall{ID: fmt.Sprintf("t%d", i), Name: "grep",
			Input: json.RawMessage(`{"pattern":"handler"}`)},
			output: "internal/server/handler.go\ninternal/server/routes.go\n", done: true})
		t.info(fmt.Sprintf("verify: go build ./... passed (%dms)", i))
	}
	return t
}

// TestWrittenOnceEntriesAreRenderedOnce: an entry whose text can never
// change must come out of the cache on the next frame. Notices, the
// lines the user typed, verdicts, tips and the banner are never marked
// done, so before fixedText they fell through to "re-render me" and the
// whole backlog was rebuilt on every frame of the session.
func TestWrittenOnceEntriesAreRenderedOnce(t *testing.T) {
	tr := bigTranscript(400)
	o := renderOpts{width: 100, cap: 10}
	first := tr.render(o)
	stamps := make([]time.Time, len(tr.entries))
	for i, e := range tr.entries {
		stamps[i] = e.renderedAt
	}
	if got := tr.render(o); got != first {
		t.Fatal("the second frame of an unchanged transcript differs from the first")
	}
	redone := 0
	for i, e := range tr.entries {
		if fixedText(e.kind) && !e.renderedAt.Equal(stamps[i]) {
			redone++
		}
	}
	if redone > 0 {
		t.Fatalf("%d written-once entries were rendered again on an unchanged frame", redone)
	}
	// A width change must still invalidate every one of them.
	tr.render(renderOpts{width: 60, cap: 10})
	for i, e := range tr.entries {
		if fixedText(e.kind) && e.renderedAt.Equal(stamps[i]) {
			t.Fatalf("entry %d kept a render made for another width", i)
		}
	}
}

func BenchmarkTranscriptRender2000Lines(b *testing.B) {
	t := bigTranscript(2000)
	o := renderOpts{width: 100, cap: 10}
	got := t.render(o) // prime the per-entry cache, as a live session would
	b.Logf("%d entries, %d bytes rendered", len(t.entries), len(got))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = t.render(o)
	}
}
