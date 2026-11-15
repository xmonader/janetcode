package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

func reply(w http.ResponseWriter, path string) {
	w.Header().Set("Content-Type", "text/event-stream")
	frames := []string{}
	switch {
	case strings.HasSuffix(path, "/messages"):
		frames = []string{
			`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"test","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}`,
			`{"type":"message_stop"}`,
		}
	case strings.HasSuffix(path, "/responses"):
		frames = []string{`{"type":"response.output_text.delta","delta":"ok"}`, `{"type":"response.completed","response":{"status":"completed","output":[]}}`}
	case strings.Contains(path, ":streamGenerateContent"):
		frames = []string{`{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}`}
	default:
		frames = []string{`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `[DONE]`}
	}
	for _, f := range frames {
		var event struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(f), &event)
		if event.Type != "" {
			fmt.Fprintf(w, "event: %s\n", event.Type)
		}
		fmt.Fprintf(w, "data: %s\n\n", f)
	}
}

func TestNativeProtocolsAndGatewayHeaders(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		for _, name := range []string{"opencode-go", "opencode-zen"} {
			t.Run(fmt.Sprintf("%s/legacy=%v", name, legacy), func(t *testing.T) {
				for _, model := range []string{"kimi-k3", "gpt-5.6-luna", "grok-4.7", "muse-spark-1.3", "claude-sonnet-5-5", "qwen3.8-flash", "qwen3.8-max", "minimax-m3", "gemini-3.1-pro"} {
					t.Run(model, func(t *testing.T) {
						var mu sync.Mutex
						var gotPath, gotKey, gotSession, gotUA, bodyModel string
						srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
							mu.Lock()
							defer mu.Unlock()
							gotPath, gotSession, gotUA = r.URL.Path, r.Header.Get("x-opencode-session"), r.Header.Get("User-Agent")
							gotKey = r.Header.Get("Authorization") + r.Header.Get("x-api-key") + r.Header.Get("x-goog-api-key")
							var body map[string]any
							_ = json.NewDecoder(r.Body).Decode(&body)
							bodyModel, _ = body["model"].(string)
							reply(w, r.URL.Path)
						}))
						defer srv.Close()
						root := "/inference"
						if name == "opencode-go" {
							root += "/go"
						}
						if legacy {
							root = "/zen"
							if name == "opencode-go" {
								root += "/go"
							}
						}
						p, err := New(Config{Name: name, BaseURL: srv.URL + root + "/v1", APIKeys: []string{"test-key"}, MaxRetries: -1})
						if err != nil {
							t.Fatal(err)
						}
						ctx := llm.WithSessionID(context.Background(), "saved-session")
						resp, err := p.Stream(ctx, llm.Request{Model: name + "/" + model, MaxTokens: 100, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
						if err != nil {
							t.Fatal(err)
						}
						if resp.Message.Text() != "ok" {
							t.Fatalf("text=%q", resp.Message.Text())
						}
						mu.Lock()
						defer mu.Unlock()
						proto := protocol(name, model)
						want := root + "/v1/chat/completions"
						if !legacy {
							want = root + "/openai/v1/chat/completions"
						}
						switch proto {
						case "responses":
							want = strings.TrimSuffix(want, "/chat/completions") + "/responses"
						case "anthropic":
							want = root + "/anthropic/v1/messages"
							if legacy {
								want = root + "/v1/messages"
							}
						case "gemini":
							want = root + "/google/v1beta/models/" + model + ":streamGenerateContent"
							if legacy {
								want = root + "/v1/models/" + model + ":streamGenerateContent"
							}
						}
						if gotPath != want {
							t.Fatalf("path=%s want=%s", gotPath, want)
						}
						if gotSession != "saved-session" || !strings.HasPrefix(gotUA, "janetcode/") {
							t.Fatalf("session=%q user-agent=%q", gotSession, gotUA)
						}
						wantKey := "Bearer test-key"
						if proto == "anthropic" || proto == "gemini" {
							wantKey = "test-key"
						}
						if gotKey != wantKey {
							t.Fatal("wrong auth style")
						}
						if proto != "gemini" && bodyModel != model {
							t.Fatalf("wire model=%q", bodyModel)
						}
					})
				}
			})
		}
	}
}

func TestModelsAndProtocolCorrection(t *testing.T) {
	var chat, responsesCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-opencode-session") == "" {
			t.Error("missing session header")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"data":[{"id":"kimi-k3"},{"id":"jev-1.13"}]}`)
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			chat.Add(1)
			w.WriteHeader(404)
			fmt.Fprint(w, `{"error":{"message":"use the responses endpoint"}}`)
		default:
			responsesCalls.Add(1)
			reply(w, r.URL.Path)
		}
	}))
	defer srv.Close()
	p, err := New(Config{Name: "opencode-go", BaseURL: srv.URL + "/v1", APIKeys: []string{"k"}, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	models, err := p.ListModels(context.Background())
	if err != nil || len(models) != 1 || models[0].ID != "kimi-k3" {
		t.Fatalf("models=%v err=%v", models, err)
	}
	for i := 0; i < 2; i++ {
		if _, err := p.Stream(context.Background(), llm.Request{Model: "new-open-model", MaxTokens: 100, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if chat.Load() != 1 || responsesCalls.Load() != 2 {
		t.Fatalf("chat=%d responses=%d", chat.Load(), responsesCalls.Load())
	}
	if _, err := p.Stream(context.Background(), llm.Request{Model: "jev-1.13"}, nil); err == nil {
		t.Fatal("accepted unsupported model")
	}
}

func TestQuotaErrorsFailWithoutRetrying(t *testing.T) {
	for _, status := range []int{429, 402} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", "7200")
				w.WriteHeader(status)
				io.WriteString(w, `{"error":{"message":"weekly usage limit exceeded","resets_in":7200}}`)
			}))
			defer srv.Close()
			p, err := New(Config{Name: "opencode-go", BaseURL: srv.URL + "/v1", APIKeys: []string{"k"}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = p.Stream(context.Background(), llm.Request{Model: "kimi-k3", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
			if err == nil || calls.Load() != 1 {
				t.Fatalf("calls=%d err=%v", calls.Load(), err)
			}
			if status == 429 && !strings.Contains(err.Error(), "2h0m0s") {
				t.Fatalf("missing reset: %v", err)
			}
		})
	}
}

func TestNativeProtocolTable(t *testing.T) {
	for _, tc := range []struct{ name, model, want string }{
		{"opencode-go", "kimi-k3", "openai"}, {"opencode-go", "gpt-6-luna", "responses"},
		{"opencode-go", "qwen3.8-max", "anthropic"}, {"opencode-zen", "qwen3.8-max", "openai"},
		{"opencode-go", "minimax-m3", "anthropic"}, {"opencode-go", "minimax-m3.1-flash", "openai"},
		{"opencode-zen", "minimax-m3", "openai"}, {"opencode-zen", "claude-sonnet-5-5", "anthropic"},
		{"opencode-go", "gemini-4-argon", "gemini"}, {"opencode-zen", "jev-1.13", "unsupported"},
	} {
		if got := protocol(tc.name, tc.model); got != tc.want {
			t.Errorf("%s/%s protocol=%s want=%s", tc.name, tc.model, got, tc.want)
		}
	}
}

func TestResetInformationAndBurstLimits(t *testing.T) {
	for _, tc := range []struct {
		header, body string
		want         time.Duration
	}{
		{"7200", `{}`, 2 * time.Hour},
		{"", `{"error":{"resets_in":7200}}`, 2 * time.Hour},
		{"", `{"error":{"message":"Plan limit reached. Resets in 3 hours 12 minutes."}}`, 3*time.Hour + 12*time.Minute},
	} {
		if got := resetAfter(tc.header, []byte(tc.body)); got != tc.want {
			t.Errorf("reset=%s want=%s", got, tc.want)
		}
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"too many requests per minute"}}`)
			return
		}
		reply(w, r.URL.Path)
	}))
	defer srv.Close()
	p, err := New(Config{Name: "opencode-go", BaseURL: srv.URL + "/v1", APIKeys: []string{"k"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Stream(context.Background(), llm.Request{Model: "kimi-k3", Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	if err != nil || calls.Load() != 2 {
		t.Fatalf("ordinary throttle: calls=%d err=%v", calls.Load(), err)
	}
}
