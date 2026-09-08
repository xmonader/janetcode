package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// httpTransport talks to a remote server. It starts with Streamable HTTP
// (every message is a POST whose reply is JSON or an SSE stream) and, when
// the server rejects that, falls back to the older SSE transport (one GET
// stream for replies, POSTs to the endpoint the stream announces).
type httpTransport struct {
	c       *Client
	client  *http.Client
	url     string
	headers map[string]string
	mode    string // "", "http" or "sse" once known

	mu        sync.Mutex
	sessionID string // Streamable HTTP session
	endpoint  string // legacy SSE POST endpoint
	lastHint  string
	closed    chan struct{}
	closeOnce sync.Once
	sseReady  chan error
}

func newHTTPTransport(c *Client, cfg ServerConfig) (*httpTransport, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("bad url %q", cfg.URL)
	}
	headers := map[string]string{}
	for k, v := range cfg.Headers {
		headers[k] = os.ExpandEnv(v)
	}
	mode := strings.ToLower(cfg.Type)
	if mode != "http" && mode != "sse" {
		mode = ""
	}
	return &httpTransport{
		c:       c,
		client:  &http.Client{Timeout: 0}, // streams stay open; ctx bounds calls
		url:     cfg.URL,
		headers: headers,
		mode:    mode,
		closed:  make(chan struct{}),
	}, nil
}

func (t *httpTransport) send(ctx context.Context, msg rpcMessage) error {
	t.mu.Lock()
	mode := t.mode
	t.mu.Unlock()
	switch mode {
	case "sse":
		return t.sendLegacy(ctx, msg)
	case "http":
		return t.sendStreamable(ctx, msg)
	}
	// Unknown yet: try Streamable HTTP; 404/405 means the old transport.
	err := t.sendStreamable(ctx, msg)
	var se *statusError
	if errors.As(err, &se) && (se.code == http.StatusNotFound || se.code == http.StatusMethodNotAllowed) {
		t.mu.Lock()
		t.mode = "sse"
		t.mu.Unlock()
		return t.sendLegacy(ctx, msg)
	}
	if err == nil {
		t.mu.Lock()
		t.mode = "http"
		t.mu.Unlock()
	}
	return err
}

type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.code, strings.TrimSpace(e.body))
}

func (t *httpTransport) newRequest(ctx context.Context, method, target string, body []byte) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion)
	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
	t.mu.Lock()
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.mu.Unlock()
	return req, nil
}

// sendStreamable POSTs the message and dispatches whatever the reply
// carries: a JSON message, or an SSE stream of messages until it ends.
func (t *httpTransport) sendStreamable(ctx context.Context, msg rpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	req, err := t.newRequest(ctx, http.MethodPost, t.url, data)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.mu.Lock()
		t.sessionID = sid
		t.mu.Unlock()
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		resp.Body.Close()
		return nil
	}
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		se := &statusError{code: resp.StatusCode, body: string(body)}
		t.mu.Lock()
		t.lastHint = se.Error()
		t.mu.Unlock()
		return se
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		// The stream ends when the server has sent the reply (and any
		// requests or notifications it wanted to interleave).
		go func() {
			defer resp.Body.Close()
			t.readSSE(resp.Body, nil)
		}()
		return nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) == 0 {
		return nil
	}
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("[")) {
		var batch []json.RawMessage
		if err := json.Unmarshal(body, &batch); err != nil {
			return fmt.Errorf("bad batch reply: %w", err)
		}
		for _, m := range batch {
			t.c.dispatchRaw(m)
		}
		return nil
	}
	t.c.dispatchRaw(body)
	return nil
}

// sendLegacy opens the SSE stream on first use, then POSTs to the endpoint
// the server announced. Replies arrive on the stream.
func (t *httpTransport) sendLegacy(ctx context.Context, msg rpcMessage) error {
	if err := t.ensureSSE(ctx); err != nil {
		return err
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	t.mu.Lock()
	endpoint := t.endpoint
	t.mu.Unlock()
	req, err := t.newRequest(ctx, http.MethodPost, endpoint, data)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &statusError{code: resp.StatusCode, body: string(body)}
	}
	// Some servers answer the POST with the message itself instead of
	// using the stream; accept either.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		if len(bytes.TrimSpace(body)) > 0 {
			t.c.dispatchRaw(body)
		}
	}
	return nil
}

func (t *httpTransport) ensureSSE(ctx context.Context) error {
	t.mu.Lock()
	if t.endpoint != "" {
		t.mu.Unlock()
		return nil
	}
	if t.sseReady == nil {
		t.sseReady = make(chan error, 1)
		go t.openSSE()
	}
	ready := t.sseReady
	t.mu.Unlock()
	select {
	case err := <-ready:
		ready <- err // let later callers see the same outcome
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// openSSE runs the long-lived GET stream for the legacy transport.
func (t *httpTransport) openSSE() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-t.closed
		cancel()
	}()
	req, err := t.newRequest(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		t.sseReady <- err
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := t.client.Do(req)
	if err != nil {
		t.sseReady <- err
		return
	}
	if resp.StatusCode >= 300 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		t.sseReady <- &statusError{code: resp.StatusCode, body: "not an SSE endpoint: " + string(body)}
		return
	}
	defer resp.Body.Close()
	announced := false
	t.readSSE(resp.Body, func(event, data string) bool {
		if event != "endpoint" {
			return false
		}
		ep := strings.TrimSpace(data)
		if base, err := url.Parse(t.url); err == nil {
			if ref, err := url.Parse(ep); err == nil {
				ep = base.ResolveReference(ref).String()
			}
		}
		t.mu.Lock()
		t.endpoint = ep
		t.mu.Unlock()
		if !announced {
			announced = true
			t.sseReady <- nil
		}
		return true
	})
	if !announced {
		t.sseReady <- errors.New("SSE stream ended before announcing an endpoint")
	}
	select {
	case <-t.closed:
	default:
		t.c.exited(errors.New("SSE stream closed"))
	}
}

// readSSE parses an event stream, handing "message" events to the client.
// onEvent, when set, sees every event first and returns true to consume it.
func (t *httpTransport) readSSE(r io.Reader, onEvent func(event, data string) bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), 64<<20)
	var (
		event string
		data  []string
	)
	flush := func() {
		if len(data) == 0 {
			event, data = "", nil
			return
		}
		payload := strings.Join(data, "\n")
		ev := event
		event, data = "", nil
		if onEvent != nil && onEvent(ev, payload) {
			return
		}
		if ev == "" || ev == "message" {
			t.c.dispatchRaw([]byte(payload))
		}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
			// comment / keepalive
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	flush()
}

func (t *httpTransport) close() {
	t.closeOnce.Do(func() { close(t.closed) })
	t.mu.Lock()
	sid, mode := t.sessionID, t.mode
	t.mu.Unlock()
	if mode == "http" && sid != "" {
		// Tell the server the session is over; failure is harmless.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if req, err := t.newRequest(ctx, http.MethodDelete, t.url, nil); err == nil {
			if resp, err := t.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	}
}

func (t *httpTransport) hint() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastHint != "" {
		return "\nlast response: " + t.lastHint
	}
	return ""
}
