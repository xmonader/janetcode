package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A headless run with no memory setting makes no model call after the
// answer: neither the per-turn extraction nor the session-end consolidation.
func TestHeadlessSkipsMemoryByDefault(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`
		if calls == 1 {
			chunk = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write_file","arguments":"{\"path\":\"out.txt\",\"content\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`
		}
		io.WriteString(w, "data: "+chunk+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Chdir(work)

	stdout, stderr := captureStd(t)
	err := run([]string{"-p", "write out.txt", "--provider", "openai", "--model", "test-model", "--yolo"})
	out, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	if _, err := os.Stat(filepath.Join(work, "out.txt")); err != nil || !strings.Contains(out, "done") {
		t.Fatalf("stdout = %q, out.txt err = %v", out, err)
	}
	if calls != 2 { // write, answer
		t.Fatalf("model calls = %d, want 2\nstderr: %s", calls, errOut)
	}
}

// A headless run's requests carry the coding core and nothing else; the
// rest is listed by tool_search and loadable through it.
func TestHeadlessSendsTheCodingCore(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	var first map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if first == nil {
			json.Unmarshal(raw, &first)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Chdir(work)
	stdout, stderr := captureStd(t)
	err := run([]string{"-p", "hi", "--provider", "openai", "--model", "test-model", "--yolo"})
	stdout()
	if errOut := stderr(); err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	var names []string
	var search string
	for _, raw := range first["tools"].([]any) {
		fn := raw.(map[string]any)["function"].(map[string]any)
		names = append(names, fn["name"].(string))
		if fn["name"] == "tool_search" {
			search = fn["description"].(string)
		}
	}
	if strings.Join(names, ",") != "bash,edit_file,read_file,tool_search,write_file" {
		t.Fatalf("tools = %v", names)
	}
	for _, loadable := range []string{"todo", "task", "glob", "grep"} {
		if !strings.Contains(search, loadable) {
			t.Fatalf("tool_search does not list %s: %q", loadable, search)
		}
	}
}
