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

// A small repository rides in the first request whole, and an edit to one
// of its files needs no read_file first: the inlined copy counts as read.
func TestHeadlessInlinesSmallRepo(t *testing.T) {
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello world\n"), 0o644)
	os.WriteFile(filepath.Join(work, "task.md"), []byte("Uppercase hello.txt in place.\n"), 0o644)
	home := t.TempDir()
	var calls int
	var first string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if calls == 1 {
			first = string(raw)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := `{"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}`
		if calls == 1 {
			chunk = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"edit_file","arguments":"{\"path\":\"hello.txt\",\"old_string\":\"hello world\",\"new_string\":\"HELLO WORLD\"}"}}]},"finish_reason":"tool_calls"}]}`
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
	err := run([]string{"-p", "do task.md", "--provider", "openai", "--model", "test-model", "--yolo"})
	_, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal([]byte(first), &req)
	var user string
	for _, m := range req.Messages {
		if m.Role == "user" {
			user += m.Content
		}
	}
	if !strings.Contains(user, "<file path=\"task.md\">\nUppercase hello.txt in place.\n</file>") {
		t.Fatalf("first request does not carry the repository:\n%s", user)
	}
	if got, _ := os.ReadFile(filepath.Join(work, "hello.txt")); string(got) != "HELLO WORLD\n" {
		t.Fatalf("hello.txt = %q\nstderr: %s", got, errOut)
	}
}
