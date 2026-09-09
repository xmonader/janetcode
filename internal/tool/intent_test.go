package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIntentRoundTrip(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"],"additionalProperties":false}`)
	withIntent := WithIntent(schema)
	var obj struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
		Additional *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(withIntent, &obj); err != nil {
		t.Fatalf("the schema stopped being valid JSON: %v", err)
	}
	if _, ok := obj.Properties[IntentKey]; !ok {
		t.Fatal("intent was not added")
	}
	if _, ok := obj.Properties["path"]; !ok {
		t.Fatal("the tool's own properties were lost")
	}
	// Required, or the model leaves it out; the tool's own requirements
	// are kept.
	if len(obj.Required) != 2 || obj.Required[0] != IntentKey || obj.Required[1] != "path" {
		t.Fatalf("required = %v, want [intent path]", obj.Required)
	}
	if obj.Additional == nil || *obj.Additional {
		t.Fatal("additionalProperties:false must survive")
	}

	// Stripping leaves input the tools can still decode with unknown
	// fields disallowed.
	got, rest := TakeIntent(json.RawMessage(`{"intent":"read the config","path":"a.go"}`))
	if got != "read the config" {
		t.Fatalf("intent = %q", got)
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := decode(rest, &in); err != nil {
		t.Fatalf("the remaining input must decode strictly: %v", err)
	}
	if in.Path != "a.go" {
		t.Fatalf("path = %q", in.Path)
	}

	// Absent, empty and malformed inputs are all left alone.
	if got, rest := TakeIntent(json.RawMessage(`{"path":"a"}`)); got != "" || string(rest) != `{"path":"a"}` {
		t.Fatalf("untouched input changed: %q %s", got, rest)
	}
	if _, rest := TakeIntent(nil); rest != nil {
		t.Fatal("nil input should stay nil")
	}
	if _, rest := TakeIntent(json.RawMessage(`not json`)); string(rest) != "not json" {
		t.Fatal("unparseable input should pass through")
	}
	// A schema with no properties, or an unparseable one, is unchanged.
	for _, s := range []string{`{"type":"object"}`, `not json`} {
		if got := string(WithIntent(json.RawMessage(s))); got != s {
			t.Fatalf("schema %q became %q", s, got)
		}
	}
	// A tool declaring its own intent keeps it.
	own := `{"type":"object","properties":{"intent":{"type":"number"}}}`
	if got := string(WithIntent(json.RawMessage(own))); !strings.Contains(got, `"type":"number"`) {
		t.Fatalf("a tool's own intent was overwritten: %s", got)
	}
}
