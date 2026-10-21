package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// countedWrite is write_file that counts the times it actually ran.
type countedWrite struct {
	tool.WriteFile
	runs *atomic.Int32
}

func (c countedWrite) Run(ctx context.Context, in json.RawMessage) (string, error) {
	c.runs.Add(1)
	return c.WriteFile.Run(ctx, in)
}

// countedRead is read_file that counts its runs.
type countedRead struct {
	tool.ReadFile
	runs *atomic.Int32
}

func (c countedRead) Run(ctx context.Context, in json.RawMessage) (string, error) {
	c.runs.Add(1)
	return c.ReadFile.Run(ctx, in)
}

// stepCalls is one model reply asking for a read and a write together.
func stepCalls() *llm.Response {
	return &llm.Response{
		Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ID: "r1", Name: "read_file", Input: json.RawMessage(`{"path":"a.txt"}`)},
			{Type: llm.BlockToolUse, ID: "w1", Name: "write_file", Input: json.RawMessage(`{"path":"b.txt","content":"written\n"}`)},
		}},
		StopReason: llm.StopToolUse,
		Usage:      llm.Usage{InputTokens: 1, OutputTokens: 1},
	}
}

func durableAgent(t *testing.T, root string, f *fake, reads, writes *atomic.Int32, log session.Log) *Agent {
	t.Helper()
	return &Agent{
		Provider:    f,
		Tools:       tool.NewRegistry(countedRead{tool.ReadFile{Root: root}, reads}, countedWrite{tool.WriteFile{Root: root}, writes}),
		System:      "sys",
		Model:       "m",
		Root:        root,
		AutoApprove: true,
		Log:         log,
	}
}

// unanswered lists tool_use ids with no tool_result in msgs.
func unanswered(msgs []llm.Message) []string {
	got := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				got[b.ToolUseID] = true
			}
		}
	}
	var out []string
	for _, m := range msgs {
		for _, u := range m.ToolUses() {
			if !got[u.ID] {
				out = append(out, u.ID)
			}
		}
	}
	return out
}

// TestContinueAfterACrashAtEveryPoint records one real turn, then for every
// point the process could have died at — after each event it logged —
// opens the log as it stood and continues. At every point: the turn is
// reported interrupted exactly when it did not end; continuing sends a
// provider-valid history; a finished call's real result is kept; a read
// that was running runs again; and the write, once started, never runs a
// second time.
func TestContinueAfterACrashAtEveryPoint(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var reads, writes atomic.Int32
	full := &session.Memory{}
	a := durableAgent(t, root, &fake{responses: []*llm.Response{stepCalls(), text("all done")}}, &reads, &writes, full)
	if err := a.Run(context.Background(), "read a and write b", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	events := full.Events()
	if it := session.FindInterrupted(events); it != nil {
		t.Fatalf("a finished turn reported interrupted: %+v", it)
	}

	covered := map[string]int{}
	for k := 1; k < len(events); k++ {
		prefix := append([]session.Event(nil), events[:k]...)
		last := prefix[k-1]
		os.Remove(filepath.Join(root, "b.txt"))
		// The write is on disk if the prefix shows it finished, or started
		// (the crash came after it ran but before its result was logged —
		// the same state on disk as a crash mid-write that completed).
		writeStarted := false
		for _, e := range prefix {
			if !e.Surface && e.CallID == "w1" && (e.Type == session.TypeToolCall || e.Type == session.TypeToolResult) {
				writeStarted = true
			}
		}
		if writeStarted {
			os.WriteFile(filepath.Join(root, "b.txt"), []byte("written\n"), 0o644)
		}
		mem := &session.Memory{}
		for _, e := range prefix {
			mem.Append(e)
		}
		it := session.FindInterrupted(prefix)
		if it == nil {
			// Nothing to continue: only before the turn began, or after
			// the final answer (the checks after it had not run yet).
			continue
		}
		var reads2, writes2 atomic.Int32
		f := &fake{responses: []*llm.Response{text("carried on")}}
		b := durableAgent(t, root, f, &reads2, &writes2, mem)
		b.Messages = session.FoldSurface(prefix)
		b.RestoreFromLog()
		if err := b.ContinueInterrupted(context.Background(), func(Event) {}); err != nil {
			t.Fatalf("crash after event %d (%s): continue: %v", k, last.Type, err)
		}
		if len(f.requests) != 1 {
			t.Fatalf("crash after event %d (%s): %d model calls, want 1", k, last.Type, len(f.requests))
		}
		sent := f.requests[0].Messages
		if u := unanswered(sent); len(u) > 0 {
			t.Fatalf("crash after event %d (%s): sent unanswered calls %v", k, last.Type, u)
		}
		if writes2.Load() != 0 {
			t.Fatalf("crash after event %d (%s): the write ran again", k, last.Type)
		}
		// The read: re-run only when it had started and not finished.
		readState := ""
		for _, e := range prefix {
			if !e.Surface && e.CallID == "r1" {
				readState = e.Type
			}
		}
		wantReads := int32(0)
		if readState == session.TypeToolCall {
			wantReads = 1
		}
		if reads2.Load() != wantReads {
			t.Fatalf("crash after event %d (%s): read ran %d times, want %d (state %q)", k, last.Type, reads2.Load(), wantReads, readState)
		}
		if len(it.Calls) > 0 {
			results := sent[len(sent)-1]
			for _, blk := range results.Blocks {
				switch blk.ToolUseID {
				case "r1":
					if readState != "" && !strings.Contains(blk.Text, "alpha content") {
						t.Fatalf("crash after event %d: the read's answer lost its content: %q", k, blk.Text)
					}
				case "w1":
					if !writeStarted && !strings.Contains(blk.Text, "unknown") {
						t.Fatalf("crash after event %d: a write with no start record must say unknown: %q", k, blk.Text)
					}
				}
			}
		}
		// Offered once: the dead turn is closed, and the continuation ended.
		if session.FindInterrupted(mem.Events()) != nil {
			t.Fatalf("crash after event %d: still reported interrupted after continuing", k)
		}
		switch {
		case len(it.Calls) == 0:
			covered["waiting for the model"]++
		case readState == session.TypeToolCall:
			covered["read running"]++
		case writeStarted:
			covered["write started"]++
		default:
			covered["calls asked for"]++
		}
	}
	// Every kind of crash point was reached, or the loop proved nothing.
	for _, kind := range []string{"waiting for the model", "read running", "write started", "calls asked for"} {
		if covered[kind] == 0 {
			t.Errorf("no crash point of kind %q in %d events: %v", kind, len(events), covered)
		}
	}
}

// A write that was running is described from what is on disk, not run.
func TestInterruptedWriteIsDescribedFromDisk(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "done.txt"), []byte("new\n"), 0o644)
	os.WriteFile(filepath.Join(root, "half.go"), []byte("func old() {}\n"), 0o644)
	cases := []struct {
		name, input, want string
	}{
		{"write_file", `{"path":"done.txt","content":"new\n"}`, "it completed"},
		{"write_file", `{"path":"gone.txt","content":"x"}`, "the write did not happen"},
		{"write_file", `{"path":"done.txt","content":"other"}`, "did not complete"},
		{"edit_file", `{"path":"half.go","old_string":"func old()","new_string":"func neu()"}`, "did not take effect"},
		{"edit_file", `{"path":"done.txt","old_string":"old","new_string":"new"}`, "appears to have completed"},
	}
	for _, c := range cases {
		got := describeInterrupted(root, session.PendingCall{Name: c.name, Input: json.RawMessage(c.input), Started: true})
		if !strings.Contains(got, c.want) || !strings.Contains(got, "not run again") {
			t.Errorf("%s %s: %q, want %q", c.name, c.input, got, c.want)
		}
	}
}

