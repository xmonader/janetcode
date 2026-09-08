package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

type scripted struct{ responses []*llm.Response }

func (s *scripted) Name() string { return "fake" }
func (s *scripted) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	r := s.responses[0]
	s.responses = s.responses[1:]
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

// harness runs the model's Update loop by hand: background goroutines send
// into a channel and the test pumps them through Update.
type harness struct {
	t    *testing.T
	m    *model
	msgs chan tea.Msg
}

func newHarness(t *testing.T, responses ...*llm.Response) *harness {
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Provider: &scripted{responses}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}
	m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test"})
	h := &harness{t: t, m: m, msgs: make(chan tea.Msg, 100)}
	m.send = func(msg tea.Msg) { h.msgs <- msg }
	m.queue.send = m.send
	m.approver.send = m.send
	ag.Approver = m.approver
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return h
}

func (h *harness) update(msg tea.Msg) {
	mm, _ := h.m.Update(msg)
	h.m = mm.(*model)
}

func (h *harness) keys(s string) {
	for _, r := range s {
		h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

func (h *harness) enter() { h.update(tea.KeyMsg{Type: tea.KeyEnter}) }

// pump feeds background messages until the predicate holds or time runs out.
func (h *harness) pump(until func() bool) {
	deadline := time.After(5 * time.Second)
	for !until() {
		select {
		case msg := <-h.msgs:
			h.update(msg)
		case <-deadline:
			h.t.Fatalf("timed out waiting; view:\n%s", plain(h.m.View()))
		}
	}
}

func TestSubmitAndStreamResponse(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hello back"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 7, OutputTokens: 2}})
	h.keys("hi there")
	h.enter()
	if !h.m.running {
		t.Fatal("should be running after enter")
	}
	if h.m.input.Value() != "" {
		t.Fatal("input not cleared")
	}
	h.pump(func() bool { return !h.m.running })
	v := plain(h.m.View())
	for _, want := range []string{"❯ hi there", "hello back", "7 in · 2 out"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in view:\n%s", want, v)
		}
	}
	if len(h.m.opts.Agent.Messages) != 2 {
		t.Fatalf("history = %d", len(h.m.opts.Agent.Messages))
	}
	// Enter with an empty input does nothing; slash command works.
	h.enter()
	h.keys("/usage")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "session tokens · in 7") {
		t.Fatalf("usage missing:\n%s", v)
	}
}

func TestPermissionPromptAllow(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
	)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	v := plain(h.m.View())
	if !strings.Contains(v, "allow write_file") || !strings.Contains(v, "+x") || !strings.Contains(v, "approval needed") || !strings.Contains(v, "always for write_file") {
		t.Fatalf("prompt not shown:\n%s", v)
	}
	// Typing while the prompt is up must not go into the textarea.
	h.keys("q")
	if h.m.input.Value() != "" {
		t.Fatal("keys leaked into input during permission prompt")
	}
	h.keys("y")
	h.pump(func() bool { return !h.m.running })
	v = plain(h.m.View())
	if !strings.Contains(v, "✓ write_file n.txt") || !strings.Contains(v, "written") {
		t.Fatalf("after allow:\n%s", v)
	}
}

func TestPermissionDenyWithReason(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "understood"), StopReason: llm.StopEndTurn},
	)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	h.keys("n")
	if !h.m.denying {
		t.Fatal("n should open the reason prompt")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "tell the model why") {
		t.Fatalf("reason prompt missing:\n%s", v)
	}
	h.keys("wrong directory")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	res := h.m.opts.Agent.Messages[2].Blocks[0]
	if !res.IsError || !strings.Contains(res.Text, "The user said: wrong directory") {
		t.Fatalf("tool result = %q", res.Text)
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "The user said: wrong directory") || !strings.Contains(v, "understood") || h.m.input.Value() != "" || h.m.input.Placeholder != defaultPlaceholder {
		t.Fatalf("view:\n%s", v)
	}
	if _, err := os.Stat(filepath.Join(h.m.opts.Cwd, "n.txt")); err == nil {
		t.Fatal("denied write happened")
	}
}

func TestEscCancelsRunningTurn(t *testing.T) {
	blocker := &blocking{started: make(chan struct{})}
	root := t.TempDir()
	sess, _ := session.New(t.TempDir(), root, "fake", "m")
	ag := &agent.Agent{Provider: blocker, Tools: tool.NewRegistry(), Model: "m"}
	m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root})
	h := &harness{t: t, m: m, msgs: make(chan tea.Msg, 100)}
	m.send = func(msg tea.Msg) { h.msgs <- msg }
	m.queue.send = m.send
	h.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys("slow")
	h.enter()
	<-blocker.started
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	h.pump(func() bool { return !h.m.running })
	if v := plain(h.m.View()); !strings.Contains(v, "cancelled") {
		t.Fatalf("expected cancelled marker:\n%s", v)
	}
	// ctrl+c when idle quits.
	_, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c when idle should quit")
	}
}

