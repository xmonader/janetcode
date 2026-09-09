// Package acp serves janetcode over the Agent Client Protocol, so any ACP
// editor can drive it: the editor launches janetcode as a subprocess,
// writes JSON-RPC to its stdin, and reads streamed updates and permission
// requests from its stdout. Nothing but ACP messages may reach stdout, so
// every other output goes to stderr.
package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// Options configures the server.
type Options struct {
	// NewAgent builds an agent and its session for a working directory.
	// resume names an existing session to reopen, or is empty for a new
	// one; the returned id is what the editor gets back.
	NewAgent func(cwd, resume string) (*agent.Agent, *session.Session, error)
	// Close, when set, releases what NewAgent allocated for a session.
	Close   func(*agent.Agent)
	In      io.Reader
	Out     io.Writer
	Log     io.Writer
	Version string
}

// Server is one ACP connection over the option's streams.
type Server struct {
	opts Options
	c    *conn

	mu       sync.Mutex
	sessions map[string]*acpSession
	client   implInfo
}

type acpSession struct {
	id    string
	cwd   string
	agent *agent.Agent
	sess  *session.Session

	mu     sync.Mutex // one turn at a time
	cancel context.CancelFunc
	msgN   int
}

// New builds a server. Out is where ACP frames go; it must not be shared
// with anything else that writes.
func New(o Options) *Server {
	if o.Log == nil {
		o.Log = io.Discard
	}
	return &Server{opts: o, c: newConn(o.Out), sessions: map[string]*acpSession{}}
}

// Serve reads frames until the input ends or ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	return s.c.readLoop(ctx, s.opts.In, s.dispatch)
}

func (s *Server) logf(format string, args ...any) {
	fmt.Fprintf(s.opts.Log, "[acp] "+format+"\n", args...)
}

// dispatch routes one inbound request or notification.
func (s *Server) dispatch(ctx context.Context, m message) {
	notification := len(m.ID) == 0
	result, err := s.handle(ctx, m)
	if notification {
		if err != nil {
			s.logf("%s: %v", m.Method, err)
		}
		return
	}
	if err != nil {
		code := codeInternal
		var re *rpcError
		if errors.As(err, &re) {
			code = re.Code
		}
		if errors.Is(err, errUnknownMethod) {
			code = codeMethodNotFound
		}
		if errors.Is(err, errBadParams) {
			code = codeInvalidParams
		}
		_ = s.c.replyError(m.ID, code, err.Error())
		return
	}
	_ = s.c.reply(m.ID, result)
}

var (
	errUnknownMethod = errors.New("unknown method")
	errBadParams     = errors.New("invalid params")
)

func (s *Server) handle(ctx context.Context, m message) (any, error) {
	switch m.Method {
	case "initialize":
		return s.initialize(m.Params)
	case "authenticate":
		// No auth: the provider key comes from the environment janetcode
		// was launched with, so there is nothing for the editor to do.
		return struct{}{}, nil
	case "session/new":
		return s.newSession(m.Params)
	case "session/load":
		return s.loadSession(m.Params)
	case "session/prompt":
		return s.prompt(ctx, m.Params)
	case "session/cancel":
		return nil, s.cancel(m.Params)
	default:
		return nil, fmt.Errorf("%w: %s", errUnknownMethod, m.Method)
	}
}

func (s *Server) initialize(raw json.RawMessage) (any, error) {
	var p initializeParams
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, fmt.Errorf("%w: %v", errBadParams, err)
		}
	}
	if p.ClientInfo != nil {
		s.mu.Lock()
		s.client = *p.ClientInfo
		s.mu.Unlock()
		s.logf("client %s %s, protocol %d", p.ClientInfo.Name, p.ClientInfo.Version, p.ProtocolVersion)
	}
	// The protocol negotiates down to what both sides speak.
	version := ProtocolVersion
	if p.ProtocolVersion > 0 && p.ProtocolVersion < version {
		version = p.ProtocolVersion
	}
	return initializeResult{
		ProtocolVersion: version,
		AgentCapabilities: agentCapabilities{
			LoadSession:        true,
			PromptCapabilities: promptCapabilities{Image: true, EmbeddedContext: true},
		},
		AgentInfo:   implInfo{Name: "janetcode", Title: "janetcode", Version: s.opts.Version},
		AuthMethods: []authMethod{},
	}, nil
}

func (s *Server) newSession(raw json.RawMessage) (any, error) {
	var p newSessionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadParams, err)
	}
	if p.Cwd == "" {
		return nil, fmt.Errorf("%w: cwd is required", errBadParams)
	}
	as, err := s.open(p.Cwd, "")
	if err != nil {
		return nil, err
	}
	return newSessionResult{SessionID: as.id}, nil
}

