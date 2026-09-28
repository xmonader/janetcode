package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// client drives a Server over pipes the way an editor would: one reader
// goroutine answers the agent's permission requests and hands everything
// else to the test.
type client struct {
	t       *testing.T
	in      *io.PipeWriter
	frames  chan message
	next    int
	permMu  sync.Mutex
	permAns string // option id to answer permission requests with
	asked   []requestPermissionParams
}

func newClient(t *testing.T, o Options) (*client, func()) {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	o.In, o.Out, o.Log = inR, outW, io.Discard
	srv := New(o)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx) }()
	c := &client{t: t, in: inW, frames: make(chan message, 256), permAns: optAllowOnce}
	go c.readLoop(outR)
	return c, func() {
		cancel()
		inW.Close()
		outW.Close()
	}
}

// readLoop answers session/request_permission itself — the agent blocks on
// it mid-turn — and queues every other frame for the test.
func (c *client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue
		}
		if m.Method == "session/request_permission" {
			var p requestPermissionParams
			_ = json.Unmarshal(m.Params, &p)
			c.permMu.Lock()
			c.asked = append(c.asked, p)
			ans := c.permAns
			c.permMu.Unlock()
			out := requestPermissionResult{Outcome: permissionOutcome{Outcome: "selected", OptionID: ans}}
			raw, _ := json.Marshal(out)
			c.send(message{ID: m.ID, Result: raw})
			continue
		}
		select {
		case c.frames <- m:
		default:
		}
	}
}

func (c *client) permissions() []requestPermissionParams {
	c.permMu.Lock()
	defer c.permMu.Unlock()
	return append([]requestPermissionParams(nil), c.asked...)
}

func (c *client) send(m message) {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		c.t.Error(err)
		return
	}
	if strings.Contains(string(b), "\n") {
		c.t.Errorf("frame contains a newline: %s", b)
		return
	}
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Error(err)
	}
}

func (c *client) call(method string, params any) {
	c.t.Helper()
	c.next++
	raw, _ := json.Marshal(params)
	c.send(message{ID: json.RawMessage(strconv.Itoa(c.next)), Method: method, Params: raw})
}

func (c *client) notify(method string, params any) {
	raw, _ := json.Marshal(params)
	c.send(message{Method: method, Params: raw})
}

// read returns the next queued frame, failing on timeout.
func (c *client) read() message {
	c.t.Helper()
	select {
	case m := <-c.frames:
		return m
	case <-time.After(10 * time.Second):
		c.t.Fatal("timed out waiting for a frame")
		return message{}
	}
}

// readUntil returns the first frame matching want, with the ones before it.
func (c *client) readUntil(want func(message) bool) (message, []message) {
	c.t.Helper()
	var seen []message
	for i := 0; i < 200; i++ {
		m := c.read()
		if want(m) {
			return m, seen
		}
		seen = append(seen, m)
	}
	c.t.Fatalf("no matching frame in %d", len(seen))
	return message{}, seen
}

func isResponse(m message) bool { return m.Method == "" && len(m.ID) > 0 }

// scripted answers one text reply, then a tool call, then a reply.
type scripted struct {
	mu    sync.Mutex
	calls int
}

func (s *scripted) Name() string { return "fake" }
func (s *scripted) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	if emit == nil {
		emit = func(llm.Event) {}
	}
	if n == 1 {
		b := llm.Block{Type: llm.BlockToolUse, ID: "t1", Name: "write_file", Input: json.RawMessage(`{"path":"out.txt","content":"hi"}`)}
		emit(llm.Event{Type: llm.EventToolUseStart, Block: &b})
		emit(llm.Event{Type: llm.EventToolUseEnd, Block: &b})
		return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{b}}, StopReason: llm.StopToolUse}, nil
	}
	emit(llm.Event{Type: llm.EventTextDelta, Text: "done"})
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "done"), StopReason: llm.StopEndTurn}, nil
}

