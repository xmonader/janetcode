package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// A model that will not take the tier asked for will usually take the
// one below it, and the difference between tiers is how hard it thinks,
// not whether the request is valid — so the refusal is worth one clamp
// rather than a failed turn.
func TestEffortIsClampedByRankWhenRefused(t *testing.T) {
	var efforts []string
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		json.Unmarshal(raw, &body)
		efforts = append(efforts, body.OutputConfig.Effort)
		if body.OutputConfig.Effort == "high" {
			http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"effort: high is not supported by this model"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
		io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`))
		io.WriteString(w, event("content_block_stop", `{"type":"content_block_stop","index":0}`))
		io.WriteString(w, event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`))
		io.WriteString(w, event("message_stop", `{"type":"message_stop"}`))
	})
	req := llm.Request{Model: "m", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}
	resp, err := p.Stream(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "ok" {
		t.Fatalf("text = %q", resp.Message.Text())
	}
	if len(efforts) != 2 || efforts[0] != "high" || efforts[1] != "medium" {
		t.Fatalf("efforts tried = %v, want high then medium", efforts)
	}
	// The clamp is remembered, so the refusal is paid for once.
	efforts = nil
	if _, err := p.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if len(efforts) != 1 || efforts[0] != "medium" {
		t.Fatalf("second call tried %v", efforts)
	}
}

func TestClampEffortWalksDownAndThenStops(t *testing.T) {
	p, err := New(Config{APIKey: "k", Effort: "medium"})
	if err != nil {
		t.Fatal(err)
	}
	refusal := &APIError{Status: 400, Message: "effort not supported"}
	if !p.clampEffort(refusal) || p.effortFor("") != "low" {
		t.Fatalf("first clamp = %q", p.effortFor(""))
	}
	if !p.clampEffort(refusal) || p.effortFor("") != "" {
		t.Fatalf("second clamp = %q, want the parameter dropped", p.effortFor(""))
	}
	if p.clampEffort(refusal) {
		t.Fatal("there is nothing left to clamp; the error must be reported")
	}
	// Other failures are not effort problems.
	if p.clampEffort(&APIError{Status: 400, Message: "max_tokens is too large"}) {
		t.Fatal("an unrelated 400 was treated as an effort refusal")
	}
	if p.clampEffort(&APIError{Status: 500, Message: "effort"}) {
		t.Fatal("a 500 was treated as an effort refusal")
	}
}

// The request's own tier wins over the one the provider was built with,
// and a clamp learned for one tier must not silence another: a person
// who lowers the tier after a refusal has asked for something the model
// may well take, so it is tried rather than answered from the clamp.
func TestRequestEffortWinsOverTheClamp(t *testing.T) {
	var efforts []string
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body struct {
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		json.Unmarshal(raw, &body)
		efforts = append(efforts, body.OutputConfig.Effort)
		if body.OutputConfig.Effort == "max" {
			http.Error(w, `{"type":"error","error":{"type":"invalid_request_error","message":"effort: max is not supported by this model"}}`, 400)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
		io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
		io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`))
		io.WriteString(w, event("content_block_stop", `{"type":"content_block_stop","index":0}`))
		io.WriteString(w, event("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`))
		io.WriteString(w, event("message_stop", `{"type":"message_stop"}`))
	})
	msgs := []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}
	// Asked for max, refused, served xhigh; the clamp is remembered.
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: msgs, Effort: "max"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(efforts) != 2 || efforts[0] != "max" || efforts[1] != "xhigh" {
		t.Fatalf("efforts tried = %v, want max then xhigh", efforts)
	}
	efforts = nil
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: msgs, Effort: "max"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(efforts) != 1 || efforts[0] != "xhigh" {
		t.Fatalf("the clamp was not remembered: %v", efforts)
	}
	// A manual change wins: low is asked for as low, not as the clamp.
	efforts = nil
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: msgs, Effort: "low"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(efforts) != 1 || efforts[0] != "low" {
		t.Fatalf("a manual change did not win over the clamp: %v", efforts)
	}
	// And asking for the refused tier again tries it again, honestly.
	efforts = nil
	if _, err := p.Stream(context.Background(), llm.Request{Model: "m", Messages: msgs, Effort: "max"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(efforts) != 2 || efforts[0] != "max" {
		t.Fatalf("efforts tried = %v, want max retried then clamped", efforts)
	}
}