// loadSession reopens a session by id and replays its history to the
// editor as message chunks, so the conversation appears where it left off.
func (s *Server) loadSession(raw json.RawMessage) (any, error) {
	var p loadSessionParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadParams, err)
	}
	as, err := s.open(p.Cwd, p.SessionID)
	if err != nil {
		return nil, err
	}
	for _, m := range as.agent.Messages {
		kind := "agent_message_chunk"
		if m.Role == llm.RoleUser {
			kind = "user_message_chunk"
		}
		for _, b := range m.Blocks {
			if b.Type != llm.BlockText || strings.TrimSpace(b.Text) == "" {
				continue
			}
			s.update(as.id, msgUpdate{SessionUpdate: kind, Content: &contentBlock{Type: "text", Text: b.Text}})
		}
	}
	return struct{}{}, nil
}

// open builds the agent for a session and registers it under the session
// file's own id, so an editor can load it again later.
func (s *Server) open(cwd, resume string) (*acpSession, error) {
	if s.opts.NewAgent == nil {
		return nil, errors.New("no agent factory configured")
	}
	ag, sess, err := s.opts.NewAgent(cwd, resume)
	if err != nil {
		return nil, err
	}
	id := resume
	if sess != nil {
		id = sess.Meta.ID
	}
	as := &acpSession{id: id, cwd: cwd, agent: ag, sess: sess}
	ag.Approver = &approver{s: s, as: as}
	s.mu.Lock()
	s.sessions[id] = as
	s.mu.Unlock()
	s.logf("session %s in %s", id, cwd)
	return as, nil
}

func (s *Server) session(id string) (*acpSession, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	as := s.sessions[id]
	if as == nil {
		return nil, fmt.Errorf("%w: no session %q", errBadParams, id)
	}
	return as, nil
}

// update sends one session/update notification.
func (s *Server) update(sessionID string, u any) {
	if err := s.c.notify("session/update", sessionNotification{SessionID: sessionID, Update: u}); err != nil {
		s.logf("update: %v", err)
	}
}

func (s *Server) cancel(raw json.RawMessage) error {
	var p cancelParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("%w: %v", errBadParams, err)
	}
	as, err := s.session(p.SessionID)
	if err != nil {
		return err
	}
	as.mu.Lock()
	cancel := as.cancel
	as.mu.Unlock()
	if cancel != nil {
		s.logf("cancelling %s", p.SessionID)
		cancel()
	}
	return nil
}

// prompt runs one turn, streaming it to the editor.
func (s *Server) prompt(ctx context.Context, raw json.RawMessage) (any, error) {
	var p promptParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("%w: %v", errBadParams, err)
	}
	as, err := s.session(p.SessionID)
	if err != nil {
		return nil, err
	}
	blocks := toBlocks(p.Prompt)
	if len(blocks) == 0 {
		return nil, fmt.Errorf("%w: empty prompt", errBadParams)
	}

	// One turn at a time per session: a second prompt waits rather than
	// interleaving into the same history.
	as.mu.Lock()
	turnCtx, cancel := context.WithCancel(ctx)
	as.cancel = cancel
	as.mu.Unlock()
	defer func() {
		as.mu.Lock()
		as.cancel = nil
		as.mu.Unlock()
		cancel()
	}()

	err = as.agent.RunBlocks(turnCtx, blocks, s.emitter(as))
	switch {
	case err == nil:
		return promptResult{StopReason: stopCompleted}, nil
	case errors.Is(err, context.Canceled), errors.Is(turnCtx.Err(), context.Canceled):
		return promptResult{StopReason: stopCancelled}, nil
	case errors.Is(err, agent.ErrMaxIterations):
		return promptResult{StopReason: stopMaxTurns}, nil
	case errors.Is(err, agent.ErrStuck):
		return promptResult{StopReason: stopRefusal}, nil
	default:
		return nil, err
	}
}

// emitter turns the agent's loop events into session/update notifications.
func (s *Server) emitter(as *acpSession) func(agent.Event) {
	var mu sync.Mutex
	msgID := ""
	return func(e agent.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch e.Kind {
		case agent.EvText, agent.EvThinking:
			if e.Text == "" {
				return
			}
			if msgID == "" {
				as.msgN++
				msgID = fmt.Sprintf("msg-%d", as.msgN)
			}
			kind := "agent_message_chunk"
			if e.Kind == agent.EvThinking {
				kind = "agent_thought_chunk"
			}
			s.update(as.id, msgUpdate{SessionUpdate: kind, MessageID: msgID, Content: &contentBlock{Type: "text", Text: e.Text}})
		case agent.EvTextBlockDone:
			msgID = ""
		case agent.EvToolStart:
			if e.Call == nil {
				return
			}
			s.update(as.id, toolCallUpdate("tool_call", *e.Call, statusPending, ""))
		case agent.EvToolRun:
			if e.Call == nil {
				return
			}
			s.update(as.id, toolCallUpdate("tool_call_update", *e.Call, statusInProgress, ""))
		case agent.EvToolResult:
			if e.Call == nil {
				return
			}
			status := statusCompleted
			if e.IsError {
				status = statusFailed
			}
			s.update(as.id, toolCallUpdate("tool_call_update", *e.Call, status, e.Output))
		case agent.EvNotice, agent.EvCompacted:
			if e.Text == "" {
				return
			}
			s.update(as.id, msgUpdate{SessionUpdate: "agent_message_chunk", Content: &contentBlock{Type: "text", Text: "\n_" + e.Text + "_\n"}})
		}
	}
}

