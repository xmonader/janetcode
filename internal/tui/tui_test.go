package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/internal/mcp/mcptest"
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
	ag := &agent.Agent{Provider: &scripted{responses: responses}, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess}
	m := newModel(Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test"})
	m.term.out = io.Discard // no escape sequences into the test's stdout
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
	for _, want := range []string{"❯ hi there", "hello back", "in 7 · out 2"} {
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
	// The clear armed the quit, so the next press within the window quits.
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
	if v := plain(h.m.View()); !strings.Contains(v, "queued 1 · two") {
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
	if h.m.picker == nil {
		t.Fatal("/resume with no argument should open the picker")
	}
	v := plain(h.m.View())
	if !strings.Contains(v, old[:12]) || !strings.Contains(v, "2 msgs") || !strings.Contains(v, "old question") {
		t.Fatalf("listing:\n%s", v)
	}
	// Typing filters the list; esc closes it without resuming anything.
	h.keys("nothing like this")
	if v := plain(h.m.View()); !strings.Contains(v, "nothing matches") {
		t.Fatalf("search did not filter:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.picker != nil || h.m.opts.Session.Meta.ID == old {
		t.Fatal("esc should close the picker without resuming")
	}
	// enter on the highlighted row resumes it.
	h.keys("/resume")
	h.enter()
	h.enter()
	if h.m.picker != nil || h.m.opts.Session.Meta.ID != old {
		t.Fatalf("enter did not resume: session %s", h.m.opts.Session.Meta.ID)
	}
	h.keys("/clear")
	h.enter()
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
	h.keys("/undo preview")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/undo would revert 1 file") || !strings.Contains(v, "remove   n.txt") {
		t.Fatalf("view:\n%s", v)
	}
	if _, err := os.Stat(target); err != nil {
		t.Fatal("preview removed the file")
	}
	h.keys("/redo")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "nothing to redo") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/undo")
	h.enter()
	if _, err := os.Stat(target); err == nil {
		t.Fatal("undo did not remove the created file")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "↶ removed") || !strings.Contains(v, "n.txt") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/redo")
	h.enter()
	if data, err := os.ReadFile(target); err != nil || string(data) != "x\n" {
		t.Fatalf("redo did not recreate the file: %q %v", data, err)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "↷ restored") || !strings.Contains(v, "redid 1 file change") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/undo")
	h.enter()
	if _, err := os.Stat(target); err == nil {
		t.Fatal("undo after redo did not remove the file")
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

func TestMCPAuthCommand(t *testing.T) {
	srv, fake := mcptest.NewAuthServer(t)
	defer srv.Close()
	h := newHarness(t)
	reg := h.m.opts.Agent.Tools
	tokens := &mcp.TokenStore{Path: filepath.Join(t.TempDir(), "mcp-auth.json")}
	mgr := &mcp.Manager{Options: mcp.Options{Root: h.m.opts.Cwd, HandshakeTimeout: 10 * time.Second, Tokens: tokens}, Registry: reg}
	secure := mcp.ServerConfig{URL: srv.URL + "/mcp"}
	opened := make(chan string, 1)
	h.m.opts.MCP = &MCPOptions{
		Manager: mgr, Global: map[string]mcp.ServerConfig{"secure": secure, "local": {Command: "true"}}, Tokens: tokens,
		OpenURL: func(u string) error {
			opened <- u
			go func() {
				if resp, err := http.Get(u); err == nil {
					resp.Body.Close()
				}
			}()
			return nil
		},
	}
	h.update(tea.WindowSizeMsg{Width: 100, Height: 120}) // the printed URL is long
	// The server fails to start without a token and says how to fix it.
	mgr.Start(context.Background(), "secure", secure, func(name string, c *mcp.Client, err error) {
		h.msgs <- mcpDoneMsg{name: name, err: err}
	})
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "authorization required") })

	h.keys("/mcp auth local")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "runs locally") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/mcp auth nope")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, `no MCP server named "nope"`) {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/mcp logout secure")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no token stored") {
		t.Fatalf("view:\n%s", v)
	}

	h.keys("/mcp auth secure")
	h.enter()
	// Wait for the banner, not for the tool. Registering the tools is the
	// last thing the restarted server's own goroutine does; the banner is
	// printed by the mcpDoneMsg that follows it through the queue, so a
	// pump that stopped at the registration raced the message that the
	// assertions below are about.
	h.pump(func() bool { return strings.Contains(plain(h.m.View()), "mcp secure ready") })
	if _, ok := reg.Get("mcp__secure__whoami"); !ok {
		t.Fatal("the server said it was ready without registering its tool")
	}
	select {
	case u := <-opened:
		if !strings.Contains(u, "/auth/authorize?") {
			t.Fatalf("opened %q", u)
		}
	default:
		t.Fatal("browser not opened")
	}
	v := plain(h.m.View())
	for _, want := range []string{"registered client " + fake.ClientID, "authorized · restarting the server", "mcp secure ready · 1 tools"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in:\n%s", want, v)
		}
	}
	if tok, ok := tokens.Get(secure.URL); !ok || tok.AccessToken != "at-1" {
		t.Fatalf("token = %+v %v", tok, ok)
	}
	h.keys("/mcp logout secure")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "token forgotten") {
		t.Fatalf("view:\n%s", v)
	}
	if _, ok := tokens.Get(secure.URL); ok {
		t.Fatal("token still stored after logout")
	}
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
	if v := plain(h.m.View()); strings.Contains(v, "sk-one") || !strings.Contains(v, "••") || !strings.Contains(v, "2 keys entered") {
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
	if v := plain(h.m.View()); !strings.Contains(v, "remembered: hand-maintained (project): The x file is hand-maintained.") {
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
	if v := plain(h.m.View()); !strings.Contains(v, "rule write_file(docs/*)") {
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

func TestStatsCommand(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "r1", Name: "read_file", Input: json.RawMessage(`{"path":"missing.txt"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "gone"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1200, OutputTokens: 30}},
	)
	h.keys("/stats")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "0 turns · 0 model calls") || !strings.Contains(v, "no tool calls yet") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("read missing")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	h.keys("/stats")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"1 turn · 2 model calls", "1 tool call, 1 failed:", "read_file", "1 failed", "tokens · in 1.2k · out 30"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in view:\n%s", want, v)
		}
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

func TestForkTitleAndSearchCommands(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "I added a token bucket."), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "\"Rate limiting for the API.\"\n"), StopReason: llm.StopEndTurn}, // title
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "using redis now"), StopReason: llm.StopEndTurn},
	)
	h.m.opts.AutoTitle = true
	h.keys("add rate limiting to the api")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	h.runCmd(h.m.autoTitle()) // finishTurn batches this; the harness drops commands
	if got := h.m.opts.Session.Meta.Title; got != "Rate limiting for the API" {
		t.Fatalf("title = %q", got)
	}
	orig := h.m.opts.Session
	h.keys("/fork redis variant")
	h.enter()
	if h.m.opts.Session == orig || h.m.opts.Session.Meta.Title != "redis variant" || h.m.opts.Session.Meta.ForkOf != orig.Meta.ID {
		t.Fatalf("fork = %+v", h.m.opts.Session.Meta)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "forked Rate limiting for the API →") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("switch to redis")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	_, origMsgs, _ := session.Open(dirOf(orig), orig.Meta.ID)
	_, forkMsgs, _ := session.Open(dirOf(orig), h.m.opts.Session.Meta.ID)
	if len(origMsgs) != 2 || len(forkMsgs) != 4 {
		t.Fatalf("orig=%d fork=%d", len(origMsgs), len(forkMsgs))
	}
	h.keys("/title redis for real")
	h.enter()
	if re, _, _ := session.Open(dirOf(orig), h.m.opts.Session.Meta.ID); re.Meta.Title != "redis for real" {
		t.Fatalf("saved title = %q", re.Meta.Title)
	}
	h.keys("/history token bucket")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "redis for real") || !strings.Contains(v, "Rate limiting for the API") || !strings.Contains(v, "assistant: I added a token bucket.") {
		t.Fatalf("search view:\n%s", v)
	}
	h.keys("/history nothing-like-this")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no session mentions nothing-like-this") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/resume")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "Rate limiting for the API") {
		t.Fatalf("resume list lacks the title:\n%s", v)
	}
}

func gitInitTUI(t *testing.T, root string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestOpenAndDiffCommands(t *testing.T) {
	h := newHarness(t)
	root := h.m.opts.Cwd
	var lines []string
	for i := 1; i <= 30; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	h.keys("/diff")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/diff needs a git repository") {
		t.Fatalf("view:\n%s", v)
	}
	gitInitTUI(t, root)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 120}) // whole listings must fit the viewport
	h.keys("/open a.txt:10")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "# a.txt  lines 1-30 of 30") || !strings.Contains(v, "▶10 │ line 10") || !strings.Contains(v, " 9 │ line 9") {
		t.Fatalf("open view:\n%s", v)
	}
	// Two separate hunks: change line 2 and line 28; plus an untracked file.
	lines[1] = "line 2 changed"
	lines[27] = "line 28 changed"
	os.WriteFile(filepath.Join(root, "a.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "new.txt"), []byte("x\ny\n"), 0o644)
	h.keys("/diff")
	h.enter()
	v = plain(h.m.View())
	if !strings.Contains(v, "a.txt  (2 hunks, +2 -2)") || !strings.Contains(v, "+ new.txt  (new, 2 lines)") {
		t.Fatalf("diff list:\n%s", v)
	}
	h.keys("/diff a.txt")
	h.enter()
	v = plain(h.m.View())
	if !strings.Contains(v, "@@ hunk 1/2") || !strings.Contains(v, "@@ hunk 2/2") || !strings.Contains(v, "-line 28") || !strings.Contains(v, "+line 28 changed") {
		t.Fatalf("file diff:\n%s", v)
	}
	h.keys("/diff revert a.txt:2")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "reverted hunk 2 of a.txt (1 left)") {
		t.Fatalf("revert view:\n%s", v)
	}
	got, _ := os.ReadFile(filepath.Join(root, "a.txt"))
	if s := string(got); !strings.Contains(s, "line 2 changed\n") || strings.Contains(s, "line 28 changed") || !strings.Contains(s, "\nline 28\n") {
		t.Fatalf("after revert:\n%s", s)
	}
	h.keys("/diff revert a.txt:5")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "a.txt has 1 hunks") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/diff revert new.txt:1")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no tracked changes to revert") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/open missing.txt")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "open: ") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestChangesBlameAndUndoOne(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Write the list."},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"l.txt","content":"a\nb\nc\nd\ne\nf\ng\nh\n"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Capitalise b."},
			{Type: llm.BlockToolUse, ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"l.txt","old_string":"b","new_string":"B"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "Capitalise h."},
			{Type: llm.BlockToolUse, ID: "e2", Name: "edit_file", Input: json.RawMessage(`{"path":"l.txt","old_string":"h","new_string":"H"}`)},
		}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 120})
	h.keys("/changes")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no file changes in this session yet") {
		t.Fatalf("view:\n%s", v)
	}
	h.m.opts.Agent.AutoApprove = true
	h.m.opts.Agent.Tools = tool.NewRegistry(tool.WriteFile{Root: h.m.opts.Cwd}, tool.EditFile{Root: h.m.opts.Cwd})
	h.keys("make the list")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	h.keys("/changes")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"turn 1 · Write the list.", "#1   write_file l.txt  +8 -0", "turn 1 · Capitalise b.", "#2   edit_file  l.txt  +1 -1", "#3   edit_file  l.txt  +1 -1"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in view:\n%s", want, v)
		}
	}
	h.keys("/changes 3")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "#3  edit_file  l.txt  · Capitalise h.") || !regexp.MustCompile(`-h\s*\n\s*\+H`).MatchString(v) {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/blame l.txt")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "#2       2 │ B") || !strings.Contains(v, "#1       3 │ c") || !strings.Contains(v, "#3       8 │ H") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/undo 2")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "↶ #2: reverted 1 hunk(s) of l.txt") {
		t.Fatalf("view:\n%s", v)
	}
	if data, _ := os.ReadFile(filepath.Join(h.m.opts.Cwd, "l.txt")); string(data) != "a\nb\nc\nd\ne\nf\ng\nH\n" {
		t.Fatalf("file after /undo 2: %q", data)
	}
	h.keys("/undo 2")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "already undone") {
		t.Fatalf("view:\n%s", v)
	}
	h.keys("/blame missing.txt")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no change to") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestReadlineKeysEditTheInput(t *testing.T) {
	h := newHarness(t)
	h.keys("hello world")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlW})
	if got := h.m.input.Value(); got != "hello " {
		t.Fatalf("after ctrl+w: %q", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	if got := h.m.input.Value(); got != "" {
		t.Fatalf("after ctrl+u: %q", got)
	}
	// With nothing to edit, ctrl+u pages the transcript and must not panic.
	h.update(tea.KeyMsg{Type: tea.KeyCtrlU})
	h.keys("abc")
	h.update(tea.KeyMsg{Type: tea.KeyCtrlA})
	h.update(tea.KeyMsg{Type: tea.KeyCtrlK})
	if got := h.m.input.Value(); got != "" {
		t.Fatalf("after ctrl+a ctrl+k: %q", got)
	}
}

// TestFitPathKeepsTheTail: a path that does not fit loses leading
// components, not the middle of a directory name.
func TestFitPathKeepsTheTail(t *testing.T) {
	p := "/tmp/claude-1000/dd12e125-65a6-415a-b19b-31de5e8c912b/scratchpad/tuiproj"
	if got := fitPath(p, 40); got != "…/scratchpad/tuiproj" {
		t.Fatalf("got %q", got)
	}
	if got := fitPath(p, 12); got != "…/tuiproj" {
		t.Fatalf("got %q", got)
	}
	if got := fitPath("/a/b", 40); got != "/a/b" {
		t.Fatalf("fits unchanged: %q", got)
	}
}

// TestStrayApprovalKeyIsNotAPrompt: a lone "y" typed just after a
// permission prompt closed was meant for the prompt; it must not become
// a turn of the model guessing what "y" means.
func TestStrayApprovalKeyIsNotAPrompt(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "y?"), StopReason: llm.StopEndTurn})
	h.m.pendingClosedAt = time.Now()
	h.keys("y")
	h.enter()
	if h.m.running || h.m.input.Value() != "" || !strings.Contains(h.m.status, "stray") {
		t.Fatalf("running=%v input=%q status=%q", h.m.running, h.m.input.Value(), h.m.status)
	}
	// Long after the prompt, "y" is just a prompt like any other.
	h.m.pendingClosedAt = time.Now().Add(-time.Minute)
	h.keys("y")
	h.enter()
	if !h.m.running {
		t.Fatal("a y typed long after the prompt should be sent")
	}
	h.pump(func() bool { return !h.m.running })
}

// TestCtrlCWhenIdleArmsFirst: one ctrl+c when idle says "again to quit";
// the second within the window quits; after the window it arms again.
func TestCtrlCWhenIdleArmsFirst(t *testing.T) {
	h := newHarness(t)
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil || !strings.Contains(h.m.status, "again to quit") {
		t.Fatalf("first press should arm: cmd=%v status=%q", cmd, h.m.status)
	}
	if _, cmd := h.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd == nil {
		t.Fatal("second press should quit")
	}
	h2 := newHarness(t)
	h2.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	h2.m.quitArmedAt = time.Now().Add(-time.Minute)
	if _, cmd := h2.m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Fatal("a press after the window should arm again, not quit")
	}
}

// TestPermissionKeysFitOneLine: the keys line never exceeds the width,
// whatever the rule's length.
func TestPermissionKeysFitOneLine(t *testing.T) {
	h := newHarness(t)
	for _, w := range []int{60, 80, 110, 200} {
		h.m.width = w
		for _, expandable := range []bool{false, true} {
			line := plain(h.m.permissionKeys("edit_file", "edit_file(/tmp/claude-1000/-home-driver-wspace-janetcode/dd12e125-65a6-415a-b19b-31de5e8c912b/scratchpad/tuiproj/*)", expandable))
			if lipgloss.Width(line) > w-6 || !strings.Contains(line, "n") {
				t.Fatalf("width %d expandable=%v: %d cols: %q", w, expandable, lipgloss.Width(line), line)
			}
		}
	}
}

// TestGoalStartsWorkingAtOnce: /goal <objective> used to record the goal
// and stop, since the loop only advanced when a turn ended. It starts
// the first round immediately, and the status bar shows the goal.
func TestGoalStartsWorkingAtOnce(t *testing.T) {
	// A goal keeps going round after round until the model reports it
	// done, so the fake needs a reply for every round it may take.
	var replies []*llm.Response
	for i := 0; i < 10; i++ {
		replies = append(replies, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "on it"), StopReason: llm.StopEndTurn})
	}
	h := newHarness(t, replies...)
	h.keys("/goal make the tests green")
	h.enter()
	if !h.m.running {
		t.Fatal("setting a goal should start a turn")
	}
	h.pump(func() bool { return !h.m.running })
	reqs := h.m.opts.Agent.Provider.(*scripted).reqs()
	if len(reqs) == 0 || !strings.Contains(reqs[0].Messages[len(reqs[0].Messages)-1].Text(), "make the tests green") {
		t.Fatalf("the first round should carry the objective: %+v", reqs)
	}
	if v := plain(h.m.statusView()); !strings.Contains(v, "goal g1") {
		t.Fatalf("status = %q", v)
	}
}

// TestViewportPinning covers item 2 of the polish round: streaming output
// follows the bottom until the user scrolls away, a resize mid-stream does
// not yank them back, the "pgdn for latest" hint tracks that state, and a
// new prompt re-pins.
func TestViewportPinning(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn},
	)
	h.update(tea.WindowSizeMsg{Width: 80, Height: 12})
	for i := 0; i < 60; i++ {
		h.m.transcript.info(fmt.Sprintf("filler line %d", i))
	}
	h.m.refresh(false)
	if !h.m.pinned || !h.m.viewport.AtBottom() {
		t.Fatal("a fresh transcript must follow the bottom")
	}
	if strings.Contains(plain(h.m.statusView()), "pgdn for latest") {
		t.Fatal("hint shown while pinned")
	}
	// Scrolling up unpins and raises the hint.
	h.update(tea.KeyMsg{Type: tea.KeyPgUp})
	if h.m.pinned {
		t.Fatal("pgup must unpin")
	}
	if !strings.Contains(plain(h.m.statusView()), "pgdn for latest") {
		t.Fatal("hint missing after scrolling up")
	}
	// New output while scrolled up must not move the view...
	off := h.m.viewport.YOffset
	h.m.transcript.info("streamed while scrolled up")
	h.m.refresh(false)
	if h.m.viewport.YOffset != off {
		t.Fatalf("output yanked the view: offset %d → %d", off, h.m.viewport.YOffset)
	}
	// ...and neither must a resize.
	h.update(tea.WindowSizeMsg{Width: 80, Height: 14})
	if h.m.pinned {
		t.Fatalf("resize re-pinned a scrolled-up view (offset %d)", h.m.viewport.YOffset)
	}
	// Scrolling back to the bottom re-pins.
	for i := 0; i < 30; i++ {
		h.update(tea.KeyMsg{Type: tea.KeyPgDown})
	}
	if !h.m.pinned {
		t.Fatal("reaching the bottom must re-pin")
	}
	// A prompt sent while scrolled up re-pins too.
	h.update(tea.KeyMsg{Type: tea.KeyPgUp})
	if h.m.pinned {
		t.Fatal("pgup must unpin")
	}
	h.keys("hi")
	h.enter()
	if !h.m.pinned {
		t.Fatal("a new prompt must re-pin")
	}
	h.pump(func() bool { return !h.m.running })
}

// TestInputBoxStates covers item 5: the border colour and the placeholder
// must agree about whose turn it is. Idle and the connect dialog are
// accent, a running turn is dim, and a denial reason is warn like the
// permission box it answers.
func TestInputBoxStates(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	check := func(what string, want lipgloss.Style, wantHint string) {
		t.Helper()
		if got := h.m.inputBox().GetBorderTopForeground(); got != want.GetBorderTopForeground() {
			t.Errorf("%s: border = %v, want %v", what, got, want.GetBorderTopForeground())
		}
		if !strings.Contains(h.m.placeholder(), wantHint) {
			t.Errorf("%s: placeholder = %q, want it to mention %q", what, h.m.placeholder(), wantHint)
		}
	}
	check("idle", styleBoxIdle, defaultPlaceholder)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	// The permission box takes the footer over, in the same warning colour
	// the denial box uses.
	if stylePermBox.GetBorderTopForeground() != styleBoxWarn.GetBorderTopForeground() {
		t.Error("the permission box does not match the warning palette")
	}
	h.keys("n")
	if !h.m.denying {
		t.Fatal("n opens the reason prompt")
	}
	check("denial reason", styleBoxWarn, "Why?")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	check("back to idle", styleBoxIdle, defaultPlaceholder)
	h.m.opts.Agent.SetPlanMode(true)
	check("plan mode", styleBoxIdle, "Plan mode")
	h.m.opts.Agent.SetPlanMode(false)
	h.m.running = true
	check("running", styleBoxBusy, "queue")
	h.m.running = false
	// The connect dialog is another box you type into: same accent border.
	if styleDialog.GetBorderTopForeground() != styleBoxIdle.GetBorderTopForeground() {
		t.Error("the connect dialog border does not match an idle input box")
	}
}

// TestStatusBarFitsWidth covers item 6: at 80 columns nothing on the
// status line may overflow, in any state. The status text yields; the
// context gauge never does.
func TestStatusBarFitsWidth(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "x"), StopReason: llm.StopEndTurn})
	states := []struct {
		name string
		set  func()
	}{
		{"idle", func() { h.m.running, h.m.pending, h.m.status = false, nil, "" }},
		{"running with a long phase", func() {
			h.m.running = true
			h.m.startedAt = time.Now().Add(-90 * time.Second)
			h.m.phase = "write_file scheme.py · writing 754 lines (21.3KB) so far"
		}},
		{"approval needed", func() {
			h.m.running = false
			h.m.pending = &permissionMsg{call: agent.ToolCall{Name: "bash"}}
		}},
		{"done with a verdict", func() {
			h.m.pending = nil
			h.m.status = "✓ verified · done in 12.4s · in 12k · out 3.1k · $0.02 · 3 files changed · /undo reverts"
		}},
		{"scrolled up", func() { h.m.pinned = false }},
	}
	for _, w := range []int{120, 80, 60, 40, 24} {
		h.update(tea.WindowSizeMsg{Width: w, Height: 24})
		for _, st := range states {
			st.set()
			line := h.m.statusView()
			if got := lipgloss.Width(line); got > w {
				t.Errorf("width %d, %s: status line is %d columns:\n%s", w, st.name, got, plain(line))
			}
			// The gauge is the one thing that never yields.
			if !strings.Contains(plain(line), "ctx ") {
				t.Errorf("width %d, %s: the gauge was dropped:\n%s", w, st.name, plain(line))
			}
		}
		h.m.pinned = true
	}
	// At 80 columns with the scroll hint up, the hint survives and the
	// status text is the part that gives way.
	h.update(tea.WindowSizeMsg{Width: 80, Height: 24})
	h.m.pinned = false
	h.m.status = strings.Repeat("a very long status ", 6)
	line := plain(h.m.statusView())
	if !strings.Contains(line, "pgdn for latest") || !strings.Contains(line, "…") {
		t.Errorf("at 80 columns the hint should stay and the text give way:\n%s", line)
	}
	h.m.pinned = true
}

// TestHeaderNarrowWidths covers item 7: the header drops the session
// title first, then the branch, then shortens the path, and never wraps
// or spills past the width.
func TestHeaderNarrowWidths(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "x"), StopReason: llm.StopEndTurn})
	h.m.opts.Cwd = "/home/someone/work/projects/janetcode/internal/tui"
	h.m.branch = "feature/very-long-branch-name"
	h.m.opts.Session.Meta.Title = "polishing the terminal interface"
	h.m.opts.Agent.AutoApprove = true
	at := func(w int) string {
		h.update(tea.WindowSizeMsg{Width: w, Height: 24})
		line := h.m.headerView()
		if got := lipgloss.Width(line); got != w {
			t.Fatalf("width %d: header is %d columns:\n%s", w, got, plain(line))
		}
		if strings.Contains(line, "\n") {
			t.Fatalf("width %d: header wrapped:\n%s", w, plain(line))
		}
		return plain(line)
	}
	wide := at(120)
	for _, want := range []string{"janetcode", "polishing the terminal", "YOLO", "tui", "feature/very-long-branch-name"} {
		if !strings.Contains(wide, want) {
			t.Errorf("at 120 columns the header lost %q:\n%s", want, wide)
		}
	}
	// The title is the first thing to go, and the branch outlives it.
	narrow := at(90)
	if strings.Contains(narrow, "polishing") {
		t.Errorf("at 90 columns the title should be dropped first:\n%s", narrow)
	}
	if !strings.Contains(narrow, "feature/very-long-branch-name") {
		t.Errorf("at 90 columns the branch should still be whole:\n%s", narrow)
	}
	// Then the branch goes, whole rather than cut in half.
	narrower := at(60)
	if strings.Contains(narrower, "⎇") || strings.Contains(narrower, "feature/very-") {
		t.Errorf("at 60 columns the branch should be dropped, not clipped:\n%s", narrower)
	}
	if !strings.Contains(narrower, "tui") {
		t.Errorf("at 60 columns the path should survive:\n%s", narrower)
	}
	// Then the path is shortened from the front, keeping the leaf.
	if got := at(40); !strings.Contains(got, "…/tui") {
		t.Errorf("at 40 columns the path should be shortened to its tail:\n%s", got)
	}
	// Below that, whatever is left still fits exactly.
	for _, w := range []int{30, 24, 20} {
		at(w)
	}
}

// failMidStream emits some assistant text and the start of a tool call and
// then fails, the way a provider does when the connection drops.
type failMidStream struct{ err error }

func (failMidStream) Name() string { return "fake" }
func (p failMidStream) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	b := llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x"}`)}
	emit(llm.Event{Type: llm.EventTextDelta, Text: "I will start by"})
	emit(llm.Event{Type: llm.EventToolUseStart, Block: &b})
	return nil, p.err
}

// TestErrorRendering covers item 9: a multi-line error is one colour with
// its continuation indented under the mark, and a provider failure
// mid-stream leaves no half-rendered assistant or tool block behind.
func TestErrorRendering(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Provider = failMidStream{err: errors.New("Post \"https://api.example.com/v1/messages\": dial tcp 10.0.0.1:443: connect: connection refused")}
	h.update(tea.WindowSizeMsg{Width: 60, Height: 30})
	h.keys("do it")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	v := plain(h.m.View())
	if !strings.Contains(v, "✗ cannot reach the API") {
		t.Fatalf("error not shown:\n%s", v)
	}
	// Every continuation line of the error is indented two columns under
	// the mark, including the ones wrapping made.
	var seen, indented int
	for _, l := range strings.Split(v, "\n") {
		switch {
		case strings.HasPrefix(l, "✗ cannot reach the API"):
			seen++
		case strings.Contains(l, "check the base URL") || strings.Contains(l, "connection refused"):
			seen++
			if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
				indented++
			}
		}
	}
	if seen < 2 || indented == 0 {
		t.Errorf("error continuation lines are not indented under the mark:\n%s", v)
	}
	// Nothing is left streaming: no cursor, and the tool that never ran is
	// marked failed rather than spinning.
	if strings.Contains(v, "▍") {
		t.Errorf("assistant block left half-rendered:\n%s", v)
	}
	if !strings.Contains(v, "✗ write_file") {
		t.Errorf("the unfinished tool call is still open:\n%s", v)
	}
	for _, e := range h.m.transcript.entries {
		switch e.kind {
		case entryAssistant, entryThinking, entryTool:
			if !e.done {
				t.Errorf("a %v entry was left streaming after the error", e.kind)
			}
		}
	}
}

// TestThemeDetection covers item 10: an unanswered background query means
// dark, an explicit theme wins, and whatever is decided is pinned on
// lipgloss so the palette and the markdown style agree.
func TestThemeDetection(t *testing.T) {
	defer lipgloss.SetHasDarkBackground(lipgloss.HasDarkBackground())
	if got := detectTheme("light"); got != "light" || lipgloss.HasDarkBackground() {
		t.Errorf("explicit light: theme=%q dark=%v", got, lipgloss.HasDarkBackground())
	}
	if got := detectTheme("dark"); got != "dark" || !lipgloss.HasDarkBackground() {
		t.Errorf("explicit dark: theme=%q dark=%v", got, lipgloss.HasDarkBackground())
	}
	// Nothing to query in a test process: the fallback must be dark.
	if got := detectTheme(""); got != "dark" {
		t.Errorf("with no answer from the terminal the theme should be dark, got %q", got)
	}
	// A model built with a theme renders its markdown in that theme and
	// pins lipgloss to it, so the palette cannot drift the other way.
	h := newHarness(t)
	m2 := newModel(Options{Agent: h.m.opts.Agent, Session: h.m.opts.Session, Cwd: h.m.opts.Cwd, Theme: "light"})
	if m2.transcript.styleName != "light" || lipgloss.HasDarkBackground() {
		t.Errorf("model theme=%q, lipgloss dark=%v: the palette and the markdown style disagree",
			m2.transcript.styleName, lipgloss.HasDarkBackground())
	}
}

// TestLightPaletteContrast covers the other half of item 10: faint text on
// a white background was at 2.5:1, which is barely visible.
func TestLightPaletteContrast(t *testing.T) {
	for _, c := range []struct {
		name string
		col  lipgloss.AdaptiveColor
		min  float64
	}{
		{"faint", colFaint, 3.5}, {"dim", colDim, 4.0}, {"accent", colAccent, 4.0},
		{"warn", colWarn, 4.0}, {"err", colErr, 4.0}, {"ok", colOK, 4.0},
		{"user", colUser, 4.0}, {"tool", colTool, 4.0}, {"border", colBorder, 2.5},
	} {
		if got := contrast(c.col.Light, "#ffffff"); got < c.min {
			t.Errorf("light %s (%s) on white is %.2f:1, want at least %.1f", c.name, c.col.Light, got, c.min)
		}
		if got := contrast(c.col.Dark, "#1e1e1e"); got < c.min-1 {
			t.Errorf("dark %s (%s) on #1e1e1e is %.2f:1, want at least %.1f", c.name, c.col.Dark, got, c.min-1)
		}
	}
}

// contrast is the WCAG contrast ratio between two "#rrggbb" colours.
func contrast(a, b string) float64 {
	l1, l2 := relLuminance(a), relLuminance(b)
	if l1 < l2 {
		l1, l2 = l2, l1
	}
	return (l1 + 0.05) / (l2 + 0.05)
}

func relLuminance(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	var out float64
	for i, w := range []float64{0.2126, 0.7152, 0.0722} {
		v, err := strconv.ParseInt(hex[i*2:i*2+2], 16, 32)
		if err != nil {
			return 0
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			c /= 12.92
		} else {
			c = math.Pow((c+0.055)/1.055, 2.4)
		}
		out += w * c
	}
	return out
}

// TestClearAndResumeBanners covers item 11: /clear brings the welcome
// banner back and clears the header title, and /resume replaces the
// welcome with a resumed banner rather than stacking one under the other.
func TestClearAndResumeBanners(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hello back"), StopReason: llm.StopEndTurn})
	if err := h.m.opts.Session.SetTitle("an older conversation"); err != nil {
		t.Fatal(err)
	}
	h.keys("hi")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if !strings.Contains(plain(h.m.headerView()), "an older conversation") {
		t.Fatal("the title should be in the header to begin with")
	}
	old := h.m.opts.Session.Meta.ID

	h.keys("/clear")
	h.enter()
	v := plain(h.m.View())
	if !strings.Contains(v, "janetcode test") || !strings.Contains(v, "@file attaches") {
		t.Errorf("the welcome banner did not come back after /clear:\n%s", v)
	}
	if strings.Contains(plain(h.m.headerView()), "an older conversation") {
		t.Errorf("/clear left the old title in the header:\n%s", plain(h.m.headerView()))
	}
	if strings.Contains(v, "hello back") {
		t.Errorf("/clear kept the old conversation:\n%s", v)
	}

	h.keys("/resume " + old)
	h.enter()
	v = plain(h.m.View())
	if !strings.Contains(v, "resumed session "+old) {
		t.Errorf("no resumed banner:\n%s", v)
	}
	if n := strings.Count(v, "janetcode test"); n != 1 {
		t.Errorf("the resumed banner should replace the welcome, found %d banners:\n%s", n, v)
	}
	if !strings.Contains(v, "hello back") {
		t.Errorf("the resumed conversation is missing:\n%s", v)
	}
	// Exactly one banner entry, and it is the first thing in the transcript.
	banners := 0
	for _, e := range h.m.transcript.entries {
		if e.kind == entryBanner {
			banners++
		}
	}
	if banners != 1 || h.m.transcript.entries[0].kind != entryBanner {
		t.Errorf("banners=%d, first entry kind=%v", banners, h.m.transcript.entries[0].kind)
	}
}

// TestCompletionMenusMatch covers item 12: the "/" menu and the @path menu
// are the same menu — same height whatever they contain, so the transcript
// above does not reflow on every keystroke, and the same keys.
func TestCompletionMenusMatch(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "alpha.go"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "alpine.go"), []byte("x"), 0o644)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 30})

	closed := h.m.viewport.Height
	h.keys("/")
	openHeight := h.m.viewport.Height
	if openHeight >= closed {
		t.Fatal("the menu should take room from the transcript")
	}
	// Narrowing the menu must not move the viewport again.
	for _, s := range []string{"c", "o", "n"} {
		h.keys(s)
		if h.m.viewport.Height != openHeight {
			t.Fatalf("typing %q resized the transcript: %d → %d", s, openHeight, h.m.viewport.Height)
		}
	}
	rows := func() int { return strings.Count(h.m.footerView(), "\n") }
	withOne := rows()
	h.inputReset()
	h.keys("/")
	if got := rows(); got != withOne {
		t.Errorf("a full menu is %d rows and a one-row menu is %d", got, withOne)
	}
	// The @path menu is the same size, and says the same thing at the foot.
	h.inputReset()
	h.keys("see @")
	if got := rows(); got != withOne {
		t.Errorf("the @path menu is %d rows, the command menu %d", got, withOne)
	}
	if !strings.Contains(plain(h.m.footerView()), "tab or enter accepts") {
		t.Errorf("the @path menu is missing the key line:\n%s", plain(h.m.footerView()))
	}
	// Tab extends to what the matches share, in both menus.
	h.inputReset()
	h.keys("see @a")
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "see @alp" {
		t.Errorf("path tab = %q, want the shared prefix", got)
	}
	h.inputReset()
	h.keys("/conn")
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "/connect " {
		t.Errorf("command tab = %q", got)
	}
	// Down then tab takes the highlighted row in both.
	h.inputReset()
	h.keys("see @alp")
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != "see @alpine.go " {
		t.Errorf("path tab after down = %q", got)
	}
	h.inputReset()
	h.keys("/co")
	c, _ := h.m.menu()
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.update(tea.KeyMsg{Type: tea.KeyTab})
	if got := h.m.input.Value(); got != c[1].name+" " {
		t.Errorf("command tab after down = %q, want %q", got, c[1].name+" ")
	}
}