// A turn that stopped on an error or a cancel logs its end, so it is never
// mistaken for one the session died in.
func TestStoppedTurnsAreNotInterrupted(t *testing.T) {
	log := &session.Memory{}
	a, _ := newAgent(t, &fake{}, nil) // no responses: the model call fails
	a.Log = log
	if err := a.Run(context.Background(), "hi", func(Event) {}); err == nil {
		t.Fatal("expected the call to fail")
	}
	if it := session.FindInterrupted(log.Events()); it != nil {
		t.Fatalf("an errored turn was reported interrupted: %+v", it)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b, _ := newAgent(t, &fake{responses: []*llm.Response{text("x")}}, nil)
	b.Log = &session.Memory{}
	_ = b.Run(ctx, "hi", func(Event) {})
	if it := session.FindInterrupted(b.Log.Events()); it != nil {
		t.Fatalf("a cancelled turn was reported interrupted: %+v", it)
	}
}

// A session written before the marker is never offered a continuation.
func TestUnmarkedOpenTurnIsNotInterrupted(t *testing.T) {
	user := llm.TextMessage(llm.RoleUser, "hi")
	events := []session.Event{
		{Seq: 1, Type: session.TypeTurnStart, Turn: 1},
		{Seq: 2, Type: session.TypeUserMessage, Turn: 1, Surface: true, Message: &user},
	}
	if it := session.FindInterrupted(events); it != nil {
		t.Fatalf("got %+v", it)
	}
	events[0].Extra = session.TurnLogsEnd
	if it := session.FindInterrupted(events); it == nil || it.Turn != 1 {
		t.Fatalf("a marked open turn was not found: %+v", it)
	}
}

// Continuing a turn replaces the fold's stand-in answers on the history.
// That cut has to go through the history lock like every other change:
// a front-end reads the transcript with MessagesSnapshot while the
// continuation runs, and an unlocked write is a data race (go test -race).
func TestContinueReplacesStandInsUnderTheLock(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha content\n"), 0o644)
	var reads, writes atomic.Int32
	full := &session.Memory{}
	a := durableAgent(t, root, &fake{responses: []*llm.Response{stepCalls(), text("all done")}}, &reads, &writes, full)
	if err := a.Run(context.Background(), "read a and write b", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	events := full.Events()
	for k := 1; k < len(events); k++ {
		prefix := append([]session.Event(nil), events[:k]...)
		it := session.FindInterrupted(prefix)
		if it == nil || len(it.Calls) == 0 {
			continue
		}
		mem := &session.Memory{}
		for _, e := range prefix {
			mem.Append(e)
		}
		var reads2, writes2 atomic.Int32
		b := durableAgent(t, root, &fake{responses: []*llm.Response{text("carried on")}}, &reads2, &writes2, mem)
		b.Messages = session.FoldSurface(prefix)
		b.RestoreFromLog()
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
					_ = b.MessagesSnapshot()
				}
			}
		}()
		err := b.ContinueInterrupted(context.Background(), func(Event) {})
		close(stop)
		<-done
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatal("no crash point with pending calls")
}
