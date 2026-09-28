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
	"github.com/xmonader/janetcode/internal/flavor"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/weburl"
)

func TestSubmitAndStreamResponse(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hello back"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 7, OutputTokens: 2}})
	h.keys("hi there")
	h.enter()
	if !h.m.turn.running {
		t.Fatal("should be running after enter")
	}
	if h.m.input.Value() != "" {
		t.Fatal("input not cleared")
	}
	h.pump(func() bool { return !h.m.turn.running })
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

func TestQueuedPromptSendsAfterTurn(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first answer"), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second answer"), StopReason: llm.StopEndTurn},
	)
	h.keys("one")
	h.enter()
	h.keys("two")
	h.enter()
	if len(h.m.turn.queued) != 1 || h.m.input.Value() != "" {
		t.Fatalf("queued = %v input = %q", h.m.turn.queued, h.m.input.Value())
	}
	if v := plain(h.m.View()); !strings.Contains(v, "queued 1 · two") {
		t.Fatalf("queue not shown:\n%s", v)
	}
	h.pump(func() bool {
		return !h.m.turn.running && len(h.m.turn.queued) == 0 && len(h.m.opts.Agent.Messages) == 4
	})
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
	h.pump(func() bool { return !h.m.turn.running })
	if got := h.m.input.Value(); got != "later" {
		t.Fatalf("queued prompt not restored: %q", got)
	}
	if len(h.m.turn.queued) != 0 {
		t.Fatalf("queue = %v", h.m.turn.queued)
	}
}

func TestResumePicker(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "old answer"), StopReason: llm.StopEndTurn})
	h.keys("old question")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
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
	// The new transcript writes paths relative to the working directory,
	// as the one it replaced did.
	if h.m.transcript.cwd != h.m.opts.Cwd {
		t.Fatalf("after resume the transcript's cwd is %q, want %q", h.m.transcript.cwd, h.m.opts.Cwd)
	}
	h.keys("/resume 9")
	h.enter()
	if v := plain(h.m.View()); !strings.Contains(v, "no session numbered 9") {
		t.Fatalf("bad index:\n%s", v)
	}
}

func TestImageMentionSendsImageBlock(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "a cat"), StopReason: llm.StopEndTurn})
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "shot.png"), []byte("\x89PNGfake"), 0o644)
	h.keys("what is in @shot.png ?")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	blocks := h.m.opts.Agent.Messages[0].Blocks
	if len(blocks) != 2 || blocks[0].Type != llm.BlockText || blocks[1].Type != llm.BlockImage || blocks[1].MediaType != "image/png" {
		t.Fatalf("blocks = %+v", blocks)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "attached shot.png (image, 8B)") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestInitialPromptIsSubmitted(t *testing.T) {
	opts := fakeOptions(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "auto"), StopReason: llm.StopEndTurn})
	opts.InitialPrompt = "explain"
	h := newHarnessFor(t, opts)
	if !h.m.turn.running {
		t.Fatal("initial prompt not submitted")
	}
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.View()); !strings.Contains(v, "explain") || !strings.Contains(v, "auto") {
		t.Fatalf("view:\n%s", v)
	}
}

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

