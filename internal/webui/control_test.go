package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/sessionproto"
	"github.com/xmonader/janetcode/internal/tool"
)

// gateProv answers "ok" to every call, but holds each call open while its
// gate is closed, so a test decides when a turn ends.
type gateProv struct {
	mu      sync.Mutex
	gate    chan struct{}
	started chan struct{}
	seen    []string
	onCall  func(text string)
}

func (g *gateProv) Name() string { return "fake" }
func (g *gateProv) hold() {
	g.mu.Lock()
	g.gate = make(chan struct{})
	g.mu.Unlock()
}
func (g *gateProv) open() {
	g.mu.Lock()
	if g.gate != nil {
		close(g.gate)
		g.gate = nil
	}
	g.mu.Unlock()
}
func (g *gateProv) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	g.mu.Lock()
	gate := g.gate
	if n := len(req.Messages); n > 0 {
		for _, b := range req.Messages[n-1].Blocks {
			if b.Type == llm.BlockText {
				g.seen = append(g.seen, b.Text)
				if g.onCall != nil {
					g.onCall(b.Text)
				}
			}
		}
	}
	g.mu.Unlock()
	select {
	case g.started <- struct{}{}:
	default:
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if emit != nil {
		emit(llm.Event{Type: llm.EventTextDelta, Text: "ok"})
	}
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}, nil
}

type ctlEnv struct {
	t     *testing.T
	s     *server
	ts    *httptest.Server
	prov  *gateProv
	sess  *session.Session
	kill  context.CancelFunc
	roots string
}

func newCtlEnv(t *testing.T, ttl time.Duration) *ctlEnv {
	t.Helper()
	root := t.TempDir()
	sess, err := session.New(t.TempDir(), root, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	prov := &gateProv{started: make(chan struct{}, 16)}
	ag := &agent.Agent{Provider: prov, Tools: tool.NewRegistry(tool.WriteFile{Root: root}), Model: "m", Log: sess, Root: root}
	life, kill := context.WithCancel(context.Background())
	s := newServer(life, Options{Agent: ag, Session: sess, Provider: "fake", Cwd: root, Version: "test", ControlTTL: ttl})
	ts := httptest.NewServer(s.handler())
	e := &ctlEnv{t: t, s: s, ts: ts, prov: prov, sess: sess, kill: kill}
	t.Cleanup(func() {
		prov.open()
		kill()
		ts.Close()
		// A finished turn titles the session into the temp dir; let that
		// settle before the directories are removed.
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			s.mu.Lock()
			running := s.running
			s.mu.Unlock()
			if !running {
				return
			}
		}
	})
	return e
}

// call sends a request as client ("" sends no identity).
func (e *ctlEnv) call(client, method, path, body string, hdr ...string) (int, string, http.Header) {
	e.t.Helper()
	r, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	if client != "" {
		r.Header.Set(clientHeader, client)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b), res.Header
}

func (e *ctlEnv) prompt(client, text string, hdr ...string) (int, string) {
	e.t.Helper()
	b, _ := json.Marshal(map[string]string{"text": text})
	code, out, _ := e.call(client, "POST", "/api/prompt", string(b), hdr...)
	return code, out
}

// conflictOf decodes the conflict a refused request carried.
func conflictOf(t *testing.T, code int, body string) sessionproto.Conflict {
	t.Helper()
	if code != http.StatusConflict {
		t.Fatalf("status = %d (%s), want 409", code, body)
	}
	var out struct {
		Error    string
		Conflict sessionproto.Conflict
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Conflict.Code == "" {
		t.Fatalf("409 body is not a conflict: %q (%v)", body, err)
	}
	if out.Error != out.Conflict.Message {
		t.Fatalf("error %q and conflict message %q differ", out.Error, out.Conflict.Message)
	}
	return out.Conflict
}

type stream struct {
	events chan envelope
	cancel context.CancelFunc
}

// open attaches an event stream as client; role is "" or "observer".
func (e *ctlEnv) open(client, role string) *stream {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	url := e.ts.URL + "/events?client=" + client
	if role != "" {
		url += "&role=" + role
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		e.t.Fatal(err)
	}
	st := &stream{events: make(chan envelope, 256), cancel: cancel}
	go func() {
		defer res.Body.Close()
		defer close(st.events)
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			line, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var env envelope
			if json.Unmarshal([]byte(line), &env) == nil {
				st.events <- env
			}
		}
	}()
	e.t.Cleanup(cancel)
	return st
}

