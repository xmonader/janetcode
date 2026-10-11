package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/tool"
)

// prevRecorder is a tool renderer that writes down the previous result it
// was given for each call.
type prevRecorder struct{ seen map[string]string }

func (prevRecorder) RenderCall(json.RawMessage, tool.RenderContext) []string { return nil }

func (p prevRecorder) RenderResult(in json.RawMessage, out string, o tool.RenderContext) []string {
	p.seen[strings.TrimSpace(out)] = o.Prev
	return []string{out}
}

// manyTools is a transcript of n finished calls of a few tools, every
// fifth an error, with notices between them.
func manyTools(n int) *transcript {
	tr := newTranscript()
	for i := range n {
		c := agent.ToolCall{ID: fmt.Sprintf("c%d", i), Name: []string{"grep", "read_file", "todo"}[i%3]}
		e := &entry{kind: entryTool, call: c, output: fmt.Sprintf("out %d\n", i), done: true, isError: i%5 == 4}
		tr.add(e)
		tr.info(fmt.Sprintf("note %d", i))
	}
	return tr
}

// TestPreviousResultIsLinearInARenderPass: each tool entry drawn asked
// for its tool's previous result, and the answer was found by scanning
// the transcript back from the end — so a full re-render, which a resize
// or a theme change is, cost entries × tool calls. A render pass now
// keeps a running record and never scans, and gives every call the same
// answer the scan would have.
func TestPreviousResultIsLinearInARenderPass(t *testing.T) {
	tr := manyTools(300)
	rec := prevRecorder{seen: map[string]string{}}
	o := renderOpts{width: 100, renderer: func(string) (tool.Renderer, bool) { return rec, true }}
	tr.render(o)
	if tr.prevScanned != 0 {
		t.Fatalf("a render pass of %d entries scanned %d entries looking for previous results", len(tr.entries), tr.prevScanned)
	}
	checked := 0
	for _, e := range tr.entries {
		if e.kind != entryTool || e.isError {
			continue
		}
		want := tr.previousResult(e) // outside a pass: the scan
		if got := rec.seen[strings.TrimSpace(e.output)]; got != want {
			t.Fatalf("%s: the render pass gave previous result %q, the scan %q", e.call.ID, got, want)
		}
		checked++
	}
	if checked == 0 || tr.prevByTool != nil {
		t.Fatalf("checked %d calls; the pass's record outlived it: %v", checked, tr.prevByTool != nil)
	}
	// Errors are not results to compare against: the call after one
	// still sees the last success.
	if got := rec.seen["out 7"]; got != "out 1" {
		t.Fatalf("read_file after a failed read_file: previous result %q, want %q", got, "out 1")
	}
}

// BenchmarkFullRenderManyToolCalls is a full re-render (a width change)
// of a transcript of 3000 tool calls.
func BenchmarkFullRenderManyToolCalls(b *testing.B) {
	tr := manyTools(3000)
	rec := prevRecorder{seen: map[string]string{}}
	o := renderOpts{renderer: func(string) (tool.Renderer, bool) { return rec, true }}
	for i := 0; i < b.N; i++ {
		o.width = 100 + i%2
		tr.render(o)
	}
}
