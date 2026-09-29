package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/weburl"
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
	tokens  *TokenStore
	// authMeta is the resource_metadata a 401 pointed at.
	authMeta string

	mu        sync.Mutex
	sessionID string // Streamable HTTP session
	endpoint  string // legacy SSE POST endpoint
	lastHint  string
	closed    chan struct{}
	closeOnce sync.Once
	sseReady  chan error
}

func newHTTPTransport(c *Client, cfg ServerConfig, tokens *TokenStore) (*httpTransport, error) {
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
	client := &http.Client{Timeout: 0} // streams stay open; ctx bounds calls
	if weburl.PrivateTarget(u) == "" {
		// The URL names a public host, so it has no business reaching
		// this machine or its network: judge the address each
		// connection resolves to, as fetch_url does, so a name that
		// resolves (or later rebinds) to 169.254.169.254 or 127.0.0.1
		// never receives the server's headers or OAuth token. A URL
		// that spells out localhost or a private address is left alone:
		// whoever wrote it meant this machine.
		client.Transport = weburl.GuardDial(nil, true)
	}
	// Tool calls run under the turn's context, so what each sends to the
	// server lands in that turn's egress report, and a host the policy
	// blocks is refused before the arguments leave.
	client.Transport = egress.Transport(client.Transport, egress.KindMCP)
	return &httpTransport{
		c:       c,
		client:  client,
		url:     cfg.URL,
		headers: headers,
		mode:    mode,
		tokens:  tokens,
		closed:  make(chan struct{}),
	}, nil
}

// bearer returns the stored OAuth token for this server, refreshed when
// expired (or when force is set after a 401). An explicit Authorization
// header in the config wins.
func (t *httpTransport) bearer(ctx context.Context, force bool) (string, error) {
	if t.tokens == nil {
		return "", nil
	}
	for k := range t.headers {
		if strings.EqualFold(k, "Authorization") {
			return "", nil
		}
	}
	tok, ok, err := t.tokens.Bearer(ctx, t.client, t.url, force)
	if !ok {
		return "", nil
	}
	return tok, err
}

// unauthorized turns a 401 into ErrAuthRequired with a hint for the user,
// remembering where the server said its metadata lives.
func (t *httpTransport) unauthorized(resp *http.Response) error {
	hint := resourceMetadataHint(resp.Header.Get("WWW-Authenticate"))
	t.mu.Lock()
	if hint != "" {
		t.authMeta = hint
	}
	t.mu.Unlock()
	return &AuthError{Server: t.c.Name, ResourceMetadata: hint}
}

func (t *httpTransport) send(ctx context.Context, msg rpcMessage) error {
	t.mu.Lock()
	mode := t.mode
	t.mu.Unlock()
	switch mode {
	case "sse":
		return t.sendLegacy(ctx, msg)
	case "http":
		err := t.sendStreamable(ctx, msg)
		if t.dropped(ctx, err) {
			// Streamable HTTP has no stream whose end says the server is
			// gone: every request is its own connection. A connection
			// refused or reset, or a 404 for the session the server gave
			// us, is that end, so the client is closed like a stdio one
			// whose process exited, and the manager reconnects it.
			t.c.exited(err)
		}
		return err
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

// dropped reports whether err from an established Streamable HTTP
// connection means the server is gone rather than that one request
// failed: the network refused or reset it, or the server no longer knows
// the session it issued (the spec's 404 for a terminated session).
func (t *httpTransport) dropped(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	var se *statusError
	if errors.As(err, &se) {
		t.mu.Lock()
		sid := t.sessionID
		t.mu.Unlock()
		return se.code == http.StatusNotFound && sid != ""
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	var oe *net.OpError
	return errors.As(err, &oe) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET)
}

// do sends a request; on 401 with a stored token it refreshes the token
// and retries once (rebuilt by again), and turns a final 401 into
// ErrAuthRequired.
func (t *httpTransport) do(ctx context.Context, req *http.Request, again func() (*http.Request, error)) (*http.Response, error) {
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	if req.Header.Get("Authorization") != "" && t.tokens != nil {
		resp.Body.Close()
		if _, rerr := t.bearer(ctx, true); rerr == nil {
			if req, err = again(); err != nil {
				return nil, err
			}
			if resp, err = t.client.Do(req); err != nil {
				return nil, err
			}
			if resp.StatusCode != http.StatusUnauthorized {
				return resp, nil
			}
		} else {
			return nil, rerr
		}
	}
	err = t.unauthorized(resp)
	resp.Body.Close()
	return nil, err
}

// clipUTF8 renders a truncated response body as text. The read stopped at
// a byte count, which may fall inside a rune; a trailing partial rune
// would print as a replacement character in the error the user sees.
func clipUTF8(b []byte) string {
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return string(b)
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
	if tok, err := t.bearer(ctx, false); err != nil {
		return nil, err
	} else if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
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
	resp, err := t.do(ctx, req, func() (*http.Request, error) { return t.newRequest(ctx, http.MethodPost, t.url, data) })
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
		se := &statusError{code: resp.StatusCode, body: clipUTF8(body)}
		t.mu.Lock()
		t.lastHint = se.Error()
		t.mu.Unlock()
		return se
	}
	// A request's reply comes back on this response and nowhere else:
	// this client opens no standalone GET stream a server could redirect
	// it to. Once the response is used up, a call still waiting would
	// wait for good — a tools/call has no deadline but the user's
	// patience — so it is failed instead.
	isRequest := msg.Method != "" && msg.ID != nil
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		// The stream ends when the server has sent the reply (and any
		// requests or notifications it wanted to interleave).
		go func() {
			defer resp.Body.Close()
			t.readSSE(resp.Body, nil)
			if isRequest {
				t.c.unanswered(msg.ID, fmt.Errorf("%w: the server's event stream for %s ended without a reply", ErrDisconnected, msg.Method))
			}
		}()
		return nil
	}
	defer resp.Body.Close()
	if isRequest {
		defer t.c.unanswered(msg.ID, fmt.Errorf("the server's reply to %s carried no response to it", msg.Method))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFrameBytes))
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
	resp, err := t.do(ctx, req, func() (*http.Request, error) { return t.newRequest(ctx, http.MethodPost, endpoint, data) })
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &statusError{code: resp.StatusCode, body: clipUTF8(body)}
	}
	// Some servers answer the POST with the message itself instead of
	// using the stream; accept either.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxFrameBytes))
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
		go t.openSSE(t.sseReady)
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