// TestPermissionBoxExpandHint covers item 13: the box offers ctrl+o only
// when there is more of the call to see, and wears the same warning
// colour as the "approval needed" status.
func TestPermissionBoxExpandHint(t *testing.T) {
	short := agent.ToolCall{ID: "e1", Name: "edit_file", Input: json.RawMessage(`{"path":"a.go"}`),
		Preview: "--- a.go\n+++ a.go\n@@ -1,1 +1,1 @@\n-old\n+new\n"}
	if pendingExpandable(short) {
		t.Error("a five-line diff has nothing to expand")
	}
	long := short
	for i := 0; i < 200; i++ {
		long.Preview += fmt.Sprintf("+line %d\n", i)
	}
	if !pendingExpandable(long) {
		t.Error("a 200-line diff should offer ctrl+o")
	}
	// A bash command is shown whole on the tool line: never expandable.
	if pendingExpandable(agent.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"ls"}`)}) {
		t.Error("bash offered ctrl+o")
	}

	h := newHarness(t,
		&llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"n.txt","content":"x\n"}`)}}}, StopReason: llm.StopToolUse},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn},
	)
	h.keys("make n.txt")
	h.enter()
	h.pump(func() bool { return h.m.pending != nil })
	v := plain(h.m.permissionView())
	if !strings.Contains(v, "allow write_file") {
		t.Fatalf("permission box:\n%s", v)
	}
	if strings.Contains(v, "ctrl+o") {
		t.Errorf("a one-line write offered ctrl+o:\n%s", v)
	}
	// The box border and the status word are the same warning colour.
	if stylePermBox.GetBorderTopForeground() != styleWarn.GetForeground() {
		t.Errorf("permission box border %v, warning text %v",
			stylePermBox.GetBorderTopForeground(), styleWarn.GetForeground())
	}
	h.keys("y")
	h.pump(func() bool { return !h.m.running })
}