func TestAgentsOverlayOpenEscAndNoDump(t *testing.T) {
	h := newHarness(t)
	h.m.opts.Agent.Provider = &scripted{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "found the parser"), StopReason: llm.StopEndTurn},
	}}
	task := attachTask(h)
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"explore the parser","prompt":"look at the parser"}`)); err != nil {
		t.Fatal(err)
	}
	kids := task.Children()
	if len(kids) != 1 || kids[0].ID == "" || kids[0].Description != "explore the parser" {
		t.Fatalf("children = %+v", kids)
	}
	id := kids[0].ID
	runAgents(h)
	if h.m.ov.kind != overlayAgents {
		t.Fatalf("overlay = %q, want %s", h.m.ov.kind, overlayAgents)
	}
	if h.m.ov.child != id {
		t.Fatalf("one child should open its transcript, overlayChild = %q", h.m.ov.child)
	}
	v := plain(h.m.View())
	for _, want := range []string{id, "explore the parser", "look at the parser", "found the parser", "esc back"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in child transcript:\n%s", want, v)
		}
	}
	for _, e := range h.m.transcript.entries {
		if e.kind == entryInfo && strings.Contains(e.text, id) {
			t.Fatalf("listing dumped into the transcript: %q", e.text)
		}
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.kind == "" || h.m.ov.child != "" {
		t.Fatal("esc from the transcript should return to the list")
	}
	v = plain(h.m.View())
	if !strings.Contains(v, "Subagents") || !strings.Contains(v, id) {
		t.Fatalf("list after esc:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.kind != "" {
		t.Fatal("esc on the list should close the overlay")
	}
	h.keys("still here")
	if h.m.input.Value() != "still here" {
		t.Fatalf("prompt not usable after esc: %q", h.m.input.Value())
	}
	if v := plain(h.m.View()); strings.Contains(v, "esc back") {
		t.Fatalf("overlay still drawn after esc:\n%s", v)
	}
	for _, e := range h.m.transcript.entries {
		if e.kind == entryInfo && strings.Contains(e.text, id) {
			t.Fatalf("listing lingered in the transcript: %q", e.text)
		}
	}
}

func TestAgentsOverlayLiveUpdate(t *testing.T) {
	h := newHarness(t)
	gate := &gateProvider{started: make(chan struct{}), release: make(chan struct{})}
	h.m.opts.Agent.Provider = gate
	task := attachTask(h)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() {
		_, err := task.Run(ctx, json.RawMessage(`{"description":"survey tests","prompt":"find the tests"}`))
		errc <- err
	}()
	select {
	case <-gate.started:
	case <-time.After(2 * time.Second):
		t.Fatal("child did not start")
	}
	kids := task.Children()
	if len(kids) != 1 || kids[0].Done {
		t.Fatalf("want one running child, got %+v", kids)
	}
	runAgents(h)
	if h.m.ov.kind != overlayAgents || h.m.ov.child != kids[0].ID {
		t.Fatalf("should open the running child's transcript, overlay=%q child=%q", h.m.ov.kind, h.m.ov.child)
	}
	v := plain(h.m.View())
	for _, want := range []string{kids[0].ID, "survey tests", "running"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q while running:\n%s", want, v)
		}
	}
	if last := kids[0].Last; last != "" && !strings.Contains(v, last) {
		t.Fatalf("missing last action %q:\n%s", last, v)
	}
	close(gate.release)
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("child did not finish")
	}
	h.update(tickMsg(time.Now()))
	v = plain(h.m.View())
	if !strings.Contains(v, "done") {
		t.Fatalf("overlay did not pick up done:\n%s", v)
	}
	if !strings.Contains(v, "surveyed") {
		t.Fatalf("transcript did not pick up the child's answer:\n%s", v)
	}
	if strings.Contains(v, "running") {
		t.Fatalf("still shows running after done:\n%s", v)
	}
	if h.m.ov.kind != overlayAgents || h.m.ov.child != kids[0].ID {
		t.Fatal("detail view should stay open across the live update")
	}
}

func TestAgentsOverlayDetailShowsToolCall(t *testing.T) {
	h := newHarness(t)
	os.WriteFile(filepath.Join(h.m.opts.Cwd, "a.txt"), []byte("alpha from disk"), 0o644)
	h.m.opts.Agent.Provider = &scripted{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "c1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "REPORT: a.txt says alpha from disk"), StopReason: llm.StopEndTurn},
	}}
	task := attachTask(h)
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"read a.txt","prompt":"what does a.txt say"}`)); err != nil {
		t.Fatal(err)
	}
	runAgents(h)
	v := plain(h.m.View())
	for _, want := range []string{"what does a.txt say", "read_file", "a.txt", "alpha from disk", "REPORT:"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in child transcript:\n%s", want, v)
		}
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.child != "" {
		t.Fatal("esc should leave the transcript")
	}
	h.update(tea.KeyMsg{Type: tea.KeyEnter})
	if h.m.ov.child == "" {
		t.Fatal("enter on the list should reopen the transcript")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "read_file") {
		t.Fatalf("reopened transcript:\n%s", v)
	}
}

