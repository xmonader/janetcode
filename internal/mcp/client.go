// Package mcp is a client for Model Context Protocol servers over stdio.
// It speaks JSON-RPC 2.0 framed as newline-delimited JSON, performs the
// initialize handshake, lists the server's tools and calls them.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ProtocolVersion is the MCP revision this client speaks. Servers that only
// know an older revision answer with theirs; the handshake accepts any.
const ProtocolVersion = "2025-06-18"

// ServerConfig describes how to launch a stdio server. It matches the
// "mcpServers" entries Claude Code and others use in .mcp.json.
type ServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	// Cwd is the directory the server runs in; empty means the project.
	Cwd string `json:"cwd,omitempty"`
	// URL selects a remote server instead of a command: Streamable HTTP,
	// with automatic fallback to the older SSE transport.
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	// Type may say "stdio", "http" or "sse"; it is inferred when empty.
	Type string `json:"type,omitempty"`
}

// ToolInfo is one tool the server offers.
type ToolInfo struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations struct {
		Title           string `json:"title"`
		ReadOnlyHint    *bool  `json:"readOnlyHint"`
		DestructiveHint *bool  `json:"destructiveHint"`
	} `json:"annotations"`
}

// Client is a connection to one running server.
type Client struct {
	Name string
	// ServerInfo is what the server reported in the handshake.
	ServerInfo struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	Protocol string

	tr     transport
	nextID atomic.Int64

	pendMu  sync.Mutex
	pending map[int64]chan rpcMessage

	tools []ToolInfo

	root  string // reported to roots/list
	debug io.Writer

	closeOnce sync.Once
	done      chan struct{}
	exitErr   error
}

// transport moves JSON-RPC messages to and from a server. Inbound messages
// are handed to Client.dispatch by the transport itself.
type transport interface {
	send(ctx context.Context, msg rpcMessage) error
	close()
	// hint is diagnostic context for a failed handshake (stderr, last HTTP status).
	hint() string
}

type rpcMessage struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// Options tune a connection.
type Options struct {
	// Root is the project directory advertised to the server via roots/list.
	Root string
	// Debug receives protocol traces when set.
	Debug io.Writer
	// Version is reported as the client version.
	Version string
	// HandshakeTimeout bounds initialize plus tools/list. Zero means 60s.
	HandshakeTimeout time.Duration
}

// Start launches or connects to the server, completes the handshake and
// lists its tools.
func Start(ctx context.Context, name string, cfg ServerConfig, opts Options) (*Client, error) {
	c := &Client{
		Name:    name,
		pending: map[int64]chan rpcMessage{},
		root:    opts.Root,
		debug:   opts.Debug,
		done:    make(chan struct{}),
	}
	var err error
	switch {
	case strings.TrimSpace(cfg.URL) != "":
		c.tr, err = newHTTPTransport(c, cfg)
	case strings.TrimSpace(cfg.Command) != "":
		c.tr, err = newStdioTransport(c, cfg, opts.Root)
	default:
		err = errors.New("needs a command or a url")
	}
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", name, err)
	}

	timeout := opts.HandshakeTimeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	hctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := c.handshake(hctx, opts.Version); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp %s: %w%s", name, err, c.tr.hint())
	}
	if err := c.loadTools(hctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("mcp %s: list tools: %w%s", name, err, c.tr.hint())
	}
	return c, nil
}

func (c *Client) handshake(ctx context.Context, version string) error {
	if version == "" {
		version = "dev"
	}
	params := map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{"roots": map[string]any{"listChanged": false}},
		"clientInfo":      map[string]any{"name": "janetcode", "version": version},
	}
	res, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	if err := json.Unmarshal(res, &init); err != nil {
		return fmt.Errorf("initialize: bad result: %w", err)
	}
	c.Protocol = init.ProtocolVersion
	c.ServerInfo = init.ServerInfo
	return c.notify(ctx, "notifications/initialized", map[string]any{})
}

func (c *Client) loadTools(ctx context.Context) error {
	var (
		all    []ToolInfo
		cursor string
	)
	for {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		res, err := c.call(ctx, "tools/list", params)
		if err != nil {
			return err
		}
		var page struct {
			Tools      []ToolInfo `json:"tools"`
			NextCursor string     `json:"nextCursor"`
		}
		if err := json.Unmarshal(res, &page); err != nil {
			return fmt.Errorf("bad tools/list result: %w", err)
		}
		all = append(all, page.Tools...)
		if page.NextCursor == "" || page.NextCursor == cursor {
			break
		}
		cursor = page.NextCursor
	}
	c.tools = all
	return nil
}

// Tools lists what the server offers, as reported at startup.
func (c *Client) Tools() []ToolInfo { return append([]ToolInfo(nil), c.tools...) }

// CallResult is what a tool returned: its text content and whether the
// server flagged it as an error.
type CallResult struct {
	Text    string
	IsError bool
}