// next waits for the first event satisfying ok.
func (st *stream) next(t *testing.T, what string, ok func(envelope) bool) envelope {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case env, open := <-st.events:
			if !open {
				t.Fatalf("stream ended waiting for %s", what)
			}
			if ok(env) {
				return env
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (e *ctlEnv) waitStarted() {
	e.t.Helper()
	select {
	case <-e.prov.started:
	case <-time.After(5 * time.Second):
		e.t.Fatal("the turn never reached the model")
	}
}

func (e *ctlEnv) waitIdle() {
	e.t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if busy, q := e.s.busyQueued(); !busy && q == 0 {
			return
		}
	}
	e.t.Fatal("the turn never finished")
}

func userText(env envelope) bool {
	return env.Type == "entry" && env.Entry != nil && env.Entry.Kind == "user"
}

func TestTwoClientsOneControllerObserverFollows(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	obs := e.open("watcher", "observer")
	hello := obs.next(t, "hello", func(env envelope) bool { return env.Type == "hello" })
	if hello.Control == nil || hello.Control.Protocol != sessionproto.Version || hello.Control.You != sessionproto.RoleObserver {
		t.Fatalf("observer hello: %+v", hello.Control)
	}

	e.prov.hold()
	if code, body := e.prompt("alice", "first"); code != 200 {
		t.Fatalf("controller prompt: %d %s", code, body)
	}
	e.waitStarted()

	// A second client is refused, told who has the session and that a turn runs.
	code, body := e.prompt("bob", "second")
	c := conflictOf(t, code, body)
	if c.Code != sessionproto.CodeLeaseHeld || c.State.Holder == nil || c.State.Holder.Client != "alice" || !c.State.Busy || c.State.You != sessionproto.RoleNone {
		t.Fatalf("bob's conflict: %+v", c)
	}
	// Nothing bob tried reached the session: not a prompt, not a cancel, not a permission answer.
	for _, p := range []struct{ path, body string }{
		{"/api/cancel", `{}`}, {"/api/permission", `{"allow":true}`}, {"/api/steer", `{"text":"x"}`}, {"/api/command", `{"text":"/clear"}`},
	} {
		code, body, _ := e.call("bob", "POST", p.path, p.body)
		if conflictOf(t, code, body).Code != sessionproto.CodeLeaseHeld {
			t.Fatalf("%s by a non-controller was not refused", p.path)
		}
	}
	if busy, _ := e.s.busyQueued(); !busy {
		t.Fatal("bob's cancel stopped alice's turn")
	}

	// The observer sees alice's prompt, cannot act, and is counted.
	got := obs.next(t, "alice's prompt", userText)
	if got.Entry.Text != "first" {
		t.Fatalf("observer saw %q", got.Entry.Text)
	}
	code, body = e.prompt("watcher", "sneaky")
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeObserver {
		t.Fatalf("observer's conflict: %+v", c)
	}
	if v := e.s.controlView("alice"); v.Observers != 1 || v.You != sessionproto.RoleController {
		t.Fatalf("alice's view: %+v", v)
	}

	// A handoff is refused while the turn runs and allowed after it.
	code, body, _ = e.call("bob", "POST", "/api/control", `{"action":"take"}`)
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeBusy {
		t.Fatalf("mid-turn take: %+v", c)
	}
	e.prov.open()
	e.waitIdle()
	code, body, _ = e.call("bob", "POST", "/api/control", `{"action":"take"}`)
	if code != 200 {
		t.Fatalf("idle take: %d %s", code, body)
	}
	var snap sessionproto.Snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil || snap.You != sessionproto.RoleController || snap.Holder.Client != "bob" {
		t.Fatalf("take result: %s (%v)", body, err)
	}

	// The observer is told of the handoff on its stream.
	ev := obs.next(t, "control change", func(env envelope) bool {
		return env.Type == "control" && env.Control.Holder != nil && env.Control.Holder.Client == "bob"
	})
	if ev.Control.You != sessionproto.RoleObserver {
		t.Fatalf("observer's own role in the control event: %v", ev.Control.You)
	}

	// Alice, still acting under her old epoch, gets a stale conflict with the current state.
	code, body = e.prompt("alice", "late", epochHeader, strconv.FormatUint(snap.Epoch-1, 10))
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeStaleLease || c.State.Holder.Client != "bob" || c.State.Epoch != snap.Epoch {
		t.Fatalf("alice's stale conflict: %+v", c)
	}
	if code, body = e.prompt("bob", "third"); code != 200 {
		t.Fatalf("bob after handoff: %d %s", code, body)
	}
	e.waitIdle()
}

func TestObserverCanReadEverythingReadOnly(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	e.open("watcher", "observer").next(t, "hello", func(env envelope) bool { return env.Type == "hello" })
	for _, path := range []string{"/api/state", "/api/control", "/api/workspace", "/api/commands"} {
		if code, body, _ := e.call("watcher", "GET", path, ""); code != 200 {
			t.Fatalf("observer GET %s = %d %s", path, code, body)
		}
	}
	if code, body, _ := e.call("watcher", "POST", "/api/mermaid", `{"source":"graph TD; A-->B"}`); code == http.StatusConflict {
		t.Fatalf("rendering a diagram is not a change, got %d %s", code, body)
	}
	code, body, _ := e.call("watcher", "POST", "/api/new", `{}`)
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeObserver {
		t.Fatalf("observer /api/new: %+v", c)
	}
}

func TestObserverDisconnectIsForgotten(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	obs := e.open("watcher", "observer")
	obs.next(t, "hello", func(env envelope) bool { return env.Type == "hello" })
	if v := e.s.controlView("alice"); v.Observers != 1 {
		t.Fatalf("observers = %d, want 1", v.Observers)
	}
	obs.cancel()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if e.s.controlView("alice").Observers == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a closed observer stream is still counted")
		}
	}
	// And with the stream gone it is no longer barred: it may act as a plain client.
	if code, body := e.prompt("watcher", "hi"); code != 200 {
		t.Fatalf("former observer: %d %s", code, body)
	}
	e.waitIdle()
}

