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
	raw, ok := t.Parent.Tools.Get("task")
	if !ok {
		return "", fmt.Errorf("no task tool in this session")
	}
	task, ok := raw.(*Task)
	if !ok {
		return "", fmt.Errorf("task tool has the wrong type")
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
		payload, err := json.Marshal(st)
		if err != nil {
			return "", err
		}
		out, err := task.Run(ctx, payload)
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
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, fmt.Errorf("script is empty")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("bad script: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("script is empty")
		}
		raw = json.RawMessage(s)
	}
	var steps []wfStep
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, fmt.Errorf("script must be a JSON array of {description, prompt}: %w", err)
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
