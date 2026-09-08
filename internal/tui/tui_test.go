package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/testserver"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/prompt"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

type scripted struct {
	responses []*llm.Response
	mu        sync.Mutex
	requests  []llm.Request
}

// reqs returns the requests seen so far.
func (s *scripted) reqs() []llm.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llm.Request(nil), s.requests...)
}

func (s *scripted) Name() string { return "fake" }
func (s *scripted) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	s.mu.Lock()
	s.requests = append(s.requests, req)
	s.mu.Unlock()
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
	ag := &agent.Agent{Provider: &scripted{responses: responses}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}
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
	// ctrl+c with typed text clears it; when idle and empty it quits.
	h.keys("draft")
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil || h.m.input.Value() != "" {
		t.Fatalf("ctrl+c should clear the input first: cmd=%v value=%q", cmd, h.m.input.Value())
	}
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

func TestImageMentionSendsImageBlock(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "a cat"), StopReason: llm.StopEndTurn})
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "shot.png"), []byte("\x89PNGfake"), 0o644)
	h.keys("what is in @shot.png ?")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	blocks := h.m.opts.Agent.Messages[0].Blocks
	if len(blocks) != 2 || blocks[0].Type != llm.BlockText || blocks[1].Type != llm.BlockImage || blocks[1].MediaType != "image/png" {
		t.Fatalf("blocks = %+v", blocks)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "attached shot.png (image, 8B)") {
		t.Fatalf("view:\n%s", v)
	}
}

// TestFakeMCPServerProcess is not a test: TestMCPTrustFlow spawns the test
// binary with MCP_FAKE_SERVER=1 and this "test" then serves the protocol.
func TestFakeMCPServerProcess(t *testing.T) {
	if os.Getenv("MCP_FAKE_SERVER") != "1" {
		t.Skip("helper process only")
	}
	testserver.Run()
}

func TestMCPTrustFlow(t *testing.T) {
	h := newHarness(t)
	reg := h.m.opts.Agent.Tools
	mgr := &mcp.Manager{Options: mcp.Options{Root: h.m.opts.Cwd, HandshakeTimeout: 20 * time.Second}, Registry: reg}
	fake := mcp.ServerConfig{Command: os.Args[0], Args: []string{"-test.run=TestFakeMCPServerProcess"}, Env: map[string]string{"MCP_FAKE_SERVER": "1"}}
	h.m.opts.MCP = &MCPOptions{
		Manager: mgr, Project: map[string]mcp.ServerConfig{"proj": fake}, ProjectHash: "h1",
		Trust: mcp.TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")},
	}
	h.m.Init()
	if v := plain(h.m.View()); !strings.Contains(v, "not been trusted") || !strings.Contains(v, "proj") {
		t.Fatalf("trust notice missing:\n%s", v)
	}
	h.keys("/mcp")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no MCP servers running") {
		t.Fatalf("status:\n%s", v)
	}
	h.keys("/mcp trust")
	h.enter()
	h.pump(func() bool { _, ok := reg.Get("mcp__proj__echo"); return ok })
	if !h.m.opts.MCP.Trust.Trusted(h.m.opts.Cwd, "h1") {
		t.Fatal("trust not recorded")
	}
	h.keys("/mcp")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "✓ proj · 4 tools") || !strings.Contains(v, "mcp__proj__echo") || !strings.Contains(v, "mcp proj ready · 4 tools · 1 prompts") || !strings.Contains(v, "/mcp__proj__greet  (prompt)") {
		t.Fatalf("status after trust:\n%s", v)
	}
	// The server's prompt is a slash command with completion and arguments.
	h.keys("/mcp__proj__gr")
	if v := plain(h.m.View()); !strings.Contains(v, "/mcp__proj__greet <who>") {
		t.Fatalf("prompt completion missing:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	h.enter() // no argument: the required one is reported
	if v := plain(h.m.View()); !strings.Contains(v, "needs <who>") {
		t.Fatalf("missing-arg error absent:\n%s", v)
	}
	h.m.opts.Agent.Provider = &scripted{responses: []*llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "hello there"), StopReason: llm.StopEndTurn}}}
	h.keys("/mcp__proj__greet the team")
	h.enter()
	h.pump(func() bool { return !h.m.running && len(h.m.opts.Agent.Messages) == 2 })
	if sent := h.m.opts.Agent.Messages[0].Text(); sent != "Say hello to the team" {
		t.Fatalf("prompt sent = %q", sent)
	}
	mgr.Close()
}

