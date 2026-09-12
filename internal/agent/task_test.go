package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
	"github.com/xmonader/janetcode/internal/tool"
)

// scriptedByPrompt answers each conversation according to its first user
// message, so parent and children can share one provider.
type scriptedByPrompt struct {
	mu    sync.Mutex
	calls int
}

func (s *scriptedByPrompt) Name() string { return "fake" }
func (s *scriptedByPrompt) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	first := req.Messages[0].Text()
	last := req.Messages[len(req.Messages)-1]
	switch {
	case strings.HasPrefix(first, "explore"):
		// child: read a.txt once, then report
		if last.Role == llm.RoleUser && len(last.ToolUses()) == 0 && last.Blocks[0].Type != llm.BlockToolResult {
			for _, d := range req.Tools {
				if d.Name == "write_file" || d.Name == "task" {
					return nil, errorf("child must not see %s", d.Name)
				}
			}
			return toolCall("c1", "read_file", `{"path":"a.txt"}`), nil
		}
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "REPORT: a.txt says "+strings.TrimSpace(last.Blocks[0].Text)), StopReason: llm.StopEndTurn, Usage: llm.Usage{InputTokens: 100, OutputTokens: 10}}, nil
	default:
		// parent: fan out two tasks, then summarise
		if last.Role == llm.RoleUser && last.Blocks[0].Type == llm.BlockToolResult {
			var texts []string
			for _, b := range last.Blocks {
				texts = append(texts, b.Text)
			}
			return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "SUMMARY: "+strings.Join(texts, " | ")), StopReason: llm.StopEndTurn}, nil
		}
		r := toolCall("p1", "task", `{"description":"one","prompt":"explore one"}`)
		r.Message.Blocks = append(r.Message.Blocks, llm.Block{Type: llm.BlockToolUse, ID: "p2", Name: "task", Input: json.RawMessage(`{"description":"two","prompt":"explore two"}`)})
		return r, nil
	}
}

type strErr string

func (e strErr) Error() string { return string(e) }
func errorf(f string, a ...any) error {
	return strErr(strings.TrimSpace(strings.ReplaceAll(f, "%s", a[0].(string))))
}

func TestTaskRunsSubagentsInParallel(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	prov := &scriptedByPrompt{}
	parent := &Agent{Provider: prov, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, task)

	var events []Event
	if err := parent.Run(context.Background(), "go", func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}
	final := parent.Messages[len(parent.Messages)-1].Text()
	if !strings.HasPrefix(final, "SUMMARY: ") || strings.Count(final, "REPORT: a.txt says") != 2 {
		t.Fatalf("final = %q", final)
	}
	if !strings.Contains(final, `[subagent "one"`) || !strings.Contains(final, "1 tool calls") {
		t.Fatalf("missing footer: %q", final)
	}
	if u := task.Usage(); u.InputTokens < 200 || u.OutputTokens < 20 {
		t.Fatalf("child usage not aggregated: %+v", u)
	}
	// Children's tool events must not leak into the parent's stream.
	for _, e := range events {
		if e.Call != nil && e.Call.Name == "read_file" {
			t.Fatal("child event leaked to parent")
		}
	}
	if prov.calls != 6 {
		t.Fatalf("provider calls = %d, want 2 parent + 2 per child", prov.calls)
	}
}

