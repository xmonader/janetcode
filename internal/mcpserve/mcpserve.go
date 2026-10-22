// Package mcpserve serves janetcode as an MCP server on stdio, so any
// MCP client — another coding agent, an IDE, a desktop assistant — can
// hand janetcode a task and get the result back as a tool call.
//
// It offers one tool, janetcode_task: a prompt in, the answer out, with
// the session id to continue the same conversation, the verdict and the
// usage. How the task runs is a Runner's business; the command wires in
// one that drives janetcode over ACP, so the task gets the whole agent
// under the permission mode the server was started with.
package mcpserve

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
)

// ProtocolVersion is the MCP revision served. A client asking for an
// older one gets its own back, which the handshake allows.
const ProtocolVersion = "2025-06-18"

// ToolName is the one tool this server offers.
const ToolName = "janetcode_task"

// Task is one call of the tool.
type Task struct {
	Prompt string
	// SessionID continues an earlier task's conversation; empty starts
	// a new one.
	SessionID string
}

// Report is what a task came to.
type Report struct {
	SessionID  string `json:"sessionId"`
	Text       string `json:"text"`
	StopReason string `json:"stopReason"`
	Verdict    string `json:"verdict,omitempty"`
	ToolCalls  int    `json:"toolCalls"`
	// Denied counts the calls the permission mode refused: nobody is at
	// a server to answer a question, so what would ask is refused.
	Denied       int     `json:"denied"`
	InputTokens  int     `json:"inputTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd,omitempty"`
}

// Runner runs tasks. progress, when not nil, takes one line per step
// (a tool call starting) for the client's progress notifications.
type Runner interface {
	Run(ctx context.Context, t Task, progress func(string)) (Report, error)
}

// Options configure the server.
type Options struct {
	Runner  Runner
	Version string
	// Mode describes what tasks may do without asking, for the tool's
	// description (read-only, auto-edits, …), and ReadOnly marks the
	// tool read-only for clients that honour the hint.
	Mode     string
	ReadOnly bool
	Dir      string
}

type msg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve answers MCP requests on in and out until in ends or ctx is done.
func Serve(ctx context.Context, in io.Reader, out io.Writer, o Options) error {
	s := &server{o: o, out: out, running: map[string]*running{}}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64<<10), 64<<20)
	lines := make(chan []byte)
	go func() {
		defer close(lines)
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- line:
			case <-ctx.Done():
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			s.wg.Wait()
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				// The client hung up; tasks still running are cancelled
				// and waited for, so their sessions are saved whole.
				cancel()
				s.wg.Wait()
				return sc.Err()
			}
			s.handle(ctx, line)
		}
	}
}

type server struct {
	o   Options
	out io.Writer
	wmu sync.Mutex
	wg  sync.WaitGroup

	mu      sync.Mutex
	running map[string]*running // request id → its task
}

// running is a tools/call in flight. A request the client cancelled is
// not answered (MCP: the receiver SHOULD NOT send a response for it).
type running struct {
	cancel    context.CancelFunc
	cancelled bool
}

func (s *server) write(m msg) {
	m.JSONRPC = "2.0"
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, _ = s.out.Write(append(data, '\n'))
}

func (s *server) reply(id json.RawMessage, result any) { s.write(msg{ID: id, Result: result}) }
func (s *server) fail(id json.RawMessage, code int, text string) {
	s.write(msg{ID: id, Error: &rpcError{Code: code, Message: text}})
}

func (s *server) handle(ctx context.Context, line []byte) {
	if len(strings.TrimSpace(string(line))) == 0 {
		return
	}
	var m msg
	if err := json.Unmarshal(line, &m); err != nil {
		s.fail(json.RawMessage("null"), -32700, "parse error: "+err.Error())
		return
	}
	if len(m.ID) == 0 {
		s.notification(m)
		return
	}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		version := ProtocolVersion
		if p.ProtocolVersion != "" && p.ProtocolVersion < version {
			version = p.ProtocolVersion
		}
		s.reply(m.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "janetcode", "version": s.o.Version},
			"instructions":    "janetcode is a coding agent working in " + s.o.Dir + ". Give it a whole task with janetcode_task; pass the session_id it returns to follow up in the same conversation.",
		})
	case "ping":
		s.reply(m.ID, map[string]any{})
	case "tools/list":
		s.reply(m.ID, map[string]any{"tools": []any{s.toolDef()}})
	case "tools/call":
		s.call(ctx, m)
	default:
		s.fail(m.ID, -32601, "method not found: "+m.Method)
	}
}

func (s *server) notification(m msg) {
	if m.Method != "notifications/cancelled" {
		return
	}
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return
	}
	s.mu.Lock()
	r := s.running[string(p.RequestID)]
	if r != nil {
		r.cancelled = true
	}
	s.mu.Unlock()
	if r != nil {
		r.cancel()
	}
}

