// Package httpstream is the call loop shared by the providers that speak
// a streamed HTTP protocol of their own (the Gemini API, the OpenAI
// Responses API): one attempt per watchdog, the shared retry policy, key
// rotation on a refused key, pacing after rate limits, and a resend when
// the server teaches us a parameter it does not take. A provider supplies
// only its wire format, as a Wire.
//
// The OpenAI chat-completions and Anthropic providers predate it and keep
// their own loops; this is the same policy, factored once for the
// providers added after them rather than copied into each.
package httpstream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// Wire is one provider's protocol.
type Wire interface {
	// Send builds the request for req and posts it with key. A non-2xx
	// answer is returned as *APIError (Post does that).
	Send(ctx context.Context, req llm.Request, key string) (*http.Response, error)
	// Read assembles the streamed body into a response, ticking wd for
	// every frame. A body that ends before the protocol's terminal frame
	// is an llm.Truncate error, so the call is retried whole.
	Read(body io.Reader, wd *llm.Watchdog, emit func(llm.Event)) (*llm.Response, error)
	// Learn reads a refusal for a parameter the server does not take and
	// reports whether Send will now leave it out, so the request should be
	// resent at once without spending a retry.
	Learn(err error) bool
}

// Client runs calls over a Wire.
type Client struct {
	Wire       Wire
	Ring       *llm.KeyRing
	MaxRetries int
	Limits     llm.StreamLimits
	pace       *llm.Pacer
}

// New builds a client. maxRetries follows the providers' convention:
// zero means the default of 2, negative means none.
func New(w Wire, ring *llm.KeyRing, maxRetries int, limits llm.StreamLimits) *Client {
	switch {
	case maxRetries == 0:
		maxRetries = 2
	case maxRetries < 0:
		maxRetries = 0
	}
	return &Client{Wire: w, Ring: ring, MaxRetries: maxRetries, Limits: limits, pace: &llm.Pacer{}}
}

// ForTask shares the wire and the key ring but not the pacer: one task
// being refused must not slow the others (see llm.PerTask).
func (c *Client) ForTask() *Client {
	cp := *c
	cp.pace = &llm.Pacer{}
	return &cp
}

// Stream makes one model call.
func (c *Client) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	if emit == nil {
		emit = func(llm.Event) {}
	}
	parent := ctx
	pendingGap := c.pace.Gap()
	rt := llm.NewRetrier(c.MaxRetries, emit, nil)
	callStart := time.Now()
	rotations := 0
	for {
		ctx, wd := llm.WatchSince(parent, c.Limits, callStart)
		rt.ResetWatchdog(wd)
		wd.Expect(pendingGap)
		if err := c.pace.Wait(ctx); err != nil {
			wd.Stop()
			return nil, wd.Wrap(err)
		}
		var resp *http.Response
		retryWhole := false
		for {
			key, idx := c.Ring.Current()
			var err error
			resp, err = c.Wire.Send(ctx, req, key)
			if err == nil {
				c.Ring.OK(idx)
				rt.Landed()
				break
			}
			if stall := wd.Err(); stall != nil && parent.Err() == nil {
				// The watchdog killed the wait for headers: judge the
				// stall, and retry it under a fresh watchdog.
				wd.Stop()
				if llm.LifetimeStalled(stall) {
					return nil, stall
				}
				if werr := rt.Wait(parent, stall); werr != nil {
					return nil, werr
				}
				retryWhole = true
				break
			}
			if c.Wire.Learn(err) {
				continue
			}
			if f, ok := KeyProblem(err); ok && c.Ring.Fail(idx, f) && rotations < c.Ring.Len()-1 {
				rotations++
				continue
			}
			var ae *APIError
			if errors.As(err, &ae) && (ae.Status == 429 || ae.Status == 529) && !llm.PermanentLimit(llm.ClassifyText(ae)) {
				c.pace.Refused()
				pendingGap = c.pace.Gap()
			}
			if werr := rt.Wait(parent, err); werr != nil {
				wd.Stop()
				return nil, wd.Wrap(werr)
			}
		}
		if retryWhole {
			continue
		}
		c.pace.Served()
		out, err := c.Wire.Read(resp.Body, wd, emit)
		resp.Body.Close()
		if err == nil {
			wd.Stop()
			return out, nil
		}
		stalled := wd.Err() != nil
		if stalled {
			err = wd.Wrap(err)
		}
		wd.Stop()
		if parent.Err() != nil || llm.LifetimeStalled(wd.Err()) {
			return nil, wd.Wrap(err)
		}
		if llm.Truncated(err) || stalled {
			// The stream never answered: its deltas are void, and the
			// request is sent again whole over the intact history.
			emit(llm.Event{Type: llm.EventStreamDiscard})
			if werr := rt.Wait(parent, err); werr != nil {
				return nil, wd.Wrap(werr)
			}
			continue
		}
		return nil, wd.Wrap(err)
	}
}

// APIError is a non-2xx answer.
type APIError struct {
	Status int
	// Message is the server's text, redacted and capped for display.
	Message string
	// Detail is the same text uncapped, for classification only.
	Detail     string
	RetryAfter time.Duration
}

