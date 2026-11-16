package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/egress"
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
	if !strings.Contains(errOut.String(), "round 1/5 failed (exit 3)") || !strings.Contains(errOut.String(), "[until] passed: "+check+" · this verifies the run") {
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

// A project (or session) with automatic memory switched off gets no
// extraction after a mutating turn: the extractor's reply stays unused and
// nothing is saved, though AutoMemory is on in the config.
func TestAutoMemoryOffForProjectOrSessionExtractsNothing(t *testing.T) {
	for _, mode := range []string{"project", "session"} {
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
		if mode == "project" {
			if err := store.SetAutoProject(false); err != nil {
				t.Fatal(err)
			}
		} else {
			store.AutoSession.Store(true)
		}
		var out, errOut bytes.Buffer
		if err := Run(context.Background(), Options{Agent: ag, Prompt: "write x", Stdout: &out, Stderr: &errOut, Memory: store, AutoMemory: true}); err != nil {
			t.Fatal(err)
		}
		if e := store.Entries(); len(e) != 0 {
			t.Fatalf("%s off: entries = %+v", mode, e)
		}
		if len(f.responses) != 1 {
			t.Fatalf("%s off: %d responses left, the extractor must not have run", mode, len(f.responses))
		}
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
	write := `{"path":"a.go","content":"package a"}`
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

// TestCheckBoundsAPipeHeldByALeakedGrandchild is the Supervise fix on the
// --until/--watch path: a check command that leaves a process running (a
// watched `npm test` whose suite starts a server and forgets it) held the
// output pipes open after the shell exited, and CombinedOutput's Wait
// blocked on them for as long as the orphan lived — past the ten-minute
// bound and past ctrl+c. The pipe wait is now bounded.
func TestCheckBoundsAPipeHeldByALeakedGrandchild(t *testing.T) {
	start := time.Now()
	out, code := check(context.Background(), t.TempDir(), "echo ok; sleep 30 &")
	if code != 0 || strings.TrimSpace(out) != "ok" {
		t.Fatalf("out=%q code=%d", out, code)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("check took %s; the leaked grandchild held the pipes past the command", elapsed)
	}
}

// TestWatchStopsOnBudget is the --watch half of the budget contract:
// a watched command that keeps failing used to swallow the agent's
// ErrBudget as "model turn failed" and start another priced fix round —
// burning a full call per round, forever, never exiting 55.
func TestWatchStopsOnBudget(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "fixing"), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 10, CostUSD: 1}},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", AutoApprove: true}
	var out, errOut bytes.Buffer
	err := Run(context.Background(), Options{Agent: ag, Prompt: "x", Stdout: &out, Stderr: &errOut,
		Watch: "false", WatchRounds: 100, WatchInterval: 50 * time.Millisecond, MaxCostUSD: 0.5, Cwd: root})
	if !errors.Is(err, ErrBudget) {
		t.Fatalf("err = %v\n%s", err, errOut.String())
	}
}

