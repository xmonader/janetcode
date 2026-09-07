package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHeadlessEndToEnd drives the real binary entry point against a fake
// OpenAI-compatible server: config from env, tool execution on disk, session
// persistence, and resume.
func TestHeadlessEndToEnd(t *testing.T) {
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello world\n"), 0o644)
	home := t.TempDir()

	var calls int
	var lastReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &lastReq)
		w.Header().Set("Content-Type", "text/event-stream")
		var chunks []string
		switch calls {
		case 1:
			chunks = []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}]}`,
			}
		case 2:
			chunks = []string{
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c2","function":{"name":"write_file","arguments":"{\"path\":\"out.txt\",\"content\":\"HELLO WORLD\"}"}}]},"finish_reason":"tool_calls"}]}`,
			}
		default:
			chunks = []string{`{"choices":[{"delta":{"content":"wrote out.txt"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`}
		}
		for _, c := range chunks {
			io.WriteString(w, "data: "+c+"\n\n")
		}
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("ANTHROPIC_API_KEY", "")
	os.Chdir(work)

	stdout, stderr := captureStd(t)
	err := run([]string{"-p", "uppercase hello.txt into out.txt", "--provider", "openai", "--model", "test-model", "--yolo"})
	out, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	if !strings.Contains(out, "wrote out.txt") {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "[tool] read_file") || !strings.Contains(errOut, "[tool] write_file") {
		t.Fatalf("stderr = %q", errOut)
	}
	got, err := os.ReadFile(filepath.Join(work, "out.txt"))
	if err != nil || string(got) != "HELLO WORLD" {
		t.Fatalf("out.txt = %q err=%v", got, err)
	}
	if calls != 3 {
		t.Fatalf("model calls = %d", calls)
	}
	// The last request carried the system prompt with the working directory
	// and the full tool history.
	msgs := lastReq["messages"].([]any)
	if sys := msgs[0].(map[string]any); sys["role"] != "system" || !strings.Contains(sys["content"].(string), work) {
		t.Fatalf("system message = %v", sys)
	}
	if len(msgs) != 6 { // system, user, assistant, tool, assistant, tool
		t.Fatalf("history length = %d", len(msgs))
	}

	// Resume the latest session and send another prompt: history is replayed
	// and piped stdin is attached to the prompt.
	calls = 10
	oldIn := os.Stdin
	rin, win, _ := os.Pipe()
	io.WriteString(win, "diff --git a/x b/x\n")
	win.Close()
	os.Stdin = rin
	stdout, stderr = captureStd(t)
	err = run([]string{"-p", "thanks", "--provider", "openai", "--model", "test-model", "--continue"})
	os.Stdin = oldIn
	out, errOut = stdout(), stderr()
	if err != nil {
		t.Fatalf("resume run: %v\nstderr: %s", err, errOut)
	}
	if msgs := lastReq["messages"].([]any); len(msgs) != 8 {
		t.Fatalf("resumed history length = %d", len(msgs))
	} else if u := msgs[7].(map[string]any)["content"].(string); !strings.Contains(u, "thanks") || !strings.Contains(u, "<stdin>\ndiff --git a/x b/x\n</stdin>") {
		t.Fatalf("stdin not attached: %q", u)
	}

	// Missing model for openai is a clear error, not a request.
	if err := run([]string{"-p", "x", "--provider", "openai"}); err == nil || !strings.Contains(err.Error(), "needs a model") {
		t.Fatalf("err = %v", err)
	}
}

func captureStd(t *testing.T) (func() string, func() string) {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	ro, wo, _ := os.Pipe()
	re, we, _ := os.Pipe()
	os.Stdout, os.Stderr = wo, we
	var bo, be bytes.Buffer
	done := make(chan struct{}, 2)
	go func() { io.Copy(&bo, ro); done <- struct{}{} }()
	go func() { io.Copy(&be, re); done <- struct{}{} }()
	var once bool
	finish := func() {
		if !once {
			once = true
			wo.Close()
			we.Close()
			<-done
			<-done
			os.Stdout, os.Stderr = oldOut, oldErr
		}
	}
	return func() string { finish(); return bo.String() }, func() string { finish(); return be.String() }
}