// TestUserVisibleStringStyle covers item 14: the strings the interface
// shows follow one style — sentence case, no shouting outside the header's
// mode prefixes, "·" between clauses rather than a run of spaces, and
// plural() rather than "(s)".
func TestUserVisibleStringStyle(t *testing.T) {
	// Acronyms and the header's mode prefixes are the only capitals.
	allowed := map[string]bool{
		"MCP": true, "API": true, "URL": true, "HEAD": true, "JSON": true,
		"YOLO": true, "EDITS": true, "PLAN": true, "READ-ONLY": true, "OK": true,
	}
	caps := regexp.MustCompile(`\b[A-Z][A-Z-]+\b`)
	check := func(what, s string) {
		t.Helper()
		if strings.Contains(s, "(s)") {
			t.Errorf("%s uses (s) instead of plural(): %q", what, s)
		}
		if strings.Contains(s, "  ") {
			t.Errorf("%s has a double space: %q", what, s)
		}
		if strings.HasSuffix(strings.TrimSpace(s), ".") {
			t.Errorf("%s ends with a full stop: %q", what, s)
		}
		for _, w := range caps.FindAllString(s, -1) {
			if !allowed[w] {
				t.Errorf("%s shouts %q: %q", what, w, s)
			}
		}
	}
	for _, c := range commands {
		check("the help for "+c.name, c.help)
		check("the arguments of "+c.name, c.args)
		if c.help != strings.ToLower(c.help[:1])+c.help[1:] && !allowed[strings.SplitN(c.help, " ", 2)[0]] {
			t.Errorf("the help for %s is not sentence case: %q", c.name, c.help)
		}
	}
	h := newHarness(t)
	check("the default placeholder", defaultPlaceholder)
	for _, set := range []func(){
		func() { h.m.running = true },
		func() { h.m.running, h.m.denying = false, true },
		func() { h.m.denying = false; h.m.opts.Agent.SetPlanMode(true) },
		func() { h.m.opts.Agent.SetPlanMode(false); h.m.opts.Agent.ReadOnly = true },
	} {
		set()
		check("a placeholder", h.m.placeholder())
	}
	h.m.opts.Agent.ReadOnly = false
	check("the welcome banner", h.m.welcome(""))
	check("the resumed banner", h.m.welcome(resumedLine("abc123", 2)))
	// Sizes, durations and token counts go through the shared helpers.
	if shortBytes(2048) != "2.0KB" || shortDuration(1500*time.Millisecond) != "1.5s" || shortTokens(12000) != "12k" {
		t.Errorf("the shared formatters changed shape: %q %q %q",
			shortBytes(2048), shortDuration(1500*time.Millisecond), shortTokens(12000))
	}
	if plural(1, "file") != "1 file" || plural(2, "file") != "2 files" {
		t.Errorf("plural() = %q / %q", plural(1, "file"), plural(2, "file"))
	}
}