func TestCtrlVPastesClipboardImage(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "a screenshot"), StopReason: llm.StopEndTurn})
	h.m.clipboard = func() ([]byte, error) { return nil, errors.New("no image in the clipboard") }
	h.keys("look")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if h.m.input.Value() != "look" || !strings.Contains(h.m.status, "no image") {
		t.Fatalf("input %q status %q", h.m.input.Value(), h.m.status)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake")
	h.m.clipboard = func() ([]byte, error) { return png, nil }
	h.update(tea.KeyMsg{Type: tea.KeyCtrlV})
	v := h.m.input.Value()
	if !strings.HasPrefix(v, "look @") || !strings.HasSuffix(v, ".png ") {
		t.Fatalf("input after paste: %q", v)
	}
	path := strings.TrimSpace(strings.TrimPrefix(v, "look @"))
	t.Cleanup(func() { os.Remove(path) })
	h.enter()
	h.pump(func() bool { return !h.m.running })
	blocks := h.m.opts.Agent.Messages[0].Blocks
	if len(blocks) != 2 || blocks[1].Type != llm.BlockImage || blocks[1].MediaType != "image/png" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestMenuNavigation(t *testing.T) {
	h := newHarness(t)
	up := tea.KeyMsg{Type: tea.KeyUp}
	down := tea.KeyMsg{Type: tea.KeyDown}
	h.keys("/")
	c, idx := h.m.menu()
	if len(c) < 5 || idx != 0 {
		t.Fatalf("menu = %d rows idx %d", len(c), idx)
	}
	h.update(down)
	h.update(down)
	if _, idx := h.m.menu(); idx != 2 {
		t.Fatalf("idx after two downs = %d", idx)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "▸ "+c[2].name) {
		t.Fatalf("highlight missing:\n%s", v)
	}
	h.update(up)
	h.update(up)
	h.update(up) // wraps to the last row
	if _, idx := h.m.menu(); idx != len(c)-1 {
		t.Fatalf("wrap idx = %d", idx)
	}
	// Typing resets the highlight; enter on a no-arg command runs it.
	h.keys("he")
	c, idx = h.m.menu()
	if idx != 0 || c[0].name != "/help" {
		t.Fatalf("after typing: %v %d", c, idx)
	}
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "commands") || h.m.input.Value() != "" {
		t.Fatalf("/help did not run from the menu: input %q\n%s", h.m.input.Value(), v)
	}
	// A command that takes arguments is filled in, not run.
	h.keys("/mod")
	h.enter()
	if h.m.input.Value() != "/model " {
		t.Fatalf("input = %q", h.m.input.Value())
	}
	h.inputReset()
	// Path menus: down selects the second file, enter fills it in.
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "alpha.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "beta.go"), []byte("x"), 0o644)
	h.keys("see @")
	h.update(down)
	h.enter()
	if h.m.input.Value() != "see @beta.go " {
		t.Fatalf("input = %q", h.m.input.Value())
	}
	if _, idx := h.m.menu(); idx != 0 {
		t.Fatalf("menu should be closed or reset, idx = %d", idx)
	}
}

func (h *harness) inputReset() {
	h.m.input.Reset()
	h.m.layout()
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
	ag := &agent.Agent{Provider: &scripted{responses: []*llm.Response{{Message: llm.TextMessage(llm.RoleAssistant, "auto"), StopReason: llm.StopEndTurn}}}, Tools: tool.NewRegistry(), Model: "m"}
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

// listing is a provider that can enumerate models.
type listing struct {
	scripted
	models []llm.ModelInfo
	err    error
}

func (l *listing) ListModels(ctx context.Context) ([]llm.ModelInfo, error) { return l.models, l.err }

// updateCmd runs Update and then any command it returned, feeding the
// resulting message back in (enough for the dialog's single async step).
func (h *harness) updateCmd(msg tea.Msg) {
	mm, cmd := h.m.Update(msg)
	h.m = mm.(*model)
	h.runCmd(cmd)
}

// runCmd executes a command (or batch) and feeds what it produces back.
func (h *harness) runCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch out := cmd().(type) {
	case nil:
	case tea.BatchMsg:
		for _, c := range out {
			h.runCmd(c)
		}
	default:
		h.update(out)
	}
}

