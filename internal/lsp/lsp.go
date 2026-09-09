// Package lsp talks to language servers (gopls, pyright, tsserver, …) over
// stdio so the agent sees real compiler diagnostics right after it edits a
// file, instead of discovering them a build later. Only the parts of the
// protocol needed for that are implemented: initialize, didOpen/didChange
// with full text, and publishDiagnostics. Servers start lazily on the
// first edit of a matching file and a missing binary is remembered so it
// is not retried every turn.
package lsp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Server describes how to start one language server.
type Server struct {
	// Command is the executable and its arguments.
	Command []string `json:"command"`
	// Extensions lists the file extensions (".go") the server handles.
	Extensions []string `json:"extensions"`
}

// Config is the "lsp" section of the config file.
type Config struct {
	// Disabled turns diagnostics off entirely.
	Disabled bool `json:"disabled,omitempty"`
	// Servers adds to or overrides the defaults by name. A server with an
	// empty command removes a default.
	Servers map[string]Server `json:"servers,omitempty"`
	// Timeout is how many seconds to wait for diagnostics after an edit;
	// zero means 8.
	Timeout int `json:"timeout,omitempty"`
}

// Validate rejects servers that have extensions but no command.
func (c Config) Validate() error {
	for name, s := range c.Servers {
		if len(s.Command) == 0 && len(s.Extensions) > 0 {
			return fmt.Errorf("lsp: server %q has extensions but no command", name)
		}
		for _, e := range s.Extensions {
			if !strings.HasPrefix(e, ".") {
				return fmt.Errorf("lsp: server %q extension %q must start with a dot", name, e)
			}
		}
	}
	return nil
}

// Defaults are the servers used when the binary is on PATH.
var Defaults = map[string]Server{
	"gopls":                      {Command: []string{"gopls"}, Extensions: []string{".go"}},
	"pyright":                    {Command: []string{"pyright-langserver", "--stdio"}, Extensions: []string{".py", ".pyi"}},
	"typescript-language-server": {Command: []string{"typescript-language-server", "--stdio"}, Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"}},
	"rust-analyzer":              {Command: []string{"rust-analyzer"}, Extensions: []string{".rs"}},
	"clangd":                     {Command: []string{"clangd"}, Extensions: []string{".c", ".h", ".cc", ".cpp", ".hpp", ".cxx"}},
}

// Active merges the defaults with the config's overrides.
func (c Config) Active() map[string]Server {
	out := map[string]Server{}
	for k, v := range Defaults {
		out[k] = v
	}
	for k, v := range c.Servers {
		if len(v.Command) == 0 {
			delete(out, k)
			continue
		}
		out[k] = v
	}
	return out
}

// Diagnostic is one reported problem.
type Diagnostic struct {
	Range struct {
		Start Position `json:"start"`
		End   Position `json:"end"`
	} `json:"range"`
	Severity int             `json:"severity"`
	Code     json.RawMessage `json:"code,omitempty"`
	Source   string          `json:"source,omitempty"`
	Message  string          `json:"message"`
}

// Position is zero-based in the protocol.
type Position struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// SeverityName maps the protocol's severity to a word.
func SeverityName(s int) string {
	switch s {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	}
	return "error"
}

// Format renders one diagnostic as path:line:col: severity: message.
func (d Diagnostic) Format(path string) string {
	src := ""
	if d.Source != "" {
		src = " (" + d.Source + ")"
	}
	return fmt.Sprintf("%s:%d:%d: %s: %s%s", path, d.Range.Start.Line+1, d.Range.Start.Character+1, SeverityName(d.Severity), strings.TrimSpace(d.Message), src)
}

// Manager owns one client per server, started on demand.
type Manager struct {
	Root    string
	Config  Config
	Log     func(string)
	mu      sync.Mutex
	clients map[string]*client // by server name
	failed  map[string]string  // server name → why it cannot start
	byExt   map[string]string  // extension → server name
}

// New prepares a manager; nothing starts until Diagnose is called.
func New(root string, cfg Config) *Manager {
	m := &Manager{Root: root, Config: cfg, clients: map[string]*client{}, failed: map[string]string{}, byExt: map[string]string{}}
	for name, s := range cfg.Active() {
		for _, e := range s.Extensions {
			m.byExt[strings.ToLower(e)] = name
		}
	}
	return m
}

// Enabled reports whether diagnostics may run at all.
func (m *Manager) Enabled() bool { return m != nil && !m.Config.Disabled && len(m.byExt) > 0 }

// ServerFor names the server that handles the path, or "".
func (m *Manager) ServerFor(path string) string {
	if m == nil {
		return ""
	}
	return m.byExt[strings.ToLower(filepath.Ext(path))]
}

// Diagnose tells the server the file changed and returns its diagnostics
// for that file, formatted one per line, or "" when no server handles the
// file or none answered in time. A nil error with an empty result means
// the server answered with no problems; the "ok" flag separates the two.
func (m *Manager) Diagnose(ctx context.Context, path string) (lines []string, ok bool, err error) {
	if !m.Enabled() {
		return nil, false, nil
	}
	c, err := m.clientFor(ctx, path)
	if err != nil || c == nil {
		return nil, false, err
	}
	abs := m.abs(path)
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, false, err
	}
	timeout := time.Duration(m.Config.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	ds, ok, err := c.diagnose(ctx, abs, string(data), timeout)
	if err != nil {
		return nil, false, err
	}
	return m.format(abs, ds), ok, nil
}

// Known returns the latest diagnostics for every file a server reported,
// formatted and sorted by path.
func (m *Manager) Known() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	clients := make([]*client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.mu.Unlock()
	var out []string
	for _, c := range clients {
		for uri, ds := range c.snapshot() {
			out = append(out, m.format(fromURI(uri), ds)...)
		}
	}
	sort.Strings(out)
	return out
}

// Status describes each configured server: running, not installed, or idle.
func (m *Manager) Status() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	servers := m.Config.Active()
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []string
	for _, n := range names {
		s := servers[n]
		state := "idle (starts on the first " + strings.Join(s.Extensions, "/") + " edit)"
		if why, bad := m.failed[n]; bad {
			state = "unavailable: " + why
		} else if c, running := m.clients[n]; running {
			state = fmt.Sprintf("running (pid %d, %d files open)", c.cmd.Process.Pid, c.openCount())
		} else if _, err := exec.LookPath(s.Command[0]); err != nil {
			state = "not installed (" + s.Command[0] + " not on PATH)"
		}
		out = append(out, n+": "+state)
	}
	if m.Config.Disabled {
		out = append([]string{"diagnostics are disabled in the config (\"lsp\": {\"disabled\": true})"}, out...)
	}
	return out
}

