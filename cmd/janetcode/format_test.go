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

	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/headless"
)

// A headless run in json mode puts one machine-readable object on stdout
// and nothing else, so a script never has to parse the prose.
func TestOutputFormatJSONEndToEnd(t *testing.T) {
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello\n"), 0o644)
	home := t.TempDir()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		var chunk string
		if calls == 1 {
			chunk = `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"read_file","arguments":"{\"path\":\"hello.txt\"}"}}]},"finish_reason":"tool_calls"}]}`
		} else {
			chunk = `{"choices":[{"delta":{"content":"it says hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":7}}`
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
	err := run([]string{"-p", "what does hello.txt say", "--provider", "openai", "--model", "test-model", "--yolo", "--output-format", "json"})
	out, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	var res headless.CLIResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", jerr, out)
	}
	if res.Type != headless.CLIResultType || res.Harness != "janetcode" {
		t.Fatalf("envelope = %+v", res)
	}
	if !strings.Contains(res.Result, "it says hello") {
		t.Errorf("result = %q", res.Result)
	}
	if res.ToolCalls != 1 || res.NumTurns < 1 {
		t.Errorf("tools=%d turns=%d", res.ToolCalls, res.NumTurns)
	}
	if res.Usage.InputTokens == 0 || res.Usage.OutputTokens == 0 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.SessionID == "" || res.Model != "test-model" {
		t.Errorf("identity = %+v", res)
	}
	if res.CostSource == "" || res.DurationMS < 0 || res.IsError || res.ExitCode != 0 {
		t.Errorf("res = %+v", res)
	}
	// A run that needed no rescuing says so with zeroes rather than by
	// leaving the field out: a caller must not have to tell "nothing to
	// report" from "this version does not report it".
	if res.Recoveries.Total != 0 || !strings.Contains(out, `"recoveries"`) {
		t.Errorf("recoveries = %+v in %s", res.Recoveries, out)
	}
	// The eval reader recognises it without being told which harness it is.
	if got := eval.ExtractAssistantText([]byte(out)); !strings.Contains(got, "it says hello") {
		t.Errorf("eval reader got %q", got)
	}
	if u := eval.ParseUsageJSON([]byte(out)); u.Input.Value != res.Usage.InputTokens {
		t.Errorf("eval usage = %+v", u)
	}
}

func TestOutputFormatStreamJSONEndToEnd(t *testing.T) {
	work := t.TempDir()
	home := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: "+`{"choices":[{"delta":{"content":"hello there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":9,"completion_tokens":2}}`+"\n\ndata: [DONE]\n\n")
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
	err := run([]string{"-p", "hi", "--provider", "openai", "--model", "test-model", "--output-format", "stream-json"})
	out, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		t.Fatalf("stream = %q", out)
	}
	for _, l := range lines {
		var m map[string]any
		if jerr := json.Unmarshal([]byte(l), &m); jerr != nil {
			t.Fatalf("line is not JSON: %q", l)
		}
	}
	var res headless.CLIResult
	if jerr := json.Unmarshal([]byte(lines[len(lines)-1]), &res); jerr != nil || res.Harness != "janetcode" {
		t.Fatalf("last line is not the result: %q", lines[len(lines)-1])
	}
	if !strings.Contains(res.Result, "hello there") {
		t.Fatalf("result = %q", res.Result)
	}
}

// A format that only makes sense headless says so, and exits 42.
func TestOutputFormatNeedsAHeadlessRun(t *testing.T) {
	err := run([]string{"--output-format", "json"})
	if err == nil || headless.ExitCode(err) != headless.ExitBadInput {
		t.Fatalf("err = %v, exit = %d", err, headless.ExitCode(err))
	}
	err = run([]string{"-p", "x", "--output-format", "yaml"})
	if err == nil || headless.ExitCode(err) != headless.ExitBadInput {
		t.Fatalf("err = %v, exit = %d", err, headless.ExitCode(err))
	}
}

// A --recover run is a headless run without -p: it reports like one, so a
// supervisor that restarts crashed runs can read the result as JSON.
func TestOutputFormatAcceptsRecover(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Chdir(t.TempDir())
	err := run([]string{"-c", "--recover", "--output-format", "json"})
	if err != nil && strings.Contains(err.Error(), "--output-format applies") {
		t.Fatalf("--recover refused --output-format: %v", err)
	}
}

// What the harness had to rescue belongs in the envelope: a benchmark
// that cannot see it credits the model with the harness's repair work.
func TestJSONEnvelopeReportsRecoveries(t *testing.T) {
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello\n"), 0o644)
	home := t.TempDir()

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		var chunk string
		if calls == 1 {
			// The model writes the call as prose instead of emitting it.
			prose := `I will read it.\n<invoke name="read_file">\n<parameter name="path">hello.txt</parameter>\n</invoke>`
			body, _ := json.Marshal(prose)
			chunk = `{"choices":[{"delta":{"content":` + string(body) + `},"finish_reason":"stop"}]}`
		} else {
			chunk = `{"choices":[{"delta":{"content":"it says hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":40,"completion_tokens":5}}`
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
	err := run([]string{"-p", "what does hello.txt say", "--provider", "openai", "--model", "test-model", "--yolo", "--output-format", "json"})
	out, errOut := stdout(), stderr()
	if err != nil {
		t.Fatalf("run: %v\nstderr: %s", err, errOut)
	}
	var res headless.CLIResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", jerr, out)
	}
	if res.Recoveries.ProseToolCalls != 1 || res.Recoveries.Total != 1 {
		t.Fatalf("recoveries = %+v\n%s", res.Recoveries, out)
	}
	if res.Recoveries.WatchdogKills != 0 || res.Recoveries.ModelFallbacks != 0 {
		t.Errorf("recoveries = %+v", res.Recoveries)
	}
	// The recovered call really ran: this is a rescue, not a tally.
	if res.ToolCalls != 1 {
		t.Errorf("tool calls = %d", res.ToolCalls)
	}
}
