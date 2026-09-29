package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// gatedRunner blocks inside a turn until it is released, and records how
// many turns were ever running at once.
type gatedRunner struct {
	started  chan struct{}
	release  chan struct{}
	approve  bool // ask the editor for permission instead of returning
	approver agent.Approver

	mu       sync.Mutex
	inFlight int
	most     int
	permErr  error
}

func (g *gatedRunner) RunBlocks(ctx context.Context, _ []llm.Block, _ func(agent.Event)) error {
	g.mu.Lock()
	g.inFlight++
	if g.inFlight > g.most {
		g.most = g.inFlight
	}
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		g.inFlight--
		g.mu.Unlock()
	}()
	g.started <- struct{}{}
	if g.approve {
		_, err := g.approver.Approve(ctx, agent.ToolCall{ID: "t1", Name: "bash"})
		g.mu.Lock()
		g.permErr = err
		g.mu.Unlock()
		return err
	}
	<-g.release
	return nil
}

func (g *gatedRunner) MessagesSnapshot() []llm.Message { return nil }
func (g *gatedRunner) SetApprover(a agent.Approver)    { g.approver = a }
func (g *gatedRunner) peak() int                       { g.mu.Lock(); defer g.mu.Unlock(); return g.most }
func (g *gatedRunner) permissionError() error          { g.mu.Lock(); defer g.mu.Unlock(); return g.permErr }
func gatedOptions(t *testing.T, g *gatedRunner) Options {
	t.Helper()
	dir, cwd := t.TempDir(), t.TempDir()
	return Options{
		Version: "test",
		NewAgent: func(_, _ string) (TurnRunner, *session.Session, error) {
			s, err := session.New(dir, cwd, "fake", "m")
			return g, s, err
		},
	}
}

func newSessionID(t *testing.T, c *client) string {
	t.Helper()
	c.call("initialize", initializeParams{ProtocolVersion: 1})
	c.read()
	c.call("session/new", newSessionParams{Cwd: t.TempDir()})
	var ns newSessionResult
	if err := json.Unmarshal(c.read().Result, &ns); err != nil {
		t.Fatal(err)
	}
	return ns.SessionID
}

// Every request runs on its own goroutine, so two prompts for one session
// ran the agent twice at once and interleaved into the same history — and
// the second overwrote the first's cancel, so session/cancel reached only
// one of them.
func TestTwoPromptsForOneSessionRunOneAtATime(t *testing.T) {
	g := &gatedRunner{started: make(chan struct{}, 4), release: make(chan struct{})}
	c, stop := newClient(t, gatedOptions(t, g))
	defer stop()
	id := newSessionID(t, c)

	prompt := func() {
		c.call("session/prompt", promptParams{SessionID: id, Prompt: []contentBlock{{Type: "text", Text: "go"}}})
	}
	prompt()
	select {
	case <-g.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first prompt never started")
	}
	prompt()
	select {
	case <-g.started:
		t.Fatal("the second prompt ran while the first was still running")
	case <-time.After(300 * time.Millisecond):
	}

	g.release <- struct{}{} // the first turn ends
	select {
	case <-g.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the second prompt never started")
	}
	g.release <- struct{}{}

	for range 2 {
		m, _ := c.readUntil(isResponse)
		var res promptResult
		if err := json.Unmarshal(m.Result, &res); err != nil {
			t.Fatalf("%s: %v", m.Result, err)
		}
		if res.StopReason != stopEndTurn {
			t.Fatalf("stop reason = %q", res.StopReason)
		}
	}
	if n := g.peak(); n != 1 {
		t.Fatalf("%d turns ran at once", n)
	}
}

// An editor that writes its requests and closes the pipe leaves any
// permission request outstanding with nobody to answer it. The approver
// waited for ever, and readLoop waited for the approver: the adapter hung
// on shutdown instead of ending.
func TestAPermissionRequestOutstandingWhenTheClientGoesAwayDoesNotHang(t *testing.T) {
	g := &gatedRunner{started: make(chan struct{}, 4), approve: true}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	// Read everything the server writes, but answer nothing: the
	// permission request is left outstanding, which is the point.
	replies := make(chan message, 8)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<16), 1<<22)
		for sc.Scan() {
			var m message
			if json.Unmarshal(sc.Bytes(), &m) == nil && isResponse(m) {
				select {
				case replies <- m:
				default:
				}
			}
		}
	}()
	o := gatedOptions(t, g)
	o.In, o.Out, o.Log = inR, outW, io.Discard
	srv := New(o)
	served := make(chan error, 1)
	go func() { served <- srv.Serve(context.Background()) }()

	write := func(id int, method string, params any) {
		raw, _ := json.Marshal(params)
		b, _ := json.Marshal(message{JSONRPC: "2.0", ID: json.RawMessage(strconv.Itoa(id)), Method: method, Params: raw})
		if _, err := inW.Write(append(b, '\n')); err != nil {
			t.Error(err)
		}
	}
	write(1, "session/new", newSessionParams{Cwd: t.TempDir()})
	var ns newSessionResult
	select {
	case m := <-replies:
		if err := json.Unmarshal(m.Result, &ns); err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("session/new was never answered")
	}
	write(2, "session/prompt", promptParams{SessionID: ns.SessionID, Prompt: []contentBlock{{Type: "text", Text: "go"}}})

	select {
	case <-g.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never started")
	}
	inW.Close() // the editor goes away mid-permission

	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve hung waiting for a permission nobody would answer")
	}
	if err := g.permissionError(); !errors.Is(err, ErrDisconnected) {
		t.Fatalf("the approver got %v, want ErrDisconnected", err)
	}
	outW.Close()
}