func TestStickyPlanShowsProgress(t *testing.T) {
	h := newHarness(t)
	if v := plain(h.m.View()); strings.Contains(v, "plan  ") && strings.Contains(v, "done") {
		t.Fatalf("empty plan should not draw a strip:\n%s", v)
	}
	h.m.opts.Agent.WriteTodos([]agent.Todo{
		{Text: "explore the parser", Status: agent.TodoDone},
		{Text: "write the parser", Status: agent.TodoDoing},
		{Text: "add tests", Status: agent.TodoPending},
	})
	h.m.refresh(false)
	v := plain(h.m.View())
	for _, want := range []string{"plan", "1 of 3 done", "[>]", "write the parser", "[x]", "explore the parser", "[ ]", "add tests"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in sticky plan:\n%s", want, v)
		}
	}
	h.m.opts.Agent.WriteTodos([]agent.Todo{
		{Text: "explore the parser", Status: agent.TodoDone},
		{Text: "write the parser", Status: agent.TodoDone},
		{Text: "add tests", Status: agent.TodoDoing},
	})
	h.m.refresh(false)
	v = plain(h.m.View())
	if !strings.Contains(v, "2 of 3 done") || !strings.Contains(v, "add tests") {
		t.Fatalf("plan did not update:\n%s", v)
	}
	if strings.Contains(v, "write the parser") && !strings.Contains(v, "[x]") {
		t.Fatalf("completed step lost its mark:\n%s", v)
	}
}

func TestTodosOverlayOpenLiveAndEsc(t *testing.T) {
	h := newHarness(t)
	h.keys("/todos")
	h.enter()
	if h.m.ov.kind != overlayTodos {
		t.Fatal("empty /todos should open the overlay")
	}
	if v := plain(h.m.View()); !strings.Contains(v, "no plan has been written") {
		t.Fatalf("empty overlay:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.kind != "" {
		t.Fatal("esc should close the empty overlay")
	}
	h.m.opts.Agent.WriteTodos([]agent.Todo{
		{Text: "explore the parser", Status: agent.TodoDone},
		{Text: "write the parser", Status: agent.TodoDoing},
		{Text: "add tests", Status: agent.TodoPending},
	})
	h.keys("/todos")
	h.enter()
	v := plain(h.m.View())
	for _, want := range []string{"explore the parser", "completed", "write the parser", "in_progress", "add tests", "pending"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	h.m.opts.Agent.WriteTodos([]agent.Todo{
		{Text: "explore the parser", Status: agent.TodoDone},
		{Text: "write the parser", Status: agent.TodoDone},
		{Text: "add tests", Status: agent.TodoDoing},
	})
	h.update(tickMsg(time.Now()))
	v = plain(h.m.View())
	if !strings.Contains(v, "add tests") || !strings.Contains(v, "in_progress") {
		t.Fatalf("live update:\n%s", v)
	}
	if strings.Count(v, "completed") < 2 {
		t.Fatalf("expected two completed steps:\n%s", v)
	}
	h.update(tea.KeyMsg{Type: tea.KeyEsc})
	if h.m.ov.kind != "" {
		t.Fatal("esc should restore the prompt")
	}
	h.keys("still here")
	if h.m.input.Value() != "still here" {
		t.Fatalf("prompt not usable: %q", h.m.input.Value())
	}
}

func TestAskTabsTwoQuestionsAndOther(t *testing.T) {
	h := newHarness(t)
	qs := []agent.Question{
		{Prompt: "Goal of the refactor?", Options: []agent.AskOption{{Label: "Performance"}, {Label: "Readability"}}},
		{Prompt: "How far?", Options: []agent.AskOption{{Label: "This function"}, {Label: "The package"}}},
	}
	errc := make(chan error, 1)
	var got []agent.Answer
	go func() {
		var err error
		got, err = h.m.opts.Agent.Asker.Ask(context.Background(), qs)
		errc <- err
	}()
	h.pump(func() bool { return h.m.asking != nil })
	v := plain(h.m.View())
	for _, want := range []string{"Goal of the refactor?", "How far?", "Performance", "Readability"} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q in tabs:\n%s", want, v)
		}
	}
	h.enter() // Performance
	if h.m.asking == nil || h.m.asking.tab != 1 {
		t.Fatal("should switch to the unanswered tab")
	}
	h.enter() // This function
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ask did not finish")
	}
	if len(got) != 2 || got[0].Label != "Performance" || got[1].Label != "This function" {
		t.Fatalf("answers = %+v", got)
	}
	if h.m.asking != nil {
		t.Fatal("ask overlay should close")
	}

	qs = []agent.Question{
		{Prompt: "Name?", Options: []agent.AskOption{{Label: "Alpha"}, {Label: "Beta"}}},
	}
	got = nil
	errc = make(chan error, 1)
	go func() {
		var err error
		got, err = h.m.opts.Agent.Asker.Ask(context.Background(), qs)
		errc <- err
	}()
	h.pump(func() bool { return h.m.asking != nil })
	h.update(tea.KeyMsg{Type: tea.KeyDown})
	h.update(tea.KeyMsg{Type: tea.KeyDown}) // Other
	h.enter()
	if h.m.asking == nil || !h.m.asking.othering {
		t.Fatal("Other should collect typed text")
	}
	h.keys("gamma")
	h.enter()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Other did not finish")
	}
	if len(got) != 1 || got[0].Label != agent.OtherLabel || got[0].Reason != "gamma" {
		t.Fatalf("Other = %+v", got)
	}
}

