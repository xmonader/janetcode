package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/headless"
)

// e2eStep is one scripted model reply: a tool call, text, or an HTTP error.
type e2eStep struct {
	tool, args string // a tool call
	text       string // a final answer
	status     int    // a non-200 reply (no retry for 400)
}

// e2eServer serves the steps in order, one per model request. It is the
// fake OpenAI-compatible endpoint these tests run the real command line
// against.
func e2eServer(t *testing.T, steps ...e2eStep) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		i := n
		n++
		mu.Unlock()
		s := e2eStep{text: "done"}
		if i < len(steps) {
			s = steps[i]
		}
		if s.status != 0 {
			http.Error(w, `{"error":{"message":"bad request from the fake model"}}`, s.status)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		var chunk string
		if s.tool != "" {
			args, _ := json.Marshal(s.args)
			chunk = fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c%d","function":{"name":%q,"arguments":%s}}]},"finish_reason":"tool_calls"}]}`, i, s.tool, args)
		} else {
			txt, _ := json.Marshal(s.text)
			chunk = fmt.Sprintf(`{"choices":[{"delta":{"content":%s},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2}}`, txt)
		}
		io.WriteString(w, "data: "+chunk+"\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("OPENAI_API_KEY", "test-key")
	t.Setenv("OPENAI_BASE_URL", srv.URL+"/v1")
	t.Setenv("ANTHROPIC_API_KEY", "")
}

func e2eRun(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	stdout, stderr := captureStd(t)
	err := run(append(args, "--provider", "openai", "--model", "test-model"))
	return stdout(), stderr(), err
}

// A repo-level deny rule outranks --yolo: the call is refused and the file
// is never written.
func TestE2EProjectDenyBeatsYolo(t *testing.T) {
	work := t.TempDir()
	os.MkdirAll(filepath.Join(work, ".janetcode"), 0o755)
	os.WriteFile(filepath.Join(work, ".janetcode", "permissions.json"), []byte(`{"deny":["write_file(secret.txt)"]}`), 0o644)
	e2eServer(t, e2eStep{tool: "write_file", args: `{"path":"secret.txt","content":"pwned"}`}, e2eStep{text: "tried"})
	t.Chdir(work)
	out, errOut, err := e2eRun(t, "-p", "write it", "--yolo", "--output-format", "stream-json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, errOut)
	}
	if _, serr := os.Stat(filepath.Join(work, "secret.txt")); serr == nil {
		t.Fatal("the denied write happened under --yolo")
	}
	if !strings.Contains(out, `"tool_result"`) || !strings.Contains(out, `"is_error":true`) {
		t.Errorf("no refused tool_result in %s", out)
	}
}

// There is no --allowed-tools: the per-turn equivalent is --deny (or
// --exclude-tools), which lasts for that run only. A later --resume turn
// without it can use the tool again.
func TestE2EPerTurnDenyDoesNotStick(t *testing.T) {
	work := t.TempDir()
	e2eServer(t,
		e2eStep{tool: "write_file", args: `{"path":"a.txt","content":"one"}`}, e2eStep{text: "blocked"},
		e2eStep{tool: "write_file", args: `{"path":"a.txt","content":"two"}`}, e2eStep{text: "wrote"},
	)
	t.Chdir(work)
	out, errOut, err := e2eRun(t, "-p", "write", "--yolo", "--deny", "write_file", "--output-format", "json")
	if err != nil {
		t.Fatalf("turn 1: %v\n%s", err, errOut)
	}
	if _, serr := os.Stat(filepath.Join(work, "a.txt")); serr == nil {
		t.Fatal("--deny write_file did not stop the write")
	}
	var res headless.CLIResult
	if jerr := json.Unmarshal([]byte(out), &res); jerr != nil || res.SessionID == "" {
		t.Fatalf("turn 1 envelope: %v %s", jerr, out)
	}
	_, errOut, err = e2eRun(t, "-p", "write again", "--yolo", "--resume", res.SessionID, "--output-format", "json")
	if err != nil {
		t.Fatalf("turn 2: %v\n%s", err, errOut)
	}
	b, rerr := os.ReadFile(filepath.Join(work, "a.txt"))
	if rerr != nil || string(b) != "two" {
		t.Fatalf("the resumed turn could not write: %q %v", b, rerr)
	}
}

// N stdin messages, one failing, produce N result lines, and the failure
// does not end the conversation. (streamin_test.go covers the success and
// interrupt cases only.)
func TestE2EStreamInputFailedTurnKeepsGoing(t *testing.T) {
	work := t.TempDir()
	e2eServer(t, e2eStep{text: "first"}, e2eStep{status: 400}, e2eStep{text: "third"})
	t.Chdir(work)
	r, w, _ := os.Pipe()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	go func() {
		io.WriteString(w, `{"type":"user","text":"one"}`+"\n"+`{"type":"user","text":"two"}`+"\n"+`{"type":"user","text":"three"}`+"\n")
		w.Close()
	}()
	out, errOut, _ := e2eRun(t, "-p", "", "--input-format", "stream-json", "--output-format", "stream-json", "--yolo")
	var results []headless.CLIResult
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		var m struct{ Type string }
		if json.Unmarshal([]byte(l), &m) == nil && m.Type == headless.CLIResultType {
			var res headless.CLIResult
			json.Unmarshal([]byte(l), &res)
			results = append(results, res)
		}
	}
	if len(results) != 3 {
		t.Fatalf("%d result lines, want 3\n%s\nstderr: %s", len(results), out, errOut)
	}
	if results[0].IsError || !results[1].IsError || results[2].IsError || !strings.Contains(results[2].Result, "third") {
		t.Errorf("results = %+v", results)
	}
}

// A subagent that fails is named on stderr in text mode, and its
// tool_result carries ok:false and a clipped report in stream-json.
func TestE2EFailedSubagentIsNamed(t *testing.T) {
	task := e2eStep{tool: "task", args: `{"description":"probe","prompt":"look around"}`}
	for _, format := range []string{"text", "stream-json"} {
		t.Run(format, func(t *testing.T) {
			work := t.TempDir()
			e2eServer(t, task, e2eStep{status: 400}, e2eStep{text: "the subagent failed"})
			t.Chdir(work)
			out, errOut, err := e2eRun(t, "-p", "delegate", "--yolo", "--output-format", format)
			if err != nil {
				t.Fatalf("run: %v\n%s", err, errOut)
			}
			if format == "text" {
				if !strings.Contains(errOut, "✗ subagent ") || !strings.Contains(errOut, " failed: ") {
					t.Errorf("stderr lacks the failed-subagent line: %q", errOut)
				}
				if strings.Contains(out, "✗ subagent") {
					t.Errorf("stdout carries the line: %q", out)
				}
				return
			}
			found := false
			for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
				var m struct {
					Type, Name, Report string
					OK                 *bool
				}
				if json.Unmarshal([]byte(l), &m) == nil && m.Type == "tool_result" && m.Name == "task" {
					found = true
					if m.OK == nil || *m.OK || m.Report == "" {
						t.Errorf("task result = %s", l)
					}
				}
			}
			if !found {
				t.Errorf("no task tool_result in %s", out)
			}
		})
	}
}
