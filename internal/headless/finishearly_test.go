package headless

import (
	"bytes"
	"encoding/json"
	"testing"
)

// A json run that fails before it starts still ends with a result object
// on stdout, carrying the reason and the exit status; text prints nothing.
func TestFinishEarlyWritesTheResultObject(t *testing.T) {
	resultWritten.Store(false)
	var out bytes.Buffer
	FinishEarly(&out, FormatJSON, BadInput("--max-cost: %q is not a number", "abc"))
	var res CLIResult
	if err := json.Unmarshal(out.Bytes(), &res); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, out.String())
	}
	if res.Type != CLIResultType || !res.IsError || res.ExitCode != 42 || res.Error != `--max-cost: "abc" is not a number` {
		t.Fatalf("result = %+v", res)
	}

	out.Reset()
	FinishEarly(&out, FormatText, BadInput("x"))
	FinishEarly(&out, FormatJSON, nil)
	if out.Len() != 0 {
		t.Fatalf("text mode or success printed %q", out.String())
	}

	// A run that got far enough to print its own result is not followed
	// by a second one.
	(&recorder{format: FormatStreamJSON, out: &bytes.Buffer{}}).finish(nil, BadInput("x"))
	FinishEarly(&out, FormatStreamJSON, BadInput("x"))
	if out.Len() != 0 {
		t.Fatalf("a second result was printed: %q", out.String())
	}
	resultWritten.Store(false)
}
