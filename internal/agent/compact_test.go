package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestCompactWritesShadowedLogEvents(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(`{"facts":["done"]}`)}}
	mem := &session.Memory{}
	u := llm.TextMessage(llm.RoleUser, strings.Repeat("please investigate the flaky test suite in detail ", 80))
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Log: mem, Messages: []llm.Message{
		u,
		llm.TextMessage(llm.RoleAssistant, "working"),
	}}
	_, _ = mem.Append(session.Event{Type: session.TypeUserMessage, Surface: true, Message: &a.Messages[0]})
	_, _ = mem.Append(session.Event{Type: session.TypeAssistant, Surface: true, Message: &a.Messages[1]})
	if err := a.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	ev := mem.Events()
	var sawStart, sawSum, sawEnd bool
	for _, e := range ev {
		switch e.Type {
		case session.TypeCompactStart:
			sawStart = true
		case session.TypeCompactSummary:
			sawSum = true
			if len(e.Shadowed) == 0 {
				t.Fatal("summary has no shadowed seqs")
			}
		case session.TypeCompactEnd:
			sawEnd = true
		}
	}
	if !sawStart || !sawSum || !sawEnd {
		t.Fatalf("compact events missing in %+v", ev)
	}
	if n := len(session.FoldSurface(ev)); n < 1 {
		t.Fatalf("surface after compact = %d", n)
	}
}

func TestCompactKeepsPendingUserText(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(`{"facts":["SUMMARY: did things"]}`)}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"),
		llm.TextMessage(llm.RoleAssistant, "ok"),
		llm.TextMessage(llm.RoleUser, "second request"),
	}}
	var evs []Event
	if err := a.Compact(context.Background(), func(e Event) {
		if e.Kind != EvPhase { // a phase is a status line, not a loop event
			evs = append(evs, e)
		}
	}); err != nil {
		t.Fatal(err)
	}
	req := f.requests[0]
	// The summary call replays the conversation's own prefix (system prompt,
	// tool schemas, the head verbatim) so the provider serves it from cache;
	// the instruction rides in the last user message.
	if req.System != a.systemPrompt() || len(req.Tools) != len(a.Tools.Defs()) || len(req.Messages) != 3 || !strings.Contains(req.Messages[2].Text(), "Previous working memory") {
		t.Fatalf("compaction request = %+v", req)
	}
	if len(a.Messages) != 1 || a.Messages[0].Role != llm.RoleUser || len(a.Messages[0].Blocks) != 2 {
		t.Fatalf("messages = %+v", a.Messages)
	}
	if !strings.Contains(a.Messages[0].Blocks[0].Text, "SUMMARY: did things") || a.Messages[0].Blocks[1].Text != "second request" {
		t.Fatalf("blocks = %+v", a.Messages[0].Blocks)
	}
	if len(evs) != 1 || evs[0].Kind != EvCompacted {
		t.Fatalf("events = %+v", evs)
	}
	if err := (&Agent{Provider: f, Tools: tool.NewRegistry()}).Compact(context.Background(), nil); !errors.Is(err, ErrNothingToCompact) {
		t.Fatalf("err = %v", err)
	}
}

