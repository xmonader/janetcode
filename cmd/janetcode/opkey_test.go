package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/opref"
	"github.com/xmonader/janetcode/internal/secret"
)

func opConfig(t *testing.T, baseURL, key string) *config.Config {
	t.Helper()
	cfg := &config.Config{Provider: "gw", Model: "m", Providers: map[string]config.Provider{}}
	pr := config.Provider{Type: config.TypeOpenAI, BaseURL: baseURL, Models: []string{"m"}}
	pr.SetKeys([]string{key})
	cfg.Providers["gw"] = pr
	return cfg
}

// The provider signs its request with the value op returned, not with the
// reference text.
func TestBuildProviderSendsTheResolvedOpValue(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for op")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "op"), []byte("#!/bin/sh\n[ \"$2\" = op://vault/item/key ] && printf 'sk-from-onepassword\\n'\n"), 0o755)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	opref.Forget()
	t.Cleanup(func() { opref.Forget(); secret.Forget() })

	var mu sync.Mutex
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		mu.Unlock()
		http.Error(w, "stop", http.StatusTeapot)
	}))
	defer srv.Close()
	p, err := buildProvider(opConfig(t, srv.URL, "op://vault/item/key"))
	if err != nil {
		t.Fatal(err)
	}
	p.Stream(context.Background(), llm.Request{Model: "m", MaxTokens: 10, Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}}, nil)
	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer sk-from-onepassword" {
		t.Fatalf("Authorization = %q", auth)
	}
}

func TestBuildProviderNamesAnUnreadableReference(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no op
	opref.Forget()
	_, err := buildProvider(opConfig(t, "https://gw.example/v1", "op://vault/item/key"))
	if err == nil || !strings.Contains(err.Error(), "op://vault/item/key") || !strings.Contains(err.Error(), "gw") {
		t.Fatalf("err = %v", err)
	}
}