func TestControllerThatVanishesLosesTheSession(t *testing.T) {
	e := newCtlEnv(t, 300*time.Millisecond)
	if code, body := e.prompt("alice", "hi"); code != 200 {
		t.Fatal(code, body)
	}
	e.waitIdle()
	code, body := e.prompt("bob", "too soon")
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeLeaseHeld {
		t.Fatalf("within the lease: %+v", c)
	}
	time.Sleep(400 * time.Millisecond) // alice is never heard from again
	if code, body := e.prompt("bob", "now"); code != 200 {
		t.Fatalf("after the lease lapsed: %d %s", code, body)
	}
	e.waitIdle()
	// Alice comes back: she is a stranger now.
	code, body = e.prompt("alice", "back")
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeLeaseHeld || c.State.Holder.Client != "bob" {
		t.Fatalf("returning client: %+v", c)
	}
}

func TestOpenStreamKeepsTheLeaseAndItsCloseReleasesIt(t *testing.T) {
	e := newCtlEnv(t, 800*time.Millisecond) // the stream beats every 500ms
	st := e.open("alice", "")
	if code, body := e.prompt("alice", "hi"); code != 200 {
		t.Fatal(code, body)
	}
	e.waitIdle()
	time.Sleep(1700 * time.Millisecond) // two TTLs, with only the stream's heartbeat
	code, body := e.prompt("bob", "x")
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeLeaseHeld {
		t.Fatalf("a connected controller lost the lease: %+v", c)
	}
	st.cancel() // the tab closes, or the machine drops off the network
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if code, _ = e.prompt("bob", "y"); code == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the lease of a disconnected controller never lapsed")
		}
	}
	e.waitIdle()
}

func TestSessionChangeStalesEveryLease(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	if code, body := e.prompt("alice", "hi"); code != 200 {
		t.Fatal(code, body)
	}
	e.waitIdle()
	epoch := e.s.controlView("alice").Epoch
	// alice starts a fresh session (the web twin of /clear, as /resume and /fork also move the view).
	if code, body := e.prompt("alice", "/clear"); code != 200 {
		t.Fatalf("/clear: %d %s", code, body)
	}
	code, body := e.prompt("alice", "again", epochHeader, strconv.FormatUint(epoch, 10))
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeStaleLease || c.State.Epoch <= epoch {
		t.Fatalf("alice with the old session's epoch: %+v", c)
	}
	// Re-reading the state gives her the new epoch, and she may go on.
	var st struct{ Control sessionproto.Snapshot }
	_, b, _ := e.call("alice", "GET", "/api/state", "")
	if err := json.Unmarshal([]byte(b), &st); err != nil {
		t.Fatal(err)
	}
	if code, body = e.prompt("alice", "again", epochHeader, strconv.FormatUint(st.Control.Epoch, 10)); code != 200 {
		t.Fatalf("with the fresh epoch: %d %s", code, body)
	}
	e.waitIdle()
}