func TestCompactFastDropsOldThinkingWithoutASummary(t *testing.T) {
	f := &fake{}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "one"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: strings.Repeat("reason ", 80)},
			{Type: llm.BlockText, Text: "did one"},
		}},
		llm.TextMessage(llm.RoleUser, "two"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockThinking, Text: "keep me"},
			{Type: llm.BlockText, Text: "did two"},
		}},
	}}
	var note string
	if err := a.CompactFast(context.Background(), func(e Event) {
		if e.Kind == EvCompacted {
			note = e.Text
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 0 {
		t.Fatalf("compact-fast paid for a summary: %d calls", len(f.requests))
	}
	if !strings.Contains(note, "no summary") || !strings.Contains(note, "1 thinking") {
		t.Fatalf("note = %q", note)
	}
	if n := len(a.Messages[1].Blocks); n != 1 || a.Messages[1].Blocks[0].Text != "did one" {
		t.Fatalf("old thinking still there: %+v", a.Messages[1].Blocks)
	}
	if n := len(a.Messages[3].Blocks); n != 2 || a.Messages[3].Blocks[0].Text != "keep me" {
		t.Fatalf("last thinking should stay: %+v", a.Messages[3].Blocks)
	}
}

func TestAutoCompactBeforeCallAndOnOverflow(t *testing.T) {
	big := strings.Repeat("x", 40_000) // ~10k tokens
	f := &fake{responses: []*llm.Response{
		text(`{"next":["note A"]}`), // compaction triggered by estimate
		text("answer one"),          // real call
		nil,                         // overflow error (errs below)
		text(`{"next":["note B"]}`), // compaction triggered by overflow
		text("answer two"),          // retry
	}, errs: []error{nil, nil, errors.New("400: prompt is too long: 210000 tokens > 200000 maximum")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m"}
	a.CompactAt = 7_000
	a.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, big), llm.TextMessage(llm.RoleAssistant, "ok")}
	var kinds []EventKind
	emit := func(e Event) {
		if e.Kind != EvPhase {
			kinds = append(kinds, e.Kind)
		}
	}
	if err := a.Run(context.Background(), "q1", emit); err != nil {
		t.Fatal(err)
	}
	// note, kept tail ("ok"), q1, answer one.
	if kinds[0] != EvCompacted || len(a.Messages) != 4 || a.Messages[1].Text() != "ok" || a.Messages[3].Text() != "answer one" {
		t.Fatalf("kinds=%v messages=%+v", kinds, a.Messages)
	}
	if !strings.Contains(a.Messages[0].Text(), "note A") || a.Messages[2].Text() != "q1" {
		t.Fatalf("compacted messages = %+v", a.Messages)
	}
	a.CompactAt = 1_000_000 // estimate says fine; the provider will disagree
	kinds = nil
	if err := a.Run(context.Background(), "q2", emit); err != nil {
		t.Fatal(err)
	}
	if kinds[0] != EvCompacted || a.Messages[len(a.Messages)-1].Text() != "answer two" || len(f.requests) != 5 {
		t.Fatalf("kinds=%v requests=%d last=%q", kinds, len(f.requests), a.Messages[len(a.Messages)-1].Text())
	}
	if !strings.Contains(a.Messages[0].Text(), "note B") {
		t.Fatalf("second compaction missing: %q", a.Messages[0].Text())
	}
}

func TestCompactMidTurnMarksContinuation(t *testing.T) {
	// Nothing precedes the interrupted turn, so there is no recent pair to
	// keep: the summary stands in for the whole turn and says so.
	f := &fake{responses: []*llm.Response{text("note")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		toolCall("t1", "read_file", `{}`).Message,
		llm.ToolResultMessage(llm.ToolResult("t1", "big output", false)),
	}}
	if err := a.Compact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.Messages[0].Text(), "interrupted mid-task") {
		t.Fatalf("missing continuation marker: %q", a.Messages[0].Text())
	}
}

// A compaction that would keep nothing recent keeps the last assistant
// message and everything after it instead. Otherwise the result that had
// just arrived is replaced by a summary written before it existed, and the
// model asks for it again — which is what a threshold low enough to
// compact after every tool call did.
func TestCompactKeepsTheLastExchange(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("note")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "do it"),
		toolCall("t1", "read_file", `{}`).Message,
		llm.ToolResultMessage(llm.ToolResult("t1", "big output", false)),
	}}
	var kept string
	if err := a.Compact(context.Background(), func(e Event) {
		if e.Kind == EvCompacted {
			kept = e.Text
		}
	}); err != nil {
		t.Fatal(err)
	}
	if len(a.Messages) != 3 || a.Messages[2].Blocks[0].Text != "big output" {
		t.Fatalf("messages = %+v", a.Messages)
	}
	if strings.Contains(kept, "(0 recent") {
		t.Fatalf("kept nothing: %q", kept)
	}
}