// TestPhaseNamesWhatTheTurnWaitsOn: a retry countdown and a subagent both
// reach the status line, so a stalled turn says what it is stalled on.
func TestPhaseNamesWhatTheTurnWaitsOn(t *testing.T) {
	h := newHarness(t)
	h.m.running = true
	h.m.applyEvent(agent.Event{Kind: agent.EvPhase, Text: "retrying 2/5 · rate limited · in 12s"})
	h.m.refresh(false)
	if v := plain(h.m.View()); !strings.Contains(v, "retrying 2/5 · rate limited · in 12s") {
		t.Fatalf("retry phase missing from the status line:\n%s", v)
	}
	if n := len(h.m.transcript.entries); n != 1 {
		t.Fatalf("a phase must not add a transcript entry (%d entries)", n)
	}
	call := agent.ToolCall{ID: "t1", Name: "task"}
	h.m.applyEvent(agent.Event{Kind: agent.EvToolRun, Call: &call})
	h.m.refresh(false)
	if v := plain(h.m.View()); !strings.Contains(v, "waiting on subagent") {
		t.Fatalf("subagent phase missing:\n%s", v)
	}
}

// TestTerminalIntegration covers the escape sequences the terminal needs
// to take part: OSC 133 zones around a turn, and a notification only
// while the window is unfocused.
func TestTerminalIntegration(t *testing.T) {
	var buf bytes.Buffer
	ti := &termIntegration{out: &buf, focused: true, notify: true}
	ti.turnStarted()
	ti.turnEnded(true, "done in 2s")
	if got := buf.String(); got != "\x1b]133;A\x07\x1b]133;B\x07\x1b]133;C\x07\x1b]133;D;0\x07" {
		t.Fatalf("zones = %q", got)
	}
	buf.Reset()
	ti.announce("janetcode", "approval needed: bash")
	if buf.Len() != 0 {
		t.Fatalf("a focused window was notified: %q", buf.String())
	}
	ti.focused = false
	ti.announce("janetcode", "approval needed: bash")
	if got := buf.String(); !strings.Contains(got, "\x1b]9;approval needed: bash\x07") || !strings.Contains(got, "\a") {
		t.Fatalf("notification = %q", got)
	}
	buf.Reset()
	ti.brand = "kitty"
	ti.announce("janetcode", "done")
	if got := buf.String(); !strings.HasPrefix(got, "\x1b]99;") {
		t.Fatalf("kitty notification = %q", got)
	}
	buf.Reset()
	ti.notify = false
	ti.announce("janetcode", "done")
	if buf.Len() != 0 {
		t.Fatalf("notifications were turned off: %q", buf.String())
	}
	// A failed turn closes the zone with a non-zero code.
	buf.Reset()
	ti.turnEnded(false, "error")
	if !strings.Contains(buf.String(), "133;D;1") {
		t.Fatalf("failed turn = %q", buf.String())
	}
	env := map[string]string{"JANETCODE_NOTIFY": "off"}
	if notifyEnabled(func(k string) string { return env[k] }) {
		t.Fatal("JANETCODE_NOTIFY=off should silence notifications")
	}
	env["JANETCODE_NOTIFY"] = ""
	env["KITTY_WINDOW_ID"] = "3"
	if !notifyEnabled(func(k string) string { return env[k] }) || termBrand(func(k string) string { return env[k] }) != "kitty" {
		t.Fatal("kitty should be detected and notifications on by default")
	}
}