// Close stops every server.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	clients := m.clients
	m.clients = map[string]*client{}
	m.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

func (m *Manager) abs(path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(m.Root, path)
}

func (m *Manager) format(abs string, ds []Diagnostic) []string {
	rel := abs
	if r, err := filepath.Rel(m.Root, abs); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	}
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Format(rel))
	}
	return out
}

func (m *Manager) clientFor(ctx context.Context, path string) (*client, error) {
	name := m.ServerFor(path)
	if name == "" {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.clients[name]; ok {
		return c, nil
	}
	if _, bad := m.failed[name]; bad {
		return nil, nil
	}
	s := m.Config.Active()[name]
	c, err := start(ctx, m.Root, s.Command, m.Log)
	if err != nil {
		m.failed[name] = err.Error()
		return nil, fmt.Errorf("lsp: %s: %w", name, err)
	}
	m.clients[name] = c
	return c, nil
}

// client is one running language server.
type client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	writeM sync.Mutex
	log    func(string)
	nextID int64
	pendM  sync.Mutex
	pend   map[int64]chan rpcResponse
	docM   sync.Mutex
	docs   map[string]int           // uri → version sent
	diags  map[string]diagState     // uri → latest
	waits  map[string]chan struct{} // uri → closed on the next publish
	done   chan struct{}
	err    error
}

