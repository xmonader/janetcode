package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// fake is a scripted provider. Each Stream call pops the next response and
// records the request it saw.
type fake struct {
	responses []*llm.Response
	errs      []error
	requests  []llm.Request
}

func (f *fake) Name() string { return "fake" }
func (f *fake) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	f.requests = append(f.requests, req)
	i := len(f.requests) - 1
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if i >= len(f.responses) {
		return nil, errors.New("fake: no more scripted responses")
	}
	r := f.responses[i]
	for _, b := range r.Message.Blocks {
		switch b.Type {
		case llm.BlockText:
			emit(llm.Event{Type: llm.EventTextDelta, Text: b.Text})
		case llm.BlockToolUse:
			bb := b
			emit(llm.Event{Type: llm.EventToolUseStart, Block: &bb})
			emit(llm.Event{Type: llm.EventToolUseEnd, Block: &bb})
		}
	}
	return r, nil
}

func text(s string) *llm.Response {
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, s), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}}
}

func toolCall(id, name, input string) *llm.Response {
	return &llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: json.RawMessage(input)}}},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	}
}

type panicTool struct{}

func (panicTool) Name() string                                         { return "boom" }
func (panicTool) Description() string                                  { return "panics" }
func (panicTool) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (panicTool) Mutating() bool                                       { return false }
func (panicTool) Run(context.Context, json.RawMessage) (string, error) { panic("kaboom") }

func newAgent(t *testing.T, f *fake, approver Approver) (*Agent, string) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644)
	return &Agent{
		Provider: f,
		Tools:    tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, panicTool{}),
		Approver: approver,
		System:   "sys",
		Model:    "m",
	}, root
}

func collect() (*[]Event, func(Event)) {
	var evs []Event
	return &evs, func(e Event) { evs = append(evs, e) }
}

func TestTextOnlyTurn(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("hi there")}}
	a, _ := newAgent(t, f, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "hello", emit); err != nil {
		t.Fatal(err)
	}
	if len(a.Messages) != 2 || a.Messages[1].Text() != "hi there" {
		t.Fatalf("messages = %+v", a.Messages)
	}
	req := f.requests[0]
	if req.System != "sys" || req.Model != "m" || len(req.Tools) != 3 {
		t.Fatalf("request = %+v", req)
	}
	last := (*evs)[len(*evs)-1]
	if last.Kind != EvTurnDone || last.StopReason != llm.StopEndTurn {
		t.Fatalf("last event = %+v", last)
	}
	if a.Usage.InputTokens != 1 {
		t.Fatalf("usage = %+v", a.Usage)
	}
}

