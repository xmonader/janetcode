// Package sdk runs janetcode from another Go program.
//
// It drives the janetcode binary over the Agent Client Protocol (the
// same stdio protocol an editor uses, janetcode --acp), so a program gets
// the whole agent — its tools, permission model, hooks, verification,
// memory and session files — exactly as the terminal has it,
// rather than a second, thinner copy of the loop. The binary is found on
// PATH unless Options.Binary names one.
//
//	res, err := sdk.Run(ctx, sdk.Options{Dir: repo, Mode: sdk.ModeAutoEdits}, "make the tests pass")
//	fmt.Println(res.Text, res.Verdict, res.Usage.OutputTokens)
//
// For more than one prompt, Start a Client, open a Session and call
// Prompt on it; the conversation carries over between prompts and the
// session is saved like any other, so janetcode --resume <id> reopens it.
package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
)

// Mode is what janetcode may do without asking. A call it would ask about
// goes to Options.Approve.
type Mode string

const (
	// ModeAsk asks before every edit and every command that is not
	// known to be safe. It is the default.
	ModeAsk Mode = ""
	// ModeAutoEdits applies file edits without asking; commands ask.
	ModeAutoEdits Mode = "auto-edits"
	// ModeAuto runs in-workspace edits and known-safe commands without
	// asking; anything else asks.
	ModeAuto Mode = "auto"
	// ModeReadOnly reads, searches and answers but changes nothing.
	ModeReadOnly Mode = "readonly"
	// ModeYolo runs everything without asking. The command guard still
	// refuses what it always refuses.
	ModeYolo Mode = "yolo"
)

func (m Mode) flag() (string, error) {
	switch m {
	case ModeAsk:
		return "", nil
	case ModeAutoEdits:
		return "--auto-edits", nil
	case ModeAuto:
		return "--auto", nil
	case ModeReadOnly:
		return "--readonly", nil
	case ModeYolo:
		return "--yolo", nil
	}
	return "", fmt.Errorf("sdk: unknown mode %q", string(m))
}

// Decision answers a permission request.
type Decision int

const (
	// Deny refuses this call; the model is told and carries on.
	Deny Decision = iota
	// AllowOnce lets this one call run.
	AllowOnce
	// AllowAlways lets this call and every call like it run for the
	// rest of the session.
	AllowAlways
)

// Permission is a call janetcode asks about before running it.
type Permission struct {
	SessionID string
	// ToolCallID matches the ToolCall in the events around it.
	ToolCallID string
	// Tool is the tool's name (bash, edit_file, …); Title is a one-line
	// description of the call, with any warning the harness attached.
	Tool  string
	Title string
	// Kind is the protocol's category: read, edit, delete, search,
	// execute, fetch, think or other.
	Kind  string
	Input json.RawMessage
	// Preview is the diff or command the call would apply, when there is one.
	Preview string
	// Paths are the files the call touches, when known.
	Paths []string
}

// EventKind names what an Event reports.
type EventKind string

const (
	// EventText is a piece of the answer, streamed as it is written.
	EventText EventKind = "text"
	// EventThinking is a piece of the model's reasoning.
	EventThinking EventKind = "thinking"
	// EventToolCall is a tool call starting; EventToolUpdate is its
	// progress and result (Tool.Status says which).
	EventToolCall   EventKind = "tool_call"
	EventToolUpdate EventKind = "tool_update"
)

// Event is one streamed update from a turn.
type Event struct {
	SessionID string
	Kind      EventKind
	Text      string
	Tool      *ToolCall
}

// ToolCall is a tool call as the turn reports it.
type ToolCall struct {
	ID    string
	Tool  string
	Title string
	Kind  string
	// Status is pending, in_progress, completed or failed.
	Status string
	Input  json.RawMessage
	// Output is the call's result text, once it has one.
	Output string
	Paths  []string
}

// Usage is a turn's token usage.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CacheReadTokens  int
	CacheWriteTokens int
	// CostUSD is what the provider reported the turn cost, or 0 when it
	// does not report one.
	CostUSD float64
}

// Result is what one prompt came to.
type Result struct {
	SessionID string
	// Text is the answer: every text chunk of the turn, in order.
	Text string
	// StopReason is end_turn, cancelled, max_turn_requests or refusal.
	StopReason string
	// Verdict is what the turn demonstrated, computed by the harness from
	// what it watched (tests run, checks passed), not from what the model
	// claimed. Empty when there was nothing to judge.
	Verdict   string
	Usage     Usage
	ToolCalls []ToolCall
}