// TestFocusTracksTerminal: DEC 1004 reports decide whether a turn's end
// is announced at all.
func TestFocusTracksTerminal(t *testing.T) {
	h := newHarness(t)
	if !h.m.term.focused {
		t.Fatal("a terminal that never reports focus counts as focused")
	}
	h.update(tea.BlurMsg{})
	if h.m.term.focused {
		t.Fatal("blur should clear focus")
	}
	h.update(tea.FocusMsg{})
	if !h.m.term.focused {
		t.Fatal("focus should restore it")
	}
	if title := h.m.windowTitle(); !strings.HasPrefix(title, "janetcode · ") {
		t.Fatalf("window title = %q", title)
	}
}

// TestPasteChip: a large paste becomes one chip in the input, deletes as
// one unit, and is sent in full.
func TestPasteChip(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	body := strings.Repeat("a line of a log file\n", 40)
	h.keys("look at ")
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true})
	if got := h.m.input.Value(); got != "look at [paste #1 +41 lines]" {
		t.Fatalf("input = %q", got)
	}
	if !strings.Contains(plain(h.m.View()), "[paste #1 +41 lines]") {
		t.Fatal("the chip is not shown in the input")
	}
	// A small paste is left alone.
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" and this"), Paste: true})
	if got := h.m.input.Value(); got != "look at [paste #1 +41 lines] and this" {
		t.Fatalf("small paste = %q", got)
	}
	for range " and this" {
		h.update(tea.KeyMsg{Type: tea.KeyBackspace})
	}
	// One more backspace takes the whole chip.
	h.update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := h.m.input.Value(); got != "look at " {
		t.Fatalf("backspace over the chip left %q", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(body), Paste: true})
	h.enter()
	h.pump(func() bool { return !h.m.running })
	sent := h.m.opts.Agent.Messages[0].Text()
	if !strings.Contains(sent, body) {
		t.Fatalf("the paste was not re-inflated on submit: %q", sent)
	}
	if strings.Contains(sent, "[paste #") {
		t.Fatalf("the chip was sent verbatim: %q", sent)
	}
}

// TestPasteNeverSubmitsItself: a paste ending in a newline must not send
// the prompt.
func TestPasteNeverSubmitsItself(t *testing.T) {
	h := newHarness(t)
	h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(strings.Repeat("x\n", 30)), Paste: true})
	if h.m.running {
		t.Fatal("a paste submitted the prompt")
	}
	if h.m.input.Value() == "" {
		t.Fatal("the paste did not reach the input")
	}
}

// TestPromptHistoryOutlivesTheProcess: prompts are written to a file per
// working directory, with a flag for "!" commands, and a new model in the
// same directory recalls them.
func TestPromptHistoryOutlivesTheProcess(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	if h.m.histFile == "" {
		t.Fatal("no history file for the session")
	}
	h.keys("first prompt")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	h.keys("!ls -l")
	h.enter()
	h.pump(func() bool { return !h.m.running })

	got := loadPromptHistory(h.m.histFile)
	want := []histEntry{{Text: "first prompt"}, {Text: "ls -l", Bash: true}}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("history file = %+v", got)
	}
	// A fresh model in the same directory starts with them.
	m2 := newModel(Options{Agent: h.m.opts.Agent, Session: h.m.opts.Session, Provider: "fake", Cwd: h.m.opts.Cwd, Version: "test"})
	if len(m2.history) != 2 || m2.histIdx != 2 {
		t.Fatalf("reloaded history = %+v idx=%d", m2.history, m2.histIdx)
	}
	// Up browses; the "!" comes back with the command; esc restores the draft.
	h.keys("draft")
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if got := h.m.input.Value(); got != "!ls -l" {
		t.Fatalf("recall = %q, want the bash prefix back", got)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := h.m.input.Value(); got != "draft" {
		t.Fatalf("esc left %q, want the draft back", got)
	}
}

// TestSteerAndTakeBackQueuedMessages: text typed while a turn runs must
// never be out of reach — ↑ takes the last queued message back, ctrl+s
// hands it to the running turn, and esc gives everything back.
func TestSteerAndTakeBackQueuedMessages(t *testing.T) {
	block := make(chan struct{})
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.opts.Agent.Provider = &heldOpen{until: block, inner: h.m.opts.Agent.Provider}
	h.keys("do the thing")
	h.enter()
	h.keys("also this")
	h.enter()
	if len(h.m.queued) != 1 {
		t.Fatalf("queued = %v", h.m.queued)
	}
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if h.m.input.Value() != "also this" || len(h.m.queued) != 0 {
		t.Fatalf("up did not take the queued message back: %q %v", h.m.input.Value(), h.m.queued)
	}
	// ctrl+s hands it to the running turn instead.
	h.update(tea.KeyMsg{Type: tea.KeyCtrlS})
	if h.m.input.Value() != "" {
		t.Fatalf("ctrl+s left the input alone: %q", h.m.input.Value())
	}
	if !strings.Contains(plain(h.m.View()), "steering") {
		t.Fatalf("no sign of the steering message:\n%s", plain(h.m.View()))
	}
	// Queue two more, then cancel: both come back, joined by blank lines.
	h.keys("one")
	h.enter()
	h.keys("two")
	h.enter()
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := h.m.input.Value(); got != "one\n\ntwo" {
		t.Fatalf("cancel left %q", got)
	}
	close(block)
	h.pump(func() bool { return !h.m.running && len(h.m.queued) == 0 })
}

// heldOpen holds the first call open until a channel closes, so a test can
// act while a turn is running.
type heldOpen struct {
	until chan struct{}
	inner llm.Provider
	done  bool
}