func TestToolLoopReadThenAnswer(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"a.txt"}`),
		text("it says alpha"),
	}}
	a, _ := newAgent(t, f, nil)
	evs, emit := collect()
	if err := a.Run(context.Background(), "read a.txt", emit); err != nil {
		t.Fatal(err)
	}
	if len(a.Messages) != 4 {
		t.Fatalf("expected user/assistant/user(tool_result)/assistant, got %d", len(a.Messages))
	}
	tr := a.Messages[2].Blocks[0]
	if tr.Type != llm.BlockToolResult || tr.ToolUseID != "t1" || tr.IsError || !strings.Contains(tr.Text, "1\talpha") {
		t.Fatalf("tool result = %+v", tr)
	}
	// The second request must carry the whole history.
	if len(f.requests[1].Messages) != 3 {
		t.Fatalf("second request messages = %d", len(f.requests[1].Messages))
	}
	var kinds []EventKind
	for _, e := range *evs {
		kinds = append(kinds, e.Kind)
	}
	want := []EventKind{EvToolStart, EvTextBlockDone, EvToolRun, EvToolResult, EvText, EvTextBlockDone, EvTurnDone}
	if strings.Join(kindStrings(kinds), ",") != strings.Join(kindStrings(want), ",") {
		t.Fatalf("events = %v", kinds)
	}
}

func kindStrings(k []EventKind) []string {
	s := make([]string, len(k))
	for i, x := range k {
		s[i] = string(x)
	}
	return s
}

func TestUnknownToolAndPanicBecomeErrorResults(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "nope", Input: json.RawMessage(`{}`)},
			{Type: llm.BlockToolUse, ID: "t2", Name: "boom", Input: json.RawMessage(`{}`)},
		}}, StopReason: llm.StopToolUse},
		text("ok"),
	}}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	res := a.Messages[2].Blocks
	if len(res) != 2 || !res[0].IsError || !strings.Contains(res[0].Text, `Unknown tool "nope"`) {
		t.Fatalf("results = %+v", res)
	}
	if !res[1].IsError || !strings.Contains(res[1].Text, "panicked: kaboom") {
		t.Fatalf("panic result = %+v", res[1])
	}
}

func TestPermissionDenyAllowSession(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"1"}`),
		toolCall("t2", "write_file", `{"path":"y","content":"2"}`),
		toolCall("t3", "write_file", `{"path":"z","content":"3"}`),
		text("done"),
	}}
	decisions := []Decision{Deny, AllowSession}
	asked := 0
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		d := decisions[asked]
		asked++
		return d, nil
	})
	a, root := newAgent(t, f, approver)
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if asked != 2 {
		t.Fatalf("asked %d times; AllowSession should skip the third prompt", asked)
	}
	if r := a.Messages[2].Blocks[0]; !r.IsError || !strings.Contains(r.Text, "denied") {
		t.Fatalf("deny result = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("denied write happened")
	}
	for _, p := range []string{"y", "z"} {
		if _, err := os.Stat(filepath.Join(root, p)); err != nil {
			t.Fatalf("%s not written", p)
		}
	}
	// Read-only tools never prompt.
	f2 := &fake{responses: []*llm.Response{toolCall("t1", "read_file", `{"path":"a.txt"}`), text("ok")}}
	a2, _ := newAgent(t, f2, ApproverFunc(func(context.Context, ToolCall) (Decision, error) {
		t.Fatal("read_file should not prompt")
		return Deny, nil
	}))
	if err := a2.Run(context.Background(), "read", nil); err != nil {
		t.Fatal(err)
	}
}

func TestProviderErrorKeepsHistoryConsistent(t *testing.T) {
	f := &fake{errs: []error{errors.New("boom")}, responses: []*llm.Response{nil, text("recovered")}}
	a, _ := newAgent(t, f, nil)
	if err := a.Run(context.Background(), "first", nil); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v", err)
	}
	if len(a.Messages) != 1 || a.Messages[0].Role != llm.RoleUser {
		t.Fatalf("messages after failure = %+v", a.Messages)
	}
	if err := a.Run(context.Background(), "second", nil); err != nil {
		t.Fatal(err)
	}
	// "second" merged into the pending user message; no user/user adjacency.
	if len(a.Messages) != 2 || len(a.Messages[0].Blocks) != 2 || a.Messages[0].Text() != "firstsecond" {
		t.Fatalf("messages = %+v", a.Messages)
	}
}

func TestMaxIterations(t *testing.T) {
	var rs []*llm.Response
	for i := 0; i < 5; i++ {
		rs = append(rs, toolCall("t", "read_file", `{"path":"a.txt"}`))
	}
	f := &fake{responses: rs}
	a, _ := newAgent(t, f, nil)
	a.MaxIterations = 3
	err := a.Run(context.Background(), "loop", nil)
	if !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("err = %v", err)
	}
	if last := a.Messages[len(a.Messages)-1]; last.Role != llm.RoleUser || last.Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("history should end with tool results, got %+v", last)
	}
}

