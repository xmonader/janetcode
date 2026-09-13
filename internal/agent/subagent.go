package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// PlanApprovedPrompt is the turn /plan approve starts, in every front-end.
const PlanApprovedPrompt = "The plan is approved. Execute it now, step by step, in the order written. Verify each step (build, tests) before starting the next, and stop to report if a step fails or the plan turns out to be wrong."

// TaskInput is the task tool's input. Everything that starts a subagent —
// the model's own task call, workflow steps, /agents new|fork and /review from either
// front-end — builds one of these, so a new task field reaches all of them.
type TaskInput struct {
	Description string   `json:"description"`
	Prompt      string   `json:"prompt"`
	Tools       []string `json:"tools,omitempty"`
	Agent       string   `json:"agent,omitempty"`
	Edit        bool     `json:"edit,omitempty"`
	Model       string   `json:"model,omitempty"`
	Continue    string   `json:"continue,omitempty"`
	Fork        bool     `json:"fork,omitempty"`
}

// Kind names what the input starts, for a front-end's status line.
func (in TaskInput) Kind() string {
	if in.Fork {
		return "fork"
	}
	return "subagent"
}

// Start runs a subagent from Go, the same path as a model task call.
func (t *Task) Start(ctx context.Context, in TaskInput) (string, error) {
	payload, err := json.Marshal(in)
	if err != nil {
		return "", err
	}
	return t.Run(ctx, payload)
}

// TaskTool is the session's task tool, or nil when it has none.
func (a *Agent) TaskTool() *Task {
	if a == nil || a.Tools == nil {
		return nil
	}
	t, ok := a.Tools.Get("task")
	if !ok {
		return nil
	}
	task, _ := t.(*Task)
	return task
}

// AgentsArg reads /agents [id | new <brief> | fork <brief>]. start is
// true when the argument asks for a new subagent, which is then the task
// tool started by hand: new runs the general profile, fork inherits this
// conversation. Otherwise arg, if any, names a child to open.
func AgentsArg(arg string) (in TaskInput, start bool, err error) {
	arg = strings.TrimSpace(arg)
	verb, brief, _ := strings.Cut(arg, " ")
	if verb != "new" && verb != "fork" {
		return TaskInput{}, false, nil
	}
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return TaskInput{}, false, fmt.Errorf("usage: /agents %s <brief>", verb)
	}
	desc, _, _ := strings.Cut(brief, "\n")
	desc = strings.TrimSpace(desc)
	if r := []rune(desc); len(r) > 60 {
		desc = string(r[:60])
	}
	in = TaskInput{Description: desc, Prompt: brief, Fork: verb == "fork"}
	if !in.Fork {
		in.Agent = "general"
	}
	return in, true, nil
}

// ReviewTasks are the two subagents /review fans out over diff: one for
// bugs, one for tests. focus narrows what they look at.
func ReviewTasks(focus, diff string) []TaskInput {
	if focus == "" {
		focus = "the current uncommitted changes"
	}
	briefs := []struct{ desc, agent, extra string }{
		{"review bugs", "explore", "Look for correctness bugs, logic errors, race conditions and missed edge cases. Report findings with file:line."},
		{"review tests", "general", "Look for missing tests, weak assertions and regressions. Report findings with file:line."},
	}
	out := make([]TaskInput, 0, len(briefs))
	for _, b := range briefs {
		out = append(out, TaskInput{
			Description: b.desc,
			Agent:       b.agent,
			Prompt:      b.extra + "\n\nReview " + focus + ".\n\n" + diff,
		})
	}
	return out
}

// ReviewDiff is what /review hands its subagents: git status and the diff
// against HEAD, capped so a large change does not fill their context.
func ReviewDiff(cwd string) (string, error) {
	status, _ := exec.Command("git", "-C", cwd, "status", "--short").CombinedOutput()
	diff, err := exec.Command("git", "-C", cwd, "diff", "HEAD").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("not a git repository")
	}
	out := strings.TrimSpace(string(status))
	if d := strings.TrimSpace(string(diff)); d != "" {
		if out != "" {
			out += "\n\n"
		}
		out += d
	}
	const max = 32 * 1024
	if len(out) > max {
		out = out[:max] + "\n… (diff truncated)"
	}
	return out, nil
}
