package lsp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/procgroup"
)

// maxRestarts bounds how often one server is restarted after dying.
const maxRestarts = 2

// client is one running language server.
type client struct {
	// warmOnce guards opening the project's files for the first
	// project-wide query.
	warmOnce sync.Once
	// synced is the content hash last sent per document, so a query does
	// not make the server re-parse a file that has not changed.
	synced map[string][32]byte
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
	// done is closed when the server stopped writing, for whatever
	// reason; alive and every waiting call watch it.
	done chan struct{}
	// reaped is closed once cmd.Wait has returned. done only says the
	// server stopped writing; stopping the process group has to wait for
	// the real exit, or close returns while a zombie is still there.
	reaped chan struct{}
	// err is why the server stopped answering, guarded by pendM. The
	// first reason wins: a frame the reader refused explains more than
	// the exit status that follows it.
	err error
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
	// A language server is the heaviest thing janetcode starts, and some
	// of them are launched through a wrapper or fork workers of their own.
	// Its own process group is what lets close() take the workers with it
	// instead of leaving a gigabyte of rust-analyzer behind.
	procgroup.Set(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &client{cmd: cmd, stdin: stdin, log: log, pend: map[int64]chan rpcResponse{}, docs: map[string]int{}, diags: map[string]diagState{}, waits: map[string]chan struct{}{}, done: make(chan struct{}), reaped: make(chan struct{})}
	go c.read(bufio.NewReaderSize(stdout, 1<<20))
	go func() {
		err := cmd.Wait()
		close(c.reaped)
		c.fail(fmt.Errorf("%s exited: %v %s", command[0], err, strings.TrimSpace(stderr.String())))
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
	// Record what was sent, so a later query does not re-send the same
	// text and make the server parse it again (see client.sync).
	if c.synced == nil {
		c.synced = map[string][32]byte{}
	}
	c.synced[uri] = sha256.Sum256([]byte(text))
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

// alive reports whether the server is still running.
func (c *client) alive() bool {
	select {
	case <-c.done:
		return false
	default:
		return true
	}
}

func (c *client) exitError() error {
	c.pendM.Lock()
	defer c.pendM.Unlock()
	if c.err != nil {
		return c.err
	}
	return errors.New("language server closed its output")
}

// fail records why the server stopped answering and wakes every call
// waiting for one. The first reason wins, because the reader usually
// knows more than the exit status that follows it.
func (c *client) fail(err error) {
	c.pendM.Lock()
	defer c.pendM.Unlock()
	if c.err == nil {
		c.err = err
	}
	for id, ch := range c.pend {
		close(ch)
		delete(c.pend, id)
	}
}

// close asks the server to shut down, then stops its whole process group
// so nothing it forked outlives the session.
func (c *client) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.call(ctx, "shutdown", nil)
	_ = c.notify("exit", nil)
	procgroup.Stop(c.cmd, c.reaped, 2*time.Second)
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
	case <-c.done:
		// The reader is gone, so no answer is coming — but the process
		// may well still be running, so nothing else would notice and
		// this call would have waited out its whole deadline, as would
		// every call after it.
		c.pendM.Lock()
		delete(c.pend, id)
		c.pendM.Unlock()
		return nil, c.exitError()
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
			// An end of stream is the server exiting, which the reaper
			// reports with its stderr. Anything else is the stream itself
			// going wrong — a frame bigger than the limit, a header that
			// is not LSP — and is the only account anyone will get of it,
			// since the process may keep running.
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				c.fail(err)
			}
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
