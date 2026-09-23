package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// firstRun is a server with no provider at all, the way --web starts on a
// machine with no key. Its Build accepts the key "good" and rejects any
// other the way a provider does, with a 401 from the model list.
func firstRun(t *testing.T) (*server, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	build := func(c *config.Config) (llm.Provider, error) {
		keys := c.Providers[c.Provider].Keys()
		return liveProvider{lister: func() ([]llm.ModelInfo, error) {
			if len(keys) == 0 || keys[0] != "good" {
				return nil, errors.New("Unauthorized (HTTP 401)")
			}
			return []llm.ModelInfo{{ID: "qwen3.7-plus", ContextWindow: 1_000_000}, {ID: "qwen3.7-flash"}}, nil
		}}, nil
	}
	s := newServer(context.Background(), Options{
		// What the loader hands over with no file: the default provider,
		// keyless.
		Agent: &agent.Agent{Tools: tool.NewRegistry()}, Config: &config.Config{Provider: "anthropic",
			Providers: map[string]config.Provider{"anthropic": {Type: "anthropic"}}}, ConfigPath: path, Build: build,
	})
	return s, path
}

func connectCall(t *testing.T, s *server, body string) map[string]any {
	t.Helper()
	w := postJSON(t, s.handler(), "/api/connect", body)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/connect %s = %d %s", body, w.Code, w.Body)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestWebFirstRunConnects: --web with no key refused to start, and the
// page had no way to add one. Now the page says it is not connected, a
// prompt opens the connect form instead of failing, and connecting
// switches the running agent and writes the choice to the config file.
func TestWebFirstRunConnects(t *testing.T) {
	s, path := firstRun(t)
	if s.hello().Connected {
		t.Fatal("hello says connected with no provider")
	}
	if err := s.submit("fix the bug"); err == nil || !strings.Contains(err.Error(), "not connected") {
		t.Fatalf("a prompt before connecting = %v", err)
	}

	// The form lists the presets, without keys.
	r := httptest.NewRequest(http.MethodGet, "/api/connect", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"name":"dashscope-coding"`) || strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatalf("providers on a machine with no config = %s", w.Body)
	}

	// A rejected key says so before any model is picked, with no retry.
	bad := connectCall(t, s, `{"action":"models","name":"dashscope-coding","url":"https://coding-intl.dashscope.aliyuncs.com/v1","keys":"sk-wrong"}`)
	e, _ := bad["error"].(map[string]any)
	if e["headline"] != "Authentication failed" || e["retry"] != false {
		t.Fatalf("bad key = %v", bad)
	}
	// A provider that needs a key is not tried without one.
	none := connectCall(t, s, `{"action":"models","name":"openai","url":"https://api.openai.com/v1","keys":""}`)
	if e, _ := none["error"].(map[string]any); !strings.Contains(e["reason"].(string), "needs an API key") {
		t.Fatalf("no key = %v", none)
	}

	good := connectCall(t, s, `{"action":"models","name":"dashscope-coding","url":"https://coding-intl.dashscope.aliyuncs.com/v1","keys":"good"}`)
	if ms, _ := good["models"].([]any); len(ms) != 2 {
		t.Fatalf("models = %v", good)
	}
	done := connectCall(t, s, `{"action":"connect","name":"dashscope-coding","url":"https://coding-intl.dashscope.aliyuncs.com/v1","keys":"good","model":"qwen3.7-plus"}`)
	if done["ok"] != true {
		t.Fatalf("connect = %v", done)
	}
	h := s.hello()
	if !h.Connected || h.Provider != "dashscope-coding" || h.Model != "qwen3.7-plus" {
		t.Fatalf("hello after connecting = %+v", h)
	}
	if got := s.opts.Agent.Settings().ContextWindow; got != 1_000_000 {
		t.Fatalf("context window from the listing = %d", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Provider  string                     `json:"provider"`
		Model     string                     `json:"model"`
		Providers map[string]config.Provider `json:"providers"`
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Provider != "dashscope-coding" || saved.Model != "qwen3.7-plus" || saved.Providers["dashscope-coding"].Keys()[0] != "good" {
		t.Fatalf("config file = %s", data)
	}

	// Connected, a second provider keeps its saved key when the key field
	// is left empty: switching back must not ask for it again.
	again := connectCall(t, s, `{"action":"models","name":"dashscope-coding","url":"https://coding-intl.dashscope.aliyuncs.com/v1","keys":""}`)
	if _, ok := again["models"]; !ok {
		t.Fatalf("empty key field with a saved key = %v", again)
	}
	w = httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/connect", nil))
	if !strings.Contains(w.Body.String(), `"has_key":true`) || strings.Contains(w.Body.String(), "good") {
		t.Fatalf("the provider list must say a key is set without sending it: %s", w.Body)
	}
}

// Connecting swaps the client a running turn is using; it waits.
func TestWebConnectWaitsForTheTurn(t *testing.T) {
	s, _ := firstRun(t)
	s.mu.Lock()
	s.running = true
	s.mu.Unlock()
	out := connectCall(t, s, `{"action":"connect","name":"dashscope-coding","url":"https://coding-intl.dashscope.aliyuncs.com/v1","keys":"good","model":"qwen3.7-plus"}`)
	if e, _ := out["error"].(map[string]any); !strings.Contains(e["reason"].(string), "wait for the current turn") {
		t.Fatalf("connect mid-turn = %v", out)
	}
}