func testOptions(t *testing.T) (Options, string) {
	t.Helper()
	root := t.TempDir()
	dir := t.TempDir()
	return Options{
		Version: "test",
		NewAgent: func(cwd, resume string) (TurnRunner, *session.Session, error) {
			var (
				s    *session.Session
				hist []llm.Message
				err  error
			)
			if resume != "" {
				s, hist, err = session.Open(dir, resume)
			} else {
				s, err = session.New(dir, cwd, "fake", "m")
			}
			if err != nil {
				return nil, nil, err
			}
			ag := &agent.Agent{
				Provider: &scripted{}, Model: "m", System: "sys", Log: s, Messages: hist,
				Tools: tool.NewRegistry(tool.WriteFile{Root: cwd}, tool.ReadFile{Root: cwd}),
			}
			return Wrap(ag), s, nil
		},
	}, root
}

func TestInitializeNewSessionAndPrompt(t *testing.T) {
	o, root := testOptions(t)
	c, stop := newClient(t, o)
	defer stop()

	c.call("initialize", initializeParams{ProtocolVersion: 1, ClientInfo: &implInfo{Name: "zed", Version: "1"}})
	m := c.read()
	var init initializeResult
	if err := json.Unmarshal(m.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.ProtocolVersion != 1 || !init.AgentCapabilities.LoadSession || init.AgentInfo.Name != "janetcode" {
		t.Fatalf("initialize = %+v", init)
	}

	c.call("session/new", newSessionParams{Cwd: root})
	var ns newSessionResult
	if err := json.Unmarshal(c.read().Result, &ns); err != nil {
		t.Fatal(err)
	}
	if ns.SessionID == "" {
		t.Fatal("no session id")
	}

	c.call("session/prompt", promptParams{SessionID: ns.SessionID, Prompt: []contentBlock{{Type: "text", Text: "write out.txt"}}})
	final, updates := c.readUntil(isResponse)
	var pr promptResult
	if err := json.Unmarshal(final.Result, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.StopReason != stopEndTurn {
		t.Fatalf("stopReason = %q", pr.StopReason)
	}

	// The turn must have been streamed: a tool_call, its completion, and text.
	var kinds []string
	sawWrite, sawText, sawPath := false, false, false
	for _, u := range updates {
		if u.Method != "session/update" {
			continue
		}
		var n struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(u.Params, &n); err != nil {
			t.Fatal(err)
		}
		if n.SessionID != ns.SessionID {
			t.Fatalf("update for %q, want %q", n.SessionID, ns.SessionID)
		}
		var probe struct {
			SessionUpdate string `json:"sessionUpdate"`
			Kind          string `json:"kind"`
			Status        string `json:"status"`
			Title         string `json:"title"`
		}
		_ = json.Unmarshal(n.Update, &probe)
		kinds = append(kinds, probe.SessionUpdate+"/"+probe.Status)
		if probe.SessionUpdate == "tool_call" {
			if probe.Kind != kindEdit {
				t.Fatalf("write_file kind = %q, want %q", probe.Kind, kindEdit)
			}
			sawWrite = true
		}
		// The input streams in, so the path shows up once it is complete.
		if strings.Contains(probe.Title, "out.txt") {
			sawPath = true
		}
		if probe.SessionUpdate == "agent_message_chunk" {
			sawText = true
		}
	}
	if !sawWrite || !sawText || !sawPath {
		t.Fatalf("updates = %v (write=%v text=%v path=%v)", kinds, sawWrite, sawText, sawPath)
	}
	if !strings.Contains(strings.Join(kinds, " "), "tool_call_update/completed") {
		t.Fatalf("no completion update: %v", kinds)
	}
	// The write went through the editor's permission prompt.
	perms := c.permissions()
	if len(perms) != 1 || perms[0].SessionID != ns.SessionID || perms[0].ToolCall.Kind != kindEdit {
		t.Fatalf("permissions = %+v", perms)
	}
	if len(perms[0].Options) != 3 || perms[0].Options[0].OptionID != optAllowOnce {
		t.Fatalf("options = %+v", perms[0].Options)
	}
}

// A denied call comes back to the model as a denial, and the turn still
// finishes rather than hanging.
func TestPermissionDenialEndsTheCallNotTheTurn(t *testing.T) {
	o, root := testOptions(t)
	c, stop := newClient(t, o)
	defer stop()
	c.permAns = optRejectOnce
	c.call("session/new", newSessionParams{Cwd: root})
	var ns newSessionResult
	if err := json.Unmarshal(c.read().Result, &ns); err != nil {
		t.Fatal(err)
	}
	c.call("session/prompt", promptParams{SessionID: ns.SessionID, Prompt: []contentBlock{textBlock("write out.txt")}})
	final, _ := c.readUntil(isResponse)
	var pr promptResult
	if err := json.Unmarshal(final.Result, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.StopReason != stopEndTurn {
		t.Fatalf("stopReason = %q", pr.StopReason)
	}
	if _, err := os.Stat(filepath.Join(root, "out.txt")); err == nil {
		t.Fatal("the file was written despite the denial")
	}
}

func TestUnknownMethodAndBadSession(t *testing.T) {
	o, _ := testOptions(t)
	c, stop := newClient(t, o)
	defer stop()
	c.call("nope/there", struct{}{})
	m := c.read()
	if m.Error == nil || m.Error.Code != codeMethodNotFound {
		t.Fatalf("error = %+v", m.Error)
	}
	c.call("session/prompt", promptParams{SessionID: "missing", Prompt: []contentBlock{{Type: "text", Text: "x"}}})
	m = c.read()
	if m.Error == nil || m.Error.Code != codeInvalidParams {
		t.Fatalf("error = %+v", m.Error)
	}
}

func TestPromptBlockConversion(t *testing.T) {
	blocks := toBlocks([]contentBlock{
		{Type: "text", Text: "hello"},
		{Type: "image", Data: "AAAA", MimeType: "image/png"},
		{Type: "resource", Resource: &embeddedResource{URI: "file:///a.go", Text: "package a"}},
		{Type: "resource_link", URI: "file:///b.go"},
		{Type: "audio", Data: "zzz"},
	})
	if len(blocks) != 4 {
		t.Fatalf("blocks = %+v", blocks)
	}
	if blocks[1].Type != llm.BlockImage || blocks[1].MediaType != "image/png" {
		t.Fatalf("image = %+v", blocks[1])
	}
	if !strings.Contains(blocks[2].Text, "package a") || !strings.Contains(blocks[2].Text, "file:///a.go") {
		t.Fatalf("resource = %q", blocks[2].Text)
	}
	if blocks[3].Text != "@file:///b.go" {
		t.Fatalf("link = %q", blocks[3].Text)
	}
}

func TestToolKindsAndTitles(t *testing.T) {
	for name, want := range map[string]string{
		"read_file": kindRead, "edit_file": kindEdit, "bash": kindExecute,
		"grep": kindSearch, "task": kindThink, "fetch_url": kindFetch, "mystery": kindOther,
	} {
		if got := toolKind(name); got != want {
			t.Fatalf("%s kind = %q, want %q", name, got, want)
		}
	}
	u := toolCallUpdate("tool_call", agent.ToolCall{ID: "t1", Name: "bash", Input: json.RawMessage(`{"command":"go test ./...\nsecond line"}`)}, statusPending, "")
	if u.Title != "bash go test ./..." {
		t.Fatalf("title = %q", u.Title)
	}
	if len(u.Locations) != 0 {
		t.Fatalf("bash has no path location: %+v", u.Locations)
	}
	u = toolCallUpdate("tool_call", agent.ToolCall{ID: "t2", Name: "read_file", Input: json.RawMessage(`{"path":"main.go"}`)}, statusPending, "")
	if len(u.Locations) != 1 || u.Locations[0].Path != "main.go" {
		t.Fatalf("locations = %+v", u.Locations)
	}
}

// fakeRunner is a TurnRunner with no model, no tools and no history
// behind it: it records the prompts it is given and answers each with one
// line of text. That is the whole of what the ACP server asks of an
// agent, which is what the interface is for — a test about the protocol
// need not stand up a provider and a tool registry to get one turn out.
type fakeRunner struct {
	mu       sync.Mutex
	prompts  [][]llm.Block
	history  []llm.Message
	approver agent.Approver
	reply    string
}

func (f *fakeRunner) RunBlocks(ctx context.Context, blocks []llm.Block, emit func(agent.Event)) error {
	f.mu.Lock()
	f.prompts = append(f.prompts, blocks)
	f.history = append(f.history, llm.Message{Role: llm.RoleUser, Blocks: blocks})
	f.history = append(f.history, llm.TextMessage(llm.RoleAssistant, f.reply))
	reply := f.reply
	f.mu.Unlock()
	emit(agent.Event{Kind: agent.EvText, Text: reply})
	emit(agent.Event{Kind: agent.EvTurnDone})
	return ctx.Err()
}

func (f *fakeRunner) MessagesSnapshot() []llm.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]llm.Message(nil), f.history...)
}