// Options configure a Client.
type Options struct {
	// Binary is the janetcode executable; empty means "janetcode" on PATH.
	Binary string
	// Dir is the working directory sessions are opened in; empty means
	// the current directory.
	Dir string
	// Model and Provider override the configured ones.
	Model, Provider string
	// Mode is what may run without asking.
	Mode Mode
	// Approve answers what the mode leaves to ask. Nil denies every
	// request, so an unattended program never hangs on a question.
	Approve func(ctx context.Context, p Permission) Decision
	// OnEvent receives every streamed update, in order, on the goroutine
	// reading the connection: it must return promptly.
	OnEvent func(Event)
	// Env is added to the process's environment (KEY=value entries); the
	// provider's API key usually comes from here or the parent's own.
	Env []string
	// Args are extra command-line flags (--set k=v, --add-dir, …).
	Args []string
	// Stderr receives janetcode's diagnostics; nil discards them.
	Stderr io.Writer
}

// Client is one running janetcode process. Its methods are safe for
// concurrent use; prompts on one session run one at a time.
type Client struct {
	opts   Options
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	wmu    sync.Mutex
	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[int64]chan rpcMsg
	turns    map[string]*turnState
	closed   bool
	closeErr error
	done     chan struct{}
	// Agent is what the process reported about itself.
	Agent struct {
		Name, Version string
	}
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcErr) Error() string { return fmt.Sprintf("janetcode: %s (code %d)", e.Message, e.Code) }

// ErrClosed is returned by calls on a Client whose process has ended.
var ErrClosed = errors.New("sdk: the janetcode process has ended")

// Start launches janetcode and completes the protocol handshake.
func Start(ctx context.Context, opts Options) (*Client, error) {
	bin := opts.Binary
	if bin == "" {
		bin = "janetcode"
	}
	// The process runs in Dir, so a relative path to the binary is
	// resolved here, against the caller's directory, not there.
	if strings.ContainsRune(bin, os.PathSeparator) || strings.ContainsRune(bin, '/') {
		if abs, err := filepath.Abs(bin); err == nil {
			bin = abs
		}
	}
	if opts.Dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		opts.Dir = wd
	}
	// The session's directory is sent to a process already running in
	// Dir; a relative one would be read twice over (dir/dir).
	if abs, err := filepath.Abs(opts.Dir); err == nil {
		opts.Dir = abs
	}
	args := []string{"--acp"}
	mode, err := opts.Mode.flag()
	if err != nil {
		return nil, err
	}
	if mode != "" {
		args = append(args, mode)
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.Provider != "" {
		args = append(args, "--provider", opts.Provider)
	}
	args = append(args, opts.Args...)
	cmd := exec.Command(bin, args...)
	cmd.Dir = opts.Dir
	cmd.Env = append(os.Environ(), opts.Env...)
	if opts.Stderr != nil {
		cmd.Stderr = opts.Stderr
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("sdk: start %s: %w", bin, err)
	}
	c := &Client{opts: opts, cmd: cmd, stdin: stdin, pending: map[int64]chan rpcMsg{}, turns: map[string]*turnState{}, done: make(chan struct{})}
	go c.readLoop(stdout)
	var init struct {
		AgentInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	if err := c.call(ctx, "initialize", map[string]any{
		"protocolVersion":    1,
		"clientCapabilities": map[string]any{"fs": map[string]any{}},
		"clientInfo":         map[string]any{"name": "janetcode-sdk", "version": "1"},
	}, &init); err != nil {
		c.Close()
		return nil, err
	}
	c.Agent.Name, c.Agent.Version = init.AgentInfo.Name, init.AgentInfo.Version
	return c, nil
}

// Close ends the process. Sessions are already saved on disk.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		<-c.done
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	c.stdin.Close()
	select {
	case <-c.done:
	case <-waitTimeout(c.done):
		_ = c.cmd.Process.Kill()
		<-c.done
	}
	return nil
}

// Session is one conversation.
type Session struct {
	c  *Client
	ID string
	mu sync.Mutex
}

// NewSession opens a conversation in the client's directory.
func (c *Client) NewSession(ctx context.Context) (*Session, error) {
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := c.call(ctx, "session/new", map[string]any{"cwd": c.opts.Dir, "mcpServers": []any{}}, &res); err != nil {
		return nil, err
	}
	return &Session{c: c, ID: res.SessionID}, nil
}

