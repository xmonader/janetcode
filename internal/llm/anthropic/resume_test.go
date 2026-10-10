package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// Over the wire: a Messages stream that dies mid-reply is continued with
// the partial as a trailing assistant message (prefill), and a prefill
// the server refuses is answered by regenerating the reply.
func TestResumePrefillsOverTheWire(t *testing.T) {
	partial := strings.Repeat("abcd ", 50)
	for _, refuse := range []bool{false, true} {
		var calls int
		var second struct {
			Messages []struct {
				Role    string `json:"role"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "text/event-stream")
			switch calls {
			case 1:
				io.WriteString(w, event("message_start", `{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"usage":{"input_tokens":1,"output_tokens":1}}}`))
				io.WriteString(w, event("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`))
				io.WriteString(w, event("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"`+partial+`"}}`))
				w.(http.Flusher).Flush()
				panic(http.ErrAbortHandler)
			case 2:
				body, _ := io.ReadAll(r.Body)
				json.Unmarshal(body, &second)
				if refuse {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(400)
					io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"prefill is not supported"}}`)
					return
				}
				io.WriteString(w, fullTurn(" done."))
			default:
				io.WriteString(w, fullTurn("fresh"))
			}
		})
		resp, err := llm.Resume(p, llm.ResumeConfig{}).Stream(context.Background(),
			llm.Request{Model: "m", MaxTokens: 4096, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
		if err != nil {
			t.Fatalf("refuse=%v err = %v", refuse, err)
		}
		last := second.Messages[len(second.Messages)-1]
		if last.Role != "assistant" || len(last.Content) != 1 || last.Content[0].Text != strings.TrimRight(partial, " ") {
			t.Fatalf("refuse=%v: last message = %+v", refuse, last)
		}
		want := strings.TrimRight(partial, " ") + " done."
		if refuse {
			want = "fresh"
		}
		if resp.Message.Text() != want {
			t.Fatalf("refuse=%v: text = %q", refuse, resp.Message.Text())
		}
	}
}