type diagState struct {
	version int // 0 when the server does not report one
	items   []Diagnostic
	seq     uint64
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

func start(ctx context.Context, root string, command []string, log func(string)) (*client, error) {
	if len(command) == 0 {
		return nil, errors.New("no command")
	}
	if _, err := exec.LookPath(command[0]); err != nil {
		return nil, fmt.Errorf("%s is not installed", command[0])
	}
	cmd := exec.Command(command[0], command[1:]...)
	cmd.Dir = root
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &client{cmd: cmd, stdin: stdin, log: log, pend: map[int64]chan rpcResponse{}, docs: map[string]int{}, diags: map[string]diagState{}, waits: map[string]chan struct{}{}, done: make(chan struct{})}
	go c.read(bufio.NewReaderSize(stdout, 1<<20))
	go func() {
		err := cmd.Wait()
		c.pendM.Lock()
		c.err = fmt.Errorf("%s exited: %v %s", command[0], err, strings.TrimSpace(stderr.String()))
		for id, ch := range c.pend {
			close(ch)
			delete(c.pend, id)
		}
		c.pendM.Unlock()
	}()
	ictx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   toURI(root),
		"workspaceFolders": []map[string]any{
			{"uri": toURI(root), "name": filepath.Base(root)},
		},
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{"versionSupport": true},
				"synchronization":    map[string]any{"didSave": true},
				"definition":         map[string]any{"linkSupport": true},
				"typeDefinition":     map[string]any{"linkSupport": true},
				"implementation":     map[string]any{"linkSupport": true},
				"references":         map[string]any{},
				"hover":              map[string]any{"contentFormat": []string{"markdown", "plaintext"}},
				"documentSymbol":     map[string]any{"hierarchicalDocumentSymbolSupport": true},
			},
			"workspace": map[string]any{"configuration": true, "workspaceFolders": true, "symbol": map[string]any{}},
			"window":    map[string]any{"workDoneProgress": true},
		},
		"initializationOptions": map[string]any{},
	}
	if _, err := c.call(ictx, "initialize", params); err != nil {
		c.close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := c.notify("initialized", map[string]any{}); err != nil {
		c.close()
		return nil, err
	}
	return c, nil
}

// diagnose sends the file's text and waits for diagnostics for it.
func (c *client) diagnose(ctx context.Context, abs, text string, timeout time.Duration) ([]Diagnostic, bool, error) {
	uri := toURI(abs)
	c.docM.Lock()
	version, open := c.docs[uri]
	version++
	c.docs[uri] = version
	before := c.diags[uri].seq
	wait := c.waits[uri]
	if wait == nil {
		wait = make(chan struct{})
		c.waits[uri] = wait
	}
	c.docM.Unlock()
	var err error
	if !open {
		err = c.notify("textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": languageID(abs), "version": version, "text": text}})
	} else {
		err = c.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": text}},
		})
	}
	if err != nil {
		return nil, false, err
	}
	// Servers that support didSave often run their heavier checks then.
	_ = c.notify("textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}})
	deadline := time.After(timeout)
	for {
		c.docM.Lock()
		st := c.diags[uri]
		wait = c.waits[uri]
		c.docM.Unlock()
		if st.version >= version || (st.version == 0 && st.seq > before) {
			return st.items, true, nil
		}
		select {
		case <-wait:
		case <-deadline:
			return st.items, false, nil
		case <-ctx.Done():
			return nil, false, ctx.Err()
		case <-c.done:
			return nil, false, c.exitError()
		}
	}
}

func (c *client) snapshot() map[string][]Diagnostic {
	c.docM.Lock()
	defer c.docM.Unlock()
	out := map[string][]Diagnostic{}
	for uri, st := range c.diags {
		if len(st.items) > 0 {
			out[uri] = st.items
		}
	}
	return out
}

func (c *client) openCount() int {
	c.docM.Lock()
	defer c.docM.Unlock()
	return len(c.docs)
}

func (c *client) exitError() error {
	c.pendM.Lock()
	defer c.pendM.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("language server closed its output")
}

// close asks the server to shut down and kills it if it lingers.
func (c *client) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.call(ctx, "shutdown", nil)
	_ = c.notify("exit", nil)
	select {
	case <-c.done:
	case <-time.After(2 * time.Second):
		_ = c.cmd.Process.Kill()
	}
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.pendM.Lock()
	if c.err != nil {
		c.pendM.Unlock()
		return nil, c.err
	}
	c.nextID++
	id := c.nextID
	ch := make(chan rpcResponse, 1)
	c.pend[id] = ch
	c.pendM.Unlock()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.pendM.Lock()
		delete(c.pend, id)
		c.pendM.Unlock()
		return nil, err
	}
	select {
	case r, ok := <-ch:
		if !ok {
			return nil, c.exitError()
		}
		if r.Error != nil {
			return nil, r.Error
		}
		return r.Result, nil
	case <-ctx.Done():
		c.pendM.Lock()
		delete(c.pend, id)
		c.pendM.Unlock()
		return nil, ctx.Err()
	}
}

