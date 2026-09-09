package inspect

import (
	"context"
	"encoding/json"
	"fmt"
)

// Tool lets the model inspect the harness it is running in. An agent that
// can be asked "what is in your context" or "what did that search return"
// can answer questions about its own behaviour instead of guessing at it,
// and so can the user through it.
type Tool struct{ Registry *Registry }

func (Tool) Name() string { return "inspect" }
func (t Tool) Description() string {
	d := "Look at the state of the session you are running in: what is in the context being sent, what the archive holds, what the event log recorded, which tools and servers are loaded, how the configuration resolved, and more. Call it with no subject to list what can be inspected. This reports the harness, not the project — use the file tools for the code."
	if t.Registry != nil {
		if names := t.Registry.Names(); len(names) > 0 {
			d += " Subjects: "
			for i, n := range names {
				if i > 0 {
					d += ", "
				}
				d += n
			}
			d += "."
		}
	}
	return d
}
func (Tool) Mutating() bool { return false }
func (Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"subject":{"type":"string","description":"What to look at; omit to list the subjects"},
"arg":{"type":"string","description":"Optional argument, such as an id or a filter"}},
"additionalProperties":false}`)
}

func (t Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Subject string `json:"subject"`
		Arg     string `json:"arg"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if t.Registry == nil {
		return "", fmt.Errorf("nothing is registered for inspection in this session")
	}
	req := in.Subject
	if in.Arg != "" {
		req += " " + in.Arg
	}
	return t.Registry.Report(ctx, req)
}