// Model deltas and tool output reach a headless stdout verbatim, and a
// hostile file can carry terminal escape sequences: an OSC 52 in a read
// file would write the caller's clipboard the moment a wrapper printed
// it on a terminal. The strip keeps SGR colour (build output stays
// coloured) and drops every state-changing sequence.
func TestTextModeStripsUnsafeANSI(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "answer\x1b]0;pwned\x07"},
		}}, StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", AutoApprove: true}
	var out, errOut bytes.Buffer
	err := Run(context.Background(), Options{Agent: ag, Prompt: "x", Stdout: &out, Stderr: &errOut, Cwd: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "answer\n" {
		t.Fatalf("stdout = %q", out.String())
	}
	if strings.ContainsRune(errOut.String(), 0x1b) {
		t.Fatalf("escape byte on stderr: %q", errOut.String())
	}
}

// Every text-mode branch prints something the run did not author. A
// notice quotes the shell command a job ran, a retry and a compaction
// quote a provider's error, a tool result is file contents, and an MCP
// server supplies its own tool names. Only EvText, EvToolResult and the
// verdict were stripped, so a notice carrying an OSC 52 wrote the
// caller's clipboard.
func TestEveryTextBranchStripsUnsafeANSI(t *testing.T) {
	const osc52 = "\x1b]52;c;cHduZWQ=\x07"
	const c1CSI = "\u009b2J"
	for _, tc := range []struct {
		name string
		e    agent.Event
	}{
		{"text", agent.Event{Kind: agent.EvText, Text: "hi" + osc52}},
		{"tool name", agent.Event{Kind: agent.EvToolRun, Call: &agent.ToolCall{Name: "mcp__x__" + osc52, Input: []byte(`{}`)}}},
		{"tool intent", agent.Event{Kind: agent.EvToolRun, Call: &agent.ToolCall{Name: "bash", Intent: "run" + osc52, Input: []byte(`{}`)}}},
		{"tool input", agent.Event{Kind: agent.EvToolRun, Call: &agent.ToolCall{Name: "bash", Input: []byte(`{"c":"` + c1CSI + `"}`)}}},
		{"tool result", agent.Event{Kind: agent.EvToolResult, Output: "out" + osc52}},
		{"compacted", agent.Event{Kind: agent.EvCompacted, Text: "at 80%" + osc52}},
		{"verdict", agent.Event{Kind: agent.EvVerdict, Text: "passed" + c1CSI}},
		{"retry", agent.Event{Kind: agent.EvRetry, Text: "429" + osc52}},
		{"notice", agent.Event{Kind: agent.EvNotice, Text: "job exited: " + osc52}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			printEvent(Options{Stdout: &out, Stderr: &errOut, Agent: &agent.Agent{}}, tc.e)
			for _, w := range []struct {
				where string
				got   string
			}{{"stdout", out.String()}, {"stderr", errOut.String()}} {
				for _, bad := range []rune{0x1b, 0x9b, 0x9d} {
					if strings.ContainsRune(w.got, bad) {
						t.Errorf("%s kept %#x: %q", w.where, bad, w.got)
					}
				}
			}
		})
	}
}

// The watched command's output is the same problem by another route: it
// is a child process's stderr, printed to the caller's terminal.
func TestWatchedCommandOutputIsStripped(t *testing.T) {
	if got := safeLine("fail\x1b]52;c;cHduZWQ=\x07", 200); strings.ContainsRune(got, 0x1b) {
		t.Fatalf("safeLine kept an escape: %q", got)
	}
}

// An interrupt that lands while the --until check is running kills the
// check, and the dead check was read as a failed round: the last round
// reported the check as not passing and exited 1 instead of 130, and an
// earlier one printed a bogus failure and started another turn.
func TestUntilInterruptedDuringTheCheckExitsAsCancelled(t *testing.T) {
	root := t.TempDir()
	f := &fake{responses: []*llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
	}}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", AutoApprove: true}
	var out, errOut bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	err := Run(ctx, Options{Agent: ag, Prompt: "x", Stdout: &out, Stderr: &errOut, Until: "sleep 30", UntilRounds: 1, Cwd: root})
	if code := ExitCode(err); code != ExitCancelled {
		t.Fatalf("exit = %d (%v), want %d\n%s", code, err, ExitCancelled, errOut.String())
	}
	if strings.Contains(errOut.String(), "failed") {
		t.Fatalf("an interrupted check was reported as a failed round:\n%s", errOut.String())
	}
}

// blocking is a provider whose call waits for its context.
type blocking struct{ started chan struct{} }

func (b *blocking) Name() string { return "blocking" }
func (b *blocking) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

// A run that outlives --max-wall-time is stopped and reports the limit
// (exit 56), not an interrupt: nobody pressed ^C.
func TestMaxWallTimeStopsTheRun(t *testing.T) {
	p := &blocking{started: make(chan struct{})}
	ag := &agent.Agent{Provider: p, Tools: tool.NewRegistry(), Model: "m"}
	var out, errOut syncBuffer
	start := time.Now()
	err := Run(context.Background(), Options{Agent: ag, Prompt: "slow", Stdout: &out, Stderr: &errOut, Format: FormatJSON, MaxWallTime: 100 * time.Millisecond})
	if !errors.Is(err, ErrWallTime) || ExitCode(err) != ExitWallTime {
		t.Fatalf("err = %v (exit %d)", err, ExitCode(err))
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s", time.Since(start))
	}
	if !strings.Contains(out.String(), "wall-time limit") {
		t.Fatalf("result = %s", out.String())
	}
}

