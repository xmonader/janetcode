package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/tool"
)

const workflowMaxChildren = 4

// Workflow runs a short script of task children on the existing task tool.
type Workflow struct{ Parent *Agent }

func (*Workflow) Name() string { return "workflow" }
func (*Workflow) Description() string {
	return "Run a short orchestration script that starts real task subagents and returns their reports. script is a JSON array of {description, prompt, agent?} steps. Each step calls the existing task tool (same profiles, continue ids, eviction). At most four children in one workflow. Do not nest workflow inside workflow."
}
func (*Workflow) Mutating() bool { return false }
func (*Workflow) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"script":{"description":"JSON array of {description, prompt, agent?} or a string of that JSON"},
"description":{"type":"string","description":"Short label for the workflow"}},
"required":["script"],"additionalProperties":false}`)
}

func (t *Workflow) Run(ctx context.Context, input json.RawMessage) (string, error) {
	if t.Parent == nil || t.Parent.Tools == nil {
		return "", fmt.Errorf("workflow has no parent agent")
	}
	task := t.Parent.TaskTool()
	if task == nil {
		return "", fmt.Errorf("no task tool in this session")
	}
	var in struct {
		Script      json.RawMessage `json:"script"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	steps, err := parseWorkflow(in.Script)
	if err != nil {
		return "", err
	}
	if len(steps) == 0 {
		return "", fmt.Errorf("script must contain at least one step")
	}
	if len(steps) > workflowMaxChildren {
		return "", fmt.Errorf("at most %d task children in one workflow", workflowMaxChildren)
	}
	var b strings.Builder
	if d := strings.TrimSpace(in.Description); d != "" {
		fmt.Fprintf(&b, "# %s\n", d)
	}
	for i, st := range steps {
		out, err := task.Start(ctx, TaskInput{Description: st.Description, Prompt: st.Prompt, Agent: st.Agent})
		if err != nil {
			fmt.Fprintf(&b, "## step %d %s\nerror: %s\n", i+1, st.Description, err.Error())
			return strings.TrimRight(b.String(), "\n"), nil
		}
		fmt.Fprintf(&b, "## step %d %s\n%s\n", i+1, st.Description, strings.TrimRight(out, "\n"))
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

type wfStep struct {
	Description string `json:"description"`
	Prompt      string `json:"prompt"`
	Agent       string `json:"agent,omitempty"`
}

func parseWorkflow(raw json.RawMessage) ([]wfStep, error) {
	var steps []wfStep
	if err := decodeSteps(raw, "script", "{description, prompt}", &steps); err != nil {
		return nil, err
	}
	for i := range steps {
		if strings.TrimSpace(steps[i].Prompt) == "" {
			return nil, fmt.Errorf("step %d: prompt is required", i+1)
		}
		if strings.TrimSpace(steps[i].Description) == "" {
			steps[i].Description = fmt.Sprintf("step-%d", i+1)
		}
	}
	return steps, nil
}

var _ tool.Tool = (*Workflow)(nil)

// decodeSteps reads a tool's JSON array of steps into v. Models often send
// the array as a string holding the JSON, so a string is unwrapped first.
// field names the input field and shape the step, for the error.
func decodeSteps(raw json.RawMessage, field, shape string, v any) error {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) > 0 && raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return fmt.Errorf("bad %s: %w", field, err)
		}
		raw = json.RawMessage(strings.TrimSpace(s))
	}
	if len(raw) == 0 {
		return fmt.Errorf("%s is empty", field)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s must be a JSON array of %s: %w", field, shape, err)
	}
	return nil
}
