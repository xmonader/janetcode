package run

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/recipe"
)

// The label is written into the session as the turn's user message, so
// two identical replays must produce the same one. It was built by
// ranging over the argument map, whose order Go randomises.
func TestLabelIsDeterministic(t *testing.T) {
	args := map[string]string{"env": "prod", "region": "eu", "tag": "v2", "who": "me", "why": "rollout"}
	want := Label("deploy", args)
	if !strings.HasPrefix(want, "/recipe run deploy ") {
		t.Fatalf("label = %q", want)
	}
	for i := 0; i < 20; i++ {
		if got := Label("deploy", args); got != want {
			t.Fatalf("label moved between identical runs: %q then %q", want, got)
		}
	}
	for k, v := range args {
		if !strings.Contains(want, k+"="+v) {
			t.Fatalf("label %q does not carry %s=%s", want, k, v)
		}
	}
	if got := Label("deploy", nil); got != "/recipe run deploy" {
		t.Fatalf("a recipe with no arguments = %q", got)
	}
}

func TestNeedsMCP(t *testing.T) {
	plain := recipe.Recipe{Steps: []recipe.Step{{Tool: "bash"}, {Tool: "write_file"}}}
	if NeedsMCP(plain) {
		t.Fatal("a file-and-shell recipe needs no MCP server started for it")
	}
	withMCP := recipe.Recipe{Steps: []recipe.Step{{Tool: "bash"}, {Tool: "mcp__docs__search"}}}
	if !NeedsMCP(withMCP) {
		t.Fatal("a step calling an MCP-contributed tool needs its server")
	}
}