// openSSE runs the long-lived GET stream for the legacy transport. ready
// is the latch ensureSSE is waiting on, passed in rather than read from
// the transport because a failure clears the field.
func (t *httpTransport) openSSE(ready chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		// Ends with the transport, or with this function: without the
		// second case the watcher stays parked on a channel that is only
		// closed by Close, so a transport abandoned without one leaks
		// this goroutine and its context for the life of the process.
		select {
		case <-t.closed:
			cancel()
		case <-ctx.Done():
		}
	}()
	// A stream that never opened is not a verdict on the server, only on
	// this attempt: a blip on the initial GET used to be latched in
	// sseReady and returned to every later caller for the rest of the
	// process, with no way to retry short of restarting the server.
	fail := func(err error) {
		t.mu.Lock()
		if t.sseReady == ready {
			t.sseReady = nil
		}
		t.mu.Unlock()
		ready <- err
	}
	req, err := t.newRequest(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		fail(err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := t.do(ctx, req, func() (*http.Request, error) {
		r, err := t.newRequest(ctx, http.MethodGet, t.url, nil)
		if r != nil {
			r.Header.Set("Accept", "text/event-stream")
		}
		return r, err
	})
	if err != nil {
		fail(err)
		return
	}
	if resp.StatusCode >= 300 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		fail(&statusError{code: resp.StatusCode, body: "not an SSE endpoint: " + clipUTF8(body)})
		return
	}
	defer resp.Body.Close()
	announced := false
	var refused error
	t.readSSE(resp.Body, func(event, data string) bool {
		if event != "endpoint" {
			return false
		}
		ep, err := sameOriginEndpoint(t.url, strings.TrimSpace(data))
		if err != nil {
			// Every POST carries the configured headers and the stored
			// OAuth token, so an endpoint elsewhere would hand this
			// server's credentials to whatever host the stream named.
			// One that was already announced stays; a first one that is
			// refused ends the attempt now rather than at the timeout.
			t.c.logf("ignoring SSE endpoint: %v", err)
			if !announced {
				refused = err
				cancel()
			}
			return true
		}
		t.mu.Lock()
		t.endpoint = ep
		t.mu.Unlock()
		if !announced {
			announced = true
			ready <- nil
		}
		return true
	})
	switch {
	case refused != nil:
		fail(refused)
	case !announced:
		fail(errors.New("SSE stream ended before announcing an endpoint"))
	}
	select {
	case <-t.closed:
	default:
		t.c.exited(errors.New("SSE stream closed"))
	}
}

// sameOriginEndpoint resolves the endpoint a legacy SSE stream announced
// against the server's URL and refuses one on another scheme, host or
// port.
func sameOriginEndpoint(serverURL, announced string) (string, error) {
	base, err := url.Parse(serverURL)
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(announced)
	if err != nil {
		return "", fmt.Errorf("bad endpoint %q: %w", announced, err)
	}
	ep := base.ResolveReference(ref)
	if urlOrigin(ep) != urlOrigin(base) {
		return "", fmt.Errorf("the server announced an endpoint on another origin (%s://%s), not %s://%s", ep.Scheme, ep.Host, base.Scheme, base.Host)
	}
	return ep.String(), nil
}

// urlOrigin is scheme://host:port, lower-cased, with the scheme's default
// port spelled out so "https://h" and "https://h:443" agree.
func urlOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port
}

// readSSE parses an event stream, handing "message" events to the client.
// onEvent, when set, sees every event first and returns true to consume it.
func (t *httpTransport) readSSE(r io.Reader, onEvent func(event, data string) bool) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<16), maxFrameBytes)
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
	// A stream that broke rather than ended leaves whatever it was
	// carrying unanswered; say so, since the caller only sees a timeout.
	if err := sc.Err(); err != nil {
		t.c.logf("event stream: %v", err)
	}
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