// A question belongs to the turn that asked it. When that turn ends
// without an answer — cancelled, or failed — the overlay used to stay up
// over the prompt, taking the whole keyboard and answering into a
// channel nobody was reading any more.
func TestATurnEndingClosesWhateverItWasAsking(t *testing.T) {
	h := newHarness(t)
	h.m.startTurn("thinking")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = h.m.opts.Agent.Asker.Ask(ctx, []agent.Question{
			{Prompt: "Which way?", Options: []agent.AskOption{{Label: "Left"}, {Label: "Right"}}},
		})
	}()
	h.pump(func() bool { return h.m.asking != nil })

	cancel()
	h.update(agentDoneMsg{err: context.Canceled})
	if h.m.asking != nil {
		t.Fatal("the question outlived the turn that asked it")
	}
	if h.m.denying || h.m.pending != nil {
		t.Fatal("a prompt outlived the turn")
	}
	// The prompt is usable again: the keyboard is not held by the overlay.
	h.keys("next")
	if h.m.input.Value() != "next" {
		t.Fatalf("prompt not usable: %q", h.m.input.Value())
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
	h.pump(func() bool { return !h.m.turn.running })
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
	h.pump(func() bool { return !h.m.turn.running })
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

func TestForkStayLeavesLiveSession(t *testing.T) {
	h := newHarness(t,
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "I added a token bucket."), StopReason: llm.StopEndTurn},
		&llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "still on original"), StopReason: llm.StopEndTurn},
	)
	h.keys("add rate limiting")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	orig := h.m.opts.Session
	h.keys("/fork stay other path")
	h.enter()
	if h.m.opts.Session != orig {
		t.Fatal("fork stay swapped the live session")
	}
	if h.m.opts.Session.Meta.ForkOf != "" {
		t.Fatalf("live session gained fork_of: %+v", h.m.opts.Session.Meta)
	}
	infos, err := session.List(dirOf(orig), orig.Meta.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	var copyID string
	for _, in := range infos {
		if in.Meta.ForkOf == orig.Meta.ID {
			copyID = in.Meta.ID
			if in.Meta.Title != "other path" {
				t.Fatalf("copy title = %q", in.Meta.Title)
			}
		}
	}
	if copyID == "" {
		t.Fatalf("copy not listed: %+v", infos)
	}
	h.keys("keep going")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	_, origMsgs, _ := session.Open(dirOf(orig), orig.Meta.ID)
	_, copyMsgs, _ := session.Open(dirOf(orig), copyID)
	if len(origMsgs) <= len(copyMsgs) {
		t.Fatalf("orig=%d copy=%d; later messages should land only on the live session", len(origMsgs), len(copyMsgs))
	}
}