func TestDoubleCtrlCForcesQuit(t *testing.T) {
	blocker := &blocking{started: make(chan struct{})}
	root := t.TempDir()
	sess, _ := session.New(t.TempDir(), root, "fake", "m")
	ag := &agent.Agent{Provider: blocker, Tools: tool.NewRegistry(), Model: "m"}
	m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root})
	h := &harness{t: t, m: m, msgs: make(chan tea.Msg, 100)}
	m.send = func(msg tea.Msg) { h.msgs <- msg }
	m.queue.send = m.send
	h.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.keys("slow")
	h.enter()
	<-blocker.started
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil || !h.m.cancelling {
		t.Fatal("first ctrl+c should cancel, not quit")
	}
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("second ctrl+c should quit")
	}
}

type blocking struct{ started chan struct{} }

func (b *blocking) Name() string { return "blocking" }
func (b *blocking) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	close(b.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestQueuedPromptSendsAfterTurn(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first answer"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second answer"), StopReason: llm.StopEndTurn},
	)
	h.keys("one")
	h.enter()
	h.keys("two")
	h.enter()
	if len(h.m.queued) != 1 || h.m.input.Value() != "" {
		t.Fatalf("queued = %v input = %q", h.m.queued, h.m.input.Value())
	}
	if v := plain(h.m.View()); !strings.Contains(v, "queued 1 two") {
		t.Fatalf("queue not shown:\n%s", v)
	}
	h.pump(func() bool { return !h.m.running && len(h.m.queued) == 0 && len(h.m.opts.Agent.Messages) == 4 })
	v := plain(h.m.View())
	for _, want := range []string{"❯ one", "first answer", "❯ two", "second answer"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in view:\n%s", want, v)
		}
	}
	if strings.Contains(v, "queued") {
		t.Fatalf("queue line should be gone:\n%s", v)
	}
}

func TestQueuedPromptRestoredOnCancel(t *testing.T) {
	b := &blocking{started: make(chan struct{})}
	h := newHarness(t)
	h.m.opts.Agent.Provider = b
	h.keys("slow")
	h.enter()
	<-b.started
	h.keys("later")
	h.enter()
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	h.pump(func() bool { return !h.m.running })
	if got := h.m.input.Value(); got != "later" {
		t.Fatalf("queued prompt not restored: %q", got)
	}
	if len(h.m.queued) != 0 {
		t.Fatalf("queue = %v", h.m.queued)
	}
}

func TestMentionCompletionAndAttachment(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	root := h.m.opts.Cwd
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(root, "main_test.go"), []byte("package main\n"), 0o644)
	tab := tea.KeyMsg{Type: tea.KeyTab}
	h.keys("explain @m")
	h.update(tab)
	if got := h.m.input.Value(); got != "explain @main" {
		t.Fatalf("after tab: %q", got)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "@main.go") || !strings.Contains(v, "@main_test.go") {
		t.Fatalf("completions not shown:\n%s", v)
	}
	h.keys("_")
	h.update(tab)
	if got := h.m.input.Value(); got != "explain @main_test.go " {
		t.Fatalf("after second tab: %q", got)
	}
	h.enter()
	h.pump(func() bool { return !h.m.running })
	sent := h.m.opts.Agent.Messages[0].Blocks[0].Text
	if !strings.HasPrefix(sent, "explain @main_test.go") || !strings.Contains(sent, "<attached path=\"main_test.go\">\npackage main") {
		t.Fatalf("prompt sent = %q", sent)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "attached main_test.go (13B)") || strings.Contains(v, "<attached") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestShellPassthrough(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "seen"), StopReason: llm.StopEndTurn})
	h.keys("!echo hello-from-shell; echo err >&2; exit 3")
	h.enter()
	if !h.m.running {
		t.Fatal("shell should mark the UI busy")
	}
	h.pump(func() bool { return !h.m.running })
	v := plain(h.m.View())
	for _, want := range []string{"shell", "hello-from-shell", "output shared with the model"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in view:\n%s", want, v)
		}
	}
	// The next prompt carries the command and its output.
	h.keys("what did that print?")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if n := len(h.m.opts.Agent.Messages); n != 2 {
		t.Fatalf("messages = %d, the shell context must merge into the user turn", n)
	}
	sent := h.m.opts.Agent.Messages[0].Text()
	for _, want := range []string{"$ echo hello-from-shell", "\nhello-from-shell\n", "[exit code 3]", "what did that print?"} {
		if !strings.Contains(sent, want) {
			t.Fatalf("missing %q in shared context: %q", want, sent)
		}
	}
}