func (f *fakeRunner) SetApprover(ap agent.Approver) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.approver = ap
}

// The editor says which directory a session is for, and it is not
// necessarily the one the binary was launched from. Every tool is rooted
// at that directory, so the factory has to be told which one it is.
func TestSessionCwdReachesTheAgent(t *testing.T) {
	dir := t.TempDir()
	var gotCwd []string
	o := Options{
		Version: "test",
		NewAgent: func(cwd, resume string) (TurnRunner, *session.Session, error) {
			gotCwd = append(gotCwd, cwd)
			s, err := session.New(dir, cwd, "fake", "m")
			if err != nil {
				return nil, nil, err
			}
			return &fakeRunner{reply: "ok"}, s, nil
		},
	}
	c, stop := newClient(t, o)
	defer stop()

	workspace := t.TempDir()
	c.call("session/new", newSessionParams{Cwd: workspace})
	var ns newSessionResult
	if err := json.Unmarshal(c.read().Result, &ns); err != nil {
		t.Fatal(err)
	}
	if len(gotCwd) != 1 || gotCwd[0] != workspace {
		t.Fatalf("the factory was given %v, want the workspace %q", gotCwd, workspace)
	}
	// A second workspace gets its own session, rooted at its own directory.
	other := t.TempDir()
	c.call("session/new", newSessionParams{Cwd: other})
	var ns2 newSessionResult
	if err := json.Unmarshal(c.read().Result, &ns2); err != nil {
		t.Fatal(err)
	}
	if len(gotCwd) != 2 || gotCwd[1] != other {
		t.Fatalf("second session got %v", gotCwd)
	}
	if ns2.SessionID == ns.SessionID {
		t.Fatal("two workspaces shared one session")
	}
	// And the fake really is enough to drive a turn: the prompt reaches
	// it and its answer comes back as a message chunk.
	c.call("session/prompt", promptParams{SessionID: ns.SessionID, Prompt: []contentBlock{textBlock("hello")}})
	final, updates := c.readUntil(isResponse)
	var pr promptResult
	if err := json.Unmarshal(final.Result, &pr); err != nil {
		t.Fatal(err)
	}
	if pr.StopReason != stopEndTurn {
		t.Fatalf("stopReason = %q", pr.StopReason)
	}
	said := false
	for _, u := range updates {
		if u.Method == "session/update" && strings.Contains(string(u.Params), `"text":"ok"`) {
			said = true
		}
	}
	if !said {
		t.Fatalf("the fake's answer was not streamed: %+v", updates)
	}
	// A session without a cwd is refused rather than silently rooted at
	// whatever directory the process happens to be in.
	c.call("session/new", newSessionParams{})
	if m := c.read(); m.Error == nil {
		t.Fatal("session/new with no cwd was accepted")
	}
}