// CallTool invokes a tool; ctx cancellation abandons the call.
func (c *Client) CallTool(ctx context.Context, name string, args json.RawMessage) (CallResult, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	res, err := c.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return CallResult{}, err
	}
	var out struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource *struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		StructuredContent json.RawMessage `json:"structuredContent"`
		IsError           bool            `json:"isError"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return CallResult{}, fmt.Errorf("bad tools/call result: %w", err)
	}
	var sb strings.Builder
	for _, part := range out.Content {
		switch part.Type {
		case "text":
			sb.WriteString(part.Text)
			sb.WriteString("\n")
		case "resource":
			if part.Resource != nil {
				fmt.Fprintf(&sb, "[resource %s]\n%s\n", part.Resource.URI, part.Resource.Text)
			}
		case "image", "audio":
			fmt.Fprintf(&sb, "[%s content omitted: %s]\n", part.Type, part.MimeType)
		default:
			fmt.Fprintf(&sb, "[%s content]\n", part.Type)
		}
	}
	if sb.Len() == 0 && len(out.StructuredContent) > 0 {
		sb.Write(out.StructuredContent)
	}
	return CallResult{Text: strings.TrimRight(sb.String(), "\n"), IsError: out.IsError}, nil
}

// Close stops the server or connection. It is safe to call more than once.
func (c *Client) Close() error {
	c.tr.close()
	c.failPending(errors.New("connection closed"))
	c.closeOnce.Do(func() { close(c.done) })
	return nil
}

// exited is called by a transport when the server went away.
func (c *Client) exited(err error) {
	c.exitErr = err
	c.failPending(fmt.Errorf("server exited: %v", err))
	c.closeOnce.Do(func() { close(c.done) })
}

// Done is closed when the server process has exited.
func (c *Client) Done() <-chan struct{} { return c.done }

// --- transport ---

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan rpcMessage, 1)
	c.pendMu.Lock()
	if c.pending == nil {
		c.pendMu.Unlock()
		return nil, errors.New("connection closed")
	}
	c.pending[id] = ch
	c.pendMu.Unlock()

	rawID := json.RawMessage(fmt.Sprint(id))
	if err := c.send(ctx, rpcMessage{JSONRPC: "2.0", ID: &rawID, Method: method, Params: mustJSON(params)}); err != nil {
		c.dropPending(id)
		return nil, err
	}
	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, msg.Error
		}
		return msg.Result, nil
	case <-ctx.Done():
		c.dropPending(id)
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("server exited: %v", c.exitErr)
	}
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	return c.send(ctx, rpcMessage{JSONRPC: "2.0", Method: method, Params: mustJSON(params)})
}

func (c *Client) send(ctx context.Context, msg rpcMessage) error {
	if c.debug != nil {
		data, _ := json.Marshal(msg)
		c.logf("-> %s", data)
	}
	return c.tr.send(ctx, msg)
}

// dispatchRaw parses one frame from the server and routes it.
func (c *Client) dispatchRaw(line []byte) {
	if len(strings.TrimSpace(string(line))) == 0 {
		return
	}
	c.logf("<- %s", line)
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		c.logf("bad frame: %v", err)
		return
	}
	c.dispatch(msg)
}

// dispatch routes a response to its caller, answers a server request, and
// ignores notifications.
func (c *Client) dispatch(msg rpcMessage) {
	switch {
	case msg.Method != "" && msg.ID != nil:
		go c.handleRequest(msg)
	case msg.Method != "":
		// notification (progress, log, list_changed): nothing to do
	case msg.ID != nil:
		var id int64
		if err := json.Unmarshal(*msg.ID, &id); err != nil {
			return
		}
		c.pendMu.Lock()
		ch := c.pending[id]
		delete(c.pending, id)
		c.pendMu.Unlock()
		if ch != nil {
			ch <- msg
		}
	}
}

// handleRequest answers the few requests a server may send us.
func (c *Client) handleRequest(req rpcMessage) {
	reply := rpcMessage{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "ping":
		reply.Result = json.RawMessage(`{}`)
	case "roots/list":
		roots := []map[string]string{}
		if c.root != "" {
			roots = append(roots, map[string]string{"uri": "file://" + c.root, "name": "project"})
		}
		reply.Result = mustJSON(map[string]any{"roots": roots})
	default:
		reply.Error = &rpcError{Code: -32601, Message: "method not supported by janetcode: " + req.Method}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c.send(ctx, reply)
}

func (c *Client) dropPending(id int64) {
	c.pendMu.Lock()
	delete(c.pending, id)
	c.pendMu.Unlock()
}

func (c *Client) failPending(err error) {
	c.pendMu.Lock()
	defer c.pendMu.Unlock()
	for id, ch := range c.pending {
		ch <- rpcMessage{Error: &rpcError{Code: -32000, Message: err.Error()}}
		delete(c.pending, id)
	}
	c.pending = nil
}

func (c *Client) logf(format string, args ...any) {
	if c.debug != nil {
		fmt.Fprintf(c.debug, "[mcp %s] "+format+"\n", append([]any{c.Name}, args...)...)
	}
}

func mustJSON(v any) json.RawMessage {
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}