func (e *APIError) Error() string            { return fmt.Sprintf("%s (HTTP %d)", e.Message, e.Status) }
func (e *APIError) HTTPStatus() int          { return e.Status }
func (e *APIError) RetryHint() time.Duration { return e.RetryAfter }

// ErrorDetail implements llm.DetailedError.
func (e *APIError) ErrorDetail() string {
	if e.Detail != "" {
		return e.Detail
	}
	return e.Message
}

// KeyProblem reports whether another key might avoid err, and how the key
// ring should judge this one.
func KeyProblem(err error) (llm.Failure, bool) {
	var ae *APIError
	if !errors.As(err, &ae) {
		return 0, false
	}
	full := ae.ErrorDetail()
	msg := strings.ToLower(full)
	switch {
	case ae.Status == 429 || ae.Status == 401 || ae.Status == 403 || ae.Status == 402:
	case strings.Contains(msg, "quota") || strings.Contains(msg, "rate limit"):
	default:
		return 0, false
	}
	return llm.ClassifyFailure(ae.Status, full), true
}

// Post sends a JSON body to an SSE endpoint. headers carry the
// credentials, which never go in the URL, so an error that quotes the URL
// quotes no key. A non-2xx answer becomes *APIError with the server's
// message pulled out of the usual {"error":{"message":...}} envelope (or
// a list of them, which is how Gemini's streaming endpoint answers).
func Post(ctx context.Context, hc *http.Client, url string, body []byte, headers map[string]string) (*http.Response, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	hreq.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		if v != "" {
			hreq.Header.Set(k, v)
		}
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	msg := ErrorMessage(raw)
	detail := llm.SafeErrorDetail(resp.StatusCode, msg)
	msg = llm.SafeErrorMessage(resp.StatusCode, msg)
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return nil, &APIError{Status: resp.StatusCode, Message: msg, Detail: detail, RetryAfter: llm.RetryAfterFrom(resp.Header, resp.StatusCode)}
}

// ErrorMessage pulls the message out of an error body, or returns the
// body itself.
func ErrorMessage(raw []byte) string {
	type env struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var one env
	if json.Unmarshal(raw, &one) == nil && one.Error != nil && one.Error.Message != "" {
		return one.Error.Message
	}
	var many []env
	if json.Unmarshal(raw, &many) == nil && len(many) > 0 && many[0].Error != nil && many[0].Error.Message != "" {
		return many[0].Error.Message
	}
	return strings.TrimSpace(string(raw))
}

// ReadSSE calls frame for every data frame, with the event name that
// preceded it ("" when none did), until frame reports the terminal frame.
// A body that ends first is an llm.Truncate error, naming whatever the
// server sent instead of SSE when that was all it sent.
//
// An event may carry its data over several data: lines, which SSE joins
// with newlines and hands on at the blank line that ends the event. A
// frame is also handed on as soon as what has been gathered is a whole
// JSON value, so a server that never sends the blank line still has
// every frame read as it arrives.
func ReadSSE(r io.Reader, wd *llm.Watchdog, name string, frame func(event string, data []byte) (done bool, err error)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var preamble strings.Builder
	event := ""
	var data []string
	frames := 0
	// flush hands on the event gathered so far, if there is one.
	flush := func() (bool, error) {
		payload := strings.TrimSpace(strings.Join(data, "\n"))
		ev := event
		data = nil
		if payload == "" || payload == "[DONE]" {
			return false, nil
		}
		event = ""
		frames++
		return frame(ev, []byte(payload))
	}
	for sc.Scan() {
		wd.Tick()
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		case strings.HasPrefix(line, "data:"):
		case strings.TrimSpace(line) == "":
			done, err := flush()
			if err != nil || done {
				return err
			}
			event = ""
			continue
		default:
			if t := strings.TrimSpace(line); !strings.HasPrefix(t, ":") && preamble.Len() < 400 {
				preamble.WriteString(t)
				preamble.WriteByte('\n')
			}
			continue
		}
		data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		if joined := strings.Join(data, "\n"); strings.TrimSpace(joined) != "[DONE]" && !json.Valid([]byte(joined)) {
			continue
		}
		done, err := flush()
		if err != nil || done {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return llm.Truncate(fmt.Errorf("%s: stream read: %w", name, err))
	}
	if done, err := flush(); err != nil || done {
		return err
	}
	if frames == 0 {
		if body := strings.TrimSpace(preamble.String()); body != "" {
			return llm.Truncate(fmt.Errorf("%s: no stream frames from server: %s", name, llm.BoundErrorMessage(body)))
		}
		return llm.Truncate(fmt.Errorf("%s: empty stream from server", name))
	}
	return llm.Truncate(fmt.Errorf("%s: %w", name, ErrEndedEarly))
}

// ErrEndedEarly is the body ending with no frame having reported itself
// terminal. A protocol whose last frame is not marked (Gemini's) checks
// for it and decides from what it saw whether the answer was complete.
var ErrEndedEarly = errors.New("stream ended before the response was complete")

// HTTPClient is the client both providers use unless a test gives one:
// headers bounded, the body left to the watchdog, redirects that could
// leak the key refused.
func HTTPClient(hc *http.Client) *http.Client {
	if hc == nil {
		hc = &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: 2 * time.Minute, Proxy: http.ProxyFromEnvironment}}
	}
	return llm.SafeRedirectClient(hc)
}