// LoadSession reopens a saved conversation by id.
func (c *Client) LoadSession(ctx context.Context, id string) (*Session, error) {
	if err := c.call(ctx, "session/load", map[string]any{"sessionId": id, "cwd": c.opts.Dir, "mcpServers": []any{}}, nil); err != nil {
		return nil, err
	}
	return &Session{c: c, ID: id}, nil
}

// Image is an image attached to a prompt.
type Image struct {
	MIME string // image/png, image/jpeg, …
	Data []byte
}

type turnState struct {
	text  strings.Builder
	tools map[string]*ToolCall
	order []string
}

// Prompt sends one message and waits for the turn to end. Cancelling ctx
// cancels the turn; the result then has StopReason "cancelled".
func (s *Session) Prompt(ctx context.Context, text string, images ...Image) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.c
	st := &turnState{tools: map[string]*ToolCall{}}
	c.mu.Lock()
	c.turns[s.ID] = st
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.turns, s.ID)
		c.mu.Unlock()
	}()
	blocks := []map[string]any{{"type": "text", "text": text}}
	for _, img := range images {
		blocks = append(blocks, map[string]any{"type": "image", "mimeType": img.MIME, "data": encodeBase64(img.Data)})
	}
	var res struct {
		StopReason string `json:"stopReason"`
		Meta       struct {
			Janetcode struct {
				Verdict string `json:"verdict"`
				Usage   struct {
					InputTokens      int     `json:"inputTokens"`
					OutputTokens     int     `json:"outputTokens"`
					CacheReadTokens  int     `json:"cacheReadTokens"`
					CacheWriteTokens int     `json:"cacheWriteTokens"`
					CostUSD          float64 `json:"costUsd"`
				} `json:"usage"`
			} `json:"janetcode"`
		} `json:"_meta"`
	}
	// The turn is cancelled over the protocol, not by abandoning the
	// call: the agent then stops cleanly and answers "cancelled".
	stop := context.AfterFunc(ctx, func() {
		_ = c.notify("session/cancel", map[string]any{"sessionId": s.ID})
	})
	defer stop()
	err := c.call(context.WithoutCancel(ctx), "session/prompt", map[string]any{"sessionId": s.ID, "prompt": blocks}, &res)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	out := &Result{SessionID: s.ID, Text: st.text.String(), StopReason: res.StopReason, Verdict: res.Meta.Janetcode.Verdict}
	for _, id := range st.order {
		out.ToolCalls = append(out.ToolCalls, *st.tools[id])
	}
	c.mu.Unlock()
	u := res.Meta.Janetcode.Usage
	out.Usage = Usage{InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheReadTokens: u.CacheReadTokens, CacheWriteTokens: u.CacheWriteTokens, CostUSD: u.CostUSD}
	return out, nil
}

// Run is the one-shot form: start janetcode, run one prompt in a new
// session, and close it again.
func Run(ctx context.Context, opts Options, prompt string) (*Result, error) {
	c, err := Start(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	s, err := c.NewSession(ctx)
	if err != nil {
		return nil, err
	}
	return s.Prompt(ctx, prompt)
}

// --- protocol ---

func (c *Client) send(m rpcMsg) error {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	return err
}

func (c *Client) call(ctx context.Context, method string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := c.nextID.Add(1)
	ch := make(chan rpcMsg, 1)
	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return c.exitErr()
	}
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.send(rpcMsg{ID: json.RawMessage(fmt.Sprint(id)), Method: method, Params: raw}); err != nil {
		c.drop(id)
		return fmt.Errorf("sdk: %s: %w", method, err)
	}
	select {
	case m, ok := <-ch:
		if !ok {
			return c.exitErr()
		}
		if m.Error != nil {
			return m.Error
		}
		if out != nil && len(m.Result) > 0 {
			return json.Unmarshal(m.Result, out)
		}
		return nil
	case <-ctx.Done():
		c.drop(id)
		return ctx.Err()
	}
}

func (c *Client) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(rpcMsg{Method: method, Params: raw})
}

func (c *Client) drop(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) exitErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closeErr != nil {
		return fmt.Errorf("%w: %v", ErrClosed, c.closeErr)
	}
	return ErrClosed
}

func (c *Client) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	for sc.Scan() {
		var m rpcMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		switch {
		case m.Method != "" && len(m.ID) > 0:
			go c.answer(m)
		case m.Method == "session/update":
			c.update(m.Params)
		case m.Method == "" && len(m.ID) > 0:
			var id int64
			if json.Unmarshal(m.ID, &id) != nil {
				continue
			}
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}
	err := c.cmd.Wait()
	c.mu.Lock()
	if err == nil {
		err = sc.Err()
	}
	c.closeErr = err
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
	c.pending = nil
	c.mu.Unlock()
	close(c.done)
}

