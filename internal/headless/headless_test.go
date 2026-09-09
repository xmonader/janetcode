package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

type fake struct {
	responses []*llm.Response
	calls     int
}

func (f *fake) Name() string { return "fake" }
func (f *fake) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	f.calls++
	r := f.responses[0]
	f.responses = f.responses[1:]
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

func TestRunStreamsTextAndDeniesWithoutTTY(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "could not write"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 3, OutputTokens: 4}},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m"}
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	err = Run(context.Background(), Options{Agent: ag, Session: sess, Prompt: "write x", Stdout: &out, Stderr: &errOut, Stdin: devnull})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "could not write\n" {
		t.Fatalf("stdout = %q", out.String())
	}
	e := errOut.String()
	if !strings.Contains(e, "[permission] write_file denied") || !strings.Contains(e, "[tool error]") || !strings.Contains(e, "[usage] in=3 out=4") {
		t.Fatalf("stderr = %q", e)
	}
	if _, err := os.Stat(filepath.Join(root, "x")); err == nil {
		t.Fatal("file was written despite denial")
	}
	// Session persisted all four messages.
	_, msgs, err := session.Open(filepath.Dir(sess.Path()), sess.Meta.ID)
	if err != nil || len(msgs) != 4 {
		t.Fatalf("session: err=%v n=%d", err, len(msgs))
	}
}

func TestUntilReloopsUntilCheckPasses(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "first try"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "second try"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", AutoApprove: true}
	var out, errOut bytes.Buffer
	// The check fails once and passes on its second run.
	check := "test -f flag || { touch flag; echo 'not yet'; exit 3; }"
	err := Run(context.Background(), Options{Agent: ag, Prompt: "do it", Stdout: &out, Stderr: &errOut, Until: check, Cwd: root})
	if err != nil {
		t.Fatalf("err = %v\n%s", err, errOut.String())
	}
	if !strings.Contains(out.String(), "first try") || !strings.Contains(out.String(), "second try") {
		t.Fatalf("stdout = %q", out.String())
	}
	if !strings.Contains(errOut.String(), "round 1/5 failed (exit 3)") || !strings.Contains(errOut.String(), "[until] passed") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	// The failure went back to the model as the next user message.
	if got := ag.Messages[2].Text(); !strings.Contains(got, "Exit code: 3") || !strings.Contains(got, "not yet") {
		t.Fatalf("second prompt = %q", got)
	}
	// A check that never passes stops after the round limit.
	f2 := &fake{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "a"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "b"), StopReason: llm.StopEndTurn},
	}}
	ag2 := &agent.Agent{Provider: f2, Tools: tool.NewRegistry(), Model: "m"}
	err = Run(context.Background(), Options{Agent: ag2, Prompt: "x", Stdout: &out, Stderr: &errOut, Until: "false", UntilRounds: 2, Cwd: root})
	if !errors.Is(err, ErrUntilFailed) {
		t.Fatalf("err = %v", err)
	}
}

