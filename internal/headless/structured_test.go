package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/agent"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

const answerSchema = `{"type":"object","properties":{"files":{"type":"integer"}},"required":["files"],"additionalProperties":false}`

func structuredCall(id, input string) *llm.Response {
	return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{
		{Type: llm.BlockToolUse, ID: id, Name: StructuredToolName, Input: json.RawMessage(input)},
	}}, StopReason: llm.StopToolUse}
}

func endTurn(text string) *llm.Response {
	return &llm.Response{Message: llm.TextMessage(llm.RoleAssistant, text), StopReason: llm.StopEndTurn}
}

func runSchema(t *testing.T, f *fake, schema string, format Format) (string, string, *agent.Agent, error) {
	t.Helper()
	ag := &agent.Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m"}
	var out, errOut bytes.Buffer
	devnull, _ := os.Open(os.DevNull)
	defer devnull.Close()
	err := Run(context.Background(), Options{Agent: ag, Prompt: "count files", Stdout: &out, Stderr: &errOut, Stdin: devnull, Format: format, JSONSchema: json.RawMessage(schema)})
	return out.String(), errOut.String(), ag, err
}

// The first call breaks the schema and is sent back with the violation;
// the second is the answer. Stdout holds only the value, the prose goes
// to stderr, and the tool is gone from the registry afterwards.
func TestJSONSchemaRetriesAnInvalidCallAndPrintsTheValue(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		structuredCall("a", `{"files":"three"}`),
		structuredCall("b", `{"files": 3}`),
		endTurn("done"),
	}}
	out, errOut, ag, err := runSchema(t, f, answerSchema, FormatText)
	if err != nil {
		t.Fatal(err)
	}
	if out != "{\"files\":3}\n" {
		t.Fatalf("stdout = %q", out)
	}
	if !strings.Contains(errOut, "expected integer, got string") || !strings.Contains(errOut, "done") {
		t.Fatalf("stderr = %q", errOut)
	}
	if _, ok := ag.Tools.Get(StructuredToolName); ok {
		t.Fatal("structured_output left on the registry")
	}
}

// A turn that ends in prose is nudged; after the nudges run out the run
// fails with the schema exit code.
func TestJSONSchemaNudgesThenFails(t *testing.T) {
	f := &fake{responses: []*llm.Response{endTurn("3 files"), endTurn("still prose"), endTurn("prose again")}}
	_, errOut, _, err := runSchema(t, f, answerSchema, FormatText)
	if !errors.Is(err, ErrNoStructuredOutput) || ExitCode(err) != ExitSchema {
		t.Fatalf("err = %v (exit %d)", err, ExitCode(err))
	}
	if f.calls != 1+structuredNudges || !strings.Contains(errOut, "asking again (2/2)") {
		t.Fatalf("calls = %d, stderr = %q", f.calls, errOut)
	}
}

// A non-object schema travels wrapped in {"result": ...} and comes out
// unwrapped in the json envelope.
func TestJSONSchemaWrapsANonObjectSchema(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		endTurn("let me answer"),
		structuredCall("a", `{"result": ["a.go","b.go"]}`),
		endTurn(""),
	}}
	out, _, _, err := runSchema(t, f, `{"type":"array","items":{"type":"string"}}`, FormatJSON)
	if err != nil {
		t.Fatal(err)
	}
	var res CLIResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %q", err, out)
	}
	var got bytes.Buffer
	_ = json.Compact(&got, res.StructuredOutput)
	if got.String() != `["a.go","b.go"]` || res.IsError {
		t.Fatalf("structured_output = %s, is_error = %v", got.String(), res.IsError)
	}
}

func TestLoadSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(p, []byte(answerSchema+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if doc, err := LoadSchema("@" + p); err != nil || string(doc) != answerSchema {
		t.Fatalf("file: %q %v", doc, err)
	}
	for _, bad := range []string{"", "{", "@/nonexistent/s.json", `{"pattern":"("}`} {
		if _, err := LoadSchema(bad); !errors.Is(err, ErrBadInput) {
			t.Errorf("LoadSchema(%q) = %v, want bad input", bad, err)
		}
	}
}

// A subagent must not inherit the tool: its call would answer for the
// parent.
func TestStructuredToolIsParentOnly(t *testing.T) {
	st, err := newStructuredTool(json.RawMessage(answerSchema))
	if err != nil {
		t.Fatal(err)
	}
	var po agent.ParentOnly = st
	if !po.ParentOnly() {
		t.Fatal("structured_output reaches subagents")
	}
}