// session/cancel is for the session, not for whichever prompt happens to
// hold the turn: a prompt queued behind the running one used to start as
// soon as the cancelled one ended, running a turn the editor had just
// cancelled and answering end_turn.
func TestCancelAlsoEndsAPromptWaitingForTheTurn(t *testing.T) {
	r := &cancelAwareRunner{started: make(chan struct{}, 4), release: make(chan struct{})}
	dir := t.TempDir()
	c, stop := newClient(t, Options{
		Version: "test",
		NewAgent: func(_, _ string) (TurnRunner, *session.Session, error) {
			s, err := session.New(dir, t.TempDir(), "fake", "m")
			return r, s, err
		},
	})
	defer stop()
	id := newSessionID(t, c)

	prompt := func() {
		c.call("session/prompt", promptParams{SessionID: id, Prompt: []contentBlock{{Type: "text", Text: "go"}}})
	}
	prompt()
	select {
	case <-r.started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first prompt never started")
	}
	prompt()
	time.Sleep(300 * time.Millisecond) // let the second one queue
	c.notify("session/cancel", cancelParams{SessionID: id})

	for range 2 {
		m, _ := c.readUntil(isResponse)
		var res promptResult
		if err := json.Unmarshal(m.Result, &res); err != nil {
			t.Fatalf("%s: %v", m.Result, err)
		}
		if res.StopReason != stopCancelled {
			t.Fatalf("stop reason = %q, want cancelled for both prompts", res.StopReason)
		}
	}
	select {
	case <-r.started:
		t.Fatal("the queued prompt ran after the session was cancelled")
	default:
	}
}

// Every request runs on its own goroutine, so a session/cancel the editor
// sent right after a session/prompt can be handled first. It closed the
// cancel channel before the prompt took it, and the prompt then ran its
// whole turn although the editor had cancelled it. Handled in that
// order, the prompt read before the cancel must still end cancelled —
// and a prompt read after it must still run.
func TestACancelHandledBeforeTheEarlierPromptStillCancelsIt(t *testing.T) {
	r := &cancelAwareRunner{started: make(chan struct{}, 2), release: make(chan struct{}, 1)}
	dir := t.TempDir()
	s := New(Options{Out: io.Discard, NewAgent: func(cwd, _ string) (TurnRunner, *session.Session, error) {
		sess, err := session.New(dir, cwd, "fake", "m")
		return r, sess, err
	}})
	res, err := s.newSession(json.RawMessage(`{"cwd":` + strconv.Quote(t.TempDir()) + `}`))
	if err != nil {
		t.Fatal(err)
	}
	id := res.(newSessionResult).SessionID
	params := func(v any) json.RawMessage {
		b, _ := json.Marshal(v)
		return b
	}
	prompt := params(promptParams{SessionID: id, Prompt: []contentBlock{{Type: "text", Text: "go"}}})
	// Read order: prompt (1), cancel (2), prompt (3). Handled: 2, 1, 3.
	if err := s.cancel(params(cancelParams{SessionID: id}), 2); err != nil {
		t.Fatal(err)
	}
	done := make(chan any, 1)
	go func() {
		out, err := s.prompt(context.Background(), prompt, 1)
		if err != nil {
			done <- err
			return
		}
		done <- out
	}()
	select {
	case out := <-done:
		if pr, ok := out.(promptResult); !ok || pr.StopReason != stopCancelled {
			t.Fatalf("prompt = %+v, want cancelled", out)
		}
	case <-r.started:
		t.Fatal("the prompt the editor cancelled ran its turn")
	case <-time.After(10 * time.Second):
		t.Fatal("the cancelled prompt never returned")
	}
	r.release <- struct{}{}
	out, err := s.prompt(context.Background(), prompt, 3)
	if err != nil {
		t.Fatal(err)
	}
	if pr := out.(promptResult); pr.StopReason != stopEndTurn {
		t.Fatalf("a prompt sent after the cancel = %+v, want it to run", pr)
	}
}

// cancelAwareRunner holds its turn until released or cancelled.
type cancelAwareRunner struct {
	started  chan struct{}
	release  chan struct{}
	approver agent.Approver
}

func (r *cancelAwareRunner) RunBlocks(ctx context.Context, _ []llm.Block, _ func(agent.Event)) error {
	r.started <- struct{}{}
	select {
	case <-r.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *cancelAwareRunner) MessagesSnapshot() []llm.Message { return nil }
func (r *cancelAwareRunner) SetApprover(a agent.Approver)    { r.approver = a }