func (h *harness) withConfig(t *testing.T, cfg *config.Config, build func(*config.Config) (llm.Provider, error)) string {
	path := filepath.Join(t.TempDir(), "config.json")
	h.m.opts.Config, h.m.opts.ConfigPath, h.m.opts.Build = cfg, path, build
	return path
}

func TestConnectDialogSwitchesProvider(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "m", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	var built *config.Config
	lister := &listing{models: []llm.ModelInfo{{ID: "zeta/z"}, {ID: "deepseek/deepseek-v4-flash", Name: "DeepSeek V4 Flash", ContextWindow: 128000, Price: &llm.Price{Input: 0.15, Output: 0.6}}}}
	path := h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { built = c; return lister, nil })
	esc := tea.KeyMsg{Type: tea.KeyEsc}
	down := tea.KeyMsg{Type: tea.KeyDown}

	h.keys("/connect")
	h.enter()
	if h.m.connect == nil || h.m.connect.step != stepPick {
		t.Fatalf("dialog not open: %+v", h.m.connect)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "Connect to a provider") || !strings.Contains(v, "CONFIGURED") || !strings.Contains(v, "fake") || !strings.Contains(v, "OpenRouter") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("openrouter")
	h.enter()
	if h.m.connect.step != stepURL || h.m.connect.field.Value() != "https://openrouter.ai/api/v1" {
		t.Fatalf("step %d url %q", h.m.connect.step, h.m.connect.field.Value())
	}
	h.enter() // keep the preset URL
	if h.m.connect.step != stepKeys {
		t.Fatalf("step = %d", h.m.connect.step)
	}
	h.enter() // hosted provider without a key is refused
	if h.m.connect.step != stepKeys || !strings.Contains(plain(h.m.View()), "needs an API key") {
		t.Fatalf("step %d view:\n%s", h.m.connect.step, plain(h.m.View()))
	}
	h.keys("sk-one, sk-two")
	if v := plain(h.m.View()); strings.Contains(v, "sk-one") || !strings.Contains(v, "••") || !strings.Contains(v, "2 key(s) entered") {
		t.Fatalf("key visible:\n%s", v)
	}
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter}) // runs the model fetch
	if h.m.connect.step != stepModel || h.m.connect.loading || len(h.m.connect.models) != 2 {
		t.Fatalf("step %d loading %v models %d", h.m.connect.step, h.m.connect.loading, len(h.m.connect.models))
	}
	if built == nil || built.Provider != "openrouter" || strings.Join(built.Active().Keys(), ",") != "sk-one,sk-two" {
		t.Fatalf("list built with %+v", built)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "▸ deepseek/deepseek-v4-flash") || !strings.Contains(v, "zeta/z") {
		t.Fatalf("view:\n%s", v)
	}
	h.update(down)
	if !strings.Contains(plain(h.m.View()), "▸ zeta/z") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
	h.keys("deep") // filtering resets the highlight to the first match
	h.enter()
	if h.m.connect != nil {
		t.Fatalf("dialog still open: %+v", h.m.connect)
	}
	if h.m.opts.Agent.Model != "deepseek/deepseek-v4-flash" || h.m.opts.Agent.Provider != lister || h.m.opts.Provider != "openrouter" || cfg.Provider != "openrouter" {
		t.Fatalf("agent %s/%v opts %s cfg %s", h.m.opts.Agent.Model, h.m.opts.Agent.Provider, h.m.opts.Provider, cfg.Provider)
	}
	if !strings.Contains(plain(h.m.View()), "connected to openrouter · deepseek/deepseek-v4-flash") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
	raw, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(raw), `"sk-two"`) || !strings.Contains(string(raw), `"provider": "openrouter"`) || !strings.Contains(string(raw), `"model": "deepseek/deepseek-v4-flash"`) {
		t.Fatalf("saved config: %s %v", raw, err)
	}
	if !strings.Contains(string(raw), `"context_window": 128000`) || !strings.Contains(string(raw), `"models"`) {
		t.Fatalf("model metadata not saved: %s", raw)
	}
	if ag := h.m.opts.Agent; ag.ContextWindow != 128000 || ag.Price == nil || ag.Price.Output != 0.6 || ag.CompactThreshold() != 96000 {
		t.Fatalf("agent window=%d price=%+v threshold=%d", ag.ContextWindow, ag.Price, ag.CompactThreshold())
	}
	if !strings.Contains(plain(h.m.View()), "128k ctx · $0.15/$0.6 per M") {
		t.Fatalf("details missing from view:\n%s", plain(h.m.View()))
	}

	// /model alone opens the picker for the current provider; a typed id
	// that is not listed is used as is; esc leaves the dialog.
	h.inputReset()
	h.keys("/model")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.connect == nil || h.m.connect.step != stepModel || h.m.connect.name != "openrouter" {
		t.Fatalf("step %d loading %v models %d", h.m.connect.step, h.m.connect.loading, len(h.m.connect.models))
	}
	h.keys("custom/model")
	h.enter()
	if h.m.connect != nil || h.m.opts.Agent.Model != "custom/model" {
		t.Fatalf("model = %s", h.m.opts.Agent.Model)
	}
	h.inputReset()
	h.keys("hello")
	h.inputReset()
	h.keys("/connect")
	h.enter()
	h.update(esc)
	if h.m.connect != nil || h.m.input.Value() != "" {
		t.Fatalf("esc did not close: %+v %q", h.m.connect, h.m.input.Value())
	}

	// A listing failure still lets the user type a model id; esc from a
	// /model picker closes it rather than stepping back to the keys.
	lister.err = errors.New("boom")
	h.keys("/model")
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(plain(h.m.View()), "could not list models: boom") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
	h.update(esc)
	if h.m.connect != nil {
		t.Fatalf("esc should close the /model picker: %+v", h.m.connect)
	}
}

