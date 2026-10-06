package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/jsonschema"
	"github.com/xmonader/janetcode/internal/llm"
)

// --json-schema makes a headless run answer with data instead of prose.
// The model is given one more tool, structured_output, whose input schema
// is the caller's schema; its call is the answer. The harness validates
// the call itself rather than trusting a provider's constrained decoding,
// which half the OpenAI-compatible servers do not implement, and a call
// that does not validate goes back to the model with every violation
// listed, as any failed tool call does.

// StructuredToolName is the tool the model delivers its answer through.
const StructuredToolName = "structured_output"

// structuredNudges bounds the reminders sent when a turn ends without the
// tool having been called with a valid value.
const structuredNudges = 2

// ErrNoStructuredOutput means the model never delivered a value matching
// --json-schema.
var ErrNoStructuredOutput = errors.New("the model did not produce output matching --json-schema")

// LoadSchema reads a --json-schema argument: inline JSON, or @path to a
// file holding it. It compiles the schema so a bad one fails before any
// model call, and returns the document as given.
func LoadSchema(arg string) (json.RawMessage, error) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return nil, BadInput("--json-schema is empty")
	}
	doc := []byte(arg)
	if strings.HasPrefix(arg, "@") {
		b, err := os.ReadFile(arg[1:])
		if err != nil {
			return nil, BadInput("--json-schema: %v", err)
		}
		doc = b
	}
	if _, err := jsonschema.Compile(doc); err != nil {
		return nil, BadInput("--json-schema: %v", err)
	}
	return json.RawMessage(bytes.TrimSpace(doc)), nil
}

// structuredTool is structured_output for one run.
type structuredTool struct {
	schema *jsonschema.Schema
	input  json.RawMessage
	// wrapped is set when the caller's schema is not an object schema:
	// tool input must be an object, so the value travels as {"result": v}.
	wrapped bool

	mu    sync.Mutex
	value json.RawMessage
}

func newStructuredTool(doc json.RawMessage) (*structuredTool, error) {
	s, err := jsonschema.Compile(doc)
	if err != nil {
		return nil, BadInput("--json-schema: %v", err)
	}
	t := &structuredTool{schema: s, input: doc}
	var top map[string]any
	if json.Unmarshal(doc, &top) != nil || top["type"] != "object" {
		t.wrapped = true
		t.input = json.RawMessage(`{"type":"object","properties":{"result":` + string(doc) + `},"required":["result"],"additionalProperties":false}`)
	}
	return t, nil
}

func (t *structuredTool) Name() string { return StructuredToolName }

func (t *structuredTool) Description() string {
	what := "Its input is the answer itself"
	if t.wrapped {
		what = "Put the answer in the \"result\" field"
	}
	return "Deliver your final answer as structured data. This run's caller reads only this tool's input, not your prose, so call it once you have the answer. " + what + "; it must match the schema exactly. If the call is rejected, fix every listed problem and call it again."
}

func (t *structuredTool) Schema() json.RawMessage { return t.input }
func (t *structuredTool) Mutating() bool          { return false }

// ParentOnly keeps the tool off subagents: a child's call would deliver
// the session's answer in the parent's place.
func (t *structuredTool) ParentOnly() bool { return true }

func (t *structuredTool) Run(_ context.Context, input json.RawMessage) (string, error) {
	v := input
	if t.wrapped {
		var w struct {
			Result *json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(input, &w); err != nil || w.Result == nil {
			return "", fmt.Errorf("put the answer in the \"result\" field")
		}
		v = *w.Result
	}
	if err := t.schema.Validate(v); err != nil {
		return "", fmt.Errorf("the answer does not match the schema:\n%v", err)
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, v); err != nil {
		return "", fmt.Errorf("the answer is not valid JSON: %v", err)
	}
	t.mu.Lock()
	t.value = json.RawMessage(buf.Bytes())
	t.mu.Unlock()
	return "Recorded. That is the answer; end your turn now without repeating it.", nil
}

// Value is the last valid answer, or nil.
func (t *structuredTool) Value() json.RawMessage {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.value
}

// structuredInstruction goes with the prompt so the model knows, from the
// start, how its answer is read.
func structuredInstruction() llm.Block {
	return llm.Block{Type: llm.BlockText, Synthetic: true, Text: "This is a non-interactive run whose caller needs structured data. When you have the answer, call the " + StructuredToolName + " tool with it; that call is the only part of your reply the caller reads."}
}

// structuredNudge is sent when a turn ended without a valid answer.
func structuredNudge() llm.Block {
	return llm.Block{Type: llm.BlockText, Synthetic: true, Text: "You ended the turn without delivering the answer. Call the " + StructuredToolName + " tool now with an answer that matches its schema."}
}
