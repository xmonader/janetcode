package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/tool"
)

const runCodeMaxSteps = 20

var runCodeSkip = map[string]bool{
	"run_code": true, "execute_code": true, "workflow": true, "task": true, "ask": true, "cron": true,
}

// RunCode runs a short program of real tool calls in this session.
// Each step goes through authorize/guards/the event log.
type RunCode struct{ Parent *Agent }

func (*RunCode) Name() string { return "run_code" }
func (*RunCode) Description() string {
	return "Run a short program of tool calls in this session in one turn. code is a JSON array of {name, input} steps; each step is a real tool call (approvals, guards, the event log). Use it to chain several calls with the results of earlier steps available as later output. Do not use it to skip permission. Nested run_code, workflow, task and ask are refused."
}
func (*RunCode) Mutating() bool { return true }
func (*RunCode) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"code":{"description":"JSON array of steps: [{\"name\":\"read_file\",\"input\":{\"path\":\"a.txt\"}}, ...]. A string is parsed as that JSON."},
"description":{"type":"string","description":"Short label for the program"}},
"required":["code"],"additionalProperties":false}`)
}

func (t *RunCode) Run(ctx context.Context, input json.RawMessage) (string, error) {
	if t.Parent == nil {
		return "", fmt.Errorf("run_code has no parent agent")
	}
	var in struct {
		Code        json.RawMessage `json:"code"`
		Description string          `json:"description"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	steps, err := parseRunCode(in.Code)
	if err != nil {
		return "", err
	}
	if len(steps) == 0 {
		return "", fmt.Errorf("code must contain at least one step")
	}
	if len(steps) > runCodeMaxSteps {
		return "", fmt.Errorf("at most %d steps", runCodeMaxSteps)
	}
	var b strings.Builder
	if d := strings.TrimSpace(in.Description); d != "" {
		fmt.Fprintf(&b, "# %s\n", d)
	}
	for i, st := range steps {
		if runCodeSkip[st.Name] {
			return "", fmt.Errorf("step %d: %s cannot be called from run_code", i+1, st.Name)
		}
		id := fmt.Sprintf("runcode-%d", i+1)
		out, isErr := t.Parent.runNested(ctx, id, st.Name, st.Input)
		fmt.Fprintf(&b, "## %s\n%s\n", st.Name, strings.TrimRight(out, "\n"))
		if isErr {
			fmt.Fprintf(&b, "\n[step %d failed]\n", i+1)
			return strings.TrimRight(b.String(), "\n"), nil
		}
	}
	fmt.Fprintf(&b, "\n[%s]", plural(len(steps), "tool call"))
	return strings.TrimRight(b.String(), "\n"), nil
}

type runStep struct {
	Name  string
	Input json.RawMessage
}

func parseRunCode(raw json.RawMessage) ([]runStep, error) {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 {
		return nil, fmt.Errorf("code is empty")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("bad code: %w", err)
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, fmt.Errorf("code is empty")
		}
		raw = json.RawMessage(s)
	}
	var steps []struct {
		Name  string          `json:"name"`
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(raw, &steps); err != nil {
		return nil, fmt.Errorf("code must be a JSON array of {name, input}: %w", err)
	}
	out := make([]runStep, 0, len(steps))
	for i, s := range steps {
		name := strings.TrimSpace(s.Name)
		if name == "" {
			name = strings.TrimSpace(s.Tool)
		}
		if name == "" {
			return nil, fmt.Errorf("step %d: name is required", i+1)
		}
		in := s.Input
		if len(in) == 0 {
			in = json.RawMessage(`{}`)
		}
		out = append(out, runStep{Name: name, Input: in})
	}
	return out, nil
}

var _ tool.Tool = (*RunCode)(nil)