func TestTaskContinueSeesHistory(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)
	out1, err := task.Run(context.Background(), json.RawMessage(`{"description":"one","prompt":"explore first"}`))
	if err != nil {
		t.Fatal(err)
	}
	ids := task.ChildIDs()
	if len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	out2, err := task.Run(context.Background(), json.RawMessage(`{"description":"one","prompt":"explore again","continue":"`+ids[0]+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out1, "id="+ids[0]) || !strings.Contains(out2, "id="+ids[0]) {
		t.Fatalf("out1=%s out2=%s", out1, out2)
	}
	kid := task.children[ids[0]]
	if len(kid.agent.Messages) < 4 {
		t.Fatalf("continue lost history: %d messages", len(kid.agent.Messages))
	}
	log, ok := task.ChildLog(ids[0])
	if !ok || log.ID != ids[0] || !log.Done {
		t.Fatalf("ChildLog = %+v ok=%v", log, ok)
	}
	steps := log.Steps()
	var kinds []string
	for _, st := range steps {
		kinds = append(kinds, st.Kind)
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "prompt") || !strings.Contains(joined, "tool") || !strings.Contains(joined, "text") {
		t.Fatalf("steps = %v", steps)
	}
}

func TestExploreAgentIsBuiltInReadOnly(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent} // no Profiles: explore must still exist
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, tool.WriteFile{Root: root}, tool.EditFile{Root: root}, tool.Bash{Root: root}, tool.Grep{Root: root}, task)
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"map","prompt":"explore the tree","agent":"explore"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "REPORT:") {
		t.Fatalf("out = %s", out)
	}
	ids := task.ChildIDs()
	if len(ids) != 1 {
		t.Fatalf("ids = %v", ids)
	}
	kid := task.children[ids[0]]
	for _, d := range kid.agent.Tools.Defs() {
		switch d.Name {
		case "write_file", "edit_file", "bash", "task":
			t.Fatalf("explore child has mutating tool %s", d.Name)
		}
	}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"y","agent":"no-such-agent"}`)); err == nil || !strings.Contains(err.Error(), "no custom agent") {
		t.Fatalf("unknown agent: %v", err)
	}
}

func TestTaskRejectsBadInput(t *testing.T) {
	task := &Task{Parent: &Agent{Tools: tool.NewRegistry()}}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"  "}`)); err == nil {
		t.Fatal("empty prompt accepted")
	}
	if _, err := (&Task{}).Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"y"}`)); err == nil {
		t.Fatal("missing parent accepted")
	}
}

func TestTaskRestrictsSubagentTools(t *testing.T) {
	root := t.TempDir()
	parent := &Agent{Tools: tool.NewRegistry(tool.ReadFile{Root: root}, tool.Grep{Root: root}, tool.Bash{Root: root})}
	task := &Task{Parent: parent}
	parent.Tools.Add(task)
	child, err := task.child([]string{"grep"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if defs := child.Tools.Defs(); len(defs) != 1 || defs[0].Name != "grep" {
		t.Fatalf("child tools = %+v", defs)
	}
	if _, err := task.child([]string{"bash"}, nil); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("mutating tool accepted: %v", err)
	}
	if _, err := task.child([]string{"nope"}, nil); err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Fatalf("unknown tool accepted: %v", err)
	}
	all, _ := task.child(nil, nil)
	if defs := all.Tools.Defs(); len(defs) != 2 {
		t.Fatalf("default child tools = %+v", defs)
	}
}

func TestTaskRunsCustomAgentProfile(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	f := &fake{responses: []*llm.Response{text("review: fine")}}
	parent := &Agent{Provider: f, Tools: tool.NewRegistry(tool.ReadFile{Root: root}, tool.Grep{Root: root}, tool.Bash{Root: root}), Model: "big", System: "sys"}
	task := &Task{Parent: parent, Profiles: func() []skill.Profile {
		return []skill.Profile{{Name: "reviewer", Description: "Reviews code", Tools: []string{"grep"}, Model: "small", Body: "Be strict about error handling."}}
	}}
	parent.Tools.Add(task)
	if d := task.Description(); !strings.Contains(d, "reviewer (Reviews code)") {
		t.Fatalf("description = %q", d)
	}
	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"review","prompt":"review a.go","agent":"Reviewer"}`))
	if err != nil || !strings.HasPrefix(out, "review: fine") {
		t.Fatalf("out=%q err=%v", out, err)
	}
	req := f.requests[0]
	if req.Model != "small" || !strings.Contains(req.System, "# Agent: reviewer\nBe strict about error handling.") || !strings.HasPrefix(req.System, "sys") {
		t.Fatalf("request = model %q system %q", req.Model, req.System)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "grep" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"y","agent":"nobody"}`)); err == nil || !strings.Contains(err.Error(), "no custom agent") {
		t.Fatalf("unknown agent err = %v", err)
	}
}

// editScript drives an editing subagent: it writes b.txt, runs a command,
// and reports; the parent summarises the tool result.
type editScript struct {
	mu   sync.Mutex
	dirs []string
}

func (s *editScript) Name() string { return "fake" }
func (s *editScript) Stream(ctx context.Context, req llm.Request, emit func(llm.Event)) (*llm.Response, error) {
	first := req.Messages[0].Text()
	last := req.Messages[len(req.Messages)-1]
	if strings.HasPrefix(first, "edit:") {
		n := 0
		for _, m := range req.Messages {
			for _, b := range m.Blocks {
				if b.Type == llm.BlockToolResult {
					n++
				}
			}
		}
		switch n {
		case 0:
			return toolCall("c1", "write_file", `{"path":"b.txt","content":"new file\n"}`), nil
		case 1:
			return toolCall("c2", "bash", `{"command":"pwd && echo changed >> a.txt"}`), nil
		case 2:
			s.mu.Lock()
			s.dirs = append(s.dirs, strings.TrimSpace(strings.Split(last.Blocks[0].Text, "\n")[0]))
			s.mu.Unlock()
			return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "REPORT: added b.txt and a line to a.txt"), StopReason: llm.StopEndTurn}, nil
		}
	}
	if last.Role == llm.RoleUser && last.Blocks[0].Type == llm.BlockToolResult {
		return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "SUMMARY: "+last.Blocks[0].Text), StopReason: llm.StopEndTurn}, nil
	}
	return toolCall("p1", "task", `{"description":"add b","prompt":"edit: add b.txt","edit":true}`), nil
}

func TestTaskEditsInAWorktreeAndReturnsAPatch(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644)
	gitInit(t, root)
	os.WriteFile(filepath.Join(root, "uncommitted.txt"), []byte("wip\n"), 0o644) // must be visible to the child
	prov := &editScript{}
	parent := &Agent{Provider: prov, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent, Root: root}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)
	if err := parent.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	final := parent.Messages[len(parent.Messages)-1].Text()
	for _, want := range []string{"REPORT: added b.txt", "## Changes made in the worktree (not applied)", "a.txt |", "b.txt |", "Patch saved to ", "+new file", "+changed"} {
		if !strings.Contains(final, want) {
			t.Fatalf("missing %q in:\n%s", want, final)
		}
	}
	if len(prov.dirs) != 1 || prov.dirs[0] == root || !strings.Contains(prov.dirs[0], "janetcode-wt-") {
		t.Fatalf("child ran in %q, want a worktree", prov.dirs)
	}
	// The worktree outlives the run so the subagent can be continued in it,
	// and Close takes it away.
	if _, err := os.Stat(prov.dirs[0]); err != nil {
		t.Fatalf("worktree removed while the subagent is still continuable: %v", err)
	}
	task.Close(context.Background())
	if _, err := os.Stat(prov.dirs[0]); err == nil {
		t.Fatal("worktree not removed by Close")
	}
	if got, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(got) != "alpha\n" {
		t.Fatalf("real tree changed: %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "b.txt")); err == nil {
		t.Fatal("b.txt leaked into the real tree")
	}
	// The saved patch applies cleanly to the real tree.
	i := strings.Index(final, "Patch saved to ")
	path := strings.Fields(final[i+len("Patch saved to "):])[0]
	if out, err := gitRun(context.Background(), root, "apply", path); err != nil {
		t.Fatalf("git apply: %v %s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(got) != "new file\n" {
		t.Fatalf("after apply b.txt = %q", got)
	}
	os.Remove(path)
	if out, _ := gitRun(context.Background(), root, "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Fatalf("stale worktrees: %s", out)
	}
	if out, _ := gitRun(context.Background(), root, "for-each-ref", "refs/janetcode/"); out != "" {
		t.Fatalf("snapshot ref left behind: %s", out)
	}
}

func TestTaskEditNeedsARepo(t *testing.T) {
	root := t.TempDir()
	parent := &Agent{Provider: &editScript{}, Model: "m", AutoApprove: true}
	task := &Task{Parent: parent, Root: root}
	parent.Tools = tool.NewRegistry(task)
	if err := parent.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if final := parent.Messages[len(parent.Messages)-1].Text(); !strings.Contains(final, "need a git repository") {
		t.Fatalf("final = %q", final)
	}
}

// Usage is cumulative over a subagent's life; continuing one must bill the
// parent only for the new exchange.
func TestTaskContinueBillsUsageOnce(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"one","prompt":"explore first"}`)); err != nil {
		t.Fatal(err)
	}
	id := task.ChildIDs()[0]
	first := task.Usage()
	if first.InputTokens == 0 {
		t.Fatal("first run billed nothing")
	}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"one","prompt":"explore again","continue":"`+id+`"}`)); err != nil {
		t.Fatal(err)
	}
	child := task.children[id].agent
	if got := task.Usage(); got.InputTokens != child.Usage.InputTokens {
		t.Fatalf("parent billed %d input tokens, subagent spent %d", got.InputTokens, child.Usage.InputTokens)
	}
}

func TestTaskEvictsOldestSubagent(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)
	for i := 0; i < maxChildren+2; i++ {
		if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"explore"}`)); err != nil {
			t.Fatal(err)
		}
	}
	ids := task.ChildIDs()
	if len(ids) != maxChildren {
		t.Fatalf("kept %d subagents, want %d", len(ids), maxChildren)
	}
	if ids[0] != "task-3" {
		t.Fatalf("oldest kept is %s, want the oldest two evicted", ids[0])
	}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"explore","continue":"task-1"}`)); err == nil {
		t.Fatal("continuing an evicted subagent should fail")
	}
}

// goal_status is read-only but writes the parent's goal log, so it must not
// reach a subagent.
func TestTaskWithholdsGoalStatus(t *testing.T) {
	parent := &Agent{Model: "m", Log: &session.Memory{}}
	task := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ListDir{Root: t.TempDir()}, task, &GoalStatus{Parent: parent})
	child, err := task.child(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range child.Tools.Defs() {
		if d.Name == "goal_status" {
			t.Fatal("subagent can end the parent's goal")
		}
	}
	if _, err := task.child([]string{"goal_status"}, nil); err == nil {
		t.Fatal("goal_status accepted in the tools list")
	}
}

// A job can be given to a different model than the session's: a cheap one
// to read a lot of code, a strong one for the part that has to be right.
func TestTaskRunsOnAnotherModel(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha"), 0o644)
	other := &scriptedByPrompt{}
	parent := &Agent{Provider: &scriptedByPrompt{}, Model: "session-model", System: "sys", AutoApprove: true}
	var asked string
	task := &Task{Parent: parent, Models: func(name string) (llm.Provider, string, error) {
		asked = name
		if name == "nowhere-5" {
			return nil, "", errorf("no configured provider serves %s", name)
		}
		return other, name, nil
	}}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)

	out, err := task.Run(context.Background(), json.RawMessage(`{"description":"explore","prompt":"explore first","model":"glm-5"}`))
	if err != nil {
		t.Fatal(err)
	}
	if asked != "glm-5" {
		t.Fatalf("resolver asked for %q", asked)
	}
	if !strings.Contains(out, "on glm-5") {
		t.Fatalf("the report should say which model ran it:\n%s", out)
	}
	kid := task.children[task.ChildIDs()[0]].agent
	if kid.Model != "glm-5" || kid.Provider != other {
		t.Fatalf("child ran on %q with the wrong provider", kid.Model)
	}
	// The parent is untouched.
	if parent.Model != "session-model" {
		t.Fatalf("the session's model changed to %q", parent.Model)
	}
	// A model nothing serves is refused, with the resolver's reason.
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"explore","model":"nowhere-5"}`)); err == nil {
		t.Fatal("an unroutable model was accepted")
	}
	// Without a resolver the parameter is refused rather than ignored.
	bare := &Task{Parent: parent}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, bare)
	if _, err := bare.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"explore","model":"glm-5"}`)); err == nil {
		t.Fatal("model accepted with no way to route it")
	}
}

// An editing subagent whose model name cannot be routed must not leave its
// checkout behind: the worktree and the base snapshot are made before the
// name is resolved, and nothing remembers them when the call then fails.
func TestTaskEditRefusedModelLeavesNoWorktree(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644)
	gitInit(t, root)
	parent := &Agent{Provider: &editScript{}, Model: "m", System: "sys", AutoApprove: true}
	task := &Task{Parent: parent, Root: root}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)

	// No resolver at all: the model parameter is refused.
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"go","edit":true,"model":"glm-5"}`)); err == nil {
		t.Fatal("a model name was accepted without a resolver")
	}
	// A resolver that cannot serve the name: refused too.
	task.Models = func(string) (llm.Provider, string, error) {
		return nil, "", strErr("no configured provider serves it")
	}
	if _, err := task.Run(context.Background(), json.RawMessage(`{"description":"x","prompt":"go","edit":true,"model":"nowhere-5"}`)); err == nil {
		t.Fatal("an unroutable model was accepted")
	}

	if out, _ := gitRun(context.Background(), root, "worktree", "list"); strings.Count(out, "\n") != 0 {
		t.Fatalf("stale worktrees left behind:\n%s", out)
	}
	if out, _ := gitRun(context.Background(), root, "for-each-ref", "refs/janetcode/"); out != "" {
		t.Fatalf("snapshot ref left behind: %s", out)
	}
	if ids := task.ChildIDs(); len(ids) != 0 {
		t.Fatalf("a failed run registered subagents: %v", ids)
	}
}

