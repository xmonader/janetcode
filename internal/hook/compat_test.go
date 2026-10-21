package hook

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// TestClaudeFormatConfigRunsUnchanged: a hooks object copied from Claude
// Code or Qwen Code -- their event names, groups with a matcher, their
// tool names -- loads and runs here, and a hook type janetcode cannot run
// is refused by name.
func TestClaudeFormatConfigRunsUnchanged(t *testing.T) {
	src := `{
	  "PreToolUse": [
	    {"matcher": "Bash", "hooks": [{"type": "command", "command": "echo '{\"hookSpecificOutput\":{\"hookEventName\":\"PreToolUse\",\"permissionDecision\":\"deny\",\"permissionDecisionReason\":\"no shell today\"}}'"}]},
	    {"matcher": "Write|Edit", "hooks": [{"type": "command", "command": "echo edited", "timeout": 5}]}
	  ],
	  "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "echo '{\"hookSpecificOutput\":{\"additionalContext\":\"branch is main\"}}'"}]}],
	  "pre_tool": [{"match": "^read_file$", "command": "echo native"}]
	}`
	var c Config
	if err := json.Unmarshal([]byte(src), &c); err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if n := len(c.For(PreTool)); n != 3 {
		t.Fatalf("pre_tool has %d hooks, want the two groups' and the native one", n)
	}
	r := &Runner{Cwd: t.TempDir(), Config: c}
	ctx := context.Background()
	if res := r.Run(ctx, Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{}`)}); !res.Denied || res.Reason != "no shell today" {
		t.Fatalf("Bash matcher / permissionDecision: %+v", res)
	}
	if res := r.Run(ctx, Input{Event: PreTool, Tool: "edit_file", Input: json.RawMessage(`{}`)}); res.Denied || len(res.Notes) != 1 || res.Notes[0] != "edited" {
		t.Fatalf("Write|Edit matcher: %+v", res)
	}
	if res := r.Run(ctx, Input{Event: PreTool, Tool: "read_file", Input: json.RawMessage(`{}`)}); len(res.Notes) != 1 || res.Notes[0] != "native" {
		t.Fatalf("an alias leaked onto read_file, or the native hook did not run: %+v", res)
	}
	if res := r.Run(ctx, Input{Event: UserPrompt, Prompt: "hi"}); len(res.Notes) != 1 || res.Notes[0] != "branch is main" {
		t.Fatalf("additionalContext: %+v", res)
	}

	bad := `{"PreToolUse": [{"matcher": "*", "hooks": [{"type": "prompt", "prompt": "is this safe?"}]}]}`
	if err := json.Unmarshal([]byte(bad), &c); err == nil || !strings.Contains(err.Error(), `"prompt" hooks are not supported`) {
		t.Fatalf("err = %v", err)
	}
}

// TestInputCarriesTheirFieldNames: a script written for the other
// harnesses reads tool_name, tool_input and hook_event_name.
func TestInputCarriesTheirFieldNames(t *testing.T) {
	b, err := json.Marshal(Input{Event: PostTool, Cwd: "/w", Tool: "bash", Input: json.RawMessage(`{"command":"ls"}`), Output: "a\nb"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for k, want := range map[string]any{"event": "post_tool", "tool": "bash", "hook_event_name": "PostToolUse", "tool_name": "bash", "tool_response": "a\nb", "cwd": "/w"} {
		if m[k] != want {
			t.Errorf("%s = %v, want %v (%s)", k, m[k], want, b)
		}
	}
	if ti, _ := m["tool_input"].(map[string]any); ti["command"] != "ls" {
		t.Errorf("tool_input = %v", m["tool_input"])
	}
	b, _ = json.Marshal(Input{Event: StopFailure, ErrorType: "rate_limit", Error: "429 slow down"})
	_ = json.Unmarshal(b, &m)
	if m["error"] != "rate_limit" || m["error_details"] != "429 slow down" || m["error_type"] != "rate_limit" {
		t.Errorf("stop_failure fields: %s", b)
	}
}

// TestTheirAnswersAreUnderstood: continue:false, systemMessage,
// permissionDecision "ask" and allow, updatedInput and a permission
// request's decision object.
func TestTheirAnswersAreUnderstood(t *testing.T) {
	for _, c := range []struct {
		out  string
		want Output
	}{
		{`{"continue": false, "stopReason": "frozen"}`, Output{Decision: "block", Reason: "frozen"}},
		{`{"systemMessage": "formatted 3 files"}`, Output{Message: "formatted 3 files"}},
		{`{"hookSpecificOutput": {"permissionDecision": "ask", "permissionDecisionReason": "prod"}}`, Output{Decision: "ask", Reason: "prod"}},
		{`{"hookSpecificOutput": {"permissionDecision": "allow", "updatedInput": {"command": "ls -a"}}}`, Output{Decision: "allow", Input: json.RawMessage(`{"command": "ls -a"}`)}},
		{`{"hookSpecificOutput": {"decision": {"behavior": "deny", "message": "not on fridays"}}}`, Output{Decision: "deny", Reason: "not on fridays"}},
		{`{"continue": "run the tests first"}`, Output{Continue: "run the tests first"}},
	} {
		got, ok := parseOutput(c.out)
		if !ok || got.Decision != c.want.Decision || got.Reason != c.want.Reason || got.Message != c.want.Message ||
			got.Continue != c.want.Continue || string(got.Input) != string(c.want.Input) {
			t.Errorf("%s: got %+v", c.out, got)
		}
	}
	if _, ok := parseOutput("plain words"); ok {
		t.Error("plain text parsed as JSON")
	}

	r := &Runner{Cwd: t.TempDir(), Config: Config{Hooks: Hooks{PreTool: {{Command: `echo '{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"prod db"},"systemMessage":"checked"}'`}}}}}
	res := r.Run(context.Background(), Input{Event: PreTool, Tool: "bash", Input: json.RawMessage(`{}`)})
	if !res.Ask || res.Denied || len(res.Messages) != 1 || res.Messages[0] != "checked" {
		t.Fatalf("ask: %+v", res)
	}
}

// On their Stop hook, "continue": false means "stop now": it outranks a
// block, which is what sends the model back to work. Read as a block,
// it did the opposite and fed the stopReason back as the next prompt.
func TestTheirContinueFalseOnStopLetsTheTurnEnd(t *testing.T) {
	r := &Runner{Cwd: t.TempDir(), Config: Config{Hooks: Hooks{Stop: {{Command: `echo '{"continue": false, "stopReason": "budget spent"}'`}}}}}
	res := r.Run(context.Background(), Input{Event: Stop, Text: "done"})
	if res.Continue != "" || res.Denied {
		t.Fatalf("continue:false sent the model back to work: %+v", res)
	}
	// A block is still a block.
	r = &Runner{Cwd: t.TempDir(), Config: Config{Hooks: Hooks{Stop: {{Command: `echo '{"decision": "block", "reason": "run the tests"}'`}}}}}
	if res := r.Run(context.Background(), Input{Event: Stop, Text: "done"}); res.Continue != "run the tests" {
		t.Fatalf("block on stop: %+v", res)
	}
}