func TestFirstRunOpensConnectDialog(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Provider = nil
	cfg := &config.Config{Provider: "anthropic", Providers: map[string]config.Provider{"anthropic": {Type: "anthropic"}}}
	lister := &listing{models: []llm.ModelInfo{{ID: "llama3"}}}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return lister, nil })
	h.m.opts.Connect = true
	h.m.ready = false
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if h.m.connect == nil || h.m.connect.step != stepPick {
		t.Fatalf("dialog not opened: %+v", h.m.connect)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "not connected · /connect") || strings.Contains(v, "✓") {
		t.Fatalf("view:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	h.keys("hello")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "not connected to a provider yet: run /connect") {
		t.Fatalf("view:\n%s", v)
	}
	// Ollama needs no key: the key step is skipped after the URL.
	h.inputReset()
	h.keys("/connect")
	h.enter()
	h.keys("ollama")
	h.enter()
	if h.m.connect.step != stepURL {
		t.Fatalf("step = %d", h.m.connect.step)
	}
	h.updateCmd(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.connect == nil || h.m.connect.step != stepModel || len(h.m.connect.models) != 1 {
		t.Fatalf("step %d loading %v models %d", h.m.connect.step, h.m.connect.loading, len(h.m.connect.models))
	}
	h.enter()
	if h.m.opts.Agent.Provider != lister || h.m.opts.Agent.Model != "llama3" || !strings.Contains(plain(h.m.View()), "ollama/llama3") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
}

func TestExportWritesMarkdown(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hello there"), StopReason: llm.StopEndTurn})
	h.keys("hi")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	h.keys("/export notes/out.md")
	h.enter()
	data, err := os.ReadFile(filepath.Join(h.m.opts.Cwd, "notes", "out.md"))
	if err != nil {
		t.Fatalf("export file: %v\n%s", err, plain(h.m.View()))
	}
	if !strings.Contains(string(data), "## User\n\nhi") || !strings.Contains(string(data), "hello there") {
		t.Fatalf("export = %s", data)
	}
	if !strings.Contains(plain(h.m.View()), "exported 2 messages") {
		t.Fatalf("view:\n%s", plain(h.m.View()))
	}
}

func TestMemoryCommandsAndAutoExtraction(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "wrote"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "The x file is hand-maintained."), StopReason: llm.StopEndTurn},
	)
	store := memory.Open(t.TempDir(), h.m.opts.Cwd)
	h.m.opts.Memory, h.m.opts.AutoMemory = store, true
	h.m.opts.Agent.AutoApprove = true
	h.keys("/remember   Always run make test before committing ")
	h.enter()
	if e := store.Entries(); len(e) != 1 || e[0].Name != "always-test-before-committing" || e[0].Type != memory.TypeFeedback || e[0].Source != "user" {
		t.Fatalf("entries = %+v", e)
	}
	h.keys("/remember -g I prefer replies under ten lines")
	h.enter()
	if e, ok := store.Get("prefer-replies-under-ten-lines"); !ok || e.Scope != "global" || len(store.Entries()) != 2 {
		t.Fatalf("global entry = %+v", store.Entries())
	}
	h.keys("/memory")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "memory: 2 entries") || !strings.Contains(v, "(feedback): Always run make test before committing") || !strings.Contains(v, "(project, global)") && !strings.Contains(v, ", global)") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/memory make test")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "    Always run make test before committing") {
		t.Fatalf("recall view:\n%s", v)
	}
	h.keys("write x")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	// The turn changed a file, so extraction runs; drive its command.
	if !h.m.opts.Agent.LastTurnMutated() {
		t.Fatal("turn not mutated")
	}
	h.runCmd(h.m.extractMemory())
	if e, ok := store.Get("hand-maintained"); !ok || e.Source != "auto" {
		t.Fatalf("entries after extraction = %+v", store.Entries())
	}
	if v := plain(h.m.View()); !strings.Contains(v, "remembered hand-maintained (project): The x file is hand-maintained.") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/forget hand-maintained")
	h.enter()
	if _, ok := store.Get("hand-maintained"); ok {
		t.Fatal("forget by name failed")
	}
	h.keys("/forget 1")
	h.enter()
	if e := store.Entries(); len(e) != 1 {
		t.Fatalf("entries after forget = %+v", e)
	}
	h.keys("/forget 9")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no memory entry 9") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestPermissionPromptRuleKeySavesRule(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"docs/a.md","content":"1"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "wrote"), StopReason: llm.StopEndTurn},
	)
	var saved []string
	h.m.opts.Agent.SaveRule = func(r perm.Rule) error { saved = append(saved, r.String()); return nil }
	h.m.opts.Perms = &PermOptions{Save: func(kind string, r perm.Rule) error { saved = append(saved, kind+" "+r.String()); return nil }}
	h.keys("write it")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	if v := plain(h.m.View()); !strings.Contains(v, "save rule write_file(docs/*)") {
		t.Fatalf("prompt view:\n%s", v)
	}
	h.keys("r")
	h.pump(func() bool { return !h.m.running })
	if len(saved) != 1 || saved[0] != "write_file(docs/*)" || len(h.m.opts.Agent.Rules.Allow) != 1 {
		t.Fatalf("saved = %v rules = %+v", saved, h.m.opts.Agent.Rules)
	}
	h.keys("/permissions deny bash(git push*)")
	h.enter()
	if len(saved) != 2 || saved[1] != "deny bash(git push*)" || len(h.m.opts.Agent.Rules.Deny) != 1 {
		t.Fatalf("saved = %v", saved)
	}
	h.keys("/permissions")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "deny   bash(git push*)") || !strings.Contains(v, "allow  write_file(docs/*)") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestVerifyCommandSetsRunsAndGatesTurns(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"bad"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t2", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"ok"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "fixed"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.m.opts.Verify = &VerifyOptions{Trust: mcp.TrustStore{Path: filepath.Join(t.TempDir(), "trust.json")}}
	h.keys("/verify")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no verification commands") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/verify set grep -q ok a.txt")
	h.enter()
	cfg, _, err := verify.Load(h.m.opts.Cwd)
	if err != nil || len(cfg.Commands) != 1 || cfg.Commands[0] != "grep -q ok a.txt" || h.m.opts.Agent.Verify == nil || !h.m.opts.Verify.ProjectTrusted {
		t.Fatalf("cfg = %+v err = %v agent.Verify = %v", cfg, err, h.m.opts.Agent.Verify)
	}
	h.keys("/verify")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if v := plain(h.m.View()); !strings.Contains(v, "verification failed: grep -q ok a.txt (exit 2") {
		t.Fatalf("manual run view:\n%s", v)
	}
	h.keys("write it")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	v := plain(h.m.View())
	if !strings.Contains(v, "sending the model back to fix it (1/3)") || !strings.Contains(v, "verified: grep -q ok a.txt") {
		t.Fatalf("gated turn view:\n%s", v)
	}
	if got, _ := os.ReadFile(filepath.Join(h.m.opts.Cwd, "a.txt")); string(got) != "ok" {
		t.Fatalf("a.txt = %q", got)
	}
	h.keys("/verify clear")
	h.enter()
	if cfg, _, _ := verify.Load(h.m.opts.Cwd); len(cfg.Commands) != 0 || h.m.opts.Agent.Verify != nil {
		t.Fatalf("clear left cfg = %+v verify = %v", cfg, h.m.opts.Agent.Verify)
	}
}