func (b *heldOpen) Name() string { return "heldOpen" }
func (b *heldOpen) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if !b.done {
		b.done = true
		select {
		case <-b.until:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.inner.Stream(ctx, req, emit)
}

// TestCommandAvailabilityAndArgumentCompletion: an idle-only command
// resolved mid-turn is blocked with one message; arguments that can be
// enumerated are offered; and fuzzy matching finds a command from its
// letters.
func TestCommandAvailabilityAndArgumentCompletion(t *testing.T) {
	if c, ok := lookup("/compact"); !ok || c.avail != availIdle {
		t.Fatal("/compact should be idle-only")
	}
	if c, ok := lookup("/help"); !ok || c.avail != availAlways {
		t.Fatal("/help should always be available")
	}
	block := make(chan struct{})
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn})
	h.m.opts.Agent.Provider = &heldOpen{until: block, inner: h.m.opts.Agent.Provider}
	h.keys("go")
	h.enter()
	h.keys("/compact")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "/compact waits for the turn to finish") {
		t.Fatalf("no blocked message:\n%s", v)
	}
	close(block)
	h.pump(func() bool { return !h.m.running })

	// Fuzzy matching: the letters in order, and a "/" in the query splits.
	if !fuzzyCommand("/resume", "/rsm") || !fuzzyCommand("/permissions", "/prms") {
		t.Fatal("fuzzy match failed")
	}
	if fuzzyCommand("/resume", "/xyz") {
		t.Fatal("fuzzy match too loose")
	}
	h.m.input.SetValue("/rsm")
	if c := h.m.completions(); len(c) == 0 || c[0].name != "/resume" {
		t.Fatalf("fuzzy completion = %+v", c)
	}
	// Argument completion, and enter still runs a typed argument.
	h.m.input.SetValue("/undo ")
	c := h.m.completions()
	if len(c) == 0 || c[0].name != "/undo preview" {
		t.Fatalf("/undo completions = %+v", c)
	}
	if !h.m.compArgs {
		t.Fatal("an argument menu should be marked as one")
	}
}

// TestShortWindowScalesTheInterface: caps follow the terminal height, and
// a very short window drops what is optional rather than crowding out
// the transcript.
func TestShortWindowScalesTheInterface(t *testing.T) {
	if outputCap(90) != 30 || outputCap(6) != 4 || outputCap(300) != 40 {
		t.Fatalf("caps = %d %d %d", outputCap(90), outputCap(6), outputCap(300))
	}
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 90, Height: 40})
	if h.m.compactMode() || h.m.tight() {
		t.Fatal("a 40-row window is neither compact nor tight")
	}
	if !strings.Contains(plain(h.m.View()), "janetcode test") {
		t.Fatal("the welcome banner should be shown in a tall window")
	}
	h.update(tea.WindowSizeMsg{Width: 90, Height: 18})
	if !h.m.compactMode() || h.m.tight() {
		t.Fatal("an 18-row window is compact, not tight")
	}
	if h.m.menuRows() != 4 {
		t.Fatalf("compact menu rows = %d", h.m.menuRows())
	}
	h.update(tea.WindowSizeMsg{Width: 90, Height: 14})
	if !h.m.tight() {
		t.Fatal("a 14-row window is tight")
	}
	if strings.Contains(plain(h.m.View()), "janetcode test ·") {
		t.Fatalf("the welcome banner survived a tight window:\n%s", plain(h.m.View()))
	}
	// Nothing is persisted: growing the window puts it back.
	h.update(tea.WindowSizeMsg{Width: 90, Height: 40})
	if h.m.tight() || h.m.compactMode() || !strings.Contains(plain(h.m.View()), "janetcode test") {
		t.Fatal("the compact choice outlived the window that caused it")
	}
}

// TestPromptDoesNotJump: the row above the input is reserved, so a
// notice arriving or leaving never moves the input box.
func TestPromptDoesNotJump(t *testing.T) {
	block := make(chan struct{})
	rs := make([]*llm.Response, 5)
	for i := range rs {
		rs[i] = &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}
	}
	h := newHarness(t, rs...)
	h.m.opts.Agent.Provider = &heldOpen{until: block, inner: h.m.opts.Agent.Provider}
	promptRow := func() int {
		for i, l := range strings.Split(plain(h.m.View()), "\n") {
			if strings.Contains(l, "❯ ") && strings.Contains(l, "│") {
				return i
			}
		}
		return -1
	}
	h.keys("go")
	h.enter()
	before := promptRow()
	if before < 0 {
		t.Fatalf("no input box:\n%s", plain(h.m.View()))
	}
	for i := 0; i < 3; i++ {
		h.keys(fmt.Sprintf("queued %d", i))
		h.enter()
	}
	if got := promptRow(); got != before {
		t.Fatalf("three queued messages moved the prompt from %d to %d", before, got)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "queued 3") {
		t.Fatalf("the queue is not reported:\n%s", v)
	}
	close(block)
	h.pump(func() bool { return !h.m.running })
}

// TestColourFallbacks: NO_COLOR renders every style plain, and a
// sixteen-colour terminal gets ANSI slots instead of truecolor hex
// quantised down to black.
func TestColourFallbacks(t *testing.T) {
	saved := []lipgloss.AdaptiveColor{colAccent, colDim, colFaint, colWarn, colErr, colOK, colUser, colTool, colHeadBg, colHeadFg, colBorder, colBorderA, colDiffAdd, colDiffDel}
	profile := lipgloss.ColorProfile()
	defer func() {
		colAccent, colDim, colFaint, colWarn, colErr, colOK = saved[0], saved[1], saved[2], saved[3], saved[4], saved[5]
		colUser, colTool, colHeadBg, colHeadFg = saved[6], saved[7], saved[8], saved[9]
		colBorder, colBorderA, colDiffAdd, colDiffDel = saved[10], saved[11], saved[12], saved[13]
		buildStyles()
		lipgloss.SetColorProfile(profile)
	}()

	// Truecolor: the palette is hex and the styles carry SGR colours.
	lipgloss.SetColorProfile(termenv.TrueColor)
	buildStyles()
	if !strings.Contains(styleErr.Render("x"), "\x1b[38;2;") {
		t.Fatalf("truecolor error style = %q", styleErr.Render("x"))
	}

	// Sixteen colours: ANSI slots, never a dark hex quantised to black.
	lipgloss.SetColorProfile(termenv.ANSI)
	if got := applyColorProfile(func(string) string { return "" }); got != termenv.ANSI {
		t.Fatalf("profile = %v", got)
	}
	if colErr.Dark != "9" || colAccent.Dark != "12" {
		t.Fatalf("palette not switched: %+v %+v", colErr, colAccent)
	}
	out := styleErr.Render("x")
	if !strings.Contains(out, "\x1b[9") && !strings.Contains(out, "\x1b[3") {
		t.Fatalf("ansi error style = %q", out)
	}
	if strings.Contains(out, "38;2;") {
		t.Fatalf("truecolor survived on a 16-colour terminal: %q", out)
	}

	// NO_COLOR: nothing at all.
	if got := applyColorProfile(func(k string) string {
		if k == "NO_COLOR" {
			return "1"
		}
		return ""
	}); got != termenv.Ascii {
		t.Fatalf("NO_COLOR profile = %v", got)
	}
	for _, s := range []lipgloss.Style{styleErr, styleAccent, styleWarn, styleKey, styleHeader} {
		if got := s.Render("x"); strings.Contains(got, "\x1b[3") || strings.Contains(got, "\x1b[4") {
			t.Fatalf("NO_COLOR left colour behind: %q", got)
		}
	}
}

// TestErrorMessageShape: every provider failure reads the same way —
// Headline (code): why. What to do.
func TestErrorMessageShape(t *testing.T) {
	cases := []struct{ in, head, code, todo string }{
		{"Post \"https://x/v1\": dial tcp 10.0.0.1:443: connect: connection refused", "cannot reach the API", "refused", "/connect"},
		{"http 401: invalid x-api-key", "authentication failed", "http 401", "/connect"},
		{"http 429: rate limit exceeded", "rate limited or out of quota", "http 429", "/model"},
		{"http 404: model not found", "model not available", "http 404", "/model"},
		{"context deadline exceeded", "the request timed out", "timeout", "Resend"},
		{"http 503: upstream unavailable", "the provider failed", "http 503", "Resend"},
	}
	for _, c := range cases {
		got := friendlyError(errors.New(c.in), "openai")
		if !strings.HasPrefix(got, c.head+" ("+c.code+"): ") {
			t.Errorf("%q\n  got %q\n want it to start %q", c.in, got, c.head+" ("+c.code+"): ")
		}
		if !strings.HasSuffix(got, ".") || !strings.Contains(got, c.todo) {
			t.Errorf("%q gave no advice: %q", c.in, got)
		}
		// The provider's own words survive between the two.
		if !strings.Contains(got, strings.SplitN(c.in, ":", 2)[len(strings.SplitN(c.in, ":", 2))-1][:5]) {
			t.Errorf("%q lost the reason: %q", c.in, got)
		}
	}
	// Anything unrecognised is passed through rather than dressed up.
	if got := friendlyError(errors.New("something odd"), "openai"); got != "something odd" {
		t.Errorf("unrecognised error = %q", got)
	}
}

