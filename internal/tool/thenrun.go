package tool

import (
	"encoding/json"
	"strings"
)

// ThenRunKey is the field on write_file and edit_file that names a shell
// command to run once the edit has landed: the change and its check in one
// call instead of two. Every round trip re-sends the whole conversation,
// and an edit followed by a test run is the commonest pair a coding turn
// makes. The agent takes the field off before the tool sees it and runs
// the command as an ordinary bash call, with bash's own permission,
// sandbox and hooks (see agent.execute); the tool itself never runs it.
const ThenRunKey = "then_run"

// thenRunProp is the schema property both edit tools add when ThenRun is set.
const thenRunProp = `"then_run":{"type":"string","description":"Shell command run once the edit lands, e.g. the tests; output appended"},
`

// TakeThenRun removes then_run from a tool input and returns it with the
// remaining input. An input without the field comes back unchanged.
func TakeThenRun(input json.RawMessage) (string, json.RawMessage) {
	if len(input) == 0 || !strings.Contains(string(input), `"`+ThenRunKey+`"`) {
		return "", input
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(input, &obj) != nil {
		return "", input
	}
	raw, ok := obj[ThenRunKey]
	if !ok {
		return "", input
	}
	delete(obj, ThenRunKey)
	var cmd string
	_ = json.Unmarshal(raw, &cmd)
	rest, err := json.Marshal(obj)
	if err != nil {
		return "", input
	}
	return strings.TrimSpace(cmd), rest
}
