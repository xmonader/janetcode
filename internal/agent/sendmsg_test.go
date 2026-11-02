package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestParseDelivery(t *testing.T) {
	for in, want := range map[string]Delivery{"": DeliverSteer, "Steer": DeliverSteer, " queue ": DeliverQueue, "interject": DeliverInterject} {
		if got, err := ParseDelivery(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := ParseDelivery("shout"); err == nil {
		t.Error("bad delivery accepted")
	}
}

// parked blocks every model call until released and says when the first
// one began, so a test sends its messages once the fork has already taken
// the steering it started with.
type parked struct {
	ch    chan struct{}
	enter chan struct{}
	once  sync.Once
}

func (p *parked) Name() string { return "fake" }
func (p *parked) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	p.once.Do(func() { close(p.enter) })
	select {
	case <-p.ch:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}, nil
}

// startFork starts a background fork blocked on the provider.
func startFork(t *testing.T, desc string) (*Task, string, chan struct{}) {
	t.Helper()
	root := t.TempDir()
	block := make(chan struct{})
	prov := &parked{ch: block, enter: make(chan struct{})}
	parent := &Agent{Provider: prov, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task, &SendSubagentMessage{Task: task})
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"`+desc+`","prompt":"explore","fork":true}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-prov.enter:
	case <-time.After(5 * time.Second):
		t.Fatal("the fork never reached the provider")
	}
	return task, task.ChildIDs()[0], block
}

func steerNotes(a *Agent) []string {
	a.steerMu.Lock()
	defer a.steerMu.Unlock()
	return append([]string(nil), a.steer...)
}

func TestMessageSteerAndInterjectOrder(t *testing.T) {
	task, id, block := startFork(t, "Audit the parser")
	defer close(block)
	ctx := context.Background()
	if _, err := task.Message(ctx, id, "first", DeliverSteer); err != nil {
		t.Fatal(err)
	}
	// By description, case-insensitively, as a unique prefix.
	if out, err := task.Message(ctx, "audit the", "urgent", DeliverInterject); err != nil || !strings.Contains(out, "msg-"+id+"-2") {
		t.Fatalf("interject by name: %q %v", out, err)
	}
	notes := steerNotes(task.children[id].agent)
	if len(notes) != 2 || !strings.Contains(notes[0], "urgent") || !strings.Contains(notes[1], "first") {
		t.Fatalf("interject must go ahead of steering already waiting: %q", notes)
	}
	if !strings.HasPrefix(notes[0], "Message from the parent agent (msg-") {
		t.Errorf("message not marked as the parent's: %q", notes[0])
	}
}

func TestMessageQueueRunsAsALaterTurn(t *testing.T) {
	task, id, block := startFork(t, "slow")
	if out, err := task.Message(context.Background(), id, "and then do B", DeliverQueue); err != nil || !strings.Contains(out, "queued") {
		t.Fatalf("%q %v", out, err)
	}
	if n := len(steerNotes(task.children[id].agent)); n != 0 {
		t.Fatalf("a queued message must not steer the current turn (%d notes)", n)
	}
	close(block)
	task.Wait(context.Background())
	deadline := time.Now().Add(5 * time.Second)
	for {
		task.mu.Lock()
		done, queued := task.children[id].done, len(task.children[id].queued)
		task.mu.Unlock()
		if done && queued == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("queued turn never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var sawB bool
	for _, m := range task.children[id].agent.MessagesSnapshot() {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "and then do B") {
			sawB = true
		}
	}
	if !sawB {
		t.Fatal("the queued message was never run as a turn")
	}
}

func TestMessageWakesAFinishedSubagent(t *testing.T) {
	task, id, block := startFork(t, "slow")
	close(block)
	task.Wait(context.Background())
	task.mu.Lock()
	done := task.children[id].done
	task.mu.Unlock()
	if !done {
		t.Fatal("fork should have finished")
	}
	out, err := task.Message(context.Background(), id, "one more thing", DeliverSteer)
	if err != nil || !strings.Contains(out, "woke it") {
		t.Fatalf("%q %v", out, err)
	}
	task.Wait(context.Background())
	notes := task.FinishedForks()
	found := false
	for _, n := range notes {
		found = found || strings.Contains(n, id)
	}
	if !found {
		t.Fatalf("a woken subagent's answer must come back as a background update: %v", notes)
	}
}

func TestMessageLimits(t *testing.T) {
	task, id, block := startFork(t, "slow")
	defer close(block)
	ctx := context.Background()
	for _, c := range []struct{ ref, text, want string }{
		{id, "  ", "text is empty"},
		{id, strings.Repeat("x", maxMessageBytes+1), "limit"},
		{"task-99", "hi", "no subagent"},
		{"", "hi", "empty"},
	} {
		if _, err := task.Message(ctx, c.ref, c.text, DeliverSteer); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q/%d bytes: err %v want %q", c.ref, len(c.text), err, c.want)
		}
	}
	for i := 0; i < maxPendingMessages; i++ {
		if _, err := task.Message(ctx, id, "n", DeliverSteer); err != nil {
			t.Fatalf("message %d: %v", i, err)
		}
	}
	if _, err := task.Message(ctx, id, "one too many", DeliverQueue); err == nil || !strings.Contains(err.Error(), "waiting") {
		t.Errorf("pending cap: %v", err)
	}
	// The lifetime quota counts sends, delivered or not.
	task.mu.Lock()
	task.children[id].sent = maxMessagesPerChild
	task.mu.Unlock()
	task.children[id].agent.takeSteering()
	if _, err := task.Message(ctx, id, "late", DeliverSteer); err == nil || !strings.Contains(err.Error(), "quota") {
		t.Errorf("lifetime quota: %v", err)
	}
}

func TestMessageAmbiguousDescription(t *testing.T) {
	task, _, block := startFork(t, "audit parser")
	defer close(block)
	task.mu.Lock()
	task.children["task-2"] = &subagent{agent: task.children["task-1"].agent, desc: "audit lexer"}
	task.mu.Unlock()
	if _, err := task.Message(context.Background(), "audit", "x", DeliverSteer); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("ambiguous prefix: %v", err)
	}
}

func TestSendSubagentMessageToolAndDepth(t *testing.T) {
	task, id, block := startFork(t, "slow")
	defer close(block)
	tl := &SendSubagentMessage{Task: task}
	if !tl.ParentOnly() || tl.Mutating() {
		t.Fatal("it stays on the parent session and does not edit files")
	}
	out, err := tl.Run(context.Background(), json.RawMessage(`{"subagent_id":"`+id+`","text":"hi","delivery":"interject"}`))
	if err != nil || !strings.Contains(out, "interject") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"subagent_id":"`+id+`","text":"hi","delivery":"loud"}`)); err == nil {
		t.Error("bad delivery accepted")
	}
	task.Parent.Forked = true
	if _, err := tl.Run(context.Background(), json.RawMessage(`{"subagent_id":"`+id+`","text":"hi"}`)); err == nil || !strings.Contains(err.Error(), "only the main session") {
		t.Errorf("a fork may not message: %v", err)
	}
	// And the fork or subagent built from the parent never receives the tool.
	task.Parent.Forked = false
	for _, d := range task.children[id].agent.Tools.Defs() {
		if d.Name == "send_subagent_message" {
			t.Error("the fork inherited send_subagent_message")
		}
	}
}