func TestCancelDuringApprovalRecordsResults(t *testing.T) {
	f := &fake{responses: []*llm.Response{{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)},
			{Type: llm.BlockToolUse, ID: "t2", Name: "write_file", Input: json.RawMessage(`{"path":"y","content":"2"}`)},
		}}, StopReason: llm.StopToolUse}}}
	ctx, cancel := context.WithCancel(context.Background())
	a, _ := newAgent(t, f, ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		cancel()
		return Deny, ctx.Err()
	}))
	err := a.Run(ctx, "write", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	res := a.Messages[2].Blocks
	if len(res) != 2 || res[0].ToolUseID != "t1" || res[1].ToolUseID != "t2" || !res[1].IsError {
		t.Fatalf("results = %+v", res)
	}
}

type reasoning struct{ reason string }

func (r reasoning) Approve(ctx context.Context, call ToolCall) (Decision, error) { return Deny, nil }
func (r reasoning) ApproveWithReason(ctx context.Context, call ToolCall) (Decision, string, error) {
	return Deny, r.reason, nil
}

func TestDenyReasonReachesModel(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x","content":"1"}`),
		text("ok"),
	}}
	a, _ := newAgent(t, f, reasoning{reason: "use the config dir instead"})
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	r := a.Messages[2].Blocks[0]
	if !r.IsError || !strings.HasPrefix(r.Text, DeniedMessage) || !strings.HasSuffix(r.Text, "The user said: use the config dir instead") {
		t.Fatalf("deny result = %q", r.Text)
	}
}

type guardTool struct{ ask bool }

func (guardTool) Name() string            { return "guarded" }
func (guardTool) Description() string     { return "guarded" }
func (guardTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (guardTool) Mutating() bool          { return true }
func (guardTool) Run(context.Context, json.RawMessage) (string, error) {
	return "ran", nil
}
func (g guardTool) Guard(json.RawMessage) (bool, string) { return g.ask, "reaches outside" }

func TestGuardedToolAsksEvenWithAutoApprove(t *testing.T) {
	asked := 0
	approver := ApproverFunc(func(ctx context.Context, call ToolCall) (Decision, error) {
		asked++
		if call.Warning != "reaches outside" {
			t.Errorf("warning = %q", call.Warning)
		}
		return AllowSession, nil
	})
	f := &fake{responses: []*llm.Response{toolCall("1", "guarded", `{}`), toolCall("2", "guarded", `{}`), text("done")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(guardTool{ask: true}), Approver: approver, AutoApprove: true}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	// Auto-approve is bypassed and "always" does not stick for guarded calls.
	if asked != 2 {
		t.Fatalf("asked %d times, want 2", asked)
	}
	asked = 0
	f = &fake{responses: []*llm.Response{toolCall("1", "guarded", `{}`), text("done")}}
	a = &Agent{Provider: f, Tools: tool.NewRegistry(guardTool{ask: false}), Approver: approver, AutoApprove: true}
	if err := a.Run(context.Background(), "go", nil); err != nil || asked != 0 {
		t.Fatalf("err=%v asked=%d", err, asked)
	}
}

func TestEditCoaching(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("1", "edit_file", `{"path":"a.txt","old_string":"nope","new_string":"x"}`),
		toolCall("2", "edit_file", `{"path":"a.txt","old_string":"nope2","new_string":"x"}`),
		toolCall("3", "edit_file", `{"path":"a.txt","old_string":"alpha","new_string":"beta"}`),
		toolCall("4", "edit_file", `{"path":"a.txt","old_string":"nope3","new_string":"x"}`),
		text("done"),
	}}
	a, root := newAgent(t, f, ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Allow, nil }))
	a.Tools.Add(tool.EditFile{Root: root})
	if err := a.Run(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	result := func(i int) string {
		return a.Messages[i].Blocks[0].Text
	}
	// Unread file: the first failure inlines the content.
	if r := result(2); !strings.Contains(r, "without reading it first") || !strings.Contains(r, "1\talpha") {
		t.Fatalf("first failure: %q", r)
	}
	// Second failure in a row escalates; content is not repeated.
	if r := result(4); !strings.Contains(r, "failure 2 in a row") || strings.Contains(r, "without reading") {
		t.Fatalf("second failure: %q", r)
	}
	// Success resets the streak, so the next failure is a plain error.
	if r := result(8); strings.Contains(r, "in a row") || strings.Contains(r, "without reading") {
		t.Fatalf("after success: %q", r)
	}
}

