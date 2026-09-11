package headless

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/config"
	"github.com/xmonader/janetcode/internal/eval"
	"github.com/xmonader/janetcode/internal/llm"
)

func TestParseFormat(t *testing.T) {
	for _, ok := range []string{"", "text", "json", "stream-json"} {
		if _, err := ParseFormat(ok); err != nil {
			t.Errorf("ParseFormat(%q) = %v", ok, err)
		}
	}
	if _, err := ParseFormat("yaml"); err == nil {
		t.Fatal("an unknown format must be refused")
	}
}

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, ExitOK},
		{errors.New("something"), ExitOther},
		{context.Canceled, ExitCancelled},
		{fmt.Errorf("wrapped: %w", context.Canceled), ExitCancelled},
		{fmt.Errorf("%w: spent $1", ErrBudget), ExitBudget},
		{fmt.Errorf("turn: %w", agent.ErrMaxIterations), ExitTurnLimit},
		{fmt.Errorf("%w: two prompts", ErrBadInput), ExitBadInput},
		{fmt.Errorf("%w: %v", ErrConfig, errors.New("bad json")), ExitConfig},
		{fmt.Errorf("%w: unknown provider %q", config.ErrBadConfig, "nope"), ExitConfig},
		{fmt.Errorf("x: %w", llm.ErrNoAPIKey), ExitAuth},
		{fmt.Errorf("x: %w", config.ErrNoCredentials), ExitAuth},
		{errors.New("provider refused (HTTP 401)"), ExitAuth},
		{errors.New("provider refused (HTTP 403)"), ExitAuth},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

// fakeAgentEvents replays a run's events through the recorder.
func replay(r *recorder, events ...agent.Event) {
	for _, e := range events {
		r.event(e)
	}
}

func call(id, name string) *agent.ToolCall {
	return &agent.ToolCall{ID: id, Name: name, Input: json.RawMessage(`{"command":"go test"}`)}
}

func TestRecorderBuildsTheResultEnvelope(t *testing.T) {
	var out strings.Builder
	opts := Options{Stdout: &out, Agent: &agent.Agent{Model: "m", Price: &llm.Price{Input: 1, Output: 2}}}
	r := newRecorder(opts, FormatJSON)
	replay(r,
		agent.Event{Kind: agent.EvText, Text: "the "},
		agent.Event{Kind: agent.EvText, Text: "answer"},
		agent.Event{Kind: agent.EvToolRun, Call: call("t1", "bash")},
		agent.Event{Kind: agent.EvToolResult, Call: call("t1", "bash"), Output: "ok", Duration: time.Second},
		agent.Event{Kind: agent.EvToolRun, Call: call("t2", "write_file")},
		agent.Event{Kind: agent.EvToolResult, Call: call("t2", "write_file"), Output: agent.DeniedMessage + " no", IsError: true},
		agent.Event{Kind: agent.EvVerdict, Text: "tests pass"},
		agent.Event{Kind: agent.EvTurnDone, Usage: llm.Usage{InputTokens: 1000, OutputTokens: 500, CacheReadTokens: 20, CacheWriteTokens: 10}, StopReason: llm.StopEndTurn},
	)
	res := r.result(nil, nil)
	if res.Type != eval.CLIResultType || res.Harness != "janetcode" {
		t.Fatalf("envelope identity = %+v", res)
	}
	if res.Result != "the answer" {
		t.Errorf("result = %q", res.Result)
	}
	if res.Usage.InputTokens != 1000 || res.Usage.OutputTokens != 500 || res.Usage.CacheReadTokens != 20 || res.Usage.CacheWriteTokens != 10 {
		t.Errorf("usage = %+v", res.Usage)
	}
	if res.ToolCalls != 2 || res.PermissionDenials != 1 || res.NumTurns != 1 {
		t.Errorf("counts: tools=%d denials=%d turns=%d", res.ToolCalls, res.PermissionDenials, res.NumTurns)
	}
	if res.Verdict != "tests pass" {
		t.Errorf("verdict = %q", res.Verdict)
	}
	// 1000/1e6*1 + 500/1e6*2 = 0.002
	if res.CostUSD < 0.0019 || res.CostUSD > 0.0021 || res.CostSource != "list price" {
		t.Errorf("cost = %v (%s)", res.CostUSD, res.CostSource)
	}
	if res.IsError || res.ExitCode != ExitOK {
		t.Errorf("clean run reported as an error: %+v", res)
	}

	// An unpriced model says so rather than reporting zero as a fact.
	r2 := newRecorder(Options{Stdout: &out, Agent: &agent.Agent{}}, FormatJSON)
	if got := r2.result(nil, nil).CostSource; got != "unpriced" {
		t.Errorf("cost source = %q", got)
	}
}