func TestResumePicker(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "old answer"), StopReason: llm.StopEndTurn})
	h.keys("old question")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	old := h.m.opts.Session.Meta.ID
	h.keys("/clear")
	h.enter()
	if h.m.opts.Session.Meta.ID == old || len(h.m.opts.Agent.Messages) != 0 {
		t.Fatal("/clear should start an empty session")
	}
	h.keys("/resume")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, " 1  "+old) || !strings.Contains(v, "2 msgs") || !strings.Contains(v, "old question") {
		t.Fatalf("listing:\n%s", v)
	}
	h.keys("/resume 1")
	h.enter()
	if h.m.opts.Session.Meta.ID != old || len(h.m.opts.Agent.Messages) != 2 {
		t.Fatalf("resume: session %s messages %d", h.m.opts.Session.Meta.ID, len(h.m.opts.Agent.Messages))
	}
	v = plain(h.m.View())
	if !strings.Contains(v, "resumed session "+old) || !strings.Contains(v, "old answer") {
		t.Fatalf("after resume:\n%s", v)
	}
	h.keys("/resume 9")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no session numbered 9") {
		t.Fatalf("bad index:\n%s", v)
	}
}

func TestCustomSlashCommand(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "reviewed"), StopReason: llm.StopEndTurn})
	dir := filepath.Join(h.m.opts.Cwd, ".janetcode", "commands")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "review.md"), []byte("# Careful review\nReview $ARGUMENTS for bugs."), 0o644)
	// Commands load at construction; rebuild the model against the same dir.
	h.m.custom = map[string]prompt.Custom{}
	for _, c := range prompt.CustomCommands(h.m.opts.Cwd, "") {
		h.m.custom["/"+c.Name] = c
	}
	h.keys("/rev")
	if v := plain(h.m.View()); !strings.Contains(v, "/review") || !strings.Contains(v, "Careful review") {
		t.Fatalf("completion missing:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	h.keys("main.go")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if sent := h.m.opts.Agent.Messages[0].Text(); sent != "# Careful review\nReview main.go for bugs." {
		t.Fatalf("sent = %q", sent)
	}
	h.keys("/help")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/review") {
		t.Fatalf("help lacks custom command:\n%s", v)
	}
}

func TestFindScrollsToMatches(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 80, Height: 12})
	for i := 0; i < 30; i++ {
		h.m.transcript.info(fmt.Sprintf("filler line %d", i))
	}
	h.m.transcript.info("the NEEDLE is here")
	for i := 0; i < 30; i++ {
		h.m.transcript.info(fmt.Sprintf("more %d", i))
	}
	h.m.transcript.info("second needle")
	h.m.refresh(false)
	h.m.viewport.GotoTop()
	h.keys("/find needle")
	h.enter()
	if !strings.Contains(h.m.status, "match 1/2") {
		t.Fatalf("status = %q", h.m.status)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "the NEEDLE is here") {
		t.Fatalf("viewport not scrolled to the match:\n%s", v)
	}
	h.keys("/find")
	h.enter()
	if !strings.Contains(h.m.status, "match 2/2") {
		t.Fatalf("repeat status = %q", h.m.status)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "second needle") {
		t.Fatalf("viewport not at second match:\n%s", v)
	}
	h.keys("/find")
	h.enter()
	if !strings.Contains(h.m.status, "match 1/2") {
		t.Fatalf("wrap status = %q", h.m.status)
	}
	h.keys("/find zzz")
	h.enter()
	if !strings.Contains(h.m.status, "no match") {
		t.Fatalf("miss status = %q", h.m.status)
	}
}

func TestUndoCommand(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "written"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	h.keys("/undo")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "nothing to undo") {
		t.Fatalf("view:\n%s", v)
	}
	h.m.opts.Agent.AutoApprove = true
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	target := filepath.Join(h.m.opts.Cwd, "n.txt")
	if _, err := os.Stat(target); err != nil {
		t.Fatal("file not written")
	}
	h.keys("/undo")
	h.enter()
	if _, err := os.Stat(target); err == nil {
		t.Fatal("undo did not remove the created file")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "↶ removed") || !strings.Contains(v, "n.txt") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("continue")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if sent := h.m.opts.Agent.Messages[len(h.m.opts.Agent.Messages)-2].Text(); !strings.Contains(sent, "I reverted the file changes") || !strings.Contains(sent, "continue") {
		t.Fatalf("model not told about the undo: %q", sent)
	}
}