func TestLoopDetectionNudgesThenStops(t *testing.T) {
	var responses []*llm.Response
	for i := 0; i < 6; i++ {
		responses = append(responses, toolCall(fmt.Sprint(i), "read_file", `{"path":"a.txt"}`))
	}
	responses = append(responses, text("done"))
	f := &fake{responses: responses}
	a, _ := newAgent(t, f, nil)
	var notices []string
	err := a.Run(context.Background(), "loop", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	})
	if !errors.Is(err, ErrStuck) {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != loopStopAt {
		t.Fatalf("model called %d times, want %d", len(f.requests), loopStopAt)
	}
	third := a.Messages[2*loopNudgeAt].Blocks[0].Text
	if !strings.Contains(third, "[harness] This is the 3rd identical read_file call") {
		t.Fatalf("no nudge on the third call: %q", third)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "turn stopped") {
		t.Fatalf("notices = %v", notices)
	}
	// The history is still valid: it ends with the tool results, so the
	// user's next message merges in and the model can continue.
	if last := a.Messages[len(a.Messages)-1]; last.Role != llm.RoleUser || last.Blocks[0].Type != llm.BlockToolResult {
		t.Fatalf("history ends with %+v", last)
	}
	// Failing calls are called out sooner.
	f2 := &fake{responses: []*llm.Response{
		toolCall("1", "read_file", `{"path":"missing.txt"}`),
		toolCall("2", "read_file", `{"path":"missing.txt"}`),
		text("gave up"),
	}}
	a2, _ := newAgent(t, f2, nil)
	if err := a2.Run(context.Background(), "x", nil); err != nil {
		t.Fatal(err)
	}
	if r := a2.Messages[4].Blocks[0].Text; !strings.Contains(r, "failed 2 times with the same input") {
		t.Fatalf("second failure: %q", r)
	}
}

func TestIntegrityCheckFlagsShrunkHistory(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("one"), text("two")}}
	a, _ := newAgent(t, f, nil)
	var notices []string
	emit := func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}
	if err := a.Run(context.Background(), "first request", emit); err != nil {
		t.Fatal(err)
	}
	// Something outside the agent truncates the history behind its back.
	a.Messages = nil
	if err := a.Run(context.Background(), "second", emit); err != nil {
		t.Fatal(err)
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "shrank") {
		t.Fatalf("notices = %v", notices)
	}
}

func TestSystemExtraAndLastTurnMutated(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		text("no tools"),
		toolCall("t1", "write_file", `{"path":"x","content":"1"}`),
		text("wrote"),
	}}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	extra := "\n# Project memory\n- fact one\n"
	a.SystemExtra = func() string { return extra }
	if err := a.Run(context.Background(), "chat", nil); err != nil {
		t.Fatal(err)
	}
	if a.LastTurnMutated() {
		t.Fatal("turn without mutation reported as mutated")
	}
	if got := f.requests[0].System; !strings.HasSuffix(got, extra) || !strings.HasPrefix(got, "sys") {
		t.Fatalf("system = %q", got)
	}
	extra = "\n# Project memory\n- fact two\n"
	if err := a.Run(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if !a.LastTurnMutated() {
		t.Fatal("mutating turn not reported")
	}
	if got := f.requests[1].System; !strings.Contains(got, "fact one") || strings.Contains(got, "fact two") {
		t.Fatalf("extra re-evaluated mid-session (cache prefix would change): %q", got)
	}
	a.RefreshExtra()
	f.responses = append(f.responses, text("ok"))
	if err := a.Run(context.Background(), "again", nil); err != nil {
		t.Fatal(err)
	}
	if got := f.requests[3].System; !strings.Contains(got, "fact two") {
		t.Fatalf("extra not refreshed: %q", got)
	}
	a.Usage = llm.Usage{InputTokens: 200, CacheReadTokens: 700, CacheWriteTokens: 100}
	if pct, ok := a.CacheRate(); !ok || pct != 70 {
		t.Fatalf("cache rate = %d %v", pct, ok)
	}
	a.Usage = llm.Usage{InputTokens: 200}
	if _, ok := a.CacheRate(); ok {
		t.Fatal("cache rate reported without cache traffic")
	}
}

