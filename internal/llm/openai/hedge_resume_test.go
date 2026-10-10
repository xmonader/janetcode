package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// Over the wire: a stream that breaks after real text is continued with
// the partial as the last assistant message marked "prefix" (DeepSeek's
// prefix completion), and the caller gets one whole reply.
func TestResumeSendsPrefixOverTheWire(t *testing.T) {
	partial := strings.Repeat("abcd ", 50)
	var calls int32
	var second map[string]any
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomicAdd(&calls)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": partial}}}})
			io.WriteString(w, "data: "+string(b)+"\n\n")
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler) // the connection dies mid-reply
		}
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &second)
		io.WriteString(w, sse(`{"choices":[{"delta":{"content":" done."},"finish_reason":"stop"}]}`))
	})
	resp, err := llm.Resume(p, llm.ResumeConfig{KeepsThinking: true}).Stream(context.Background(),
		llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	want := strings.TrimRight(partial, " ") + " done."
	if calls != 2 || resp.Message.Text() != want {
		t.Fatalf("calls = %d text = %q", calls, resp.Message.Text())
	}
	msgs, _ := second["messages"].([]any)
	last, _ := msgs[len(msgs)-1].(map[string]any)
	if last["role"] != "assistant" || last["prefix"] != true || last["content"] != strings.TrimRight(partial, " ") {
		t.Fatalf("last message = %v", last)
	}
}

// An ordinary request never carries the flag: a strict server would
// reject a field it does not know.
func TestNoPrefixWithoutPrefill(t *testing.T) {
	w, err := buildRequest(llm.Request{Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "hi"), llm.TextMessage(llm.RoleAssistant, "partial")}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(w)
	if strings.Contains(string(b), `"prefix"`) {
		t.Fatalf("request = %s", b)
	}
}

// Over the wire: a route that has not started answering by the hedge
// delay is raced by a second server, and the second one's answer is the
// call's.
func TestHedgeAcrossTwoServers(t *testing.T) {
	var slowCalls, fastCalls atomic.Int32
	release := make(chan struct{})
	defer close(release)
	slow := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		slowCalls.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	fast := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		fastCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, sse(`{"model":"fast","choices":[{"delta":{"content":"hedged"},"finish_reason":"stop"}]}`))
	})
	began := time.Now()
	resp, err := llm.Hedge(slow, fast, llm.HedgeConfig{After: 50 * time.Millisecond}).Stream(context.Background(),
		llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil || resp.Message.Text() != "hedged" {
		t.Fatalf("resp = %+v err = %v", resp, err)
	}
	if time.Since(began) > 3*time.Second || slowCalls.Load() != 1 || fastCalls.Load() != 1 {
		t.Fatalf("took %s, slow %d fast %d", time.Since(began), slowCalls.Load(), fastCalls.Load())
	}
}