// An editor cancels: the user presses stop, or closes the panel while a
// turn is running. The prompt must come back as cancelled rather than
// hanging or erroring, and the session must still be usable afterwards.
func TestCancelEndsTheTurnAndLeavesTheSessionUsable(t *testing.T) {
	dir := t.TempDir()
	release := make(chan struct{})
	slow := &blockingProvider{release: release}
	o := Options{
		Version: "test",
		NewAgent: func(cwd, resume string) (TurnRunner, *session.Session, error) {
			s, err := session.New(dir, cwd, "fake", "m")
			if err != nil {
				return nil, nil, err
			}
			return Wrap(&agent.Agent{Provider: slow, Model: "m", Log: s, Tools: tool.NewRegistry()}), s, nil
		},
	}
	slow.started.Add(1)
	c, stop := newClient(t, o)
	defer stop()
	c.call("session/new", newSessionParams{Cwd: t.TempDir()})
	var ns newSessionResult
	if err := json.Unmarshal(c.read().Result, &ns); err != nil {
		t.Fatal(err)
	}
	c.call("session/prompt", promptParams{SessionID: ns.SessionID, Prompt: []contentBlock{textBlock("think about it")}})
	slow.started.Wait()
	c.notify("session/cancel", cancelParams(ns))
	final, _ := c.readUntil(isResponse)
	var pr promptResult
	if err := json.Unmarshal(final.Result, &pr); err != nil {
		t.Fatalf("cancel produced %v (%s)", err, final.Error)
	}
	if pr.StopReason != stopCancelled {
		t.Fatalf("stopReason = %q, want %q", pr.StopReason, stopCancelled)
	}
	// The session survives: a second prompt still runs.
	close(release)
	slow.answer = true
	c.call("session/prompt", promptParams{SessionID: ns.SessionID, Prompt: []contentBlock{textBlock("again")}})
	final, _ = c.readUntil(isResponse)
	if err := json.Unmarshal(final.Result, &pr); err != nil || pr.StopReason != stopEndTurn {
		t.Fatalf("second prompt after cancel: %v %q", err, pr.StopReason)
	}
}