// A threshold below the fixed cost of a request is raised, once, with a
// notice: obeying it would compact before there is anything to compact.
func TestCompactAtBelowTheFloorIsRaised(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("hello")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", System: strings.Repeat("prompt ", 400)}
	a.CompactAt = 900
	floor := a.MinCompactAt()
	if floor <= 900 {
		t.Fatalf("floor = %d", floor)
	}
	var notes []string
	emit := func(e Event) {
		if e.Kind == EvNotice {
			notes = append(notes, e.Text)
		}
	}
	if err := a.Run(context.Background(), "hi", emit); err != nil {
		t.Fatal(err)
	}
	if a.CompactAt != floor || a.CompactThreshold() != floor {
		t.Fatalf("CompactAt = %d, want %d", a.CompactAt, floor)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "below the") {
		t.Fatalf("notes = %v", notes)
	}
	// Nothing was summarised: the fake had one response, the answer.
	if len(f.requests) != 1 {
		t.Fatalf("requests = %d", len(f.requests))
	}
	// The notice is said once per session, not once per turn.
	f.responses = append(f.responses, text("again"))
	notes = nil
	if err := a.Run(context.Background(), "again", emit); err != nil {
		t.Fatal(err)
	}
	if len(notes) != 0 {
		t.Fatalf("notice repeated: %v", notes)
	}
}

func TestEstimateTokensUsesRealUsage(t *testing.T) {
	f := &fake{responses: []*llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "a"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 9000, CacheReadTokens: 1000}}}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m"}
	a.CompactAt = -1
	if err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if est := a.EstimateTokens(); est < 10000 || est > 10100 {
		t.Fatalf("estimate = %d", est)
	}
	if !isContextOverflow(errors.New("context_length_exceeded")) || isContextOverflow(errors.New("rate limited")) {
		t.Fatal("overflow detection wrong")
	}
}

func TestPruneDedupesReadsAndTrimsOldOutput(t *testing.T) {
	root := t.TempDir()
	reg := tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root})
	long := strings.Repeat("y", 10_000)
	use := func(id, name, input string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}}
	}
	res := func(id, text string) llm.Message { return llm.ToolResultMessage(llm.ToolResult(id, text, false)) }
	a := &Agent{Provider: &fake{}, Tools: reg, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		use("1", "read_file", `{"path":"a.go"}`), res("1", "first read "+long),
		use("2", "write_file", `{"path":"b.go","content":"x"}`), res("2", "wrote "+long),
		use("3", "read_file", `{ "path" : "a.go" }`), res("3", "second read "+long),
		use("4", "read_file", `{"path":"c.go"}`), res("4", "c "+long),
		llm.TextMessage(llm.RoleAssistant, "done"),
		llm.TextMessage(llm.RoleUser, "next"),
	}}
	before := a.EstimateTokens()
	if saved := a.Prune(); saved < 17_000 {
		t.Fatalf("saved %d bytes", saved)
	}
	get := func(i int) string { return a.Messages[i].Blocks[0].Text }
	if get(2) != superseded {
		t.Fatalf("first read should be superseded: %q", get(2)[:40])
	}
	if !strings.HasPrefix(get(4), "wrote ") || len(get(4)) > pruneKeep+100 || !strings.Contains(get(4), "omitted") {
		t.Fatalf("old write result should be trimmed: len=%d", len(get(4)))
	}
	// The recent tail (the last six messages, from an assistant call on)
	// is untouched, including the newest read of a.go.
	if len(get(6)) != len("second read "+long) || len(get(8)) != len("c "+long) {
		t.Fatalf("tail results were modified: %d %d", len(get(6)), len(get(8)))
	}
	if a.EstimateTokens() >= before {
		t.Fatal("estimate did not drop")
	}
}

func TestCompactKeepsRecentTail(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("SUMMARY")}}
	big := strings.Repeat("z", 8000)
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, big),
		llm.TextMessage(llm.RoleAssistant, "old answer"),
		llm.TextMessage(llm.RoleUser, "second question"),
		llm.TextMessage(llm.RoleAssistant, "recent answer"),
		llm.TextMessage(llm.RoleUser, "pending"),
	}}
	a.CompactAt = 100
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// note, the recent tail from the earliest assistant message that fits,
	// then the pending user text.
	if len(a.Messages) != 5 || !strings.Contains(a.Messages[0].Text(), "SUMMARY") || a.Messages[1].Text() != "old answer" || a.Messages[3].Text() != "recent answer" || a.Messages[4].Text() != "pending" {
		t.Fatalf("messages = %+v", a.Messages)
	}
	// The summariser saw only the head.
	if got := len(f.requests[0].Messages); got != 2 { // big + instruction
		t.Fatalf("summariser got %d messages", got)
	}
}

