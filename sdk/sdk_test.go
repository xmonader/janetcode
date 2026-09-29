package sdk_test

import (
	"context"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/mcp"
	"github.com/xmonader/janetcode/sdk"
)

// janetcode --mcp-serve, end to end through the real binary: janetcode's
// own MCP client lists the tool and runs a task, in read-only mode (the
// write is refused) and in auto-edits mode (it lands), and a follow-up
// continues the same session.
func TestMCPServeEndToEnd(t *testing.T) {
	bin := janetcode(t)
	srv := fakeModel(t)
	env := map[string]string{}
	for _, kv := range isolatedEnv(t, srv.URL) {
		k, v, _ := strings.Cut(kv, "=")
		env[k] = v
	}
	for _, tc := range []struct {
		args  []string
		wrote bool
	}{
		{[]string{"--mcp-serve", "--provider", "openai", "--model", "test-model"}, false},
		{[]string{"--mcp-serve", "--auto-edits", "--provider", "openai", "--model", "test-model"}, true},
	} {
		work := t.TempDir()
		c, err := mcp.Start(context.Background(), "janetcode", mcp.ServerConfig{Command: bin, Args: tc.args, Env: env, Cwd: work}, mcp.Options{Root: work, HandshakeTimeout: 30 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		tools := c.Tools()
		if len(tools) != 1 || tools[0].Name != "janetcode_task" {
			c.Close()
			t.Fatalf("tools = %+v", tools)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		res, err := c.CallTool(ctx, "janetcode_task", json.RawMessage(`{"prompt":"write hi to out.txt"}`))
		cancel()
		if err != nil || res.IsError {
			c.Close()
			t.Fatalf("%v: call = %+v, %v", tc.args, res, err)
		}
		_, statErr := os.Stat(filepath.Join(work, "out.txt"))
		if wrote := statErr == nil; wrote != tc.wrote {
			c.Close()
			t.Fatalf("%v: wrote=%v, want %v; result %q", tc.args, wrote, tc.wrote, res.Text)
		}
		const marker = "[janetcode session "
		i := strings.Index(res.Text, marker)
		if i < 0 {
			c.Close()
			t.Fatalf("result = %q", res.Text)
		}
		id := res.Text[i+len(marker):]
		id = id[:strings.Index(id, " ")]
		ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
		res2, err := c.CallTool(ctx, "janetcode_task", json.RawMessage(`{"prompt":"and again","session_id":"`+id+`"}`))
		cancel()
		c.Close()
		if err != nil || res2.IsError || !strings.Contains(res2.Text, marker+id) {
			t.Fatalf("follow-up = %+v, %v (want session %s)", res2, err, id)
		}
	}
}

var (
	buildOnce sync.Once
	binPath   string
	buildErr  error
)

// janetcode builds the binary once for the package's tests.
func janetcode(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("builds the janetcode binary")
	}
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "janetcode-sdk-bin")
		if err != nil {
			buildErr = err
			return
		}
		name := "janetcode"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		binPath = filepath.Join(dir, name)
		out, err := exec.Command("go", "build", "-o", binPath, "../cmd/janetcode").CombinedOutput()
		if err != nil {
			buildErr = err
			binPath = string(out)
		}
	})
	if buildErr != nil {
		t.Fatalf("build: %v\n%s", buildErr, binPath)
	}
	return binPath
}

// fakeModel is an OpenAI-compatible server whose first answer writes a
// file and whose second says it is done.
func fakeModel(t *testing.T) *httptest.Server {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []json.RawMessage `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(raw, &req)
		w.Header().Set("Content-Type", "text/event-stream")
		var chunk string
		switch {
		case len(req.Tools) == 0:
			// A side call (memory, titles): nothing to add.
			chunk = `{"choices":[{"delta":{"content":"{}"},"finish_reason":"stop"}]}`
		case !strings.Contains(string(raw), `"role":"tool"`):
			calls.Add(1)
			chunk = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write_file","arguments":"{\"path\":\"out.txt\",\"content\":\"hi\\n\"}"}}]},"finish_reason":"tool_calls"}]}`
		default:
			calls.Add(1)
			chunk = `{"choices":[{"delta":{"content":"wrote out.txt"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":4}}`
		}
		io.WriteString(w, "data: "+chunk+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func isolatedEnv(t *testing.T, base string) []string {
	home := t.TempDir()
	return []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"OPENAI_API_KEY=test-key",
		"OPENAI_BASE_URL=" + base + "/v1",
		"ANTHROPIC_API_KEY=",
	}
}

