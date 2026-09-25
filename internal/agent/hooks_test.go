package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// hookLog binds a hook that saves each stdin it gets to its own file,
// named by the time it ran (background hooks run at once), and returns a
// reader for what was recorded, oldest first.
func hookLog(t *testing.T) (string, func() []map[string]any) {
	t.Helper()
	dir := t.TempDir()
	cmd := `cat > '` + dir + `'/$(date +%s%N)`
	return cmd, func() []map[string]any {
		names, _ := filepath.Glob(filepath.Join(dir, "*"))
		sort.Strings(names)
		var got []map[string]any
		for _, n := range names {
			data, _ := os.ReadFile(n)
			var m map[string]any
			if err := json.Unmarshal(data, &m); err != nil {
				continue // still being written
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

// eventually waits for a background hook to have recorded n stdins.
func eventually(t *testing.T, logged func() []map[string]any, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := logged()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAPermissionRequestHookAnswersInTheUsersPlace(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"ok.txt","content":"x"}`),
		toolCall("t2", "write_file", `{"path":"no.txt","content":"x"}`),
		text("done"),
	}}
	ap := &seesApprover{decision: Allow}
	a, root := newAgent(t, f, ap)
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.PermissionAsk: {{Match: "Write", Command: `if grep -q ok.txt; then echo '{"hookSpecificOutput":{"decision":{"behavior":"allow"}}}'; else echo '{"hookSpecificOutput":{"decision":{"behavior":"deny","message":"not that file"}}}'; fi`}},
	}}}
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	if len(ap.inputs) != 0 {
		t.Fatalf("the user was asked %d times; the hook answered both", len(ap.inputs))
	}
	if _, err := os.Stat(filepath.Join(root, "ok.txt")); err != nil {
		t.Fatalf("allowed write did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "no.txt")); err == nil {
		t.Fatal("denied write ran")
	}
	if r := a.Messages[4].Blocks[0]; !r.IsError || !strings.Contains(r.Text, "not that file") {
		t.Fatalf("deny result = %+v", r)
	}
}

func TestNotificationHooksHearWhenTheUserIsNeeded(t *testing.T) {
	f := &fake{responses: []*llm.Response{toolCall("t1", "write_file", `{"path":"x","content":"x"}`), text("done")}}
	a, root := newAgent(t, f, &seesApprover{decision: Allow})
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: root})
	logCmd, logged := hookLog(t)
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.Notification: {{Command: logCmd}},
	}}}
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, m := range eventually(t, logged, 2) {
		kinds[str(m["notification_type"])] = true
	}
	if !kinds["permission_prompt"] || !kinds["idle_prompt"] {
		t.Fatalf("notifications = %v", kinds)
	}
}

func TestStopFailureSaysWhyATurnFailed(t *testing.T) {
	f := &fake{responses: []*llm.Response{toolCall("t1", "read_file", `{"path":"a"}`), toolCall("t2", "read_file", `{"path":"b"}`)}}
	a, root := newAgent(t, f, nil)
	a.MaxIterations = 1
	a.Tools = tool.NewRegistry(tool.ReadFile{Root: root})
	logCmd, logged := hookLog(t)
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{Hooks: hook.Hooks{
		hook.StopFailure:  {{Command: logCmd}},
		hook.Notification: {{Command: logCmd}},
	}}}
	if err := a.Run(context.Background(), "go", func(Event) {}); !errors.Is(err, ErrMaxIterations) {
		t.Fatalf("err = %v", err)
	}
	got := eventually(t, logged, 1)
	time.Sleep(50 * time.Millisecond) // an idle_prompt would have landed by now
	got = logged()
	if len(got) != 1 || got[0]["hook_event_name"] != "StopFailure" || got[0]["error"] != "max_turns" {
		t.Fatalf("hook stdins = %v", got)
	}
}

func TestSubagentHooksBracketASubagent(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("REPORT: all clear")}}
	parent := &Agent{Provider: f, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: t.TempDir()}, task)
	logCmd, logged := hookLog(t)
	parent.Hooks = &hook.Runner{Cwd: t.TempDir(), Config: hook.Config{Hooks: hook.Hooks{
		hook.SubagentStart: {{Command: logCmd + `; echo "use the staging database"`}},
		hook.SubagentStop:  {{Command: logCmd}},
	}}}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"one","prompt":"look around"}`)); err != nil {
		t.Fatal(err)
	}
	if brief := f.requests[0].Messages[0].Text(); !strings.Contains(brief, "look around") || !strings.Contains(brief, "use the staging database") {
		t.Fatalf("brief = %q", brief)
	}
	got := eventually(t, logged, 2)
	if len(got) != 2 || got[0]["hook_event_name"] != "SubagentStart" || got[0]["agent_type"] != "general" ||
		got[1]["hook_event_name"] != "SubagentStop" || got[1]["last_assistant_message"] != "REPORT: all clear" ||
		got[0]["agent_id"] != got[1]["agent_id"] {
		t.Fatalf("hook stdins = %v", got)
	}
}

func str(v any) string { s, _ := v.(string); return s }