// --max-wall-time bounds a --watch run too: the watch ends on the limit
// and the run exits 56, not 0 as if someone had pressed ^C.
func TestMaxWallTimeStopsAWatch(t *testing.T) {
	root := t.TempDir()
	ag := &agent.Agent{Provider: &fake{}, Tools: tool.NewRegistry(), Model: "m"}
	var out, errOut syncBuffer
	err := Run(context.Background(), Options{Agent: ag, Prompt: "watch", Stdout: &out, Stderr: &errOut, Cwd: root,
		Watch: "true", WatchInterval: 10 * time.Millisecond, MaxWallTime: 100 * time.Millisecond})
	if !errors.Is(err, ErrWallTime) || ExitCode(err) != ExitWallTime {
		t.Fatalf("err = %v (exit %d), want the wall-time limit (exit %d)\n%s", err, ExitCode(err), ExitWallTime, errOut.String())
	}
}

// httpFake answers from a script but sends every request over HTTP through
// the egress transport, as a real provider does, so the recorder on the
// call's context sees a host and a byte count.
type httpFake struct {
	fake
	client *http.Client
	url    string
}

func (h *httpFake) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	body, _ := json.Marshal(req)
	hr, _ := http.NewRequestWithContext(ctx, "POST", h.url, bytes.NewReader(body))
	resp, err := h.client.Do(hr)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	return h.fake.Stream(ctx, req, emit)
}

func memoryEgressRun(t *testing.T, autoOff bool) (CLIResult, *session.Session) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	root := t.TempDir()
	f := &httpFake{client: &http.Client{Transport: egress.Transport(nil, egress.KindModel)}, url: srv.URL}
	f.responses = []*llm.Response{
		{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"x","content":"1"}`)},
		}}, StopReason: llm.StopToolUse},
		{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "Files under x are generated; edit the templates instead.\nNONE"), StopReason: llm.StopEndTurn},
		{Message: llm.TextMessage(llm.RoleAssistant, "NONE"), StopReason: llm.StopEndTurn}, // sleep at the session's end
	}
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", AutoApprove: true}
	store := memory.Open(t.TempDir(), root)
	if autoOff {
		store.AutoSession.Store(true)
	}
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := Run(context.Background(), Options{Agent: ag, Session: sess, Prompt: "write x", Stdout: &out, Stderr: &errOut, Memory: store, AutoMemory: true, Format: FormatJSON}); err != nil {
		t.Fatalf("%v\n%s", err, errOut.String())
	}
	var res CLIResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("result: %v\n%s", err, out.String())
	}
	return res, sess
}

// The extractor sends the conversation to the model again after the turn;
// that request is in the egress record: the headless result and the
// session log carry an "after turn 1: memory extraction" report naming the
// provider host and what was sent.
func TestMemoryExtractionIsInTheEgressRecord(t *testing.T) {
	res, sess := memoryEgressRun(t, false)
	var rep *egress.Report
	for i := range res.Egress {
		if res.Egress[i].Phase == "memory extraction" {
			rep = &res.Egress[i]
		}
	}
	if rep == nil {
		t.Fatalf("no memory extraction report in result egress: %+v", res.Egress)
	}
	txt := rep.Text()
	for _, want := range []string{"after turn 1: memory extraction", "127.0.0.1", "extraction: ", "existing entries", "model requests"} {
		if !strings.Contains(txt, want) {
			t.Errorf("report lacks %q:\n%s", want, txt)
		}
	}
	var sleep *egress.Report
	for i := range res.Egress {
		if strings.HasPrefix(res.Egress[i].Phase, "memory consolidation") {
			sleep = &res.Egress[i]
		}
	}
	if sleep == nil || !strings.Contains(sleep.Text(), "sleep consolidation") || !strings.Contains(sleep.Text(), "127.0.0.1") {
		t.Errorf("no sleep report naming the host: %+v", res.Egress)
	}
	if rep.Turn != 1 {
		t.Errorf("turn = %d", rep.Turn)
	}
	found := false
	for _, e := range sess.Events() {
		if e.Type == session.TypeEgress && strings.Contains(e.Output, `"phase":"memory extraction"`) {
			found = true
		}
	}
	if !found {
		t.Error("session log has no egress event for the extraction")
	}
}

// With automatic memory off for the session there is no extractor call and
// so no extraction entry.
func TestMemoryAutoOffLeavesNoExtractionEgress(t *testing.T) {
	res, _ := memoryEgressRun(t, true)
	for _, r := range res.Egress {
		if r.Phase != "" {
			t.Fatalf("a memory call was recorded though none was made: %+v", r)
		}
	}
}