func TestOverflowLowersCompactThreshold(t *testing.T) {
	for msg, want := range map[string]int{
		"400: prompt is too long: 210000 tokens > 200000 maximum":                           200000,
		"This model's maximum context length is 32768 tokens. However, you requested 40000": 32768,
		"rate limited": 0,
	} {
		if got := windowFromError(errors.New(msg)); got != want {
			t.Errorf("%q -> %d, want %d", msg, got, want)
		}
	}
	f := &fake{responses: []*llm.Response{nil, text("note"), text("ok")},
		errs: []error{errors.New("maximum context length is 32768 tokens")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("x", 4000)), llm.TextMessage(llm.RoleAssistant, "old")}}
	if err := a.Run(context.Background(), "q", nil); err != nil {
		t.Fatal(err)
	}
	if a.CompactAt != 32768*3/4 || a.CompactThreshold() != 24576 {
		t.Fatalf("CompactAt = %d", a.CompactAt)
	}
}

func TestCompactThresholdFromWindow(t *testing.T) {
	cases := []struct{ at, window, want int }{
		{0, 0, DefaultCompactAt},
		{0, 128_000, 96_000},
		{0, 1_000_000, MaxAutoCompactAt},
		{0, 4_000, 8_000},
		{50_000, 1_000_000, 50_000},
		{-1, 128_000, 0},
	}
	for _, c := range cases {
		a := &Agent{}
		a.CompactAt, a.ContextWindow = c.at, c.window
		if got := a.CompactThreshold(); got != c.want {
			t.Errorf("CompactAt=%d window=%d: got %d want %d", c.at, c.window, got, c.want)
		}
	}
}

func TestPruneToCondensesOldestResultsFirst(t *testing.T) {
	root := t.TempDir()
	reg := tool.NewRegistry(tool.ReadFile{Root: root}, tool.Bash{Root: root})
	long := strings.Repeat("line of output\n", 400) // ~6k bytes
	use := func(id, name, input string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}}
	}
	res := func(id, text string) llm.Message { return llm.ToolResultMessage(llm.ToolResult(id, text, false)) }
	a := &Agent{Provider: &fake{}, Tools: reg, Archive: NewArchive(t.TempDir()), Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		use("1", "bash", `{"command":"go test ./..."}`), res("1", long+"FAIL\tpkg\n[exit code 1]"),
		use("2", "read_file", `{"path":"a.go"}`), res("2", long),
		use("3", "grep", `{"pattern":"TODO","path":"."}`), res("3", long),
		use("4", "read_file", `{"path":"b.go"}`), res("4", long),
		llm.TextMessage(llm.RoleAssistant, "done"),
		llm.TextMessage(llm.RoleUser, "next"),
	}}
	before := a.EstimateTokens()
	target := before - 2_000
	replaced := a.pruneTo(target)
	if replaced == 0 || a.EstimateTokens() > target {
		t.Fatalf("replaced %d, estimate %d > target %d", replaced, a.EstimateTokens(), target)
	}
	get := func(i int) string { return a.Messages[i].Blocks[0].Text }
	if !strings.HasPrefix(get(2), `[pruned: bash "go test ./..." → exit 1; last line: FAIL`) || !strings.Contains(get(2), `expand_output {"id":`) {
		t.Fatalf("bash placeholder: %q", get(2))
	}
	if replaced == 1 {
		if !strings.HasPrefix(get(4), "line of output") {
			t.Fatal("second result should be untouched when the first was enough")
		}
	} else if !strings.HasPrefix(get(4), "[pruned: read_file a.go: 400 lines were shown") {
		t.Fatalf("read placeholder: %q", get(4))
	}
	// The tail (from the last assistant text on) and anything past the
	// target stay verbatim; a second pass at the same target is a no-op.
	if len(get(8)) != len(long) {
		t.Fatalf("tail result modified: %d", len(get(8)))
	}
	if again := a.pruneTo(target); again != 0 {
		t.Fatalf("second prune replaced %d", again)
	}
	// The archive still has the full text.
	id := a.Archive.IDFor("1")
	if id == "" {
		t.Fatal("bash output not archived")
	}
	// Placeholders for the other tools.
	if got := placeholder("grep", json.RawMessage(`{"pattern":"x"}`), "a\nb\n", ""); got != `[pruned: grep "x": 2 matching lines]` {
		t.Fatalf("grep: %q", got)
	}
	if got := placeholder("mcp__x__y", nil, "first line here\nmore", ""); got != "[pruned: mcp__x__y: first line here]" {
		t.Fatalf("default: %q", got)
	}
}