// ctrl+g moves the command running in the foreground into the jobs
// registry, and the transcript says which job it became.
func TestCtrlGPromotesARunningCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell")
	}
	h := newHarness(t)
	run := &tool.Running{}
	jobs := &tool.Jobs{Dir: t.TempDir()}
	defer jobs.KillAll()
	h.m.opts.Running = run

	done := make(chan struct{})
	go func() {
		defer close(done)
		b := tool.Bash{Root: t.TempDir(), Jobs: jobs, Running: run}
		in, _ := json.Marshal(map[string]any{"command": "echo start; sleep 30", "timeout_ms": 120000})
		_, _ = b.Run(context.Background(), in)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for len(run.List()) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the command never registered as promotable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// While a command is promotable the status line offers the key.
	h.m.running = true
	if !strings.Contains(h.m.statusView(), "ctrl+g") {
		t.Errorf("the status line does not offer the key:\n%s", h.m.statusView())
	}
	h.update(tea.KeyMsg{Type: tea.KeyCtrlG})
	var said bool
	for _, e := range h.m.transcript.entries {
		if strings.Contains(e.text, "moved to background as job 1") {
			said = true
		}
	}
	if !said {
		t.Fatalf("the transcript does not name the job: %q", h.m.status)
	}
	if len(jobs.List()) != 1 {
		t.Fatalf("jobs = %+v", jobs.List())
	}
	<-done
	jobs.KillAll()
	// With nothing running the key says so instead of doing nothing
	// quietly, and the status line stops advertising it.
	h.update(tea.KeyMsg{Type: tea.KeyCtrlG})
	if !strings.Contains(h.m.status, "no command is running") {
		t.Errorf("status = %q", h.m.status)
	}
	if strings.Contains(h.m.statusView(), "ctrl+g") {
		t.Errorf("the status line still offers the key:\n%s", h.m.statusView())
	}
}

// A host the user names in the UI is one fetch_url may reach even on a
// private network. main seeds the set from the first prompt only, so
// without this every later message lost the permission.
func TestTypedHostsAreNotedFromEveryUserMessage(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "looked"), StopReason: llm.StopEndTurn})
	typed := &tool.URLSet{}
	h.m.opts.Typed = typed
	h.keys("check http://localhost:8080/health please")
	h.enter()
	h.pump(func() bool { return !h.m.running })
	if !typed.Has("localhost:8080") {
		t.Fatal("the host the user typed was not noted")
	}
	// A command the user runs themselves counts too.
	h.m.opts.Typed.Note("curl http://127.0.0.1:9000/")
	if !typed.Has("127.0.0.1:9000") {
		t.Fatal("the host from a ! command was not noted")
	}
	// And a host nobody named stays out.
	if typed.Has("169.254.169.254") {
		t.Fatal("a host nobody typed is in the set")
	}
}

// A short terminal used to lose the top of a modal: the dialog always drew
// twelve list rows, and lipgloss.Place centred a box taller than the body,
// so the title, the search field and the highlighted row scrolled off the
// screen. The list now shrinks to what the body can hold.
func TestDialogFitsShortTerminal(t *testing.T) {
	h := newHarness(t)
	cfg := &config.Config{Provider: "fake", Model: "m", Providers: map[string]config.Provider{
		"fake": {Type: "openai", BaseURL: "http://fake", APIKey: "k0"},
	}}
	h.withConfig(t, cfg, func(c *config.Config) (llm.Provider, error) { return &listing{}, nil })
	h.update(tea.WindowSizeMsg{Width: 60, Height: 18})
	h.keys("/connect")
	h.enter()
	if h.m.connect == nil {
		t.Fatal("dialog not open")
	}
	v := plain(h.m.View())
	if lines := strings.Count(v, "\n") + 1; lines > 18 {
		t.Fatalf("view is %d lines at height 18:\n%s", lines, v)
	}
	if !strings.Contains(v, "Connect to a provider") || !strings.Contains(v, "search providers") {
		t.Fatalf("title or search field cut off:\n%s", v)
	}
	if !strings.Contains(v, "▸ ") {
		t.Fatalf("highlight not visible:\n%s", v)
	}
	// Tall terminals keep the full twelve rows.
	h.update(tea.WindowSizeMsg{Width: 100, Height: 40})
	if got := strings.Count(plain(h.m.View()), "\n"); got < 20 {
		t.Fatalf("tall view collapsed to %d lines", got)
	}
}

func TestFitRows(t *testing.T) {
	for _, c := range []struct{ height, chrome, want int }{
		{40, 9, dialogRows}, {18, 9, 9}, {12, 9, minDialogRows}, {4, 9, minDialogRows},
	} {
		if got := fitRows(c.height, c.chrome); got != c.want {
			t.Errorf("fitRows(%d,%d) = %d, want %d", c.height, c.chrome, got, c.want)
		}
	}
}

// typed feeds text the way bubbletea's reader does: every run of
// characters between spaces arrives as one KeyRunes message, and a space
// as KeySpace. h.keys sends one rune at a time, which hides the whole
// class of collisions this models.
func (h *harness) typed(s string) {
	flush := func(run []rune) {
		if len(run) > 0 {
			h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: run})
		}
	}
	var run []rune
	for _, r := range s {
		if r == ' ' {
			flush(run)
			run = nil
			h.update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
			continue
		}
		run = append(run, r)
	}
	flush(run)
}

// Bubbletea splits typed text at spaces and hands each run over as one
// KeyRunes message whose String() is the word itself, and bubbles'
// key.Matches compares nothing else. Typing "move it right" therefore
// lost the word "right", "up" pulled a history entry into the middle of
// a sentence, and "home" jumped the cursor to the start of the line.
func TestTypedWordsAreNotKeyPresses(t *testing.T) {
	h := newHarness(t)
	h.m.remember("first prompt")
	for _, word := range []string{"right", "left", "up", "down", "home", "end", "delete", "esc", "enter", "tab", "insert", "pgup"} {
		h.m.input.Reset()
		h.typed("aa " + word + " bb")
		if got := h.m.input.Value(); got != "aa "+word+" bb" {
			t.Errorf("typing %q gave %q", "aa "+word+" bb", got)
		}
	}
	// Real key presses still are key presses.
	h.m.input.Reset()
	h.update(tea.KeyMsg{Type: tea.KeyUp})
	if h.m.input.Value() != "first prompt" {
		t.Fatalf("up no longer browses history: %q", h.m.input.Value())
	}
}

// The same collision in the searchable modals: typing "end" in the
// session or model search used to page the highlight instead.
func TestTypedWordsInPickerSearch(t *testing.T) {
	h := newHarness(t)
	h.m.openPicker(&picker{
		title: "Pick",
		rows: []row{{key: "alpha", cells: []string{"alpha"}, name: "alpha"},
			{key: "friend", cells: []string{"friend"}, name: "friend"}},
		choose: func(r row, typed string) tea.Cmd { return nil },
	})
	h.typed("friend")
	if got := h.m.picker.field.Value(); got != "friend" {
		t.Fatalf("picker search took %q", got)
	}
	h.typed(" end")
	if got := h.m.picker.field.Value(); got != "friend end" {
		t.Fatalf("picker search took %q", got)
	}
}

// The prompt offers r to save a rule, and used to print that rule as an
// absolute path truncated in the middle — "edit_file(/tmp/claude-1000/-h…"
// said nothing about what would be allowed. Paths are now written the way
// the rest of the interface writes them.
func TestSuggestedRuleUsesShortPaths(t *testing.T) {
	h := newHarness(t)
	cwd := h.m.opts.Cwd
	got := h.m.suggestedRule("edit_file", json.RawMessage(`{"path":`+strconv.Quote(filepath.Join(cwd, "main.go"))+`}`))
	if got != "edit_file(./*)" {
		t.Fatalf("rule in the working directory = %q", got)
	}
	got = h.m.suggestedRule("edit_file", json.RawMessage(`{"path":`+strconv.Quote(filepath.Join(cwd, "internal", "tui", "x.go"))+`}`))
	if got != "edit_file(internal/tui/*)" {
		t.Fatalf("rule under the working directory = %q", got)
	}
	// A bash rule is a command, not a path, and is left alone.
	if got := h.m.suggestedRule("bash", json.RawMessage(`{"command":"go test ./..."}`)); got != "bash(go test *)" {
		t.Fatalf("bash rule = %q", got)
	}
}

// The banner used to say "esc cancels" about the permission prompt while
// /help said esc denies and c cancels the turn.
func TestBannerMatchesPermissionKeys(t *testing.T) {
	h := newHarness(t)
	v := plain(h.m.View())
	if !strings.Contains(v, "esc denies") || strings.Contains(v, "n deny · esc cancels") {
		t.Fatalf("banner:\n%s", v)
	}
}

// A goal round is machinery, not something the user typed. The whole
// continuation — objective plus a paragraph telling the model that
// rounds are a budget and not to announce them — used to be echoed into
// the transcript as a user message, and the session title was generated
// from it, so /resume listed sessions called "Work on this goal until it
// is complete or you are blocked".
func TestGoalRoundIsOneLine(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "on it")})
	h.keys("/goal make the tests pass")
	h.enter()
	v := plain(h.m.View())
	if strings.Contains(v, "Rounds are a budget") || strings.Contains(v, "Work on this goal until") {
		t.Fatalf("continuation prompt echoed to the user:\n%s", v)
	}
	if !strings.Contains(v, "goal g1 round 1/") || !strings.Contains(v, "make the tests pass") {
		t.Fatalf("round not announced:\n%s", v)
	}
	if got := h.m.opts.Session.Meta.Title; got != "make the tests pass" {
		t.Fatalf("session title = %q", got)
	}
	// The model still receives the full continuation.
	if !strings.Contains(h.m.lastUserText, "Rounds are a budget") {
		t.Fatalf("the model got %q", h.m.lastUserText)
	}
}

// Notices and verdicts are composed by the agent, which only knows
// absolute paths; one file name could wrap the line twice and bury the
// sentence it belonged to.
func TestNoticePathsAreShortened(t *testing.T) {
	h := newHarness(t)
	cwd := h.m.opts.Cwd
	h.m.transcript.apply(agent.Event{Kind: agent.EvVerdict,
		Text: "⚠ self-checked: 1 file(s) edited and tests passed, but " + filepath.Join(cwd, "main_test.go") + " changed in the same turn"})
	line := h.m.transcript.entries[len(h.m.transcript.entries)-1].text
	if strings.Contains(line, cwd) {
		t.Fatalf("absolute path in the verdict: %s", line)
	}
	if !strings.Contains(line, "main_test.go changed in the same turn") {
		t.Fatalf("verdict: %s", line)
	}
}

