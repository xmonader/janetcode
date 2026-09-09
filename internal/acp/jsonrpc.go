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

	pendMu sync.Mutex
	nextID int
	pend   map[string]chan message
}

func newConn(out io.Writer) *conn {
	return &conn{out: out, pend: map[string]chan message{}}
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

// readLoop reads frames until the stream ends, handing requests and
// notifications to handle and responses to the caller that is waiting.
// Each request runs in its own goroutine: a prompt takes minutes, and
// session/cancel has to arrive while it is running. When the input ends,
// the loop waits for the handlers still running — a client that writes
// its requests and closes the pipe must still get their answers.
func (c *conn) readLoop(ctx context.Context, in io.Reader, handle func(context.Context, message)) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			_ = c.replyError(nil, codeParse, "invalid JSON: "+err.Error())
			continue
		}
		if m.Method == "" {
			c.resolve(m)
			continue
		}
		wg.Add(1)
		go func(m message) {
			defer wg.Done()
			handle(ctx, m)
		}(m)
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