// approver asks the editor for permission through the protocol, so the
// user answers where they are working instead of in a terminal.
type approver struct {
	s  *Server
	as *acpSession
}

func (a *approver) Approve(ctx context.Context, call agent.ToolCall) (agent.Decision, error) {
	d, _, err := a.ApproveWithReason(ctx, call)
	return d, err
}

func (a *approver) ApproveWithReason(ctx context.Context, call agent.ToolCall) (agent.Decision, string, error) {
	tc := toolCallUpdate("tool_call", call, statusPending, "")
	if call.Warning != "" {
		tc.Title += " — " + call.Warning
	}
	if call.Preview != "" {
		tc.Content = append(tc.Content, toolCallContent{Type: "content", Content: &contentBlock{Type: "text", Text: call.Preview}})
	}
	params := requestPermissionParams{
		SessionID: a.as.id,
		ToolCall:  tc,
		Options: []permissionOption{
			{OptionID: optAllowOnce, Name: "Allow", Kind: optAllowOnce},
			{OptionID: optAllowAlways, Name: "Allow always", Kind: optAllowAlways},
			{OptionID: optRejectOnce, Name: "Deny", Kind: optRejectOnce},
		},
	}
	var res requestPermissionResult
	if err := a.s.c.call(ctx, "session/request_permission", params, &res); err != nil {
		return agent.Deny, "", err
	}
	switch {
	case res.Outcome.Outcome == "cancelled":
		return agent.Deny, "the request was cancelled", context.Canceled
	case res.Outcome.OptionID == optAllowOnce:
		return agent.Allow, "", nil
	case res.Outcome.OptionID == optAllowAlways:
		return agent.AllowAlways, "", nil
	case res.Outcome.OptionID == optRejectAlways:
		return agent.Deny, "the user denied this kind of call for the rest of the session", nil
	default:
		return agent.Deny, "", nil
	}
}

// toolCallUpdate builds the tool_call / tool_call_update payload for one
// call, naming what it is about and where it touches the tree so the
// editor can show and link it.
func toolCallUpdate(variant string, call agent.ToolCall, status, output string) toolUpdate {
	u := toolUpdate{
		SessionUpdate: variant,
		ToolCallID:    call.ID,
		Title:         toolTitle(call),
		Kind:          toolKind(call.Name),
		Status:        status,
	}
	if len(call.Input) > 0 {
		u.RawInput = call.Input
	}
	if p := inputPath(call.Input); p != "" {
		u.Locations = []toolCallLocation{{Path: p}}
	}
	if output != "" {
		u.Content = []toolCallContent{{Type: "content", Content: &contentBlock{Type: "text", Text: output}}}
	}
	return u
}

// toolTitle is a one-line human label: the tool and what it is about.
func toolTitle(call agent.ToolCall) string {
	if s := inputSubject(call.Input); s != "" {
		return call.Name + " " + s
	}
	return call.Name
}

func toolKind(name string) string {
	switch name {
	case "read_file", "list_dir", "expand_output":
		return kindRead
	case "write_file", "edit_file":
		return kindEdit
	case "grep", "glob", "repo_map":
		return kindSearch
	case "bash", "job":
		return kindExecute
	case "task":
		return kindThink
	case "fetch_url", "web_search":
		return kindFetch
	default:
		return kindOther
	}
}

// inputSubject is what a call is about — the path, command or pattern —
// for the title.
func inputSubject(raw json.RawMessage) string {
	var in map[string]any
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	for _, k := range []string{"path", "command", "pattern", "query", "url", "description"} {
		if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
			return firstLine(v, 80)
		}
	}
	return ""
}

func inputPath(raw json.RawMessage) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return ""
	}
	return in.Path
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max-1] + "…"
	}
	return s
}

// toBlocks converts the editor's prompt content into agent blocks.
func toBlocks(cbs []contentBlock) []llm.Block {
	var out []llm.Block
	for _, cb := range cbs {
		switch cb.Type {
		case "text":
			if cb.Text != "" {
				out = append(out, llm.Block{Type: llm.BlockText, Text: cb.Text})
			}
		case "image":
			if cb.Data != "" {
				out = append(out, llm.Block{Type: llm.BlockImage, MediaType: cb.MimeType, Data: cb.Data})
			}
		case "resource":
			// An embedded resource is context the editor already read for
			// us: attach it as text naming where it came from.
			if cb.Resource != nil && cb.Resource.Text != "" {
				out = append(out, llm.Block{Type: llm.BlockText, Text: fmt.Sprintf("<context path=%q>\n%s\n</context>", cb.Resource.URI, cb.Resource.Text)})
			}
		case "resource_link":
			if cb.URI != "" {
				out = append(out, llm.Block{Type: llm.BlockText, Text: "@" + cb.URI})
			}
		}
	}
	return out
}
