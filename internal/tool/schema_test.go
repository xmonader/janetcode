package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// builtins is a registry of the tools that do not need a live host, for
// the checks every tool schema has to pass.
func builtins(t *testing.T) *Registry {
	t.Helper()
	root := t.TempDir()
	return NewRegistry(
		ReadFile{Root: root}, WriteFile{Root: root}, EditFile{Root: root},
		ListDir{Root: root}, Glob{Root: root}, Grep{Root: root},
		Bash{Root: root}, JobTool{}, ReadManyFiles{Root: root},
	)
}

// Every tool schema must be a JSON Schema object, and the same bytes every
// time it is asked for: the tool definitions sit at the front of the
// request, so a schema that varies — map iteration order, a timestamp —
// breaks the provider's prompt cache on every call.
func TestSchemasAreObjectsAndStable(t *testing.T) {
	r := builtins(t)
	for _, name := range r.Names() {
		tl, _ := r.Get(name)
		first := tl.Schema()
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(first, &obj); err != nil {
			t.Fatalf("%s: schema is not valid JSON: %v", name, err)
		}
		if string(obj["type"]) != `"object"` {
			t.Errorf("%s: schema type is %s, want \"object\"", name, obj["type"])
		}
		if _, ok := obj["properties"]; !ok {
			t.Errorf("%s: schema has no properties", name)
		}
		for i := 0; i < 3; i++ {
			if again := tl.Schema(); !bytes.Equal(first, again) {
				t.Fatalf("%s: schema is not stable across calls", name)
			}
		}
		if intent := WithIntent(first); !bytes.Equal(intent, WithIntent(first)) {
			t.Fatalf("%s: WithIntent is not deterministic", name)
		}
	}
}

// A tool that decodes with unknown fields disallowed must declare every
// field it accepts, or a model following the description gets "invalid
// input" — and a provider enforcing additionalProperties:false cannot
// send the field at all. read_file's description advertises anchors.
func TestReadFileDeclaresAnchors(t *testing.T) {
	var obj struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(ReadFile{}.Schema(), &obj); err != nil {
		t.Fatal(err)
	}
	if _, ok := obj.Properties["anchors"]; !ok {
		t.Fatal("read_file schema does not declare anchors, which its description tells the model to use")
	}
	root := t.TempDir()
	w := WriteFile{Root: root}
	if err := w.write(t, "f.txt", "one\ntwo\n"); err != nil {
		t.Fatal(err)
	}
	out, err := (ReadFile{Root: root}).Run(context.Background(), json.RawMessage(`{"path":"f.txt","anchors":true}`))
	if err != nil {
		t.Fatalf("anchors read: %v", err)
	}
	if out == "" {
		t.Fatal("no output")
	}
}

// write is a small helper so the schema test can make a file without
// depending on the other test files' helpers.
func (t WriteFile) write(tb testing.TB, path, content string) error {
	tb.Helper()
	in, err := json.Marshal(map[string]string{"path": path, "content": content})
	if err != nil {
		return err
	}
	_, err = t.Run(context.Background(), in)
	return err
}

// Defs is memoised on Version; the memo must never outlive a change to
// the visible tool set.
func TestDefsCacheFollowsVersion(t *testing.T) {
	r := builtins(t)
	first := r.Defs()
	if got := r.Defs(); len(got) != len(first) {
		t.Fatalf("cached defs changed length: %d vs %d", len(got), len(first))
	}
	r.Add(fakeTool{})
	after := r.Defs()
	if len(after) != len(first)+1 {
		t.Fatalf("added tool missing from defs: %d want %d", len(after), len(first)+1)
	}
	r.SetIntent(true)
	withIntent := r.Defs()
	if !bytes.Contains(withIntent[0].InputSchema, []byte(`"intent"`)) {
		t.Fatalf("intent not applied after SetIntent: %s", withIntent[0].InputSchema)
	}
	r.Remove("fake")
	if len(r.Defs()) != len(first) {
		t.Fatal("removed tool still in defs")
	}
	// Mutating a returned slice must not reach the cache.
	defs := r.Defs()
	defs[0] = llm.ToolDef{Name: "clobbered"}
	if r.Defs()[0].Name == "clobbered" {
		t.Fatal("caller mutated the cached definitions")
	}
}

type fakeTool struct{}

func (fakeTool) Name() string            { return "fake" }
func (fakeTool) Description() string     { return "fake" }
func (fakeTool) Mutating() bool          { return false }
func (fakeTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object","properties":{}}`) }
func (fakeTool) Run(context.Context, json.RawMessage) (string, error) {
	return "", nil
}
