package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/hook"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/tool"
	"github.com/xmonader/janetcode/internal/verify"
)

// seesApprover records the inputs it was asked to approve.
type seesApprover struct {
	inputs   []string
	decision Decision
}

func (s *seesApprover) Approve(_ context.Context, c ToolCall) (Decision, error) {
	s.inputs = append(s.inputs, string(c.Input))
	if s.decision == Deny {
		return Allow, nil
	}
	return s.decision, nil
}

func TestHooksDenyRewriteAnnotateAndContinue(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"rm -rf build"}`),        // denied by hook
		toolCall("t2", "write_file", `{"path":"x","content":"v1"}`), // rewritten by hook
		text("done"),        // stop hook sends it back
		text("really done"), // stop hook accepts
	}}
	ap := &seesApprover{}
	a, root := newAgent(t, f, ap)
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{
		hook.PreTool: {
			{Match: "bash", Command: `grep -q 'rm -rf' && { echo "no rm -rf" >&2; exit 2; }; exit 0`},
			{Match: "write_file", Command: `echo '{"input":{"path":"x","content":"v2"}}'`},
		},
		hook.PostTool: {{Match: "write_file", Command: `echo "remember to run gofmt"`}},
		hook.Stop:     {{Command: `grep -q '"really' || echo '{"continue":"not yet: run the tests"}'`}},
	}}
	var notices []string
	err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	res := func(i int) llm.Block { return a.Messages[i].Blocks[0] }
	if r := res(2); !r.IsError || !strings.Contains(r.Text, "Blocked by a hook: no rm -rf") {
		t.Fatalf("deny result = %+v", r)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "x")); string(data) != "v2" {
		t.Fatalf("x = %q, rewrite not applied", data)
	}
	// The approver saw the rewritten input, i.e. what actually ran.
	if len(ap.inputs) != 1 || !strings.Contains(ap.inputs[0], `"v2"`) {
		t.Fatalf("approver saw %q", ap.inputs)
	}
	if r := res(4); !strings.Contains(r.Text, "[hook: a hook rewrote this call's input to: {\"path\":\"x\",\"content\":\"v2\"}]") || !strings.Contains(r.Text, "[hook: remember to run gofmt]") {
		t.Fatalf("notes missing: %q", r.Text)
	}
	// Stop hook: "done" was pushed back once, then "really done" ended the turn.
	if len(notices) != 1 || !strings.Contains(notices[0], "not yet: run the tests") {
		t.Fatalf("notices = %q", notices)
	}
	last := a.Messages[len(a.Messages)-1]
	if last.Text() != "really done" || !strings.Contains(a.Messages[len(a.Messages)-2].Text(), "not yet: run the tests") {
		t.Fatalf("tail = %+v", a.Messages[len(a.Messages)-3:])
	}
	if len(f.requests) != 4 {
		t.Fatalf("requests = %d", len(f.requests))
	}
}

func TestStopHookIsBounded(t *testing.T) {
	var rs []*llm.Response
	for i := 0; i < 6; i++ {
		rs = append(rs, text("done"))
	}
	f := &fake{responses: rs}
	a, root := newAgent(t, f, nil)
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{hook.Stop: {{Command: `echo "keep going" >&2; exit 2`}}}}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != maxStopContinues+1 {
		t.Fatalf("requests = %d, want %d", len(f.requests), maxStopContinues+1)
	}
}

func TestPreCompactHookNoteLandsInHandoff(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("SUMMARY")}}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, "first"), llm.TextMessage(llm.RoleAssistant, "ok"),
	}}
	a.Hooks = &hook.Runner{Cwd: t.TempDir(), Config: hook.Config{hook.PreCompact: {{Command: `python3 -c 'import sys,json; d=json.load(sys.stdin); print("tokens were", d["tokens"])'`}}}}
	if err := a.ForceCompact(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := a.Messages[0].Text(); !strings.Contains(got, "[From a pre_compact hook:]\ntokens were ") {
		t.Fatalf("note = %q", got)
	}
}

func TestPermissionRulesAllowDenyAndAlways(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"go test ./..."}`),          // allowed by rule: no prompt
		toolCall("t2", "bash", `{"command":"git push origin"}`),        // denied by rule: no prompt
		toolCall("t3", "bash", `{"command":"make build"}`),             // asks; user answers AllowAlways
		toolCall("t4", "bash", `{"command":"make test"}`),              // covered by the new rule
		toolCall("t5", "write_file", `{"path":"h.txt","content":"x"}`), // approved by a hook
		text("done"),
	}}
	ap := &seesApprover{}
	ap.decision = AllowAlways
	a, root := newAgent(t, f, ap)
	a.Tools = tool.NewRegistry(tool.Bash{Root: root}, tool.WriteFile{Root: root})
	r1, _ := perm.Parse("bash(go test *)")
	r2, _ := perm.Parse("bash(git push*)")
	a.Rules = perm.Rules{Allow: []perm.Rule{r1}, Deny: []perm.Rule{r2}}
	var saved []perm.Rule
	a.SaveRule = func(r perm.Rule) error { saved = append(saved, r); return nil }
	a.Hooks = &hook.Runner{Cwd: root, Config: hook.Config{hook.PreTool: {{Match: "write_file", Command: `echo '{"decision":"allow"}'`}}}}
	var notices []string
	if err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	res := func(i int) llm.Block { return a.Messages[i].Blocks[0] }
	if r := res(2); r.IsError || strings.Contains(r.Text, "denied") {
		t.Fatalf("allowed call failed: %+v", r)
	}
	if r := res(4); !r.IsError || !strings.Contains(r.Text, "Denied by permission rule bash(git push*)") {
		t.Fatalf("deny result = %+v", r)
	}
	if len(ap.inputs) != 1 || !strings.Contains(ap.inputs[0], "make build") {
		t.Fatalf("approver asked for %q, want only 'make build'", ap.inputs)
	}
	if len(saved) != 1 || saved[0].String() != "bash(make *)" || len(notices) != 1 || !strings.Contains(notices[0], "saved permission rule: bash(make *)") {
		t.Fatalf("saved=%v notices=%q", saved, notices)
	}
	if r := res(8); r.IsError {
		t.Fatalf("make test should be covered by the saved rule: %+v", r)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "h.txt")); string(data) != "x" {
		t.Fatal("hook-approved write did not run")
	}
}

