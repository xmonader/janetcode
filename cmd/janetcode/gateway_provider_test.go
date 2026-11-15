package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
)

func TestGatewayProviderBuildFromEnvironment(t *testing.T) {
	for _, tc := range []struct{ provider, key, model, path string }{
		{"zai-coding-plan", "ZAI_CODING_PLAN_API_KEY", "glm-5", "/api/coding/paas/v4/chat/completions"},
		{"opencode-go", "OPENCODE_GO_API_KEY", "gpt-5.6-luna", "/inference/go/openai/v1/responses"},
		{"opencode-zen", "OPENCODE_API_KEY", "kimi-k3", "/inference/openai/v1/chat/completions"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			var mu sync.Mutex
			seen := false
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				seen = true
				if r.URL.Path != tc.path {
					t.Errorf("path=%s want=%s", r.URL.Path, tc.path)
				}
				if r.Header.Get("Authorization") != "Bearer dummy-gateway-key" {
					t.Error("environment key did not reach provider")
				}
				if config.IsOpenCode(tc.provider) && r.Header.Get("x-opencode-session") != "test-session" {
					t.Error("missing session header")
				}
				var body struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(r.Body).Decode(&body)
				if body.Model != tc.model {
					t.Errorf("wire model=%s", body.Model)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.provider == "opencode-go" {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
				}
			}))
			defer srv.Close()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			t.Setenv("JANETCODE_PROVIDER", "")
			t.Setenv("JANETCODE_MODEL", "")
			t.Setenv(tc.key, "dummy-gateway-key")
			t.Setenv("OPENCODE_GO_BASE_URL", srv.URL+"/inference/go/v1")
			t.Setenv("OPENCODE_BASE_URL", srv.URL+"/inference/v1")
			t.Setenv("OPENCODE_ZEN_BASE_URL", srv.URL+"/inference/v1")
			base := srv.URL + "/api/coding/paas/v4"
			path, err := config.Path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"providers": map[string]any{tc.provider: map[string]string{"base_url": base}}})
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.Load(config.Overrides{Provider: tc.provider, Model: tc.model})
			if err != nil {
				t.Fatal(err)
			}
			p, err := buildProvider(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if config.IsOpenCode(tc.provider) {
				var bare string
				p, bare, err = newProviderPool(cfg, p).For(tc.provider + "/" + tc.model)
				if err != nil || bare != tc.model {
					t.Fatalf("provider pool model=%s err=%v", bare, err)
				}
			}
			resp, err := p.Stream(llm.WithSessionID(context.Background(), "test-session"), llm.Request{Model: tc.model, MaxTokens: 100, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Message.Text() != "ok" {
				t.Fatalf("reply=%q", resp.Message.Text())
			}
			mu.Lock()
			defer mu.Unlock()
			if !seen {
				t.Fatal("provider never called test server")
			}
		})
	}
}