// blockingProvider holds the first call open until released, so a cancel
// can arrive mid-turn the way it does from an editor.
type blockingProvider struct {
	release chan struct{}
	started sync.WaitGroup
	once    sync.Once
	answer  bool
}

func (b *blockingProvider) Name() string { return "blocking" }
func (b *blockingProvider) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if b.answer {
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "ok"), StopReason: llm.StopEndTurn}, nil
	}
	b.once.Do(func() { b.started.Done() })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.release:
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "late"), StopReason: llm.StopEndTurn}, nil
	}
}

// A frame that is not JSON, and a request for a session that does not
// exist, must not take the connection down: an editor that sends one bad
// line should still be able to keep working.
func TestBadInputDoesNotKillTheConnection(t *testing.T) {
	o, root := testOptions(t)
	c, stop := newClient(t, o)
	defer stop()
	if _, err := c.in.Write([]byte("this is not json\n")); err != nil {
		t.Fatal(err)
	}
	m := c.read()
	if m.Error == nil || m.Error.Code != codeParse {
		t.Fatalf("parse error = %+v", m.Error)
	}
	// JSON-RPC 2.0: when the request's id could not be read, the error
	// response's id MUST be null. Omitting it made the reply look like a
	// notification with no method, which a strict client rejects.
	if string(m.ID) != "null" {
		t.Fatalf("parse error id = %q, want null", m.ID)
	}
	// Cancelling an unknown session is a notification, so it is logged
	// and ignored rather than answered or fatal.
	c.notify("session/cancel", cancelParams{SessionID: "nope"})
	// The connection still works.
	c.call("session/new", newSessionParams{Cwd: root})
	var ns newSessionResult
	if err := json.Unmarshal(c.read().Result, &ns); err != nil || ns.SessionID == "" {
		t.Fatalf("the connection did not survive: %v", err)
	}
}

// The stop reasons are the protocol's, not ours. "completed" reads like
// the right word and is not one of them; sending it had every finished
// turn rejected by a validating client, which is how it was found. This
// pins the set against the published schema.
func TestStopReasonsAreTheProtocolsOwn(t *testing.T) {
	allowed := map[string]bool{
		"end_turn": true, "max_tokens": true, "max_turn_requests": true,
		"refusal": true, "cancelled": true,
	}
	for _, r := range []string{stopEndTurn, stopCancelled, stopRefusal, stopMaxTurns, stopMaxTokens} {
		if !allowed[r] {
			t.Fatalf("%q is not a protocol stop reason", r)
		}
	}
	if stopEndTurn != "end_turn" {
		t.Fatalf("a finished turn reports %q; the protocol calls it end_turn", stopEndTurn)
	}
	// Permission option kinds are likewise the protocol's own.
	for _, k := range []string{optAllowOnce, optAllowAlways, optRejectOnce, optRejectAlways} {
		switch k {
		case "allow_once", "allow_always", "reject_once", "reject_always":
		default:
			t.Fatalf("%q is not a protocol permission kind", k)
		}
	}
	// And so are the tool kinds this adapter emits.
	for _, k := range []string{kindRead, kindEdit, kindDelete, kindSearch, kindExecute, kindThink, kindFetch, kindOther} {
		switch k {
		case "read", "edit", "delete", "move", "search", "execute", "think", "fetch", "switch_mode", "other":
		default:
			t.Fatalf("%q is not a protocol tool kind", k)
		}
	}
}

// textBlock is a plain text prompt block, as a client sends one.
func textBlock(s string) contentBlock { return contentBlock{Type: "text", Text: s} }
