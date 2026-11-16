package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/secret"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestScriptStructuredDataAndDiscovery(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("12\tliteral gutter\nneedle\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ag, s := scriptAgent(t, root)
	ag.Tools.AddDeferred(tool.Glob{Root: root})
	before := s.Description()
	count := len(ag.Tools.Defs())
	out := runScript(t, s, `
from janetcode_tools import list_tools, describe_tool, call_result, read_file
catalog = list_tools("glob")
assert any(t["name"] == "glob" for t in catalog["tools"])
assert describe_tool("glob")["input_schema"]["properties"]["pattern"]["type"] == "string"
paths = call_result("glob", pattern="*.txt")["data"]["paths"]
r = call_result("read_file", path=paths[0], offset=1, limit=1)
assert r["data"]["lines"] == ["12\tliteral gutter"]
assert r["data"]["truncated"]
assert "needle" in read_file(paths[0])
print("structured and legacy APIs work")
`)
	if !strings.Contains(out, "structured and legacy APIs work") || strings.Contains(out, "failed") {
		t.Fatal(out)
	}
	if len(ag.Tools.Defs()) != count || ag.Tools.Loaded("glob") {
		t.Fatal("discovery activated a deferred schema")
	}
	if s.Description() != before {
		t.Fatal("discovery changed the prompt prefix")
	}
}

func TestScriptDiscoveryIsBoundedAndDescriptionStable(t *testing.T) {
	ag, s := scriptAgent(t, t.TempDir())
	before := s.Description()
	for i := 0; i < 40; i++ {
		t := &scriptNamedTool{name: fmt.Sprintf("remote_%02d", i)}
		if i < 20 {
			ag.Tools.Add(t)
		} else {
			ag.Tools.AddDeferred(t)
		}
	}
	if s.Description() != before {
		t.Fatal("MCP catalog expanded the script description")
	}
	reply := (&rpc{parent: ag}).discover("list", "remote_")
	var data struct {
		Tools     []scriptToolInfo `json:"tools"`
		Total     int              `json:"total"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal(reply.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Total != 40 || len(data.Tools) != 20 || !data.Truncated {
		t.Fatalf("unbounded catalog: %+v", data)
	}
	if reply := (&rpc{parent: ag}).discover("describe", "execute_code"); reply.Kind != "unknown" {
		t.Fatalf("exposed recursive scripting: %+v", reply)
	}
}

type scriptNamedTool struct{ name string }

func (t *scriptNamedTool) Name() string                                       { return t.name }
func (*scriptNamedTool) Description() string                                  { return "Remote test capability." }
func (*scriptNamedTool) Mutating() bool                                       { return false }
func (*scriptNamedTool) Schema() json.RawMessage                              { return json.RawMessage(`{"type":"object"}`) }
func (*scriptNamedTool) Run(context.Context, json.RawMessage) (string, error) { return "", nil }

func TestScriptRetryStopsAfterDenial(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	ag, s := scriptAgent(t, root)
	ag.ReadOnly = true
	out := runScript(t, s, `
from janetcode_tools import retry, write_file, ToolError
try:
    retry(lambda: write_file("blocked.txt", content="denied"), attempts=3, delay=0)
except ToolError as e:
    print("kind=" + e.kind)
try:
    retry(lambda: None, attempts=0)
except ValueError:
    print("bad attempts rejected")
`)
	if !strings.Contains(out, "kind=denied") || !strings.Contains(out, "bad attempts rejected") || !strings.Contains(out, "[1 tool call]") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(root, "blocked.txt")); !os.IsNotExist(err) {
		t.Fatalf("denied write touched disk: %v", err)
	}
}

func TestStructuredDataScrubbing(t *testing.T) {
	key := "sk-ant-" + strings.Repeat("a", 40)
	for _, input := range []string{
		fmt.Sprintf(`{"value":%q,"large":9007199254740993}`, key),
		`{"value":"sk-ant-\u0061` + strings.Repeat("a", 39) + `"}`,
		`{"password":"s8Qv2Lr9Wz5Bf7Mx3Jk6Nt4C"}`,
		`{"value":"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----"}`,
	} {
		data, n := redactToolData(json.RawMessage(input))
		if n == 0 || len(data) == 0 || !json.Valid(data) || strings.Contains(string(data), key) || strings.Contains(string(data), "opaquecredential") {
			t.Fatalf("not scrubbed: %s count=%d", data, n)
		}
	}
	// Invalid tool JSON is not allowed onto the RPC wire.
	if data, _ := redactToolData(json.RawMessage(`{"broken"`)); data != nil {
		t.Fatal("kept invalid data")
	}
	data, _ := redactToolData(json.RawMessage(`{"large":9007199254740993}`))
	if !strings.Contains(string(data), "9007199254740993") {
		t.Fatal("numeric precision lost")
	}
	// Registered credentials must be scrubbed before the script can inspect them.
	secret.Register("structured-test-password")
	defer secret.Forget()
	data, n := redactToolData(json.RawMessage(`{"value":"structured-test-password"}`))
	if n == 0 || strings.Contains(string(data), "structured-test-password") {
		t.Fatal("registered secret escaped")
	}
}

func TestScriptStructuredReadScrubsMultilinePrivateKey(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	const body = "cHJpdmF0ZWtleWJvZHk="
	content := "before\n-----BEGIN PRIVATE KEY-----\n" + body + "\n-----END PRIVATE KEY-----\nafter\n"
	if err := os.WriteFile(filepath.Join(root, "key.pem"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	_, s := scriptAgent(t, root)
	out := runScript(t, s, `
from janetcode_tools import call_result
r = call_result("read_file", path="key.pem")
assert "cHJpdmF0ZWtleWJvZHk=" not in r["text"]
assert r["data"]["lines"] == ["[redacted]"] * 5, r["data"]
assert r["data"]["total_lines"] == 5
assert r["data"]["offset"] == 1
print("multiline private key hidden with source positions intact")
`)
	if !strings.Contains(out, "multiline private key hidden with source positions intact") || strings.Contains(out, body) {
		t.Fatal(out)
	}
}

func TestStructuredDataScrubbingDropsPrivateKeysAcrossFields(t *testing.T) {
	data, n := redactToolData(json.RawMessage(`{"key":{"begin":"-----BEGIN PRIVATE KEY-----","body":"cHJpdmF0ZWtleWJvZHk=","end":"-----END PRIVATE KEY-----"},"metadata":7}`))
	if len(data) != 0 || n == 0 {
		t.Fatalf("private key spanning object fields retained: %s count=%d", data, n)
	}
}

func TestScriptStructuredGrepScrubsMultilinePrivateKey(t *testing.T) {
	pythonOrSkip(t)
	root := t.TempDir()
	const body = "cHJpdmF0ZWtleWJvZHk="
	content := "-----BEGIN PRIVATE KEY-----\n" + body + "\n-----END PRIVATE KEY-----\n"
	if err := os.WriteFile(filepath.Join(root, "key.pem"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	ag, s := scriptAgent(t, root)
	ag.Tools.Add(tool.Grep{Root: root})
	out := runScript(t, s, `
from janetcode_tools import call_result
r = call_result("grep", pattern=".*", path="key.pem")
assert "cHJpdmF0ZWtleWJvZHk=" not in r["text"]
assert r["data"] is None, r["data"]
print("private key spanning match objects hidden")
`)
	if !strings.Contains(out, "private key spanning match objects hidden") || strings.Contains(out, body) {
		t.Fatal(out)
	}
}