func TestVerifyGateFeedsFailuresBackThenPasses(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"a.txt","content":"v1"}`),
		text("done"), // gate runs: fails
		toolCall("t2", "write_file", `{"path":"a.txt","content":"ok"}`),
		text("fixed"), // gate runs: passes
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"grep -q ok a.txt"}}}
	var notices []string
	err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 4 {
		t.Fatalf("requests = %d, want 4", len(f.requests))
	}
	last := f.requests[2].Messages
	if fb := last[len(last)-1].Text(); !strings.Contains(fb, "Verification failed") || !strings.Contains(fb, "grep -q ok a.txt") {
		t.Fatalf("feedback = %q", fb)
	}
	joined := strings.Join(notices, "\n")
	if !strings.Contains(joined, "sending the model back to fix it (1/3)") || !strings.Contains(joined, "verified: grep -q ok a.txt") {
		t.Fatalf("notices = %q", joined)
	}
}

func TestVerifyGateSkipsReadOnlyTurnsAndGivesUp(t *testing.T) {
	f := &fake{responses: []*llm.Response{text("nothing changed")}}
	a, root := newAgent(t, f, nil)
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"false"}}}
	if err := a.Run(context.Background(), "hi", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.requests) != 1 {
		t.Fatalf("read-only turn ran the gate: requests = %d", len(f.requests))
	}
	var rs []*llm.Response
	for i := 0; i < 6; i++ {
		rs = append(rs, toolCall(fmt.Sprintf("t%d", i), "write_file", `{"path":"b.txt","content":"x"}`), text("done"))
	}
	f = &fake{responses: rs}
	a, root = newAgent(t, f, nil)
	a.AutoApprove = true
	a.Verify = &verify.Runner{Cwd: root, Config: verify.Config{Commands: []string{"false"}}}
	var last string
	if err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice {
			last = e.Text
		}
	}); err != nil {
		t.Fatal(err)
	}
	// One initial attempt plus MaxRounds retries, each a tool call and a reply.
	if want := 2 * (verify.MaxRounds + 1); len(f.requests) != want {
		t.Fatalf("requests = %d, want %d", len(f.requests), want)
	}
	if !strings.Contains(last, "giving up after 3 rounds") {
		t.Fatalf("last notice = %q", last)
	}
}

func TestDiagnoseNoteFollowsFileEdits(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.go","content":"package x"}`),
		toolCall("t2", "bash", `{"command":"true"}`),
		toolCall("t3", "write_file", `{"path":"nope/../../x.go","content":"z"}`), // fails: outside root
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	a.AutoApprove = true
	var asked []string
	a.Diagnose = func(_ context.Context, path string) string {
		asked = append(asked, path)
		return "[diagnostics]\n" + path + ":1:1: error: boom"
	}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || asked[0] != "x.go" {
		t.Fatalf("asked = %q", asked)
	}
	r1 := f.requests[1].Messages
	if got := r1[len(r1)-1].Blocks[0].Text; !strings.Contains(got, "x.go:1:1: error: boom") {
		t.Fatalf("result = %q", got)
	}
}