func TestPruneToFollowsAttention(t *testing.T) {
	root := t.TempDir()
	reg := tool.NewRegistry(tool.ReadFile{Root: root}, tool.EditFile{Root: root}, tool.Bash{Root: root})
	long := strings.Repeat("line of output\n", 400)
	use := func(id, name, input string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}}
	}
	res := func(id, text string) llm.Message { return llm.ToolResultMessage(llm.ToolResult(id, text, false)) }
	a := &Agent{Provider: &fake{}, Tools: reg, Archive: NewArchive(t.TempDir()), Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		use("1", "bash", `{"command":"go test ./..."}`), res("1", long+"FAIL\n[exit code 1]"),
		use("2", "read_file", `{"path":"pkg/a.go"}`), res("2", long),
		use("3", "grep", `{"pattern":"TODO","path":"."}`), res("3", long),
		use("4", "edit_file", `{"path":"pkg/a.go","old_string":"x","new_string":"y"}`), res("4", "edited"),
		use("5", "bash", `{"command":"go test ./..."}`), res("5", long+"ok\n[exit code 0]"),
		llm.TextMessage(llm.RoleAssistant, "done"),
		llm.TextMessage(llm.RoleUser, "next"),
	}}
	get := func(i int) string { return a.Messages[i].Blocks[0].Text }
	// One step at a time: the stale first test run goes first, then the
	// grep nothing came back to; the read of the file that was edited
	// afterwards is the most attended and goes last.
	step := func() { a.pruneTo(a.EstimateTokens() - 1_000) }
	step()
	if !strings.HasPrefix(get(2), "[pruned: bash") || strings.HasPrefix(get(4), "[pruned") || strings.HasPrefix(get(6), "[pruned") {
		t.Fatalf("after 1: %q / %q / %q", get(2)[:30], get(4)[:20], get(6)[:20])
	}
	step()
	if !strings.HasPrefix(get(6), "[pruned: grep") || strings.HasPrefix(get(4), "[pruned") {
		t.Fatalf("after 2: %q / %q", get(6)[:30], get(4)[:20])
	}
	step()
	if !strings.HasPrefix(get(4), "[pruned: read_file") {
		t.Fatalf("after 3: %q", get(4)[:30])
	}
	if strings.HasPrefix(get(10), "[pruned") {
		t.Fatal("the latest test run is still current and should be pruned last")
	}
	if got := subjectsOf("read_file", json.RawMessage(`{"path":"src/","limit":3}`)); len(got) != 1 || got[0] != "src" {
		t.Fatalf("subjects = %v", got)
	}
}

func TestCompactCooldownAfterFailedSummary(t *testing.T) {
	f := &fake{errs: []error{errors.New("429 rate limited")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m"}
	a.CompactAt = 1_000
	for i := 0; i < 20; i++ {
		a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, strings.Repeat("words and more words ", 60)), llm.TextMessage(llm.RoleAssistant, "ok"))
	}
	err := a.Compact(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("first compact err = %v", err)
	}
	if len(f.requests) != 1 || a.summaryFailedAt.IsZero() {
		t.Fatalf("requests %d, cooldown set %v", len(f.requests), !a.summaryFailedAt.IsZero())
	}
	var notes []string
	if err := a.Compact(context.Background(), func(e Event) {
		if e.Kind == EvNotice {
			notes = append(notes, e.Text)
		}
	}); err != nil {
		t.Fatalf("compact during cooldown: %v", err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("summary called during cooldown: %d requests", len(f.requests))
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "summary skipped") {
		t.Fatalf("notes = %v", notes)
	}
	// An explicit /compact ignores the cooldown, and success clears it.
	f.responses = []*llm.Response{nil, text("handoff note")} // indexed by request
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 || !a.summaryFailedAt.IsZero() {
		t.Fatalf("requests %d, cooldown cleared %v", len(f.requests), a.summaryFailedAt.IsZero())
	}
}

// The fold of the log must equal the history the model is being sent, and
// compaction must not append a second copy of every message it keeps.
func TestCompactFoldMatchesHistoryWithoutRelogging(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(`{"facts":["done"]}`)}}
	mem := &session.Memory{}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Log: mem}
	for i := 0; i < 6; i++ {
		a.Messages = append(a.Messages,
			llm.TextMessage(llm.RoleUser, strings.Repeat("investigate the flaky suite ", 60)),
			llm.TextMessage(llm.RoleAssistant, "working on it"))
	}
	for i := range a.Messages {
		typ := session.TypeUserMessage
		if a.Messages[i].Role == llm.RoleAssistant {
			typ = session.TypeAssistant
		}
		if _, err := mem.Append(session.Event{Type: typ, Surface: true, Message: &a.Messages[i]}); err != nil {
			t.Fatal(err)
		}
	}
	before := len(mem.Events())
	if err := a.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	got := session.FoldSurface(mem.Events())
	if len(got) != len(a.Messages) {
		t.Fatalf("fold = %d messages, history = %d", len(got), len(a.Messages))
	}
	for i := range got {
		if got[i].Text() != a.Messages[i].Text() {
			t.Fatalf("message %d: fold %q history %q", i, textfmt.Clip(strings.TrimSpace(got[i].Text()), 40), textfmt.Clip(strings.TrimSpace(a.Messages[i].Text()), 40))
		}
	}
	// start + summary + replacement + end, and nothing per kept message.
	if added := len(mem.Events()) - before; added > 5 {
		t.Fatalf("compaction re-logged the kept tail: %d new events", added)
	}
}