// /stats reported the session as open for 2562047h47m16.854775807s
// before the first turn: it measured from startedAt, which is the clock
// for the turn that is running and is the zero time until one does.
func TestStatsReportsWindowAge(t *testing.T) {
	h := newHarness(t)
	h.m.openedAt = time.Now().Add(-90 * time.Second)
	h.keys("/stats")
	h.enter()
	var line string
	for _, e := range h.m.transcript.entries {
		if strings.Contains(e.text, "session ") && strings.Contains(e.text, "open ") {
			line = e.text
		}
	}
	if line == "" {
		t.Fatalf("no stats line:\n%s", plain(h.m.View()))
	}
	if !strings.Contains(line, "open 1m") {
		t.Fatalf("stats line = %q", line)
	}
	if strings.Contains(line, "2562047h") {
		t.Fatalf("stats measured from the turn clock: %q", line)
	}
}

// A group heading printed hard against the last command of the group
// before it read as that command's second line.
func TestHelpSeparatesGroups(t *testing.T) {
	h := newHarness(t)
	help := h.m.helpText()
	for _, g := range helpGroups {
		if !strings.Contains(help, "\n\n"+g.title+"\n") {
			t.Errorf("group %q has no blank line before it", g.title)
		}
	}
}

// The prompt box grew a line at a time, but the textarea was resized
// after it had already scrolled: it decided where to scroll with the old
// height, and nothing ever scrolled it back. A two-line prompt showed
// only its second line, and the first line of a paste was invisible.
func TestInputBoxShowsItsFirstLine(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 110, Height: 32})
	paste := func(text string) {
		h.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text), Paste: true})
	}
	newline := func() { h.update(tea.KeyMsg{Type: tea.KeyCtrlJ}) }
	h.keys("alpha")
	newline()
	h.keys("beta")
	newline()
	h.keys("gamma")
	v := plain(h.m.View())
	for _, want := range []string{"❯ alpha", "beta", "gamma"} {
		if !strings.Contains(v, want) {
			t.Fatalf("three-line prompt lost %q:\n%s", want, v)
		}
	}
	h.m.input.Reset()
	paste("alpha\nbeta\ngamma")
	v = plain(h.m.View())
	if !strings.Contains(v, "❯ alpha") {
		t.Fatalf("first line of a paste not shown:\n%s", v)
	}
	// The invariant behind it: the textarea is never resized after it has
	// already decided where to scroll, because its height never changes.
	if got, want := h.m.input.Height(), h.m.inputLimit(); got != want {
		t.Fatalf("textarea height %d, want the box limit %d", got, want)
	}
	// A paste longer than the box keeps the tail, where the cursor is.
	h.m.input.Reset()
	var lines []string
	for i := 1; i <= 8; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	paste(strings.Join(lines, "\n"))
	v = plain(h.m.View())
	if !strings.Contains(v, "line 8") {
		t.Fatalf("tail of a long prompt not shown:\n%s", v)
	}
	if strings.Contains(v, "line 1\n") {
		t.Fatalf("box did not scroll for a long prompt:\n%s", v)
	}
	// The box never grows past its limit.
	if got := h.m.inputHeight(); got != maxInputLines {
		t.Fatalf("input height = %d", got)
	}
}

// tool.Bash reports a non-zero exit inside the output rather than as an
// error, so !nosuchcommand was shown with the same green tick as a
// command that worked.
func TestFailingBangCommandIsMarkedFailed(t *testing.T) {
	for _, c := range []struct {
		out  string
		code int
		ok   bool
	}{
		{"bash: nope: command not found\n[exit code 127]", 127, true},
		{"hello", 0, false},
		{"[exit code 1] no lines matched; for this command that is an answer, not a failure", 0, false},
	} {
		code, ok := exitCode(c.out)
		if code != c.code || ok != c.ok {
			t.Errorf("exitCode(%q) = %d,%v want %d,%v", c.out, code, ok, c.code, c.ok)
		}
	}

	h := newHarness(t)
	call := agent.ToolCall{ID: "shell-1", Name: "shell", Input: json.RawMessage(`{"command":"nosuchcommand"}`)}
	h.m.finishShell(shellDoneMsg{call: call, output: "bash: nosuchcommand: command not found\n[exit code 127]", dur: time.Millisecond})
	if !strings.Contains(h.m.status, "exit code 127") {
		t.Fatalf("status = %q", h.m.status)
	}
	last := h.m.transcript.entries[len(h.m.transcript.entries)-1]
	if !last.isError {
		t.Fatalf("failed command not marked as an error")
	}
}

// ctrl+o showed sixty lines of a tool result and then said "… (440 more
// lines, ctrl+o to expand)", naming the key that folds the entry again.
func TestExpandShowsTheRestAndSaysSoHonestly(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 100, Height: 40})
	var body strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&body, "line %d\n", i)
	}
	call := agent.ToolCall{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"seq 1 500"}`)}
	h.m.transcript.apply(agent.Event{Kind: agent.EvToolRun, Call: &call})
	h.m.transcript.apply(agent.Event{Kind: agent.EvToolResult, Call: &call, Output: body.String()})

	folded := plain(h.m.transcript.render(renderOpts{width: 100, cwd: h.m.opts.Cwd, cap: 8}))
	if !strings.Contains(folded, "ctrl+o to expand") {
		t.Fatalf("folded result offers no way to see more:\n%s", folded)
	}
	full := h.m.transcript.render(renderOpts{width: 100, cwd: h.m.opts.Cwd, cap: 8, verbose: true})
	if !strings.Contains(plain(full), "line 400") {
		t.Fatalf("expanding stopped short of 400 lines")
	}
	if strings.Contains(plain(full), "ctrl+o to expand") {
		t.Fatalf("expanded result still offers ctrl+o:\n%s", tailOf(plain(full)))
	}
	if !strings.Contains(plain(full), "the model received all of it") {
		t.Fatalf("expanded result does not say what happened to the rest:\n%s", tailOf(plain(full)))
	}
}

func tailOf(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}

// A provider announces a backoff once — "retrying 1/2 · rate limited ·
// in 6s" — and the line then stood unchanged for six seconds beside a
// counter running the other way, which reads as a frozen screen.
func TestRetryPhaseCountsDown(t *testing.T) {
	h := newHarness(t)
	h.m.setPhase("retrying 1/2 · rate limited · in 6s")
	if h.m.phaseUntil.IsZero() {
		t.Fatal("no deadline taken from the phase")
	}
	first := h.m.phaseText()
	if !strings.HasPrefix(first, "retrying 1/2 · rate limited · in ") {
		t.Fatalf("phase = %q", first)
	}
	h.m.phaseUntil = time.Now().Add(2 * time.Second)
	if got := h.m.phaseText(); got != "retrying 1/2 · rate limited · in 2s" {
		t.Fatalf("phase after two seconds left = %q", got)
	}
	// Past the deadline it says now, not a negative number.
	h.m.phaseUntil = time.Now().Add(-time.Second)
	if got := h.m.phaseText(); !strings.HasSuffix(got, "in 0s") {
		t.Fatalf("phase past the deadline = %q", got)
	}
	// A phase with no wait in it is left exactly as it came.
	h.m.setPhase("running bash")
	if !h.m.phaseUntil.IsZero() || h.m.phaseText() != "running bash" {
		t.Fatalf("phase = %q until = %v", h.m.phaseText(), h.m.phaseUntil)
	}
	// And a stale countdown never survives the next phase.
	h.m.setPhase("retrying 1/2 · in 30s")
	h.m.setPhase("thinking")
	if got := h.m.phaseText(); got != "thinking" {
		t.Fatalf("phase = %q", got)
	}
}

// The prompt's placeholder was one long sentence cut wherever the window
// ended, so a narrow terminal offered "· / for" where it meant "· / for
// commands".
func TestPlaceholderFitsTheWindow(t *testing.T) {
	h := newHarness(t)
	for _, w := range []int{120, 100, 80, 60, 48, 30} {
		h.update(tea.WindowSizeMsg{Width: w, Height: 24})
		p := h.m.placeholder()
		if got := lipgloss.Width(p) + 2; got > w-4 {
			t.Errorf("width %d: placeholder %q takes %d columns", w, p, got)
		}
		if strings.HasSuffix(p, "…") {
			t.Errorf("width %d: placeholder cut mid-phrase: %q", w, p)
		}
	}
	h.update(tea.WindowSizeMsg{Width: 120, Height: 24})
	if h.m.placeholder() != defaultPlaceholder {
		t.Fatalf("a wide window lost the full placeholder: %q", h.m.placeholder())
	}
}

func TestFitPhrase(t *testing.T) {
	if got := fitPhrase(20, "a long phrase that will not fit at all", "short"); got != "short" {
		t.Fatalf("got %q", got)
	}
	if got := fitPhrase(40, "fits fine"); got != "fits fine" {
		t.Fatalf("got %q", got)
	}
	// Nothing fits: the shortest is cut, and says so.
	if got := fitPhrase(6, "aaaaaaaaaaaa", "bbbbbbbbbbbb"); !strings.HasSuffix(got, "…") {
		t.Fatalf("got %q", got)
	}
}

// renderCode cut a long line with truncateLine, which marks the cut
// itself, and then added a second ellipsis: every clipped line of /diff,
// /open and /changes ended in "……".
func TestCodeLinesEndInOneEllipsis(t *testing.T) {
	long := "/diff <path> shows the hunks · /diff revert <path>:<n> reverts one · /undo reverts a whole turn"
	out := plain(renderCode(long, 40))
	if strings.Contains(out, "……") {
		t.Fatalf("two ellipses: %q", out)
	}
	if !strings.HasSuffix(strings.TrimRight(out, " "), "…") {
		t.Fatalf("no cut mark: %q", out)
	}
}
