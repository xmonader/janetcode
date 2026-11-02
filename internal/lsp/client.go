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

	"github.com/xmonader/janetcode/internal/pipewrite"
	"github.com/xmonader/janetcode/internal/procgroup"
	"github.com/xmonader/janetcode/internal/shell"
)

// maxRestarts bounds how often one server is restarted after dying.
const maxRestarts = 3

// client is one running language server.
type client struct {
	// warmMu guards warmed, which says the project's other files have
	// been opened for this server. It is held across the walk, so a
	// second project-wide query waits rather than walking the tree again;
	// a walk that gave up on its caller's context does not set it, so the
	// next query tries again instead of asking a cold server.
	warmMu sync.Mutex
	warmed bool
	// synced is the content hash last sent per document, so a query does
	// not make the server re-parse a file that has not changed.
	synced map[string][32]byte
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	writeM sync.Mutex
	// broken is set once a frame reached the server in part. Nothing can
	// be written after that: the server is waiting inside a header it
	// will never be given the rest of.
	broken error
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
	// stalls counts requests that timed out with no answer since the
	// server last said anything (see hung). Atomic: the read loop resets it.
	stalls int32
	// pull is set when the server advertised textDocument/diagnostic;
	// resultIDs holds the last resultId per document for it.
	pull      bool
	resultIDs map[string]string
	// watchM guards the didChangeWatchedFiles registrations and the
	// events held until the first one arrives.
	watchM sync.Mutex
	regs   []registration
	queued []FileEvent
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
	// A language server runs the project's code (rust-analyzer builds
	// build.rs and proc macros; others load project plugins), so it gets
	// the environment every project-controlled child gets: without the
	// agent's provider credentials.
	cmd.Env = shell.ScrubEnv(os.Environ())
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
				"diagnostic":         map[string]any{"dynamicRegistration": false},
			},
			"workspace": map[string]any{"configuration": true, "workspaceFolders": true, "symbol": map[string]any{},
				"didChangeWatchedFiles": map[string]any{"dynamicRegistration": true, "relativePatternSupport": true}},
			"window": map[string]any{"workDoneProgress": true},
		},
		"initializationOptions": map[string]any{},
	}
	res, err := c.call(ictx, "initialize", params)
	if err != nil {
		c.close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	c.readCaps(res)
	if err := c.notifyCtx(ictx, "initialized", map[string]any{}); err != nil {
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
		err = c.notifyCtx(ctx, "textDocument/didOpen", map[string]any{"textDocument": map[string]any{
			"uri": uri, "languageId": languageID(abs), "version": version, "text": text}})
	} else {
		err = c.notifyCtx(ctx, "textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": version},
			"contentChanges": []map[string]any{{"text": text}},
		})
	}
	if err != nil {
		c.unsent(uri, version, open)
		return nil, false, err
	}
	// Servers that support didSave often run their heavier checks then.
	_ = c.notifyCtx(ctx, "textDocument/didSave", map[string]any{"textDocument": map[string]any{"uri": uri}})
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	if c.pull {
		// Ask instead of waiting: a pull server may never push. A pull
		// that fails (cancelled, unsupported for this file) falls back
		// to waiting for a push below.
		pctx, cancel := context.WithTimeout(ctx, timeout)
		items, ok := c.pullDiagnostics(pctx, uri, version)
		cancel()
		if ok {
			return items, true, nil
		}
	}
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
		case <-deadline.C:
			c.stall() // nothing at all came back for this edit
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
// so nothing it forked outlives the session. Every step is bounded: a
// wedged server must not hold the process open — close runs at exit.
func (c *client) close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = c.call(ctx, "shutdown", nil)
	// Best-effort: if the server stopped reading, the notification's
	// write abandons on the context instead of blocking the supervise.
	_ = c.notifyCtx(ctx, "exit", nil)
	procgroup.Stop(c.cmd, c.reaped, 2*time.Second)
}

func (c *client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	if _, ok := ctx.Deadline(); !ok && method != "shutdown" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultCallTimeout)
		defer cancel()
	}
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
	if err := c.sendCtx(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
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
		// Tell the server to drop the work, and count the silence: a
		// run of unanswered requests marks the server hung (see hung).
		cctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = c.notifyCtx(cctx, "$/cancelRequest", map[string]any{"id": id})
		cancel()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) && method != "shutdown" {
			c.stall()
		}
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

func (c *client) notifyCtx(ctx context.Context, method string, params any) error {
	msg := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		msg["params"] = params
	}
	return c.sendCtx(ctx, msg)
}

func (c *client) reply(id json.RawMessage, result any) {
	_ = c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

// replyTimeout bounds a write made from the read loop. The loop is the
// only thing draining the server's stdout, so a reply that blocks on a
// full stdin pipe deadlocks the pair: the server has stopped reading
// because it is blocked writing to a stdout nobody is draining, and we
// have stopped draining because we are blocked writing to its stdin.
// Both sides then wait forever, and close()'s shutdown never reaches the
// supervisor that would kill the process.
const replyTimeout = 10 * time.Second

// send writes a message the caller has no context for — a reply to a
// server-initiated request, from the read loop. It is bounded anyway.
func (c *client) send(msg any) error {
	ctx, cancel := context.WithTimeout(context.Background(), replyTimeout)
	defer cancel()
	return c.sendCtx(ctx, msg)
}

// sendCtx writes one LSP frame, bounded by ctx.
//
// The write used to run on a goroutine so the caller could give up on it.
// Giving up did not stop it: a pipe write has no deadline, so the write
// stayed parked with the lock held and completed later, when the server
// next drained its stdin — delivering a message whose sender had already
// been told it failed, behind whatever was written next. pipewrite pushes
// the pipe's write deadline into the past instead, so the write really
// ends and the byte count is the truth about what the server received.
//
// A frame that went out in part cannot be followed by another: the server
// is mid-header, and the next frame's bytes would be read as the rest of
// the one before. There is no recovery from that, so the stream is marked
// broken and stdin closed, which is what makes the server exit and the
// reaper report it.
func (c *client) sendCtx(ctx context.Context, msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	frame := append([]byte(fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))), body...)
	c.writeM.Lock()
	defer c.writeM.Unlock()
	if c.broken != nil {
		return c.broken
	}
	if _, err := pipewrite.Write(ctx, c.stdin, frame); err != nil {
		var pe *pipewrite.PartialError
		if errors.As(err, &pe) {
			c.broken = fmt.Errorf("lsp stdin is out of frame and cannot be reused: %w", err)
			c.stdin.Close()
			return c.broken
		}
		return err
	}
	return nil
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
			// since the process may keep running. A closed pipe is the
			// exit too: cmd.Wait closes stdout once the process is gone,
			// possibly while this read is still in it, and "file already
			// closed" must not take the place of the exit status and
			// stderr the reaper is about to report.
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, os.ErrClosed) {
				c.fail(err)
			}
			return
		}
		c.touch()
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
	case "client/registerCapability":
		c.register(params)
		c.reply(id, nil)
	case "client/unregisterCapability":
		c.unregister(params)
		c.reply(id, nil)
	case "window/workDoneProgress/create", "workspace/applyEdit":
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
		p.URI = canonicalURI(p.URI)
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