func (s *server) toolDef() map[string]any {
	mode := s.o.Mode
	if mode == "" {
		mode = "read-only"
	}
	desc := "Hand a coding task to janetcode, a coding agent working in " + s.o.Dir + ". It reads, searches, edits and runs commands as the task needs, within its permission mode (" + mode + "): anything the mode would ask a person about is refused, since nobody is there to answer. Give a complete, self-contained instruction. The answer comes back as text, with the session id to continue the same conversation, what the harness saw the work demonstrate (tests run and passing, or nothing run), and the tokens used."
	return map[string]any{
		"name":        ToolName,
		"title":       "janetcode task",
		"description": desc,
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt":     map[string]any{"type": "string", "description": "The task, in full"},
				"session_id": map[string]any{"type": "string", "description": "Continue this earlier task's conversation instead of starting a new one"},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
		"outputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"sessionId":    map[string]any{"type": "string"},
				"text":         map[string]any{"type": "string"},
				"stopReason":   map[string]any{"type": "string"},
				"verdict":      map[string]any{"type": "string"},
				"toolCalls":    map[string]any{"type": "integer"},
				"denied":       map[string]any{"type": "integer"},
				"inputTokens":  map[string]any{"type": "integer"},
				"outputTokens": map[string]any{"type": "integer"},
				"costUsd":      map[string]any{"type": "number"},
			},
			"required": []string{"sessionId", "text", "stopReason"},
		},
		"annotations": map[string]any{
			"title":           "janetcode task",
			"readOnlyHint":    s.o.ReadOnly,
			"destructiveHint": !s.o.ReadOnly,
			"openWorldHint":   true,
		},
	}
}

func (s *server) call(ctx context.Context, m msg) {
	var p struct {
		Name      string `json:"name"`
		Arguments struct {
			Prompt    string `json:"prompt"`
			SessionID string `json:"session_id"`
		} `json:"arguments"`
		Meta struct {
			ProgressToken json.RawMessage `json:"progressToken"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		s.fail(m.ID, -32602, "invalid params: "+err.Error())
		return
	}
	if p.Name != ToolName {
		s.fail(m.ID, -32602, "unknown tool: "+p.Name)
		return
	}
	if strings.TrimSpace(p.Arguments.Prompt) == "" {
		s.reply(m.ID, toolError("prompt is required"))
		return
	}
	key := string(m.ID)
	tctx, cancel := context.WithCancel(ctx)
	task := &running{cancel: cancel}
	s.mu.Lock()
	if s.running[key] != nil {
		// A second call under a live id would replace the first's cancel
		// func, so a cancel meant for one would stop or miss the other.
		s.mu.Unlock()
		cancel()
		s.fail(m.ID, -32600, "invalid request: id "+key+" is already in flight")
		return
	}
	s.running[key] = task
	s.mu.Unlock()
	var progress func(string)
	if len(p.Meta.ProgressToken) > 0 {
		token := p.Meta.ProgressToken
		n := 0
		var pmu sync.Mutex
		progress = func(line string) {
			pmu.Lock()
			n++
			step := n
			pmu.Unlock()
			raw, _ := json.Marshal(map[string]any{"progressToken": token, "progress": step, "message": line})
			s.write(msg{Method: "notifications/progress", Params: raw})
		}
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		rep, err := s.o.Runner.Run(tctx, Task{Prompt: p.Arguments.Prompt, SessionID: p.Arguments.SessionID}, progress)
		cancel()
		s.mu.Lock()
		delete(s.running, key)
		cancelled := task.cancelled
		s.mu.Unlock()
		if cancelled {
			return
		}
		if err != nil {
			s.reply(m.ID, toolError(err.Error()))
			return
		}
		s.reply(m.ID, map[string]any{
			"content":           []any{map[string]any{"type": "text", "text": summary(rep)}},
			"structuredContent": rep,
			"isError":           false,
		})
	}()
}

func toolError(text string) map[string]any {
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}, "isError": true}
}

// summary is the text a client's model reads: the answer, then one line
// of what surrounds it.
func summary(r Report) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(r.Text))
	fmt.Fprintf(&b, "\n\n[janetcode session %s · %s · %d tool calls", r.SessionID, r.StopReason, r.ToolCalls)
	if r.Denied > 0 {
		fmt.Fprintf(&b, " · %d refused by the permission mode", r.Denied)
	}
	if r.Verdict != "" {
		fmt.Fprintf(&b, " · %s", r.Verdict)
	}
	fmt.Fprintf(&b, " · %d in / %d out tokens]", r.InputTokens, r.OutputTokens)
	return b.String()
}
