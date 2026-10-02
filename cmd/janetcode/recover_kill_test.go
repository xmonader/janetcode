package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestRecoverAfterKill9 is the real thing: a janetcode process is killed
// with SIGKILL while a command it started is running, and a second
// process carries the turn on with --resume --recover. The read that had
// finished keeps its result, the command is not run again, and the model
// is told it may or may not have happened.
func TestRecoverAfterKill9(t *testing.T) {
	if testing.Short() || runtime.GOOS == "windows" {
		t.Skip("builds the binary and kills it; not under -short or on Windows")
	}
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello from the file\n"), 0o644)
	home := t.TempDir()
	bin := filepath.Join(t.TempDir(), "janetcode")
	build := exec.Command("go", "build", "-o", bin, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		var chunk string
		if n == 1 {
			chunk = `{"choices":[{"delta":{"tool_calls":[` +
				`{"index":0,"id":"r1","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}},` +
				`{"index":1,"id":"b1","function":{"name":"bash","arguments":"{\"command\":\"sleep 30 && echo ran > out.txt\"}"}}` +
				`]},"finish_reason":"tool_calls"}]}`
		} else {
			chunk = `{"choices":[{"delta":{"content":"carried on"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`
		}
		io.WriteString(w, "data: "+chunk+"\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	env := append(os.Environ(),
		"HOME="+home, "XDG_CONFIG_HOME="+filepath.Join(home, ".config"), "XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"OPENAI_API_KEY=test-key", "OPENAI_BASE_URL="+srv.URL+"/v1", "ANTHROPIC_API_KEY=")
	child := exec.Command(bin, "-p", "read hello and run the slow command", "--provider", "openai", "--model", "test-model", "--yolo")
	child.Dir, child.Env = work, env
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	sessDir := filepath.Join(home, ".local", "share", "janetcode", "sessions")

	// Wait until the log shows the command started, then kill -9.
	var logPath string
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		matches, _ := filepath.Glob(filepath.Join(sessDir, "*.jsonl"))
		for _, m := range matches {
			if bashStarted(m) {
				logPath = m
			}
		}
		if logPath != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if logPath == "" {
		child.Process.Kill()
		t.Fatal("the bash call never started")
	}
	if err := child.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	child.Wait()
	id := strings.TrimSuffix(filepath.Base(logPath), ".jsonl")

	rec := exec.Command(bin, "--resume", id, "--recover", "--provider", "openai", "--model", "test-model", "--yolo")
	rec.Dir, rec.Env = work, env
	out, err := rec.CombinedOutput()
	if err != nil {
		t.Fatalf("--recover: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "carried on") {
		t.Fatalf("the continuation did not run:\n%s", out)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("model calls = %d, want 2", len(bodies))
	}
	second := bodies[1]
	if !strings.Contains(second, "hello from the file") {
		t.Errorf("the finished read's result was lost:\n%s", second)
	}
	if !strings.Contains(second, "not run again") || !strings.Contains(second, "may or may not have taken effect") {
		t.Errorf("the command's answer does not say it was interrupted:\n%s", second)
	}
	if _, err := os.Stat(filepath.Join(work, "out.txt")); err == nil {
		t.Error("the killed command ran again")
	}
	// The second request is provider-valid: every call answered.
	var req struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(second), &req); err != nil {
		t.Fatal(err)
	}
	asked, answered := map[string]bool{}, map[string]bool{}
	for _, m := range req.Messages {
		for _, c := range m.ToolCalls {
			asked[c.ID] = true
		}
		if m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	for id := range asked {
		if !answered[id] {
			t.Errorf("call %s unanswered in the continuation's request", id)
		}
	}
}

// bashStarted reports whether the session log at path holds the bash
// call's tool/call record.
func bashStarted(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, `"t":"tool/call"`) && strings.Contains(line, `"name":"bash"`) {
			return true
		}
	}
	return false
}