// Resume and fork move every client to another session: only the
// controller may do it, and afterwards a lease from before is stale.
func TestResumeAndForkMoveTheLeaseToANewEpoch(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", data)
	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	here := t.TempDir()
	old, err := session.New(dir, here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	q := llm.TextMessage(llm.RoleUser, "the old question")
	if _, err := old.Append(session.Event{Type: session.TypeUserMessage, Surface: true, Turn: 1, Message: &q}); err != nil {
		t.Fatal(err)
	}
	cur, err := session.New(dir, here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	prov := &gateProv{started: make(chan struct{}, 16)}
	ag := &agent.Agent{Provider: prov, Tools: tool.NewRegistry(), Model: "m", Log: cur, Root: here}
	cd := agent.NewCD(here)
	cd.Bind(tool.NewLocal(here), ag, cur)
	s := newServer(context.Background(), Options{Agent: ag, Session: cur, Provider: "fake", Cwd: here, CD: cd, ControlTTL: time.Minute})
	ts := httptest.NewServer(s.handler())
	defer ts.Close()
	e := &ctlEnv{t: t, s: s, ts: ts, prov: prov, sess: cur}

	if code, body, _ := e.call("alice", "POST", "/api/control", `{"action":"acquire"}`); code != 200 {
		t.Fatal(code, body)
	}
	epoch0 := s.controlView("alice").Epoch
	// bob cannot move the session out from under alice.
	if code, body := e.prompt("bob", "/resume 1"); conflictOf(t, code, body).Code != sessionproto.CodeLeaseHeld || s.sess().Meta.ID != cur.Meta.ID {
		t.Fatalf("bob moved the session: now on %s", s.sess().Meta.ID)
	}

	if code, body := e.prompt("alice", "/resume 1"); code != 200 {
		t.Fatalf("/resume: %d %s", code, body)
	}
	if s.sess().Meta.ID != old.Meta.ID {
		t.Fatalf("on %s, want the resumed %s", s.sess().Meta.ID, old.Meta.ID)
	}
	epoch1 := s.controlView("alice").Epoch
	if epoch1 <= epoch0 {
		t.Fatalf("epoch %d did not advance past %d on resume", epoch1, epoch0)
	}
	code, body := e.prompt("alice", "x", epochHeader, strconv.FormatUint(epoch0, 10))
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeStaleLease {
		t.Fatalf("pre-resume epoch: %+v", c)
	}

	// Fork continues in a copy: another move, another epoch.
	if code, body := e.prompt("alice", "/fork", epochHeader, strconv.FormatUint(epoch1, 10)); code != 200 {
		t.Fatalf("/fork: %d %s", code, body)
	}
	if id := s.sess().Meta.ID; id == old.Meta.ID || id == cur.Meta.ID {
		t.Fatalf("/fork stayed on %s", id)
	}
	if epoch2 := s.controlView("alice").Epoch; epoch2 <= epoch1 {
		t.Fatalf("epoch %d did not advance past %d on fork", epoch2, epoch1)
	}
}

// switchEnv is a server on session A with a second session B to move to,
// whose agent has a task tool so subagents can run.
type switchEnv struct {
	*ctlEnv
	a, b *session.Session
}

func newSwitchEnv(t *testing.T, prov llm.Provider) *switchEnv {
	t.Helper()
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("HOME", data)
	dir, err := session.Dir()
	if err != nil {
		t.Fatal(err)
	}
	here := t.TempDir()
	b, err := session.New(dir, here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	q := llm.TextMessage(llm.RoleUser, "session B's question")
	if _, err := b.Append(session.Event{Type: session.TypeUserMessage, Surface: true, Turn: 1, Message: &q}); err != nil {
		t.Fatal(err)
	}
	a, err := session.New(dir, here, "fake", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.EnsureWritten(); err != nil {
		t.Fatal(err)
	}
	ag := &agent.Agent{Provider: prov, Model: "m", Log: a, Root: here, AutoApprove: true}
	ag.Tools = tool.NewRegistry(tool.ReadFile{Root: here}, &agent.Task{Parent: ag})
	cd := agent.NewCD(here)
	cd.Bind(tool.NewLocal(here), ag, a)
	life, kill := context.WithCancel(context.Background())
	s := newServer(life, Options{Agent: ag, Session: a, Provider: "fake", Cwd: here, CD: cd, ControlTTL: time.Minute})
	ts := httptest.NewServer(s.handler())
	t.Cleanup(func() { kill(); ts.Close() })
	return &switchEnv{ctlEnv: &ctlEnv{t: t, s: s, ts: ts, sess: a, kill: kill}, a: a, b: b}
}

// holdProv answers every call "report" once release is closed, ignoring
// cancellation: a model call that was already in flight when its session
// was left, and finishes afterwards.
type holdProv struct {
	started chan struct{}
	release chan struct{}
}

func (h *holdProv) Name() string { return "fake" }
func (h *holdProv) Stream(_ context.Context, _ llm.Request, _ func(llm.Event)) (*llm.Response, error) {
	select {
	case h.started <- struct{}{}:
	default:
	}
	<-h.release
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "report"), StopReason: llm.StopEndTurn}, nil
}

// A subagent started in session A that ends after the server moved to B
// reports to A's log, and neither B's event stream, B's page state nor B's
// history hears of it.
func TestSubagentFinishingAfterTheSessionMovedReportsToItsOwnSession(t *testing.T) {
	prov := &holdProv{started: make(chan struct{}, 4), release: make(chan struct{})}
	e := newSwitchEnv(t, prov)
	t.Cleanup(func() {
		select {
		case <-prov.release:
		default:
			close(prov.release)
		}
	})

	if code, body := e.prompt("alice", "/agents new count the files"); code != 200 {
		t.Fatalf("/agents new: %d %s", code, body)
	}
	select {
	case <-prov.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the subagent never reached the model")
	}
	if code, body := e.prompt("alice", "/resume 1"); code != 200 || e.s.sess().Meta.ID != e.b.Meta.ID {
		t.Fatalf("/resume 1: %d %s, now on %s", code, body, e.s.sess().Meta.ID)
	}
	bBefore := len(e.b.Events())
	obs := e.open("watcher", "observer") // attaches to B

	close(prov.release) // the subagent finishes, in A's past
	deadline := time.Now().Add(5 * time.Second)
	var aLog []session.Event
	for ; time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		aLog = e.a.Events()
		if hasSubagentNotice(aLog) {
			break
		}
	}
	if !hasSubagentNotice(aLog) {
		t.Fatalf("session A's log never got the subagent's outcome: %d events", len(aLog))
	}
	time.Sleep(200 * time.Millisecond)
	if got := e.b.Events(); len(got) != bBefore {
		t.Fatalf("session B's log grew from %d to %d events with A's subagent", bBefore, len(got))
	}
	if hasSubagentNotice(e.b.Events()) {
		t.Fatal("A's subagent outcome is in B's log")
	}
	// Nothing about it on the page of B: not in the replayed stream, not in the entries.
	obs.cancel()
	for env := range obs.events {
		if env.Type == "entry" && env.Entry != nil && strings.Contains(env.Entry.Text, "count the files") && env.Entry.Kind != "user" {
			t.Fatalf("B's stream carried A's subagent: %+v", env.Entry)
		}
	}
	e.s.mu.Lock()
	for _, en := range e.s.entries {
		if strings.Contains(en.Text, "count the files") {
			e.s.mu.Unlock()
			t.Fatalf("B's page state holds A's subagent: %+v", en)
		}
	}
	e.s.mu.Unlock()
}

func hasSubagentNotice(evs []session.Event) bool {
	for _, ev := range evs {
		if ev.Type == session.TypeNotice && strings.HasPrefix(ev.Name, "subagent/") && strings.Contains(ev.Output, "count the files") {
			return true
		}
	}
	return false
}

// An approval, an undo or a cancel made under session A's lease is refused
// once the server is on B: it must not answer B's prompt, undo B's work or
// stop B's turn.
func TestStaleLeaseCannotApproveUndoOrCancelInTheNewSession(t *testing.T) {
	prov := &gateProv{started: make(chan struct{}, 4)}
	e := newSwitchEnv(t, prov)
	e.prov = prov
	if code, body, _ := e.call("alice", "POST", "/api/control", `{"action":"acquire"}`); code != 200 {
		t.Fatal(code, body)
	}
	epochA := e.s.controlView("alice").Epoch
	if code, body := e.prompt("alice", "/resume 1"); code != 200 {
		t.Fatalf("/resume 1: %d %s", code, body)
	}

	// B has a prompt waiting on a person and a turn in flight.
	pend := &permReq{call: agent.ToolCall{ID: "b-call", Name: "bash"}, reply: make(chan verdict, 1)}
	e.s.mu.Lock()
	e.s.pending = pend
	e.s.mu.Unlock()
	prov.hold()
	t.Cleanup(func() {
		prov.open()
		e.waitIdle()
		time.Sleep(200 * time.Millisecond) // the finished turn titles the session
	})
	if code, body := e.prompt("alice", "work in B", epochHeader, strconv.FormatUint(e.s.controlView("alice").Epoch, 10)); code != 200 {
		t.Fatalf("B's own turn: %d %s", code, body)
	}
	e.waitStarted()

	stale := epochHeader
	epoch := strconv.FormatUint(epochA, 10)
	for _, req := range []struct{ name, path, body string }{
		{"approval", "/api/permission", `{"decision":"allow","id":"b-call"}`},
		{"undo", "/api/command", `{"text":"/undo"}`},
		{"cancel", "/api/cancel", `{}`},
		{"prompt", "/api/prompt", `{"text":"from A"}`},
	} {
		code, body, _ := e.call("alice", "POST", req.path, req.body, stale, epoch)
		if c := conflictOf(t, code, body); c.Code != sessionproto.CodeStaleLease || c.State.Epoch <= epochA {
			t.Fatalf("%s under A's epoch: %+v", req.name, c)
		}
	}
	e.s.mu.Lock()
	still := e.s.pending == pend
	e.s.mu.Unlock()
	if !still || len(pend.reply) != 0 {
		t.Fatal("a stale approval reached B's pending prompt")
	}
	if busy, _ := e.s.busyQueued(); !busy {
		t.Fatal("a stale cancel stopped B's turn")
	}
	if e.s.sess().Meta.ID != e.b.Meta.ID {
		t.Fatal("the session moved")
	}
}

func TestShutdownTellsObservers(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	obs := e.open("watcher", "observer")
	obs.next(t, "hello", func(env envelope) bool { return env.Type == "hello" })
	e.kill()
	bye := obs.next(t, "bye", func(env envelope) bool { return env.Type == "bye" })
	if !strings.Contains(bye.Notice, "shutting down") {
		t.Fatalf("bye: %+v", bye)
	}
}

func TestProtocolVersionIsChecked(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	code, body, h := e.call("alice", "GET", "/api/state", "", protocolHeader, "99")
	if code != http.StatusBadRequest || !strings.Contains(body, sessionproto.CodeBadVersion) {
		t.Fatalf("future client: %d %s", code, body)
	}
	if h.Get(protocolHeader) != strconv.Itoa(sessionproto.Version) {
		t.Fatalf("server did not say what it speaks: %q", h.Get(protocolHeader))
	}
	if code, body, h = e.call("alice", "GET", "/api/state", "", protocolHeader, "1"); code != 200 || h.Get(protocolHeader) != "1" {
		t.Fatalf("matching client: %d %s", code, body)
	}
}

func TestQueuedPromptsKeepTheirOrder(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	e.prov.hold()
	for _, p := range []string{"one", "two", "three", "four"} {
		if code, body := e.prompt("alice", p); code != 200 {
			t.Fatalf("%s: %d %s", p, code, body)
		}
		if p == "one" {
			e.waitStarted()
		}
	}
	// The client that is not in control sees how many wait, not the prompts.
	code, body := e.prompt("bob", "five")
	if c := conflictOf(t, code, body); c.State.Queued != 3 {
		t.Fatalf("queued = %d, want 3", c.State.Queued)
	}
	e.prov.open()
	e.waitIdle()
	e.prov.mu.Lock()
	defer e.prov.mu.Unlock()
	var order []string
	for _, s := range e.prov.seen {
		switch s {
		case "one", "two", "three", "four":
			order = append(order, s)
		}
	}
	if strings.Join(order, ",") != "one,two,three,four" {
		t.Fatalf("turns ran as %v", order)
	}
}

func TestControlAPIReleaseAndAcquire(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	if code, body, _ := e.call("alice", "POST", "/api/control", `{"action":"acquire"}`); code != 200 {
		t.Fatal(code, body)
	}
	code, body, _ := e.call("bob", "POST", "/api/control", `{"action":"acquire"}`)
	if c := conflictOf(t, code, body); c.Code != sessionproto.CodeLeaseHeld {
		t.Fatalf("%+v", c)
	}
	if code, body, _ = e.call("bob", "POST", "/api/control", `{"action":"release"}`); code != 200 || strings.Contains(body, `"you":"controller"`) {
		t.Fatalf("a non-holder's release: %d %s", code, body)
	}
	if v := e.s.controlView("alice"); v.You != sessionproto.RoleController {
		t.Fatalf("bob's release took alice's lease: %+v", v)
	}
	if code, body, _ = e.call("alice", "POST", "/api/control", `{"action":"release"}`); code != 200 {
		t.Fatal(code, body)
	}
	if code, body, _ = e.call("bob", "POST", "/api/control", `{"action":"acquire"}`); code != 200 {
		t.Fatalf("after release: %d %s", code, body)
	}
	if code, _, _ = e.call("bob", "POST", "/api/control", `{"action":"bogus"}`); code != http.StatusBadRequest {
		t.Fatalf("bogus action = %d", code)
	}
}

func TestUnnamedClientsAreOneClient(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	if code, body := e.prompt("", "a"); code != 200 {
		t.Fatal(code, body)
	}
	e.waitIdle()
	if code, body := e.prompt("", "b"); code != 200 {
		t.Fatalf("a second unnamed request must not conflict with the first: %d %s", code, body)
	}
	e.waitIdle()
}

func TestPageGivesTheBrowserAnIdentity(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	res, err := http.Get(e.ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	var id string
	for _, c := range res.Cookies() {
		if c.Name == clientCookie {
			id = c.Value
		}
	}
	if cleanClient(id) == "" {
		t.Fatalf("page set no usable client cookie: %q", id)
	}
	// The cookie is what names the browser's later requests.
	req, _ := http.NewRequest("POST", e.ts.URL+"/api/control", strings.NewReader(`{"action":"acquire"}`))
	req.AddCookie(&http.Cookie{Name: clientCookie, Value: id})
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if v := e.s.controlView(id); v.You != sessionproto.RoleController {
		t.Fatalf("cookie client is not the controller: %+v", v)
	}
}

// A setting sent during a turn waits for the turn boundary, in order with
// the prompts around it, while a permission switch takes effect at once.
func TestSettingsWaitForTheTurnBoundaryInQueueOrder(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	ag := e.s.opts.Agent
	e.prov.hold()
	if code, body := e.prompt("alice", "one"); code != 200 {
		t.Fatal(code, body)
	}
	e.waitStarted()
	for _, text := range []string{"/effort high", "two", "/effort low", "three"} {
		if code, body := e.prompt("alice", text); code != 200 {
			t.Fatalf("%q: %d %s", text, code, body)
		}
	}
	if got := ag.Settings().Effort; got != "" {
		t.Fatalf("effort changed mid-turn to %q", got)
	}
	// A permission switch is for stopping or loosening the turn that is running now.
	if code, body := e.prompt("alice", "/yolo"); code != 200 || !ag.Settings().AutoApprove {
		t.Fatalf("/yolo mid-turn: %d %s, auto-approve %v", code, body, ag.Settings().AutoApprove)
	}

	// Record the effort each turn starts under.
	var mu sync.Mutex
	effortAt := map[string]string{}
	e.prov.mu.Lock()
	e.prov.onCall = func(text string) {
		mu.Lock()
		effortAt[text] = ag.Settings().Effort
		mu.Unlock()
	}
	e.prov.mu.Unlock()
	e.prov.open()
	e.waitIdle()
	mu.Lock()
	defer mu.Unlock()
	if effortAt["two"] != "high" || effortAt["three"] != "low" {
		t.Fatalf("turns ran under efforts %v, want two=high three=low", effortAt)
	}
	if got := ag.Settings().Effort; got != "low" {
		t.Fatalf("final effort %q", got)
	}
}

func TestPromptCannotImpersonateAQueuedSetting(t *testing.T) {
	e := newCtlEnv(t, time.Minute)
	if err := e.s.submitPlain(settingMark + "/effort high"); err == nil {
		t.Fatal("a prompt starting with the setting mark was accepted")
	}
}
