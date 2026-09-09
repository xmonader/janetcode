package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSwapModel(t *testing.T) {
	body, was := swapModel([]byte(`{"model":"claude-sonnet-4-5","max_tokens":8,"metadata":{"user_id":"x"}}`), "inception/mercury-2.5")
	if was != "claude-sonnet-4-5" {
		t.Fatalf("reported %q", was)
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["model"] != "inception/mercury-2.5" {
		t.Fatalf("model = %v", obj["model"])
	}
	if _, ok := obj["metadata"]; ok {
		t.Fatal("metadata should be dropped; some upstreams reject it")
	}
	if obj["max_tokens"].(float64) != 8 {
		t.Fatal("the rest of the request must survive")
	}
	// An unparseable body is forwarded rather than mangled here.
	if got, _ := swapModel([]byte("not json"), "m"); string(got) != "not json" {
		t.Fatalf("got %q", got)
	}
}

func TestJoinPathAvoidsDoubleV1(t *testing.T) {
	for _, tc := range []struct{ upstream, uri, want string }{
		{"https://openrouter.ai/api/v1", "/v1/messages?beta=true", "/messages?beta=true"},
		{"https://openrouter.ai/api/v1", "/messages", "/messages"},
		{"https://example.com/anthropic", "/v1/messages", "/v1/messages"},
	} {
		if got := joinPath(tc.upstream, tc.uri); got != tc.want {
			t.Errorf("joinPath(%q,%q) = %q, want %q", tc.upstream, tc.uri, got, tc.want)
		}
	}
}

// TestProxyForwards is the end-to-end shape: the client asks for one model
// and the upstream is asked for another, with the bearer token attached.
func TestProxyForwards(t *testing.T) {
	var sawModel, sawAuth, sawPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var obj map[string]any
		_ = json.Unmarshal(b, &obj)
		sawModel, _ = obj["model"].(string)
		sawAuth = r.Header.Get("Authorization")
		sawPath = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"message","content":[{"type":"text","text":"ok"}]}`))
	}))
	defer up.Close()

	p := &proxy{model: "target/model", upstream: up.URL, key: "secret", quiet: true}
	srv := httptest.NewServer(p)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/messages?beta=true", "application/json",
		strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if sawModel != "target/model" {
		t.Fatalf("upstream saw model %q", sawModel)
	}
	if sawAuth != "Bearer secret" {
		t.Fatalf("upstream saw auth %q", sawAuth)
	}
	if sawPath != "/v1/messages?beta=true" {
		t.Fatalf("upstream saw path %q", sawPath)
	}
	out, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(out), `"ok"`) {
		t.Fatalf("body did not come back: %s", out)
	}
}