// answer replies to a request from janetcode: a permission question, or
// a client capability this SDK does not offer.
func (c *Client) answer(m rpcMsg) {
	reply := rpcMsg{ID: m.ID}
	switch m.Method {
	case "session/request_permission":
		var p struct {
			SessionID string `json:"sessionId"`
			ToolCall  struct {
				ToolCallID string          `json:"toolCallId"`
				Title      string          `json:"title"`
				Kind       string          `json:"kind"`
				RawInput   json.RawMessage `json:"rawInput"`
				Content    []toolContent   `json:"content"`
				Locations  []struct {
					Path string `json:"path"`
				} `json:"locations"`
			} `json:"toolCall"`
		}
		_ = json.Unmarshal(m.Params, &p)
		perm := Permission{SessionID: p.SessionID, ToolCallID: p.ToolCall.ToolCallID, Title: p.ToolCall.Title, Kind: p.ToolCall.Kind, Input: p.ToolCall.RawInput}
		perm.Tool, _, _ = strings.Cut(p.ToolCall.Title, " ")
		for _, tc := range p.ToolCall.Content {
			if tc.Content != nil && tc.Content.Text != "" {
				perm.Preview = tc.Content.Text
			}
		}
		for _, l := range p.ToolCall.Locations {
			perm.Paths = append(perm.Paths, l.Path)
		}
		d := Deny
		if c.opts.Approve != nil {
			d = c.opts.Approve(context.Background(), perm)
		}
		opt := "reject_once"
		switch d {
		case AllowOnce:
			opt = "allow_once"
		case AllowAlways:
			opt = "allow_always"
		}
		reply.Result, _ = json.Marshal(map[string]any{"outcome": map[string]any{"outcome": "selected", "optionId": opt}})
	default:
		reply.Error = &rpcErr{Code: -32601, Message: "not offered by the janetcode SDK: " + m.Method}
	}
	_ = c.send(reply)
}

type toolContent struct {
	Type    string `json:"type"`
	Content *struct {
		Text string `json:"text"`
	} `json:"content"`
}

// update folds one session/update into the running turn and hands it to
// OnEvent.
func (c *Client) update(raw json.RawMessage) {
	var n struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			SessionUpdate string `json:"sessionUpdate"`
			Content       json.RawMessage
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
			RawInput      json.RawMessage `json:"rawInput"`
			Locations     []struct {
				Path string `json:"path"`
			} `json:"locations"`
		} `json:"update"`
	}
	if json.Unmarshal(raw, &n) != nil {
		return
	}
	u := n.Update
	ev := Event{SessionID: n.SessionID}
	c.mu.Lock()
	st := c.turns[n.SessionID]
	switch u.SessionUpdate {
	case "agent_message_chunk", "agent_thought_chunk":
		var cb struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(u.Content, &cb)
		ev.Kind, ev.Text = EventText, cb.Text
		if u.SessionUpdate == "agent_thought_chunk" {
			ev.Kind = EventThinking
		} else if st != nil {
			st.text.WriteString(cb.Text)
		}
	case "tool_call", "tool_call_update":
		ev.Kind = EventToolCall
		if u.SessionUpdate == "tool_call_update" {
			ev.Kind = EventToolUpdate
		}
		var tc *ToolCall
		if st != nil {
			tc = st.tools[u.ToolCallID]
			if tc == nil {
				tc = &ToolCall{ID: u.ToolCallID}
				st.tools[u.ToolCallID] = tc
				st.order = append(st.order, u.ToolCallID)
			}
		} else {
			tc = &ToolCall{ID: u.ToolCallID}
		}
		if u.Title != "" {
			tc.Title = u.Title
			tc.Tool, _, _ = strings.Cut(u.Title, " ")
		}
		if u.Kind != "" {
			tc.Kind = u.Kind
		}
		if u.Status != "" {
			tc.Status = u.Status
		}
		if len(u.RawInput) > 0 {
			tc.Input = u.RawInput
		}
		for _, l := range u.Locations {
			tc.Paths = append(tc.Paths, l.Path)
		}
		var content []toolContent
		if json.Unmarshal(u.Content, &content) == nil {
			for _, x := range content {
				if x.Content != nil && x.Content.Text != "" {
					tc.Output = x.Content.Text
				}
			}
		}
		cp := *tc
		ev.Tool = &cp
	default:
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	if c.opts.OnEvent != nil {
		c.opts.OnEvent(ev)
	}
}
