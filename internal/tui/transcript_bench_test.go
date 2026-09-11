package tui

import (
	"encoding/json"
	"fmt"
	"strings"
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

// TestSpinnerTicksDoNotRerenderToolBody: chrome (spinner, elapsed) on a
// running tool must not rebuild finished entries or the running tool's
// cached body. The hitch was needsRender returning true for every tool
// on every tick, then SetContent re-parsing the whole transcript.
func TestSpinnerTicksDoNotRerenderToolBody(t *testing.T) {
	tr := bigTranscript(400)
	call := agent.ToolCall{ID: "live", Name: "bash", Input: json.RawMessage(`{"command":"sleep 30 && echo done"}`)}
	live := tr.add(&entry{
		kind:    entryTool,
		call:    call,
		running: true,
		started: time.Now().Add(-3 * time.Second),
	})
	live.input.Write(call.Input)

	first := tr.render(renderOpts{width: 100, cap: 10, spinner: "⠋"})
	if live.toolBody == "" {
		t.Fatal("the running tool has no cached body")
	}
	if !strings.Contains(plain(first), "⠋") {
		t.Fatalf("first frame missing spinner:\n%s", plain(first))
	}
	stamps := make([]time.Time, len(tr.entries))
	for i, e := range tr.entries {
		stamps[i] = e.renderedAt
	}
	body := live.toolBody

	second := tr.render(renderOpts{width: 100, cap: 10, spinner: "⠙"})
	if live.toolBody != body {
		t.Fatal("a spinner tick rebuilt the running tool's body")
	}
	if !strings.Contains(plain(second), "⠙") {
		t.Fatalf("second frame missing the new spinner:\n%s", plain(second))
	}
	if strings.Contains(plain(second), "⠋") {
		t.Fatal("the old spinner glyph is still in the second frame")
	}
	for i, e := range tr.entries {
		if e == live {
			if !e.renderedAt.Equal(stamps[i]) {
				t.Fatal("a spinner tick re-rendered the running tool")
			}
			continue
		}
		if !e.renderedAt.Equal(stamps[i]) {
			t.Fatalf("finished entry %d (%v) was re-rendered on a spinner tick", i, e.kind)
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
