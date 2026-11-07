package tool

import (
	"encoding/json"
	"strings"
	"testing"
)

// The slim schemas are valid JSON, smaller than the full ones, and leave
// out the fields a headless run never used.
func TestSlimSchemas(t *testing.T) {
	type tl interface {
		Description() string
		Schema() json.RawMessage
	}
	for _, c := range []struct {
		name       string
		full, slim tl
		gone       []string
	}{
		{"bash", Bash{}, Bash{Slim: true}, []string{"background", "stdin", "interactive"}},
		{"read_file", ReadFile{}, ReadFile{Slim: true}, []string{"anchors"}},
		{"write_file", WriteFile{ThenRun: true}, WriteFile{ThenRun: true, Slim: true}, []string{"then_run"}},
		{"edit_file", EditFile{ThenRun: true}, EditFile{ThenRun: true, Slim: true}, []string{"then_run", "from_line", "from_anchor"}},
	} {
		var v map[string]any
		if err := json.Unmarshal(c.slim.Schema(), &v); err != nil {
			t.Fatalf("%s: slim schema is not JSON: %v", c.name, err)
		}
		if len(c.slim.Schema())+len(c.slim.Description()) >= len(c.full.Schema())+len(c.full.Description()) {
			t.Errorf("%s: slim is not smaller", c.name)
		}
		for _, g := range c.gone {
			if strings.Contains(string(c.slim.Schema()), `"`+g+`"`) {
				t.Errorf("%s: slim schema still has %s", c.name, g)
			}
		}
	}
	ts := &ToolSearch{Registry: NewRegistry(), Slim: true}
	if strings.Contains(ts.Description(), "Loadable:") {
		t.Error("slim tool_search still lists every loadable tool")
	}
}