func TestResultCarriesTheError(t *testing.T) {
	var out strings.Builder
	r := newRecorder(Options{Stdout: &out}, FormatJSON)
	res := r.result(nil, fmt.Errorf("turn: %w", agent.ErrMaxIterations))
	if !res.IsError || res.ExitCode != ExitTurnLimit || !strings.Contains(res.Error, "iterations") {
		t.Fatalf("res = %+v", res)
	}
}

func TestJSONFormatPrintsOneObject(t *testing.T) {
	var out strings.Builder
	r := newRecorder(Options{Stdout: &out}, FormatJSON)
	replay(r, agent.Event{Kind: agent.EvText, Text: "hi"},
		agent.Event{Kind: agent.EvTurnDone, Usage: llm.Usage{InputTokens: 3}})
	r.finish(nil, nil)
	var res eval.CLIResult
	if err := json.Unmarshal([]byte(out.String()), &res); err != nil {
		t.Fatalf("stdout is not one object: %v\n%s", err, out.String())
	}
	if res.Result != "hi" {
		t.Fatalf("res = %+v", res)
	}
}

func TestStreamJSONPrintsOneObjectPerLine(t *testing.T) {
	var out strings.Builder
	r := newRecorder(Options{Stdout: &out}, FormatStreamJSON)
	replay(r,
		agent.Event{Kind: agent.EvText, Text: "hi"},
		agent.Event{Kind: agent.EvToolRun, Call: call("t1", "bash")},
		agent.Event{Kind: agent.EvToolResult, Call: call("t1", "bash"), Output: "ok"},
		agent.Event{Kind: agent.EvNotice, Text: "a notice"},
		agent.Event{Kind: agent.EvVerdict, Text: "tests pass"},
		agent.Event{Kind: agent.EvTurnDone, Usage: llm.Usage{InputTokens: 3}},
	)
	r.finish(nil, nil)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 7 {
		t.Fatalf("%d lines, want 7:\n%s", len(lines), out.String())
	}
	var kinds []string
	for _, l := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line is not JSON: %q", l)
		}
		kinds = append(kinds, m["type"].(string))
	}
	want := "text tool_start tool_result notice verdict turn_done result"
	if strings.Join(kinds, " ") != want {
		t.Fatalf("kinds = %v, want %s", kinds, want)
	}
	// The last line is the same envelope json mode prints.
	var res eval.CLIResult
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil || res.Harness != "janetcode" {
		t.Fatalf("last line = %q %v", lines[len(lines)-1], err)
	}
}

// A retry that succeeded is a line of the stream and a count in the
// result: the countdown that was shown while it happened is gone, and
// the run otherwise looks like one that never had to wait.
func TestStreamJSONReportsARetry(t *testing.T) {
	var out strings.Builder
	r := newRecorder(Options{Stdout: &out}, FormatStreamJSON)
	replay(r,
		agent.Event{Kind: agent.EvRetry, Text: "retried 2× · rate limited · waited 1.5s",
			Retry: &llm.Retry{Attempts: 2, Reason: "rate limited", Wait: 1500 * time.Millisecond}},
		agent.Event{Kind: agent.EvText, Text: "hi"},
		agent.Event{Kind: agent.EvTurnDone, Usage: llm.Usage{InputTokens: 3}},
	)
	r.finish(nil, nil)
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("line is not JSON: %q", lines[0])
	}
	if first["type"] != "retry" || first["attempts"] != 2.0 || first["wait_ms"] != 1500.0 || first["text"] != "rate limited" {
		t.Fatalf("retry line = %v", first)
	}
	var res eval.CLIResult
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &res); err != nil {
		t.Fatal(err)
	}
	if res.Retries != 1 {
		t.Fatalf("retries = %d, want 1", res.Retries)
	}
}