// A prompt runs through the real binary: the write is asked about, the
// approver's answer is obeyed, events stream, and the result carries the
// answer, the tool call and the usage.
func TestPromptAsksTheApproverAndReports(t *testing.T) {
	bin := janetcode(t)
	srv := fakeModel(t)
	work := t.TempDir()
	var asked []sdk.Permission
	var events []sdk.Event
	var mu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	c, err := sdk.Start(ctx, sdk.Options{
		Binary: bin, Dir: work, Provider: "openai", Model: "test-model",
		Env: isolatedEnv(t, srv.URL),
		Approve: func(_ context.Context, p sdk.Permission) sdk.Decision {
			mu.Lock()
			asked = append(asked, p)
			mu.Unlock()
			return sdk.AllowOnce
		},
		OnEvent: func(e sdk.Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if c.Agent.Name != "janetcode" {
		t.Fatalf("agent = %+v", c.Agent)
	}
	s, err := c.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Prompt(ctx, "write hi to out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if res.StopReason != "end_turn" || !strings.Contains(res.Text, "wrote out.txt") {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(work, "out.txt")); string(got) != "hi\n" {
		t.Fatalf("out.txt = %q", got)
	}
	if len(asked) != 1 || asked[0].Tool != "write_file" || asked[0].SessionID != s.ID {
		t.Fatalf("asked = %+v", asked)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Tool != "write_file" || res.ToolCalls[0].Status != "completed" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
	if res.Usage.InputTokens == 0 || res.Usage.OutputTokens == 0 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	sawText := false
	for _, e := range events {
		if e.Kind == sdk.EventText && strings.Contains(e.Text, "wrote") {
			sawText = true
		}
	}
	if !sawText {
		t.Fatalf("no streamed text in %+v", events)
	}
}

// Without an approver a call that asks is denied, so an unattended
// program never waits on a question nobody will answer.
func TestNoApproverDenies(t *testing.T) {
	bin := janetcode(t)
	srv := fakeModel(t)
	work := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := sdk.Run(ctx, sdk.Options{Binary: bin, Dir: work, Provider: "openai", Model: "test-model", Env: isolatedEnv(t, srv.URL)}, "write hi to out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "out.txt")); err == nil {
		t.Fatal("the write ran although nobody approved it")
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Status != "failed" {
		t.Fatalf("tool calls = %+v", res.ToolCalls)
	}
}

// Read-only mode is a flag on the process: the write is not even offered.
func TestModeReadOnly(t *testing.T) {
	bin := janetcode(t)
	srv := fakeModel(t)
	work := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	approved := false
	_, err := sdk.Run(ctx, sdk.Options{Binary: bin, Dir: work, Provider: "openai", Model: "test-model", Mode: sdk.ModeReadOnly, Env: isolatedEnv(t, srv.URL),
		Approve: func(context.Context, sdk.Permission) sdk.Decision { approved = true; return sdk.AllowOnce }}, "write hi to out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "out.txt")); err == nil || approved {
		t.Fatalf("read-only mode wrote a file (approved=%v)", approved)
	}
}

// Relative paths are the caller's: the process runs in Dir, so a
// relative Dir sent on as the session's directory used to land in
// dir/dir, and a relative Binary was not found at all.
func TestRelativeDirAndBinary(t *testing.T) {
	bin := janetcode(t)
	srv := fakeModel(t)
	base := t.TempDir()
	os.Mkdir(filepath.Join(base, "work"), 0o755)
	os.Link(bin, filepath.Join(base, "jc"))
	t.Chdir(base)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := sdk.Run(ctx, sdk.Options{Binary: "./jc", Dir: "work", Provider: "openai", Model: "test-model", Mode: sdk.ModeYolo, Env: isolatedEnv(t, srv.URL)}, "write hi to out.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(base, "work", "out.txt")); err != nil {
		t.Fatalf("the file is not in the relative Dir: %v", err)
	}
}

func TestUnknownModeIsRefused(t *testing.T) {
	if _, err := sdk.Start(context.Background(), sdk.Options{Mode: "sideways"}); err == nil || !strings.Contains(err.Error(), "unknown mode") {
		t.Fatalf("err = %v", err)
	}
}

// Live: one real model through the broker, when its key is set.
func TestLive(t *testing.T) {
	if os.Getenv("JANETCODE_SDK_LIVE") == "" {
		t.Skip("set JANETCODE_SDK_LIVE=1 with OPENAI_API_KEY/OPENAI_BASE_URL to run against a real model")
	}
	bin := janetcode(t)
	work := t.TempDir()
	model := os.Getenv("JANETCODE_SDK_MODEL")
	if model == "" {
		model = "deepseek/deepseek-v4.1-flash"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := sdk.Run(ctx, sdk.Options{Binary: bin, Dir: work, Provider: "openai", Model: model, Mode: sdk.ModeAutoEdits, Stderr: os.Stderr},
		"Create a file named greeting.txt containing exactly the word hello, then say done.")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(work, "greeting.txt"))
	t.Logf("result: stop=%s verdict=%q usage=%+v tools=%d text=%q file=%q", res.StopReason, res.Verdict, res.Usage, len(res.ToolCalls), res.Text, got)
	if !strings.Contains(strings.ToLower(string(got)), "hello") {
		t.Fatalf("greeting.txt = %q", got)
	}
}