func TestAcksStayOffTranscript(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "hello there"), StopReason: llm.StopEndTurn})
	h.m.copyText = func(string) error { return nil }
	h.keys("hi")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	before := countInfo(h)
	h.keys("/verbose")
	h.enter()
	h.keys("/title hello")
	h.enter()
	h.keys("/copy")
	h.enter()
	if got := countInfo(h); got != before {
		t.Fatalf("acks leaked into the transcript: before %d after %d", before, got)
	}
	if h.m.opts.Session.Meta.Title != "hello" {
		t.Fatalf("title = %q", h.m.opts.Session.Meta.Title)
	}
	if !h.m.verbose {
		t.Fatal("verbose not toggled")
	}
	if !strings.Contains(h.m.status, "copied") {
		t.Fatalf("last ack not on the status line: %q", h.m.status)
	}
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
	h.pump(func() bool { return !h.m.turn.running })
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

// TestPhaseNamesWhatTheTurnWaitsOn: a retry countdown and a subagent both
// reach the status line, so a stalled turn says what it is stalled on.
func TestPhaseNamesWhatTheTurnWaitsOn(t *testing.T) {
	h := newHarness(t)
	h.m.turn.running = true
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

// TestFinishedRetryLeavesALine: the countdown is a status line the next
// phase overwrites, so once the call lands a turn that cost three
// attempts read exactly like a clean one. A completed retry leaves one
// dim line under the turn instead.
func TestFinishedRetryLeavesALine(t *testing.T) {
	h := newHarness(t)
	h.m.turn.running = true
	before := len(h.m.transcript.entries)
	h.m.applyEvent(agent.Event{Kind: agent.EvRetry, Text: "retried 2× · rate limited · waited 1.5s",
		Retry: &llm.Retry{Attempts: 2, Reason: "rate limited", Wait: 1500 * time.Millisecond}})
	h.m.refresh(false)
	if n := len(h.m.transcript.entries); n != before+1 {
		t.Fatalf("entries %d, want %d", n, before+1)
	}
	if v := plain(h.m.View()); !strings.Contains(v, "retried 2×") || !strings.Contains(v, "rate limited") {
		t.Fatalf("retry line missing from the transcript:\n%s", v)
	}
}

// A host the user names in the UI is one fetch_url may reach even on a
// private network. main seeds the set from the first prompt only, so
// without this every later message lost the permission.
func TestTypedHostsAreNotedFromEveryUserMessage(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "looked"), StopReason: llm.StopEndTurn})
	typed := &weburl.Set{}
	h.m.opts.Typed = typed
	h.keys("check http://localhost:8080/health please")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
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

// A provider announces a backoff once — "retrying 1/2 · rate limited ·
// in 6s" — and the line then stood unchanged for six seconds beside a
// counter running the other way, which reads as a frozen screen.
func TestRetryPhaseCountsDown(t *testing.T) {
	h := newHarness(t)
	h.m.turn.setPhase("retrying 1/2 · rate limited · in 6s")
	if h.m.turn.phaseUntil.IsZero() {
		t.Fatal("no deadline taken from the phase")
	}
	first := h.m.turn.phaseText()
	if !strings.HasPrefix(first, "retrying 1/2 · rate limited · in ") {
		t.Fatalf("phase = %q", first)
	}
	h.m.turn.phaseUntil = time.Now().Add(2 * time.Second)
	if got := h.m.turn.phaseText(); got != "retrying 1/2 · rate limited · in 2s" {
		t.Fatalf("phase after two seconds left = %q", got)
	}
	// Past the deadline it says now, not a negative number.
	h.m.turn.phaseUntil = time.Now().Add(-time.Second)
	if got := h.m.turn.phaseText(); !strings.HasSuffix(got, "in 0s") {
		t.Fatalf("phase past the deadline = %q", got)
	}
	// A phase with no wait in it is left exactly as it came.
	h.m.turn.setPhase("running bash")
	if !h.m.turn.phaseUntil.IsZero() || h.m.turn.phaseText() != "running bash" {
		t.Fatalf("phase = %q until = %v", h.m.turn.phaseText(), h.m.turn.phaseUntil)
	}
	// And a stale countdown never survives the next phase.
	h.m.turn.setPhase("retrying 1/2 · in 30s")
	h.m.turn.setPhase("thinking")
	if !h.m.turn.phaseUntil.IsZero() {
		t.Fatal("countdown survived a thinking phase")
	}
	if got := h.m.turn.phaseText(); strings.Contains(got, "retrying") {
		t.Fatalf("phase = %q", got)
	}
}

