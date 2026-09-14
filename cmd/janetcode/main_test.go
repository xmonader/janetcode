package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/rates"
)

// TestHeadlessEndToEnd drives the real binary entry point against a fake
// OpenAI-compatible server: config from env, tool execution on disk, session
// persistence, and resume.
func TestHeadlessEndToEnd(t *testing.T) {
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello world\n"), 0o644)
	os.MkdirAll(filepath.Join(work, ".janetcode", "skills", "uppercase"), 0o755)
	os.WriteFile(filepath.Join(work, ".janetcode", "skills", "uppercase", "SKILL.md"), []byte("---\ndescription: How to uppercase a text file into another file\n---\nRead the source, write the target in capitals.\n"), 0o644)
	home := t.TempDir()

	var calls int
	var lastReq, memReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		if calls == 4 {
			json.Unmarshal(raw, &memReq) // memory extraction after the mutating turn
		} else {
			json.Unmarshal(raw, &lastReq)
		}
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
		case 4:
			chunks = []string{`{"choices":[{"delta":{"content":"out.txt is generated from hello.txt."},"finish_reason":"stop"}]}`}
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
	t.Chdir(work)

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
	if calls != 4 { // read, write, answer, memory extraction
		t.Fatalf("model calls = %d", calls)
	}
	if !strings.Contains(errOut, "[memory] remembered out.txt-generated-hello.txt (project, project): out.txt is generated from hello.txt.") {
		t.Fatalf("stderr = %q", errOut)
	}
	if sys := memReq["messages"].([]any)[0].(map[string]any); !strings.Contains(sys["content"].(string), "long-term memory") {
		t.Fatalf("extraction system prompt = %v", sys)
	}
	memFiles, _ := filepath.Glob(filepath.Join(home, ".local", "share", "janetcode", "memory", "*", "*.md"))
	if len(memFiles) != 1 || filepath.Base(memFiles[0]) != "out.txt-generated-hello.txt.md" {
		t.Fatalf("memory files = %v", memFiles)
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
	// The matching project skill was listed in the system prompt and
	// attached to the request.
	if sys := msgs[0].(map[string]any)["content"].(string); !strings.Contains(sys, "# Skills\n") || !strings.Contains(sys, "- uppercase: How to uppercase") {
		t.Fatalf("skill index missing: %q", sys)
	}
	if u := fmt.Sprint(msgs[1].(map[string]any)["content"]); !strings.Contains(u, "<skill name=\"uppercase\"") || !strings.Contains(u, "write the target in capitals") {
		t.Fatalf("skill not attached: %q", u)
	}
	if !strings.Contains(errOut, "[notice] skills attached: uppercase") {
		t.Fatalf("stderr = %q", errOut)
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
	} else if sys := msgs[0].(map[string]any)["content"].(string); !strings.Contains(sys, "# Memory") || !strings.Contains(sys, "- out.txt-generated-hello.txt (project): out.txt is generated from hello.txt.") {
		t.Fatalf("memory missing from resumed system prompt: %q", sys)
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

// The rate-table refresh writes model-rates.json into the config
// directory from a goroutine. It used to be started and forgotten, so a
// run that finished before the 2.3 MB download did left a writer behind:
// TestHeadlessAppliesThePromptOverride failed once in nine full-suite
// runs with "TempDir RemoveAll: .config/janetcode not empty", the
// download landing in a directory the test was deleting. The run now
// waits for it, so when run returns nothing else is writing there.
func TestTheRateRefreshDoesNotOutliveTheRun(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()

	started := make(chan struct{})
	rateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(150 * time.Millisecond) // outlast the run, as the real one does
		io.WriteString(w, `{"test-model":{"input_cost_per_token":1e-7,"output_cost_per_token":2e-7}}`)
	}))
	defer rateSrv.Close()
	old := rates.URL
	rates.URL = rateSrv.URL
	defer func() { rates.URL = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	cfgDir := filepath.Join(home, ".config", "janetcode")
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Chdir(work)

	stdout, stderr := captureStd(t)
	err := run([]string{"-p", "hello", "--provider", "openai", "--model", "test-model"})
	_, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	select {
	case <-started:
	default:
		t.Fatal("the refresh never ran, so this proves nothing")
	}
	// The run outpaced the download, so the refresh was cancelled rather
	// than finished: what matters is that it is over. Nothing may appear
	// in the config directory after this point — the server answers at
	// 150ms and we look well past that.
	list := func() []string {
		ents, err := os.ReadDir(cfgDir)
		if err != nil {
			return nil
		}
		var out []string
		for _, e := range ents {
			out = append(out, e.Name())
		}
		return out
	}
	at := strings.Join(list(), ",")
	time.Sleep(500 * time.Millisecond)
	if after := strings.Join(list(), ","); after != at {
		t.Fatalf("something wrote to the config directory after the run: %q → %q", at, after)
	}
}

// A prompt A/B is only traceable if the flag reaches the run. The TUI and
// the eval runner both build their agent with the override applied;
// headless uses the same agent, and this is what says so.
func TestHeadlessAppliesThePromptOverride(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	base := filepath.Join(home, "base.md")
	os.WriteFile(base, []byte("You are BASEBOT, and nothing else."), 0o600)

	var lastReq map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &lastReq)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, `data: {"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Chdir(work)

	sysOf := func() string {
		return lastReq["messages"].([]any)[0].(map[string]any)["content"].(string)
	}

	// --append-system-prompt lands after the harness's own prompt.
	stdout, stderr := captureStd(t)
	err := run([]string{"-p", "hello", "--provider", "openai", "--model", "test-model", "--append-system-prompt", "Answer in French."})
	_, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	sys := sysOf()
	if !strings.Contains(sys, "You are janetcode") || !strings.HasSuffix(strings.TrimSpace(sys), "Answer in French.") {
		t.Fatalf("appended text missing: %q", sys)
	}

	// JANETCODE_SYSTEM_MD replaces the base, keeping the environment
	// section a prompt is useless without.
	t.Setenv("JANETCODE_SYSTEM_MD", base)
	stdout, stderr = captureStd(t)
	err = run([]string{"-p", "hello", "--provider", "openai", "--model", "test-model", "--append-system-prompt", "@" + base})
	_, errOut = stdout(), stderr()
	if err != nil {
		t.Fatalf("run with a replaced base: %v\nstderr: %s", err, errOut)
	}
	sys = sysOf()
	if strings.Contains(sys, "You are janetcode") {
		t.Fatalf("the base prompt was not replaced: %q", sys)
	}
	if strings.Count(sys, "You are BASEBOT") != 2 { // the base, and the appended @file
		t.Fatalf("override not applied twice over: %q", sys)
	}
	if !strings.Contains(sys, work) {
		t.Fatalf("the environment section went missing with the base: %q", sys)
	}
}