func TestReadOnlyAndPlanModeHideAndRefuseMutatingTools(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "write_file", `{"path":"x.txt","content":"1"}`),
		text("here is the plan"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.PlanMode = true
	if err := a.Run(context.Background(), "change x", nil); err != nil {
		t.Fatal(err)
	}
	for _, d := range f.requests[0].Tools {
		if d.Name == "write_file" || d.Name == "bash" {
			t.Fatalf("mutating tool %s offered in plan mode", d.Name)
		}
	}
	if !strings.Contains(f.requests[0].System, "# Plan mode") || !strings.Contains(f.requests[0].System, "# Read-only") {
		t.Fatalf("system = %q", f.requests[0].System)
	}
	r1 := f.requests[1].Messages
	if got := r1[len(r1)-1].Blocks[0].Text; !strings.Contains(got, "read-only (plan mode)") {
		t.Fatalf("result = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "x.txt")); err == nil {
		t.Fatal("file was written in plan mode")
	}
	if a.LastTurnMutated() {
		t.Fatal("refused call counted as a mutation")
	}
	f = &fake{responses: []*llm.Response{text("ok")}}
	a, _ = newAgent(t, f, nil)
	a.ReadOnly = true
	if err := a.Run(context.Background(), "look", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.requests[0].System, "# Plan mode") || !strings.Contains(f.requests[0].System, "# Read-only") {
		t.Fatalf("system = %q", f.requests[0].System)
	}
}

func TestBackgroundJobsAreReportedBetweenCalls(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "bash", `{"command":"echo bg; exit 2","background":true}`),
		toolCall("t2", "job", `{"action":"wait","id":1,"timeout_ms":5000}`),
		text("done"),
	}}
	a, root := newAgent(t, f, nil)
	a.AutoApprove = true
	a.Jobs = &tool.Jobs{Dir: t.TempDir()}
	a.Tools.Add(tool.Bash{Root: root, Jobs: a.Jobs})
	a.Tools.Add(tool.JobTool{Jobs: a.Jobs})
	var notices []string
	if err := a.Run(context.Background(), "go", func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}); err != nil {
		t.Fatal(err)
	}
	// The job may finish before the next call, so the update can land in
	// the same request as the bash result; assert on what the model was
	// told, not on which message carried it.
	if got := allText(f.requests[1].Messages); !strings.Contains(got, "Started background job 1") {
		t.Fatalf("the model was not told the job started: %q", got)
	}
	joined := allText(f.requests[2].Messages)
	if !strings.Contains(joined, "job 1 finished with exit 2") || !strings.Contains(joined, "[background job update]") {
		t.Fatalf("model was not told: %q", joined)
	}
	if len(notices) != 1 || !strings.HasPrefix(notices[0], "job 1 finished with exit 2") {
		t.Fatalf("notices = %q", notices)
	}
}

// allText joins every block of every message, for assertions that care
// what the model was told rather than which message carried it.
func allText(msgs []llm.Message) string {
	var parts []string
	for _, m := range msgs {
		for _, b := range m.Blocks {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "|")
}