func TestThinkingPhaseShowsFlavor(t *testing.T) {
	h := newHarness(t)
	h.m.turn.startedAt = time.Unix(1_700_000_000, 0)
	h.m.turn.setPhase("thinking")
	got := h.m.turn.phaseText()
	if got == "thinking" || got == "" {
		t.Fatalf("thinking was not dressed: %q", got)
	}
	if !flavor.Known(got) {
		t.Fatalf("phase %q is not a seeded flavor verb", got)
	}
	h.m.turn.setPhase("running bash")
	if got := h.m.turn.phaseText(); got != "running bash" {
		t.Fatalf("tool phase was dressed: %q", got)
	}
}

func TestTurnStartShowsFlavorInTranscript(t *testing.T) {
	h := newHarness(t)
	h.m.startTurn("thinking")
	if n := len(h.m.transcript.entries); n == 0 {
		t.Fatal("turn start should plant a thinking row")
	}
	e := h.m.transcript.entries[len(h.m.transcript.entries)-1]
	if e.kind != entryThinking || e.done {
		t.Fatalf("placeholder = kind %v done %v", e.kind, e.done)
	}
	got := thinkingView(e.text, false, 0, e.started)
	if !flavor.Known(strings.TrimPrefix(got, "· ")) {
		t.Fatalf("transcript thinking = %q", got)
	}
	h.m.transcript.closeOpen()
	for _, x := range h.m.transcript.entries {
		if x.kind == entryThinking {
			t.Fatal("empty thinking placeholder must not remain after closeOpen")
		}
	}
}

// TestSideWorkLeavesNoStaleStatus: /undo apply said "applying the
// previewed undo…" as the status, and a result that set no status of its
// own (an error, a sheet) left that line up after the work had finished.
func TestSideWorkLeavesNoStaleStatus(t *testing.T) {
	h := newHarness(t)
	h.update(tea.WindowSizeMsg{Width: 120, Height: 24})
	h.keys("/undo apply")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	if v := plain(h.m.statusView()); strings.Contains(v, "applying") || strings.Contains(v, "matching") {
		t.Fatalf("status after /undo apply finished:\n%s", v)
	}
}

// TestResumePickerShowsForkLineage: the fork /fork stay made is listed
// indented under the conversation it was copied from, and searching for
// the fork keeps the parent on screen for context.
func TestResumePickerShowsForkLineage(t *testing.T) {
	h := newHarness(t, &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "the original answer"), StopReason: llm.StopEndTurn})
	h.keys("about the parser")
	h.enter()
	h.pump(func() bool { return !h.m.turn.running })
	h.keys("/fork stay the lexer branch")
	h.enter()
	h.keys("/clear")
	h.enter()
	h.keys("/resume")
	h.enter()
	if h.m.picker == nil {
		t.Fatal("/resume should open the picker")
	}
	v := plain(h.m.View())
	var forkLine, parentLine string
	for _, l := range strings.Split(v, "\n") {
		if strings.Contains(l, "the lexer branch") {
			forkLine = l
		}
		if strings.Contains(l, "about the parser") {
			parentLine = l
		}
	}
	if parentLine == "" || forkLine == "" {
		t.Fatalf("listing lacks the pair:\n%s", v)
	}
	if !strings.Contains(forkLine, "└ ") {
		t.Fatalf("the fork is not drawn under its parent:\n%s", v)
	}
	if strings.Contains(parentLine, "└ ") {
		t.Fatalf("the parent is indented:\n%s", v)
	}
	// Searching for the fork keeps its parent, marked as context.
	h.keys("lexer")
	v = plain(h.m.View())
	if !strings.Contains(v, "the lexer branch") || !strings.Contains(v, "about the parser") {
		t.Fatalf("filtering dropped the match's parent:\n%s", v)
	}
	if !strings.Contains(v, "parent of a match") {
		t.Fatalf("the parent is not marked as context:\n%s", v)
	}
}