func TestCompactionRefreshesExtra(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("SUMMARY"), text("after")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"), llm.TextMessage(llm.RoleAssistant, "ok"),
	}}
	extra := "\n# index v1"
	a.SystemExtra = func() string { return extra }
	_ = a.extra()
	extra = "\n# index v2"
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "next", nil); err != nil {
		t.Fatal(err)
	}
	last := f.requests[len(f.requests)-1]
	if !strings.Contains(last.System, "index v2") {
		t.Fatalf("system after compaction = %q", last.System)
	}
}

func TestPrepareAddsBlocksAndNotice(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("ok")}}
	a, _ := newAgent(t, f, nil)
	a.Prepare = func(blocks []llm.Block) ([]llm.Block, string) {
		return append(blocks, llm.Block{Type: llm.BlockText, Text: "<skill>steps</skill>"}), "skills attached: deploy"
	}
	var notes []string
	if err := a.Run(context.Background(), "deploy it", func(e Event) {
		if e.Kind == EvNotice {
			notes = append(notes, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if u := f.requests[0].Messages[0]; len(u.Blocks) != 2 || u.Blocks[0].Text != "deploy it" || u.Blocks[1].Text != "<skill>steps</skill>" {
		t.Fatalf("user message = %+v", u)
	}
	if len(notes) != 1 || notes[0] != "skills attached: deploy" {
		t.Fatalf("notes = %q", notes)
	}
}

func TestContextReportAndPins(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"big.txt"}`),
		text("done"),
		text("again"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	os.WriteFile(filepath.Join(root, "big.txt"), bytes.Repeat([]byte("x"), 4000), 0o644)
	a.SystemExtra = func() string { return strings.Repeat("m", 400) }
	a.Pin("always use tabs")
	if err := a.Run(context.Background(), "read big.txt please", nil); err != nil {
		t.Fatal(err)
	}
	if sys := f.requests[0].System; !strings.Contains(sys, "# Pinned by the user") || !strings.Contains(sys, "1. always use tabs") {
		t.Fatalf("system = %q", sys)
	}
	r := a.Context()
	if r.ToolCount == 0 || r.Tools == 0 || r.Extra != 100 || r.Pins == 0 || r.UserText == 0 || r.AssistantText == 0 || r.Turns != 1 {
		t.Fatalf("report = %+v", r)
	}
	if len(r.ByTool) != 1 || r.ByTool[0].Name != "read_file" || r.ByTool[0].Calls != 1 || r.ByTool[0].Tokens < 1000 {
		t.Fatalf("by tool = %+v", r.ByTool)
	}
	if len(r.Largest) != 1 || r.Largest[0].Subject != "big.txt" || r.Largest[0].Tokens < 1000 || r.Largest[0].Index != 2 {
		t.Fatalf("largest = %+v", r.Largest)
	}
	if r.Total <= 0 || r.CompactAt != DefaultCompactAt {
		t.Fatalf("total/compactAt = %d/%d", r.Total, r.CompactAt)
	}
	if !a.Unpin(1) || a.Unpin(1) || a.Unpin(0) {
		t.Fatal("Unpin bookkeeping wrong")
	}
	a.Pin("   ")
	if len(a.Pins) != 0 {
		t.Fatal("blank pin stored")
	}
	if err := a.Run(context.Background(), "more", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.requests[2].System, "Pinned") {
		t.Fatal("pins section present with no pins")
	}
}
