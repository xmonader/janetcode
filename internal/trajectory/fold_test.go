package trajectory

import (
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

func at(base time.Time, ms int) time.Time { return base.Add(time.Duration(ms) * time.Millisecond) }

// A realistic small turn: you ask, the model calls bash twice, answers.
func sample(base time.Time) []session.Event {
	return []session.Event{
		{Seq: 1, Time: at(base, 0), Type: session.TypeTurnStart, Turn: 1},
		{Seq: 2, Time: at(base, 1), Type: session.TypeUserMessage, Turn: 1,
			Message: msg(llm.RoleUser, "run the tests")},
		{Seq: 3, Time: at(base, 2), Type: session.TypeStepStart, Turn: 1, Step: 1},
		{Seq: 4, Time: at(base, 900), Type: session.TypeAssistant, Turn: 1, Step: 1,
			Message: msg(llm.RoleAssistant, ""), Model: "qwen", TTFTMs: 400, DecodeMs: 500,
			Usage: &llm.Usage{InputTokens: 100, CacheReadTokens: 900, OutputTokens: 50}},
		{Seq: 5, Time: at(base, 910), Type: session.TypeToolCall, Turn: 1, Step: 1,
			CallID: "c1", Name: "bash", Arguments: `{"command":"go test ./..."}`},
		{Seq: 6, Time: at(base, 2910), Type: session.TypeToolResult, Turn: 1, Step: 1,
			CallID: "c1", Name: "bash", Output: "ok\tall\t2.0s", DurationMs: 2000},
		{Seq: 7, Time: at(base, 2920), Type: session.TypeToolCall, Turn: 1, Step: 1,
			CallID: "c2", Name: "bash", Arguments: `{"command":"gofmt -l ."}`},
		{Seq: 8, Time: at(base, 3020), Type: session.TypeToolResult, Turn: 1, Step: 1,
			CallID: "c2", Name: "bash", Output: "", DurationMs: 100},
		{Seq: 9, Time: at(base, 3030), Type: session.TypeStepStart, Turn: 1, Step: 2},
		{Seq: 10, Time: at(base, 4030), Type: session.TypeAssistant, Turn: 1, Step: 2,
			Message: msg(llm.RoleAssistant, "The suite passes\nand the tree is formatted."),
			Model:   "qwen", TTFTMs: 200, DecodeMs: 800,
			Usage: &llm.Usage{InputTokens: 50, CacheReadTokens: 1200, OutputTokens: 30}},
	}
}

func msg(role llm.Role, text string) *llm.Message {
	m := llm.TextMessage(role, text)
	return &m
}

// A call and its result are one thing that happened. The log has them as
// two events, and a view that prints two lines makes the reader do the
// join every time.
func TestFoldPairsACallWithItsResult(t *testing.T) {
	l := Fold(sample(time.Now()))
	var tools []Record
	for _, r := range l.All {
		if r.Kind == KindTool {
			tools = append(tools, r)
		}
	}
	if len(tools) != 2 {
		t.Fatalf("want 2 tool records, got %d: %+v", len(tools), l.All)
	}
	if tools[0].Result == "" || tools[0].Running {
		t.Fatalf("the first call never got its result: %+v", tools[0])
	}
	if tools[0].Duration != 2*time.Second {
		t.Fatalf("duration = %s, want 2s", tools[0].Duration)
	}
	if !strings.Contains(tools[0].Preview, "go test") {
		t.Fatalf("the arguments are not previewed: %q", tools[0].Preview)
	}
}

// A cancelled turn should look cancelled, not shorter: a call whose
// result never arrived stays on the ledger.
func TestFoldKeepsACallThatNeverAnswered(t *testing.T) {
	base := time.Now()
	ev := sample(base)[:6] // through the first result, then the second call alone
	ev = append(ev, session.Event{Seq: 7, Time: at(base, 2920), Type: session.TypeToolCall,
		Turn: 1, Step: 1, CallID: "c2", Name: "bash", Arguments: `{"command":"sleep 100"}`})
	l := Fold(ev)
	last := l.All[len(l.All)-1]
	if last.Kind != KindTool || !last.Running {
		t.Fatalf("the unanswered call is not marked running: %+v", last)
	}
}

// The turn/step tree is what makes a long session readable.
func TestFoldGroupsByTurnAndStep(t *testing.T) {
	l := Fold(sample(time.Now()))
	if len(l.Turns) != 1 {
		t.Fatalf("turns = %d", len(l.Turns))
	}
	titles := []string{}
	for _, g := range l.Turns[0].Groups {
		titles = append(titles, g.Title)
	}
	want := []string{"Message", "Step 1", "Step 2"}
	if strings.Join(titles, ",") != strings.Join(want, ",") {
		t.Fatalf("groups = %v, want %v", titles, want)
	}
	step1 := l.Turns[0].Groups[1]
	if got := step1.Tools(); got != "bash×2" {
		t.Fatalf("tool histogram = %q", got)
	}
	if span := step1.Span(); span < 3*time.Second {
		t.Fatalf("step 1 span = %s, want the whole ~3.1s of it", span)
	}
	steps, tools := l.Turns[0].Calls()
	if steps != 2 || tools != 2 {
		t.Fatalf("calls = %d steps, %d tools", steps, tools)
	}
}

// Averages hide provider latency. The split between waiting for the
// first token and generating the rest is the whole diagnosis.
func TestFoldKeepsTheTimingSplitAndTheTokenBuckets(t *testing.T) {
	l := Fold(sample(time.Now()))
	var first Record
	for _, r := range l.All {
		if r.Kind == KindMessage {
			first = r
			break
		}
	}
	if first.TTFT != 400*time.Millisecond || first.Decode != 500*time.Millisecond {
		t.Fatalf("timing split lost: %+v", first)
	}
	tps, ok := first.Throughput()
	if !ok || tps < 99 || tps > 101 {
		t.Fatalf("throughput = %v %v, want 50 tokens over 0.5s", tps, ok)
	}
	if first.Usage.Input != 100 || first.Usage.CacheRead != 900 {
		t.Fatalf("the cached share is folded into input: %+v", first.Usage)
	}
	if first.Text != "(tool calls only)" {
		t.Fatalf("a step that only dispatched tools reads as blank: %q", first.Text)
	}
	// The running total is on every line, so any line answers what the
	// session had cost by there.
	last := l.All[len(l.All)-1]
	if last.Cumulative.Output != 80 || last.Cumulative.CacheRead != 2100 {
		t.Fatalf("cumulative = %+v", last.Cumulative)
	}
	if l.Usage() != last.Cumulative {
		t.Fatalf("the ledger total is not the last cumulative")
	}
}

func TestPreviewCollapsesAndClips(t *testing.T) {
	if got := preview("  a\n\n  b\tc  "); got != "a b c" {
		t.Fatalf("preview = %q", got)
	}
	long := strings.Repeat("x", previewSource*3)
	got := preview(long)
	if n := len([]rune(got)); n != previewChars+1 {
		t.Fatalf("a long body previewed to %d runes", n)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("a clipped preview does not say so: %q", got[len(got)-10:])
	}
}

func TestFoldOfNothing(t *testing.T) {
	l := Fold(nil)
	if len(l.All) != 0 || len(l.Turns) != 0 {
		t.Fatalf("an empty log folded to %+v", l)
	}
	if _, ok := l.At(1); ok {
		t.Fatal("At found a record in an empty ledger")
	}
}

// The raw JSON of a tool call spends the line on punctuation: every row
// starts with the same `{"command":"` and the part that differs is what
// gets clipped off the end.
func TestArgumentsAreSummarisedNotDumped(t *testing.T) {
	got := argSummary(`{"command":"go test ./...","timeout_ms":120000}`)
	if got != "go test ./... timeout_ms=120000" {
		t.Fatalf("bash args = %q", got)
	}
	// A file's contents are reported by size, so a short file and a long
	// one look like the same tool.
	got = argSummary(`{"path":"a.go","content":"package a\n"}`)
	if !strings.HasPrefix(got, "a.go content=") || strings.Contains(got, "package a") {
		t.Fatalf("write args = %q", got)
	}
	// A malformed call is exactly what a ledger is read to find, so it
	// is previewed rather than dropped.
	if got := argSummary(`not json at all`); got != "not json at all" {
		t.Fatalf("malformed args = %q", got)
	}
	if got := argSummary(``); got != "" {
		t.Fatalf("empty args = %q", got)
	}
	// A nested value has no useful one-line form, so only its name shows.
	if got := argSummary(`{"todos":[{"text":"a"}]}`); !strings.HasPrefix(got, "todos=") {
		t.Fatalf("nested args = %q", got)
	}
}

// A refused or recovered call can reach the log as a result with no
// call event and sometimes no id. It used to produce a record with no
// name, no arguments and "→ (no output)": a line that says nothing.
func TestFoldDoesNotInventEmptyRecords(t *testing.T) {
	base := time.Now()
	ev := []session.Event{
		{Seq: 1, Time: at(base, 0), Type: session.TypeUserMessage, Turn: 1, Message: msg(llm.RoleUser, "go")},
		{Seq: 2, Time: at(base, 1), Type: session.TypeStepStart, Turn: 1, Step: 1},
		{Seq: 3, Time: at(base, 2), Type: session.TypeToolResult, Turn: 1, Step: 1,
			Name: "read_file", Output: `Unknown tool "read_file"`, IsError: true},
		{Seq: 4, Time: at(base, 3), Type: session.TypeToolResult, Turn: 1, Step: 1},
	}
	l := Fold(ev)
	for _, r := range l.All {
		if strings.TrimSpace(Content(r)) == "→ (no output)" {
			t.Fatalf("a record of nothing: %+v", r)
		}
	}
	tools := 0
	for _, r := range l.All {
		if r.Kind == KindTool {
			tools++
		}
	}
	if tools != 1 {
		t.Fatalf("want one tool record for one result, got %d: %+v", tools, l.All)
	}
}

// A result whose call event is in the log but whose id did not survive
// still finds its call rather than becoming a second record.
func TestFoldAttachesAnIdlessResultToTheWaitingCall(t *testing.T) {
	base := time.Now()
	ev := []session.Event{
		{Seq: 1, Time: at(base, 0), Type: session.TypeStepStart, Turn: 1, Step: 1},
		{Seq: 2, Time: at(base, 1), Type: session.TypeToolCall, Turn: 1, Step: 1,
			CallID: "c1", Name: "bash", Arguments: `{"command":"ls"}`},
		{Seq: 3, Time: at(base, 51), Type: session.TypeToolResult, Turn: 1, Step: 1,
			Name: "bash", Output: "a.txt"},
	}
	l := Fold(ev)
	if len(l.All) != 1 {
		t.Fatalf("want one record, got %d: %+v", len(l.All), l.All)
	}
	if r := l.All[0]; r.Running || r.Result != "a.txt" || r.Duration != 50*time.Millisecond {
		t.Fatalf("the result did not reach its call: %+v", r)
	}
}

// A step's tool results also go on the log as one surface message, the
// history the model is sent: no call id, no name, no output. A call that
// was cancelled mid-run has no result of its own, and that message was
// taken for its answer — the call read "(no output)", finished, instead
// of the cancelled call it was.
func TestFoldTheStepsResultMessageAnswersNoCall(t *testing.T) {
	base := time.Now()
	tr := llm.ToolResultMessage(llm.ToolResult("c1", "Cancelled by user before the tool finished.", true))
	ev := []session.Event{
		{Seq: 1, Time: at(base, 0), Type: session.TypeStepStart, Turn: 1, Step: 1},
		{Seq: 2, Time: at(base, 1), Type: session.TypeToolCall, Turn: 1, Step: 1,
			CallID: "c1", Name: "bash", Arguments: `{"command":"sleep 100"}`},
		{Seq: 3, Time: at(base, 900), Type: session.TypeToolResult, Turn: 1, Step: 1,
			Surface: true, Message: &tr},
	}
	l := Fold(ev)
	if len(l.All) != 1 || !l.All[0].Running {
		t.Fatalf("the cancelled call no longer reads as unanswered: %+v", l.All)
	}
}