// A deny rule is the user saying "not this, ever". The editing subagent
// inherits the parent's deny list; the read-only one did not, so
// delegating a job was a way to read exactly what a rule refused.
func TestSubagentInheritsDenyRulesButNotAllow(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "secret.txt"), []byte("token"), 0o644)
	parent := &Agent{
		Provider: &scriptedByPrompt{}, Model: "m", System: "sys",
		Rules: perm.Rules{
			Deny:  []perm.Rule{{Tool: "read_file", Pattern: "secret.txt"}},
			Allow: []perm.Rule{{Tool: "bash", Pattern: "rm -rf *"}},
		},
	}
	task := &Task{Parent: parent, Root: root}
	parent.Tools = tool.NewRegistry(tool.ReadFile{Root: root}, task)

	kid, err := task.child(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := kid.Rules.Decide("read_file", "secret.txt"); v != perm.Denied {
		t.Fatalf("a read-only subagent decided the denied path %q", v)
	}
	// The parent's allow list is not inherited: a subagent must not carry
	// permission the user granted for another context.
	if v, _ := kid.Rules.Decide("bash", "rm -rf /"); v == perm.Allowed {
		t.Fatal("a subagent inherited an allow rule")
	}

	// The editing subagent has had this all along; keep it that way.
	gitInit(t, root)
	wt, err := newWorktree(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer wt.remove(context.Background())
	if v, _ := task.editingChild(wt, nil).Rules.Decide("read_file", "secret.txt"); v != perm.Denied {
		t.Fatalf("an editing subagent decided the denied path %q", v)
	}
}
