package tool

import (
	"encoding/json"
	"slices"
	"strings"
)

// Intent: one short line, on every tool call, saying why it is being made.
//
// The model already knows why it is calling a tool; the harness never does,
// and reconstructs it later from arguments and outcomes. Asking for it
// directly costs about ten output tokens per call and buys a label for the
// user, a record that survives compaction, and — read end to end — the
// narrative of a session in a few thousand tokens instead of a transcript.
//
// The field is added to every schema centrally rather than to each tool,
// so MCP tools and tools added at runtime carry it too, and it is stripped
// from the input before the tool sees it: the tools decode with unknown
// fields disallowed, so an intent left in place would fail every call.

// IntentKey is the property name added to every tool schema.
const IntentKey = "intent"

const intentProperty = `"intent":{"type":"string","description":"One short line saying why you are making this call, in the user's terms — it is shown to them as the call runs."}`

// WithIntent adds the intent property to a tool's input schema. A schema
// it cannot parse is returned unchanged rather than corrupted.
func WithIntent(schema json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if json.Unmarshal(schema, &obj) != nil {
		return schema
	}
	props, ok := obj["properties"]
	if !ok {
		return schema
	}
	trimmed := strings.TrimSpace(string(props))
	if !strings.HasPrefix(trimmed, "{") || !strings.HasSuffix(trimmed, "}") {
		return schema
	}
	if strings.Contains(trimmed, `"`+IntentKey+`"`) {
		return schema // the tool declares its own
	}
	inner := strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	if inner == "" {
		obj["properties"] = json.RawMessage("{" + intentProperty + "}")
	} else {
		obj["properties"] = json.RawMessage("{" + intentProperty + "," + inner + "}")
	}
	// Required, or models simply leave it out — an optional "why" is the
	// first thing dropped. It is listed first so it is also written
	// first, before the model has committed to the call's arguments.
	var required []string
	if raw, ok := obj["required"]; ok {
		_ = json.Unmarshal(raw, &required)
	}
	if !slices.Contains(required, IntentKey) {
		required = append([]string{IntentKey}, required...)
	}
	if enc, err := json.Marshal(required); err == nil {
		obj["required"] = enc
	}
	// additionalProperties:false must not reject the field we just added;
	// it is already listed, so the constraint still holds for anything else.
	out, err := json.Marshal(obj)
	if err != nil {
		return schema
	}
	return out
}

// TakeIntent removes the intent from a tool input and returns both. The
// remaining input is byte-identical to what the model would have sent
// without the field, so tools that disallow unknown fields still decode.
func TakeIntent(input json.RawMessage) (string, json.RawMessage) {
	if len(input) == 0 {
		return "", input
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(input, &obj) != nil {
		return "", input
	}
	raw, ok := obj[IntentKey]
	if !ok {
		return "", input
	}
	delete(obj, IntentKey)
	var intent string
	_ = json.Unmarshal(raw, &intent)
	rest, err := json.Marshal(obj)
	if err != nil {
		return intent, input
	}
	return strings.TrimSpace(intent), rest
}