func TestPromptHistoryRecall(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "a"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "b"), StopReason: llm.StopEndTurn},
	)
	h.keys("first prompt")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	h.keys("second prompt")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	h.keys("dra")
	up := tea.KeyMsg{Type: tea.KeyUp}
	down := tea.KeyMsg{Type: tea.KeyDown}
	h.update(up)
	if h.m.input.Value() != "second prompt" {
		t.Fatalf("after up: %q", h.m.input.Value())
	}
	h.update(up)
	if h.m.input.Value() != "first prompt" {
		t.Fatalf("after up up: %q", h.m.input.Value())
	}
	h.update(up) // at the oldest entry; stays
	h.update(down)
	h.update(down)
	if h.m.input.Value() != "dra" {
		t.Fatalf("draft not restored: %q", h.m.input.Value())
	}
}

func TestInitialPromptIsSubmitted(t *testing.T) {
	root := t.TempDir()
	sess, _ := session.New(t.TempDir(), root, "fake", "m")
	ag := &agent.Agent{Provider: &scripted{[]*llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "auto"), StopReason: llm.StopEndTurn}}}, Tools: tool.NewRegistry(), Model: "m"}
	m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, InitialPrompt: "explain"})
	h := &harness{t: t, m: m, msgs: make(chan tea.Msg, 100)}
	m.send = func(msg tea.Msg) { h.msgs <- msg }
	m.queue.send = m.send
	h.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if !h.m.running {
		t.Fatal("initial prompt not submitted")
	}
	h.pump(func() bool { return !h.m.running })
	if v := plain(h.m.View()); !strings.Contains(v, "explain") || !strings.Contains(v, "auto") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestPermissionBoxShowsWarning(t *testing.T) {
	h := newHarness(t)
	h.m.pending = &permissionMsg{call: agent.ToolCall{ID: "b1", Name: "bash", Input: json.RawMessage(`{"command":"rm -rf build"}`), Warning: "recursive force delete"}, reply: make(chan verdict, 1)}
	h.m.layout()
	v := plain(h.m.View())
	for _, want := range []string{"allow bash rm -rf build", "⚠ recursive force delete", "always for bash", "approval needed"} {
		if !strings.Contains(v, want) {
			t.Errorf("missing %q in view:\n%s", want, v)
		}
	}
	h.keys("n")
	if h.m.pending == nil || !h.m.denying {
		t.Fatal("n should open the reason prompt")
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.pending != nil || h.m.denying {
		t.Fatal("esc should deny without a reason")
	}
}

func TestTailPath(t *testing.T) {
	if got := tailPath("/a/b", 20); got != "/a/b" {
		t.Fatalf("short: %q", got)
	}
	got := tailPath("/very/long/path/to/some/project/dir", 16)
	if len([]rune(got)) > 16 || !strings.HasPrefix(got, "…/") || !strings.HasSuffix(got, "/dir") {
		t.Fatalf("long: %q", got)
	}
}

// raceEnabled is set by race_on_test.go; the race detector slows rendering
// roughly tenfold, so timing bounds scale with it.
var raceEnabled bool

// TestStreamingLargeAnswerStaysResponsive feeds a 20 KB markdown answer as
// hundreds of deltas and checks the UI loop keeps up.
func TestStreamingLargeAnswerStaysResponsive(t *testing.T) {
	h := newHarness(t)
	h.m.startTurn("writing")
	para := "Paragraph with **bold**, `code`, and a [link](x).\n\n```go\nfunc f() int { return 1 }\n```\n\n"
	full := strings.Repeat(para, 20000/len(para))
	start := time.Now()
	renders := 0
	for i := 0; i < len(full); i += 40 {
		end := i + 40
		if end > len(full) {
			end = len(full)
		}
		h.m.applyEvent(agent.Event{Kind: agent.EvText, Text: full[i:end]})
		h.m.refresh(false)
		renders++
	}
	h.m.applyEvent(agent.Event{Kind: agent.EvTextBlockDone})
	h.m.refresh(false)
	elapsed := time.Since(start)
	limit := 8 * time.Second
	if raceEnabled {
		limit = 60 * time.Second
	}
	if elapsed > limit {
		t.Fatalf("streaming %d bytes in %d steps took %s", len(full), renders, elapsed)
	}
	v := plain(h.m.View())
	if !strings.Contains(v, "func f() int") {
		t.Fatalf("final render missing content:\n%s", v)
	}
}

func TestSplitMarkdownBlocks(t *testing.T) {
	got := splitMarkdownBlocks("a\n\nb\n```\nx\n\ny\n```\n\nc")
	if len(got) != 3 || got[1] != "b\n```\nx\n\ny\n```" || got[2] != "c" {
		t.Fatalf("got %q", got)
	}
}