// Pruning rewrites tool results in place; the log has to learn about it or
// a resumed session folds back to the un-pruned history.
func TestPrunedMessagesAreLogged(t *testing.T) {
	mem := &session.Memory{}
	a := &Agent{Tools: tool.NewRegistry(), Model: "m", Log: mem, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		llm.TextMessage(llm.RoleAssistant, "sure"),
	}}
	for i := range a.Messages {
		typ := session.TypeUserMessage
		if a.Messages[i].Role == llm.RoleAssistant {
			typ = session.TypeAssistant
		}
		_, _ = mem.Append(session.Event{Type: typ, Surface: true, Message: &a.Messages[i]})
	}
	a.Messages[1].Blocks[0].Text = "sure (condensed)"
	a.syncSurface()
	got := session.FoldSurface(mem.Events())
	if len(got) != 2 || got[1].Text() != "sure (condensed)" {
		t.Fatalf("fold = %+v", got)
	}
	// Nothing changed since: no further events.
	n := len(mem.Events())
	a.syncSurface()
	if len(mem.Events()) != n {
		t.Fatalf("syncSurface logged an unchanged message")
	}
}

// A model that answers the warm-prefix summary request with a tool call
// instead of text gets one cold retry, and both calls are billed.
func TestSummaryRetriesColdWhenModelCallsTool(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"a.txt"}`),
		text(`{"facts":["done"]}`),
	}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}), Model: "m", System: "sys", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("investigate the flaky suite ", 100)),
		llm.TextMessage(llm.RoleAssistant, "working"),
	}}
	if err := a.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 2 {
		t.Fatalf("%d summary requests, want a warm call and a cold retry", len(f.requests))
	}
	warm, cold := f.requests[0], f.requests[1]
	if warm.System != a.systemPrompt() || len(warm.Tools) != 1 {
		t.Fatalf("warm call: system %q tools %d", warm.System, len(warm.Tools))
	}
	if !strings.HasPrefix(cold.System, "You are compacting") || len(cold.Tools) != 0 {
		t.Fatalf("cold retry: system %q tools %d", cold.System[:min(40, len(cold.System))], len(cold.Tools))
	}
	if a.Usage.InputTokens != 2 {
		t.Fatalf("usage = %+v, want both calls billed", a.Usage)
	}
	if !strings.Contains(a.Messages[0].Text(), "done") {
		t.Fatalf("summary not applied: %q", a.Messages[0].Text())
	}
}

// Once an earlier summary wrote the model-maintained sections, a head the
// harness observed richly — edits and commands, no new user request — is
// compacted without a summary call.
func TestRichHeadSkipsSummaryCall(t *testing.T) {
	f := &fake{} // any call fails: no responses scripted
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m"}
	a.working.Decisions = []string{"use the token bucket"}
	a.working.Next = []string{"wire the limiter"}
	a.Messages = []llm.Message{llm.TextMessage(llm.RoleUser, "[Context was compacted. Working memory for this session:]\nGoal: limiter")}
	// Twelve edits: the recent tail keeps the last few verbatim, and the
	// head that remains must still hold richSlots worth of observed work.
	for i := 0; i < 12; i++ {
		a.Messages = append(a.Messages,
			llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: fmt.Sprintf("e%d", i), Name: "edit_file", Input: json.RawMessage(fmt.Sprintf(`{"path":"f%d.go","old_str":"a","new_str":"b"}`, i))}}},
			llm.ToolResultMessage(llm.ToolResult(fmt.Sprintf("e%d", i), strings.Repeat("edited f.go ok\n", 40), false)))
	}
	if err := a.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 0 {
		t.Fatalf("summary call made for a rich head: %d requests", len(f.requests))
	}
	note := a.Messages[0].Text()
	if !strings.Contains(note, "use the token bucket") || !strings.Contains(note, "wire the limiter") || !strings.Contains(note, "f0.go") {
		t.Fatalf("carried record lost content:\n%s", note)
	}
	// The switch restores the old behaviour.
	b := &Agent{Provider: &fake{}, Tools: tool.NewRegistry(), Model: "m", Off: map[string]bool{"richskip": true}, Messages: append([]llm.Message(nil), a.Messages...)}
	b.working = a.working
	if err := b.ForceCompact(context.Background(), func(Event) {}); err == nil {
		t.Fatal("richskip off: expected the summary call (and its scripted failure)")
	}
}

// The first compaction, or one whose head carries a new user request,
// always asks the model: the record has nothing to carry forward yet.
func TestRichSkipNeedsAnEarlierSummaryAndNoNewAsk(t *testing.T) {
	var w WorkingMemory
	head := []llm.Message{llm.TextMessage(llm.RoleUser, "please fix it")}
	for i := 0; i < 10; i++ {
		head = append(head, llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: fmt.Sprint(i), Name: "bash", Input: json.RawMessage(`{"command":"go test"}`)}}})
	}
	if w.rich(head) {
		t.Fatal("rich without any model-maintained sections")
	}
	w.Next = []string{"x"}
	if w.rich(head) {
		t.Fatal("rich despite a new user request in the head")
	}
	if !w.rich(head[1:]) {
		t.Fatal("not rich with 10 commands and a record to carry")
	}
}

// What compaction drops is archived first, so the note can point at it
// and expand_output can search it.
func TestCompactArchivesTheHead(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(`{"facts":["done"]}`)}}
	ar := NewArchive(t.TempDir())
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Archive: ar, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("please investigate the flaky suite ", 80)),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "c1", Name: "bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}}},
		llm.ToolResultMessage(llm.ToolResult("c1", "--- FAIL: TestParse (0.00s)\n    parse_test.go:17: got 3 want 4\n[exit code 1]", true)),
		llm.TextMessage(llm.RoleAssistant, "the parser is off by one"),
	}}
	// Enough later traffic that the failure sits in the head, not the
	// verbatim tail.
	for i := 0; i < 4; i++ {
		a.Messages = append(a.Messages, llm.TextMessage(llm.RoleUser, "go on"), llm.TextMessage(llm.RoleAssistant, "working"))
	}
	if err := a.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	note := a.Messages[0].Text()
	if !strings.Contains(note, "compacted conversation is archived as ctx-") {
		t.Fatalf("note does not point at the archive:\n%s", note)
	}
	entries := ar.List()
	if len(entries) != 1 || entries[0].Name != "compacted conversation" {
		t.Fatalf("archive = %+v", entries)
	}
	out := searchArchive(ar, nil, "why did TestParse fail", 0)
	if !strings.Contains(out, "got 3 want 4") || !strings.Contains(out, "go test ./...") {
		t.Fatalf("head not searchable:\n%s", out)
	}
}

// The compaction record is parsed, not read, so the call asks the
// provider to constrain the reply to the record's shape when it can.
func TestCompactionAsksForTheRecordShape(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(`{"goal":"g","facts":["done"]}`)}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("investigate the flaky suite ", 80)),
		llm.TextMessage(llm.RoleAssistant, "working"),
	}}
	if err := a.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	req := f.requests[0]
	if req.Schema == nil || req.Schema.Name != "working_memory" || !req.Schema.Strict {
		t.Fatalf("schema = %+v", req.Schema)
	}
	if !json.Valid(req.Schema.JSON) {
		t.Fatalf("schema is not valid JSON: %s", req.Schema.JSON)
	}
	// The prose path asks for no shape, since there is none to ask for.
	g := &fake{responses: []*llm.Response{text("a prose handoff note")}}
	b := &Agent{Provider: g, Tools: tool.NewRegistry(), Model: "m", Off: map[string]bool{"working": true}, Messages: a.Messages}
	b.Messages = []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("investigate the flaky suite ", 80)),
		llm.TextMessage(llm.RoleAssistant, "working"),
	}
	if err := b.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if g.requests[0].Schema != nil {
		t.Fatalf("prose summary asked for a schema: %+v", g.requests[0].Schema)
	}
}

// TestTouchedNoteSurvivesTheSummariser: which files have already been read
// looks unimportant to a summariser right up until the model reads them
// again. Under forced compaction a run re-read one file five times and the
// loop detector had to stop the turn, so the paths are taken from the tool
// calls rather than left to the summary.
func TestTouchedNoteSurvivesTheSummariser(t *testing.T) {
	call := func(name, path string) llm.Block {
		return llm.Block{Type: llm.BlockToolUse, Name: name, Input: json.RawMessage(`{"path":"` + path + `"}`)}
	}
	head := []llm.Message{
		{Role: llm.RoleAssistant, Blocks: []llm.Block{call("read_file", "a.go"), call("read_file", "b.go")}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{call("edit_file", "b.go"), call("write_file", "c.go")}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{call("bash", "")}},
	}
	got := touchedNote(head)
	if !strings.Contains(got, "Already read") || !strings.Contains(got, "a.go") {
		t.Fatalf("a file that was read must be named: %q", got)
	}
	if !strings.Contains(got, "Already changed by you") || !strings.Contains(got, "b.go") || !strings.Contains(got, "c.go") {
		t.Fatalf("changed files must be named: %q", got)
	}
	// b.go was read and then edited; listing it under both wastes the
	// space the note exists to save.
	if strings.Count(got, "b.go") != 1 {
		t.Fatalf("b.go should appear once, as changed: %q", got)
	}
	// Nothing to say means nothing added.
	if n := touchedNote([]llm.Message{{Role: llm.RoleAssistant, Blocks: []llm.Block{call("bash", "")}}}); n != "" {
		t.Fatalf("want an empty note, got %q", n)
	}
}

// An old call's long arguments (a whole file body) are resent on every call
// until a summary; pruning drops the long strings and keeps the path, and
// leaves the recent tail alone.
func TestPruneShrinksOldCallArguments(t *testing.T) {
	root := t.TempDir()
	reg := tool.NewRegistry(tool.WriteFile{Root: root})
	body, _ := json.Marshal(strings.Repeat("line\n", 2000))
	use := func(id string) llm.Message {
		return llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: "write_file", Input: json.RawMessage(`{"path":"b.go","content":` + string(body) + `}`)}}}
	}
	res := func(id string) llm.Message { return llm.ToolResultMessage(llm.ToolResult(id, "wrote b.go", false)) }
	a := &Agent{Provider: &fake{}, Tools: reg, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "go"),
		use("1"), res("1"),
		llm.TextMessage(llm.RoleAssistant, "done"),
		llm.TextMessage(llm.RoleUser, "again"),
		use("2"), res("2"),
		llm.TextMessage(llm.RoleAssistant, "done again"),
		llm.TextMessage(llm.RoleUser, "next"),
	}}
	if saved := a.Prune(); saved < 9_000 {
		t.Fatalf("saved %d bytes", saved)
	}
	var old struct{ Path, Content string }
	if err := json.Unmarshal(a.Messages[1].Blocks[0].Input, &old); err != nil {
		t.Fatalf("pruned input is not JSON: %v", err)
	}
	if old.Path != "b.go" || !strings.Contains(old.Content, "10000 characters omitted") {
		t.Fatalf("old input = %+v", old)
	}
	if n := len(a.Messages[5].Blocks[0].Input); n < 10_000 {
		t.Fatalf("the recent call's arguments were shrunk to %d bytes", n)
	}
}