func TestAutoMemoryAfterMutatingTurn(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "Files under x are generated; edit the templates instead.\nNONE"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", AutoApprove: true}
	store := memory.Open(t.TempDir(), root)
	var out, errOut bytes.Buffer
	err := Run(context.Background(), Options{Agent: ag, Prompt: "write x", Stdout: &out, Stderr: &errOut, Memory: store, AutoMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	if e := store.Entries(); len(e) != 1 || e[0].Description != "Files under x are generated; edit the templates instead." || e[0].Source != "auto" {
		t.Fatalf("entries = %+v\nstderr: %s", e, errOut.String())
	}
	if !strings.Contains(errOut.String(), "[memory] remembered ") || !strings.Contains(errOut.String(), "Files under x are generated") {
		t.Fatalf("stderr = %q", errOut.String())
	}
	if len(f.responses) != 0 {
		t.Fatalf("%d responses unused", len(f.responses))
	}
}

func TestWatchRerunsOnChangesAndAsksForFixes(t *testing.T) {
	root := t.TempDir()
	fix := func(id string) *llm.Response {
		return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: id, Name: "write_file", Input: json.RawMessage(`{"path":"ok.txt","content":"fixed"}`)},
		}}, StopReason: llm.StopToolUse}
	}
	done := func(text string) *llm.Response {
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, text), StopReason: llm.StopEndTurn}
	}
	f := &fake{responses: []*llm.Response{fix("t1"), done("fixed once"), fix("t2"), done("fixed twice")}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", AutoApprove: true}
	var out, errOut syncBuffer
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		finished <- Run(ctx, Options{Agent: ag, Prompt: "keep ok.txt present", Stdout: &out, Stderr: &errOut, Cwd: root, Watch: "test -f ok.txt", WatchInterval: 20 * time.Millisecond, WatchRounds: 3})
	}()
	waitFor := func(what string, n int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for strings.Count(errOut.String()+out.String(), what) < n {
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %d× %q; stderr:\n%s\nstdout:\n%s", n, what, errOut.String(), out.String())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	waitFor("[watch] ✓ test -f ok.txt", 1)
	if got, _ := os.ReadFile(filepath.Join(root, "ok.txt")); string(got) != "fixed" {
		t.Fatalf("ok.txt = %q", got)
	}
	if first := ag.Messages[0].Text(); !strings.HasPrefix(first, "keep ok.txt present") || !strings.Contains(first, "fix attempt 1 of 3") {
		t.Fatalf("first prompt = %q", first)
	}
	// The user deletes the file: the watcher notices, the check fails, the model fixes again.
	os.Remove(filepath.Join(root, "ok.txt"))
	waitFor("fixed twice", 1)
	waitFor("[watch] ✓ test -f ok.txt", 2)
	if strings.Count(errOut.String(), "asking the model to fix it (1/3)") != 2 { // the counter resets after a pass
		t.Fatalf("stderr:\n%s", errOut.String())
	}
	cancel()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(errOut.String(), "[watch] ✓"); n != 2 {
		t.Fatalf("passes = %d; stderr:\n%s", n, errOut.String())
	}
}

// syncBuffer is a bytes.Buffer safe for use from the watcher goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// --until re-enters the turn loop, and so does the verdict correction
// when an answer claims more than the turn shows. They work at different
// levels — the correction inside a turn, --until between turns — and this
// pins that they compose rather than fighting: the run still finishes,
// the round limit still bounds it, and neither swallows the other.
func TestUntilComposesWithTheVerdictCorrection(t *testing.T) {
	root := t.TempDir()
	write := `{"path":"a.txt","content":"x"}`
	f := &fake{responses: []*llm.Response{
		// Round 1: edit, then claim the tests pass without running any.
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(write)}}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "Done, all tests pass."), StopReason: llm.StopEndTurn},
		// The correction lands; the model admits it and stops.
		{Message: llm.TextMessage(llm.RoleAssistant, "You are right, I did not run them."), StopReason: llm.StopEndTurn},
		// Round 2, after --until fails: answer plainly.
		{Message: llm.TextMessage(llm.RoleAssistant, "second round"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m",
		AutoApprove: true, Log: &session.Memory{}}
	var out, errOut bytes.Buffer
	check := "test -f flag || { touch flag; echo 'not yet'; exit 3; }"
	if err := Run(context.Background(), Options{Agent: ag, Prompt: "do it", Stdout: &out, Stderr: &errOut,
		Until: check, Cwd: root}); err != nil {
		t.Fatalf("err = %v\n%s", err, errOut.String())
	}
	// Four model calls: the tool turn, the overclaim, the correction, and
	// the second --until round.
	if f.calls != 4 {
		t.Fatalf("%d model calls, want 4:\n%s", f.calls, errOut.String())
	}
	// The correction went in as a user message inside the first turn.
	var corrected bool
	for _, m := range ag.Messages {
		if m.Role == llm.RoleUser && strings.Contains(m.Text(), "[harness]") && strings.Contains(m.Text(), "all tests pass") {
			corrected = true
		}
	}
	if !corrected {
		t.Fatal("the overclaim was not put back to the model")
	}
	// And --until still drove its own round afterwards.
	if !strings.Contains(errOut.String(), "round 1/5 failed (exit 3)") || !strings.Contains(errOut.String(), "[until] passed") {
		t.Fatalf("until did not run its rounds: %q", errOut.String())
	}
	// The verdict for each turn is on the log either way.
	verdicts := 0
	for _, e := range ag.Log.Events() {
		if e.Type == session.TypeVerdict {
			verdicts++
		}
	}
	if verdicts != 2 {
		t.Fatalf("%d verdicts for 2 turns", verdicts)
	}
}

// The verdict is the point of watching a run: a headless caller that sees
// tool activity but not what the turn demonstrated has to take the
// answer's word for it, which is what the verdict exists to avoid.
func TestHeadlessReportsTheVerdict(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ID: "t1",
			Name: "write_file", Input: json.RawMessage(`{"path":"a.go","content":"package a\n"}`)}}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "wrote it"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m",
		AutoApprove: true, Log: &session.Memory{}}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), Options{Agent: ag, Prompt: "write a.go", Stdout: &out, Stderr: &errOut, Cwd: root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "[verdict]") || !strings.Contains(errOut.String(), "unverified") {
		t.Fatalf("no verdict on stderr: %q", errOut.String())
	}
	// It goes to stderr, so piping stdout still gets only the answer.
	if strings.Contains(out.String(), "verdict") {
		t.Fatalf("the verdict leaked into stdout: %q", out.String())
	}
}
