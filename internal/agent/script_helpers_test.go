package agent

import (
	"strings"
	"testing"
)

func TestScriptToolNamesCannotReplaceModuleHelpers(t *testing.T) {
	pythonOrSkip(t)
	ag, s := scriptAgent(t, t.TempDir())
	for _, name := range []string{"call_result", "list_tools", "describe_tool", "json", "os", "str", "Exception", "class", "None", "_request"} {
		ag.Tools.Add(&scriptNamedTool{name: name})
	}
	out := runScript(t, s, `
from janetcode_tools import call, call_result, describe_tool, list_tools, plain
assert describe_tool("json")["name"] == "json"
assert list_tools("Remote")["total"] == 10
assert call_result("list_tools")["data"] is None
assert call_result("class")["text"] == ""
assert call(tool="class") == ""
assert plain("12\thello") == "hello"
print("helpers intact")
`)
	if !strings.Contains(out, "helpers intact") || strings.Contains(out, "failed") {
		t.Fatal(out)
	}
}