// A consumer of stream-json pairs results with starts by id. Some calls
// never run -- refused by a permission rule, hidden by read-only mode,
// stopped by the budget, naming a tool that does not exist -- and the
// loop emits a result for them with no EvToolRun, because nothing was
// about to execute. The stream still has to be well formed.
func TestStreamJSONPairsEveryResultWithAStart(t *testing.T) {
	var out strings.Builder
	r := newRecorder(Options{Stdout: &out}, FormatStreamJSON)
	replay(r,
		// Refused by a rule: a result and no run.
		agent.Event{Kind: agent.EvToolResult, Call: call("t1", "bash"),
			Output: "Denied by permission rule bash(*).", IsError: true, Denied: true},
		// A call that did run, the ordinary way.
		agent.Event{Kind: agent.EvToolRun, Call: call("t2", "read_file")},
		agent.Event{Kind: agent.EvToolResult, Call: call("t2", "read_file"), Output: "ok"},
		agent.Event{Kind: agent.EvTurnDone},
	)
	r.finish(nil, nil)
	started, ended := map[string]bool{}, map[string]bool{}
	var starts int
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line is not JSON: %q", l)
		}
		switch m["type"] {
		case "tool_start":
			starts++
			started[m["id"].(string)] = true
		case "tool_result":
			id := m["id"].(string)
			if !started[id] {
				t.Fatalf("result for %q with no start", id)
			}
			ended[id] = true
		}
	}
	if starts != 2 || len(ended) != 2 {
		t.Fatalf("%d starts, %d results", starts, len(ended))
	}
	// The refusal is counted, whichever gate wrote it: only the
	// interactive approver's wording used to be recognised, so a --deny
	// rule produced a run reporting no denials at all.
	if got := r.result(nil, nil).PermissionDenials; got != 1 {
		t.Fatalf("permission_denials = %d, want 1", got)
	}
}

// Text mode is unchanged: nothing JSON-shaped reaches stdout from here.
func TestTextFormatWritesNoEnvelope(t *testing.T) {
	var out strings.Builder
	r := newRecorder(Options{Stdout: &out}, FormatText)
	replay(r, agent.Event{Kind: agent.EvTurnDone})
	r.finish(nil, nil)
	if out.String() != "" {
		t.Fatalf("text mode wrote %q", out.String())
	}
	if !r.text() {
		t.Fatal("text mode should ask the caller to print")
	}
}

// A run that ends inside a turn -- on the budget, on an error -- still
// paid for the calls it made, and the envelope has to say so. The
// recorder only sees usage at EvTurnDone, which such a run never
// reaches, so the figure comes from the agent's own counter.
func TestEnvelopeReportsUsageOfAnUnfinishedTurn(t *testing.T) {
	ag := &agent.Agent{Model: "m", Price: &llm.Price{Input: 3, Output: 15},
		Usage: llm.Usage{InputTokens: 1000, OutputTokens: 2000}}
	r := newRecorder(Options{Stdout: io.Discard, Agent: ag}, FormatJSON)
	res := r.result(ag, fmt.Errorf("%w: spent it all", ErrBudget))
	if res.Usage.InputTokens != 1000 || res.Usage.OutputTokens != 2000 {
		t.Fatalf("usage = %+v", res.Usage)
	}
	if res.CostUSD == 0 || res.CostSource != "list price" {
		t.Fatalf("cost = %v (%s)", res.CostUSD, res.CostSource)
	}
	if res.ExitCode != ExitBudget {
		t.Fatalf("exit = %d", res.ExitCode)
	}
}
