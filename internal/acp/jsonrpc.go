package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// ProtocolVersion is the ACP major version this adapter speaks.
const ProtocolVersion = 1

// JSON-RPC error codes used here; the first four are from the spec.
const (
	codeParse          = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// message is one JSON-RPC frame in either direction. A request has Method
// and ID, a notification has Method alone, a response has ID with Result
// or Error.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`

	// seq is the order readLoop read the frame in, counting from 1 (0 is
	// a frame that did not come through readLoop, and has no order). The
	// handlers run on goroutines of their own, so the order they happen
	// to start in is not the order the client sent them; seq is what
	// says a session/cancel came after a session/prompt.
	seq uint64
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("acp: %s (%d)", e.Message, e.Code) }

// conn is one ACP connection: newline-delimited JSON-RPC over a pair of
// streams. Nothing but ACP messages may reach out, so every writer goes
// through send, which holds the lock for exactly one line.
type conn struct {
	out io.Writer
	mu  sync.Mutex // serialises writes; one message per line

	// pendMu guards nextID and pend, the calls waiting for a reply from
	// the client.
	pendMu sync.Mutex
	nextID int
	pend   map[string]chan message
	// closed is closed once the input stream has ended, so a call still
	// waiting for a reply that can no longer come stops waiting. Without
	// it a permission request outstanding when the editor closed the pipe
	// waited for ever, and readLoop waited for it: the adapter hung
	// instead of shutting down.
	closed    chan struct{}
	closeOnce sync.Once
}

// ErrDisconnected is what a call that was waiting for the client gets when
// the client's stream ended first.
var ErrDisconnected = errors.New("acp: the client closed the connection")

func newConn(out io.Writer) *conn {
	return &conn{out: out, pend: map[string]chan message{}, closed: make(chan struct{})}
}

// stop ends the connection for anything waiting on it.
func (c *conn) stop() {
	c.closeOnce.Do(func() { close(c.closed) })
}

// send writes one message as a single line. json.Marshal escapes newlines
// inside strings, so a frame never contains an embedded newline.
func (c *conn) send(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.out.Write(append(b, '\n')); err != nil {
		return err
	}
	if f, ok := c.out.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// notify sends a notification, which expects no reply.
func (c *conn) notify(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.send(message{Method: method, Params: raw})
}

// call sends a request and waits for its response, or for ctx to end.
func (c *conn) call(ctx context.Context, method string, params any, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	c.pendMu.Lock()
	c.nextID++
	id := strconv.Itoa(c.nextID)
	ch := make(chan message, 1)
	c.pend[id] = ch
	c.pendMu.Unlock()
	defer func() {
		c.pendMu.Lock()
		delete(c.pend, id)
		c.pendMu.Unlock()
	}()

	if err := c.send(message{ID: json.RawMessage(id), Method: method, Params: raw}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return ErrDisconnected
	case m := <-ch:
		if m.Error != nil {
			return m.Error
		}
		if result == nil || len(m.Result) == 0 {
			return nil
		}
		return json.Unmarshal(m.Result, result)
	}
}

// resolve hands a response to the caller waiting on its id.
func (c *conn) resolve(m message) {
	id := string(m.ID)
	if s, err := strconv.Unquote(id); err == nil {
		id = s
	}
	c.pendMu.Lock()
	ch := c.pend[id]
	c.pendMu.Unlock()
	if ch != nil {
		ch <- m
	}
}

func (c *conn) reply(id json.RawMessage, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return c.replyError(id, codeInternal, err.Error())
	}
	return c.send(message{ID: id, Result: raw})
}

func (c *conn) replyError(id json.RawMessage, code int, msg string) error {
	return c.send(message{ID: id, Error: &rpcError{Code: code, Message: msg}})
}

// readLoop reads frames until the stream ends or ctx does, handing
// requests and notifications to handle and responses to the caller that
// is waiting. Each request runs in its own goroutine: a prompt takes
// minutes, and session/cancel has to arrive while it is running. When the
// input ends, the loop waits for the handlers still running — a client
// that writes its requests and closes the pipe must still get their
// answers.
//
// The reading itself happens on a goroutine of its own, because a Read
// on stdin cannot be interrupted: with the scanner in the loop, a
// cancelled ctx (SIGTERM) was never noticed until the editor happened to
// close the pipe, and the adapter outlived the signal meant to stop it.
// The handlers see the same ctx, so they end promptly too.
func (c *conn) readLoop(ctx context.Context, in io.Reader, handle func(context.Context, message)) error {
	var wg sync.WaitGroup
	// Deferred first, so it runs last: the handlers still in flight are
	// woken by stop before anything waits for them.
	defer wg.Wait()
	defer c.stop()

	lines := make(chan []byte)
	readErr := make(chan error, 1)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		sc := bufio.NewScanner(in)
		sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
		for sc.Scan() {
			// The scanner reuses its buffer; the line is handed on.
			line := append([]byte(nil), sc.Bytes()...)
			select {
			case lines <- line:
			case <-quit:
				return
			}
		}
		readErr <- sc.Err()
	}()

	var seq uint64
	for {
		var line []byte
		select {
		case <-ctx.Done():
			return nil
		case err := <-readErr:
			if err != nil && !errors.Is(err, io.EOF) {
				return err
			}
			return nil
		case line = <-lines:
		}
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			// The id could not be read, and JSON-RPC says the reply then
			// carries id null rather than none at all.
			_ = c.replyError(json.RawMessage("null"), codeParse, "invalid JSON: "+err.Error())
			continue
		}
		if m.Method == "" {
			c.resolve(m)
			continue
		}
		seq++
		m.seq = seq
		wg.Add(1)
		go func(m message) {
			defer wg.Done()
			handle(ctx, m)
		}(m)
	}
}
