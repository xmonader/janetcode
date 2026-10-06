package tool

import (
	"reflect"
	"testing"
)

// A gate prunes what is registered and refuses what comes later, saying
// why; a required tool gets past it.
func TestGateAppliesToLateTools(t *testing.T) {
	r := NewRegistry(&namedTool{name: "bash"}, &namedTool{name: "read_file"})
	r.Gate(func(n string) string {
		if n == "bash" || n == "mcp__x__y" || n == "structured_output" {
			return "excluded by --exclude-tools"
		}
		return ""
	})
	r.AddDeferred(&namedTool{name: "mcp__x__y"})
	r.Add(&namedTool{name: "grep"})
	r.AddRequired(&namedTool{name: "structured_output"})
	if got := r.Names(); !reflect.DeepEqual(got, []string{"grep", "read_file", "structured_output"}) {
		t.Fatalf("names = %v", got)
	}
	for _, n := range []string{"bash", "mcp__x__y"} {
		if why, ok := r.Withheld(n); !ok || why != "excluded by --exclude-tools" {
			t.Fatalf("%s withheld = %q %v", n, why, ok)
		}
	}
	if _, ok := r.Withheld("structured_output"); ok {
		t.Fatal("required tool reported withheld")
	}
}