func TestPlanModeHidesToolsUntilApproved(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "## Plan\n1. write a.txt"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"a.txt","content":"done"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "executed"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	h.keys("/plan approve")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "not in plan mode") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/plan add a.txt")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	sc := h.m.opts.Agent.Provider.(*scripted)
	if !h.m.opts.Agent.PlanMode || !strings.Contains(plain(h.m.View()), "PLAN ·") {
		t.Fatalf("plan mode not on; view:\n%s", plain(h.m.View()))
	}
	for _, d := range sc.reqs()[0].Tools {
		if d.Name == "write_file" {
			t.Fatal("write_file offered in plan mode")
		}
	}
	h.keys("/plan approve")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if h.m.opts.Agent.PlanMode {
		t.Fatal("still in plan mode after approve")
	}
	if got, _ := os.ReadFile(filepath.Join(h.m.opts.Cwd, "a.txt")); string(got) != "done" {
		t.Fatalf("a.txt = %q", got)
	}
	last := sc.reqs()[1].Messages
	if !strings.Contains(last[len(last)-1].Text(), "The plan is approved") {
		t.Fatalf("approval message missing: %q", last[len(last)-1].Text())
	}
}

func TestContextAndPinCommands(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"1"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.Agent.AutoApprove = true
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "rules.md"), []byte("tabs not spaces"), 0o644)
	h.keys("/pin")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no pins") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/pin @rules.md ")
	h.enter()
	h.keys("/pin never edit vendor")
	h.enter()
	if pins := h.m.opts.Agent.Pins; len(pins) != 2 || !strings.Contains(pins[0], "tabs not spaces") || pins[1] != "never edit vendor" {
		t.Fatalf("pins = %q", pins)
	}
	h.keys("write n")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	sc := h.m.opts.Agent.Provider.(*scripted)
	if sys := sc.reqs()[0].System; !strings.Contains(sys, "File rules.md as of pinning:\ntabs not spaces") || !strings.Contains(sys, "2. never edit vendor") {
		t.Fatalf("system = %q", sys)
	}
	re, _, err := session.Open(filepath.Dir(h.m.opts.Session.Path()), h.m.opts.Session.Meta.ID)
	if err != nil || len(re.Meta.Pins) != 2 {
		t.Fatalf("saved pins = %v err = %v", re.Meta.Pins, err)
	}
	h.keys("/unpin 1")
	h.enter()
	h.keys("/context")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "pins") || !strings.Contains(v, "(1)") || !strings.Contains(v, "by tool: write_file") || !strings.Contains(v, "largest tool results") || !strings.Contains(v, "write_file n.txt") {
		t.Fatalf("context view:\n%s", v)
	}
	h.keys("/unpin 5")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no pin 5") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestFooterShowsCacheRate(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hi"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 100, CacheReadTokens: 900}})
	if strings.Contains(plain(h.m.View()), "cache ") {
		t.Fatal("cache rate shown before any call")
	}
	h.keys("hello")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if v := plain(h.m.View()); !strings.Contains(v, "cache 90%") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/reload")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "rebuilt for the next call") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestJobsCommandAndIdleNotice(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.keys("/jobs")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no background jobs") {
		t.Fatalf("view:\n%s", v)
	}
	jobs := &tool.Jobs{Dir: t.TempDir()}
	h.m.opts.Jobs = jobs
	if _, err := jobs.Start(h.m.opts.Cwd, "echo hi; exit 4"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := jobs.Wait(ctx, 1); err != nil {
		t.Fatal(err)
	}
	h.update(tickMsg(time.Now()))
	if v := plain(h.m.View()); !strings.Contains(v, "⚙ job 1 finished with exit 4") {
		t.Fatalf("view:\n%s", v)
	}
	if msgs := h.m.opts.Agent.Messages; len(msgs) != 1 || !strings.Contains(msgs[0].Text(), "[background job update]") {
		t.Fatalf("context not added: %+v", msgs)
	}
	h.keys("/jobs")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "job 1 finished with exit 4") || !strings.Contains(v, "output:") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/jobs kill 1")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "already finished") {
		t.Fatalf("view:\n%s", v)
	}
}