func (c *client) notify(method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.send(msg)
}

func (c *client) reply(id json.RawMessage, result any) {
	_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *client) send(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.writeM.Lock()
	defer c.writeM.Unlock()
	if _, err := fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = c.stdin.Write(body)
	return err
}

// read dispatches server messages until stdout closes.
func (c *client) read(r *bufio.Reader) {
	defer close(c.done)
	for {
		body, err := readMessage(r)
		if err != nil {
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  *rpcError       `json:"error"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			continue
		}
		switch {
		case msg.Method == "" && len(msg.ID) > 0: // response
			id, err := strconv.ParseInt(string(msg.ID), 10, 64)
			if err != nil {
				continue
			}
			c.pendM.Lock()
			ch := c.pend[id]
			delete(c.pend, id)
			c.pendM.Unlock()
			if ch != nil {
				ch <- rpcResponse{Result: msg.Result, Error: msg.Error}
			}
		case len(msg.ID) > 0: // request from the server
			c.serve(msg.ID, msg.Method, msg.Params)
		default: // notification
			c.handle(msg.Method, msg.Params)
		}
	}
}

// serve answers the few requests servers make of a client with the
// emptiest valid response, so they keep going.
func (c *client) serve(id json.RawMessage, method string, params json.RawMessage) {
	switch method {
	case "workspace/configuration":
		var p struct {
			Items []json.RawMessage `json:"items"`
		}
		_ = json.Unmarshal(params, &p)
		out := make([]any, len(p.Items))
		c.reply(id, out)
	case "workspace/workspaceFolders":
		c.reply(id, []any{})
	case "client/registerCapability", "client/unregisterCapability", "window/workDoneProgress/create", "workspace/applyEdit":
		c.reply(id, nil)
	default:
		_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32601, "message": "method not supported: " + method}})
	}
}

func (c *client) handle(method string, params json.RawMessage) {
	switch method {
	case "textDocument/publishDiagnostics":
		var p struct {
			URI         string       `json:"uri"`
			Version     int          `json:"version"`
			Diagnostics []Diagnostic `json:"diagnostics"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return
		}
		c.docM.Lock()
		st := c.diags[p.URI]
		st.seq++
		st.version, st.items = p.Version, p.Diagnostics
		c.diags[p.URI] = st
		if w := c.waits[p.URI]; w != nil {
			close(w)
			c.waits[p.URI] = make(chan struct{})
		}
		c.docM.Unlock()
	case "window/logMessage", "window/showMessage":
		if c.log == nil {
			return
		}
		var p struct {
			Type    int    `json:"type"`
			Message string `json:"message"`
		}
		if json.Unmarshal(params, &p) == nil && p.Type == 1 { // error
			c.log(strings.TrimSpace(p.Message))
		}
	}
}

func readMessage(r *bufio.Reader) ([]byte, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, ":"); ok && strings.EqualFold(strings.TrimSpace(k), "Content-Length") {
			length, err = strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return nil, err
			}
		}
	}
	if length < 0 {
		return nil, errors.New("lsp: message without Content-Length")
	}
	body := make([]byte, length)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, err
	}
	return body, nil
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	if len(p) > l.n {
		p = p[:l.n]
	}
	l.n -= len(p)
	_, err := l.w.Write(p)
	return len(p), err
}

func toURI(abs string) string {
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return "file://" + abs
}

func fromURI(uri string) string {
	p := strings.TrimPrefix(uri, "file://")
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	// Unescape %20 and friends without pulling in net/url edge cases.
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) {
			if v, err := strconv.ParseUint(p[i+1:i+3], 16, 8); err == nil {
				b.WriteByte(byte(v))
				i += 2
				continue
			}
		}
		b.WriteByte(p[i])
	}
	return filepath.FromSlash(b.String())
}

func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py", ".pyi":
		return "python"
	case ".ts":
		return "typescript"
	case ".tsx":
		return "typescriptreact"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".jsx":
		return "javascriptreact"
	case ".rs":
		return "rust"
	case ".c", ".h":
		return "c"
	case ".cc", ".cpp", ".hpp", ".cxx":
		return "cpp"
	}
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}
