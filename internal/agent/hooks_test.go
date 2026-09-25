package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// hookLog binds a hook that appends each stdin it gets to a file, one
// JSON object per line, and returns a reader for what it recorded.
func hookLog(t *testing.T) (string, func() []map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hook.log")
	cmd := `cat >> '` + path + `'; echo >> '` + path + `'`
	return cmd, func() []map[string]any {
		data, _ := os.ReadFile(path)
		var got []map[string]any
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("hook stdin %q: %v", line, err)
			}
			got = append(got, m)
		}
		return got
	}
}

func TestAUserPromptHookCanRefuseAPromptBeforeAnythingIsSent(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("never")}}
	a, root := newAgent(t, f, nil)
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.UserPrompt: {{Command: `grep -q secret && { echo "no secrets in prompts" >&2; exit 2; }; exit 0`}},
	}}}
	err := a.Run(context.Background(), "the secret is 42", func(Event) {})
	var blocked *ErrPromptBlocked
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "no secrets in prompts") {
		t.Fatalf("err = %v", err)
	}
	if len(f.requests) != 0 || len(a.Messages) != 0 {
		t.Fatalf("requests = %d, messages = %d: a refused prompt went through", len(f.requests), len(a.Messages))
	}
}

func TestSessionStartAndUserPromptContextRideWithThePrompt(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("one"), text("two")}}
	a, root := newAgent(t, f, nil)
	logCmd, logged := hookLog(t)
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.SessionStart: {{Command: logCmd + `; echo '{"hookSpecificOutput":{"additionalContext":"branch is main"}}'`}},
		hook.UserPrompt:   {{Command: logCmd + `; echo "today is Friday"`}},
	}}}
	for _, p := range []string{"first", "second"} {
		if err := a.Run(context.Background(), p, func(Event) {}); err != nil {
			t.Fatal(err)
		}
	}
	got := logged()
	// session_start once, user_prompt per turn.
	if len(got) != 3 || got[0]["hook_event_name"] != "SessionStart" || got[0]["source"] != "startup" ||
		got[1]["prompt"] != "first" || got[2]["prompt"] != "second" {
		t.Fatalf("hook stdins = %v", got)
	}
	first := f.requests[0].Messages[len(f.requests[0].Messages)-1].Text()
	if !strings.Contains(first, "branch is main") || !strings.Contains(first, "today is Friday") {
		t.Fatalf("first prompt = %q", first)
	}
	second := f.requests[1].Messages[len(f.requests[1].Messages)-1].Text()
	if strings.Contains(second, "branch is main") || !strings.Contains(second, "today is Friday") {
		t.Fatalf("second prompt = %q", second)
	}
}

func TestSessionEndFiresOnceForASessionThatRan(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("one"), text("two")}}
	a, root := newAgent(t, f, nil)
	logCmd, logged := hookLog(t)
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.SessionStart: {{Command: logCmd}},
		hook.SessionEnd:   {{Command: logCmd}},
	}}}
	a.EndSession("exit") // nothing ran yet: no session_end
	if err := a.Run(context.Background(), "first", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	sess, err := session.New(t.TempDir(), t.TempDir(), "p", "m")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.OpenSession(sess, nil); err != nil { // /clear
		t.Fatal(err)
	}
	if err := a.Run(context.Background(), "second", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	a.EndSession("exit")
	a.EndSession("exit") // twice: fires once
	var seq []string
	for _, m := range logged() {
		seq = append(seq, m["hook_event_name"].(string)+":"+str(m["source"])+str(m["reason"]))
	}
	want := "SessionStart:startup SessionEnd:clear SessionStart:clear SessionEnd:exit"
	if strings.Join(seq, " ") != want {
		t.Fatalf("sequence = %q, want %q", strings.Join(seq, " "), want)
	}
}

func TestAPreToolHookThatAsksPromptsEvenInYolo(t *testing.T) {
	f := &fake{responses: []*llm.Response{toolCall("t1", "bash", `{"command":"echo hi"}`), text("done")}}
	ap := &seesApprover{decision: Allow}
	a, root := newAgent(t, f, ap)
	a.AutoApprove = true
	a.Tools = tool.NewRegistry(tool.Bash{Root: root})
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.PreTool: {{Match: "Bash", Command: `echo '{"hookSpecificOutput":{"permissionDecision":"ask","permissionDecisionReason":"check first"}}'`}},
	}}}
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(ap.inputs) != 1 {
		t.Fatalf("approver asked %d times, want 1", len(ap.inputs))
	}
}

func TestPostToolFailureSeesTheError(t *testing.T) {
	f := &fake{responses: []*llm.Response{toolCall("t1", "read_file", `{"path":"missing.go"}`), text("done")}}
	a, root := newAgent(t, f, nil)
	a.Tools = tool.NewRegistry(tool.ReadFile{Root: root})
	logCmd, logged := hookLog(t)
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.PostToolFailure: {{Match: "Read", Command: logCmd}},
	}}}
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	got := logged()
	if len(got) != 1 || got[0]["hook_event_name"] != "PostToolUseFailure" || !strings.Contains(str(got[0]["error"]), "missing.go") {
		t.Fatalf("hook stdins = %v", got)
	}
}

func TestPostCompactSeesTheSummaryAndTrigger(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("SUMMARY")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"), llm.TextMessage(llm.RoleAssistant, "ok"),
	}}
	logCmd, logged := hookLog(t)
	a.Hooks = &hook.Runner{Cwd: t.TempDir(), Config: hook.Config{Hooks: hook.Hooks{
		hook.PreCompact:  {{Command: logCmd}},
		hook.PostCompact: {{Command: logCmd}},
	}}}
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	got := logged()
	if len(got) != 2 || got[0]["trigger"] != "manual" || got[1]["hook_event_name"] != "PostCompact" ||
		got[1]["trigger"] != "manual" || !strings.Contains(str(got[1]["compact_summary"]), "SUMMARY") {
		t.Fatalf("hook stdins = %v", got)
	}
}

func str(v any) string { s, _ := v.(string); return s }
