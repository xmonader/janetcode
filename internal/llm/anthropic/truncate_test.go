package anthropic

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func fullTurn(name string) string {
	return event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`) +
		event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`) +
		event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+name+`"}}`) +
		event("content_block_stop", `{"type":"content_block_stop","index":0}`) +
		event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":2}}`) +
		event("message_stop", `{"type":"message_stop"}`)
}

// A stream that dies partway through — the model was at 95% when the
// connection dropped — never produced an answer, so the whole request is
// retried as one fresh attempt over the intact history. The partial
// deltas were only ever events; the retried response replaces them.
func TestTruncatedStreamIsRetried(t *testing.T) {
	var calls int
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
			io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half an"}}`))
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // connection dies mid-answer
		}
		io.WriteString(w, fullTurn("full answer"))
	})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
	if resp.Message.Text() != "full answer" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
}

// A stream that ends cleanly but never reaches message_stop is a death
// without a terminal frame too, not a half answer to accept silently.
func TestStreamEndingWithoutTerminalFrameIsRetried(t *testing.T) {
	var calls int
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "text/event-stream")
		if calls == 1 {
			io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
			io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"half"}}`))
			return // handler ends: clean EOF, no message_stop
		}
		io.WriteString(w, fullTurn("whole"))
	})
	resp, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || resp.Message.Text() != "whole" {
		t.Fatalf("calls = %d text = %q", calls, resp.Message.Text())
	}
}

// A failure past a real refusal is still final: a stream the server
// answered with an error status is not given a second attempt.
func TestStatusRefusalIsStillFinal(t *testing.T) {
	var calls int
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(401)
		io.WriteString(w, `{"type":"error","error":{"type":"authentication_error","message":"no"}}`)
	})
	_, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") || calls != 1 {
		t.Fatalf("err = %v calls = %d", err, calls)
	}
}
