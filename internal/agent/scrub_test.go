package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// envDumpTool stands for every tool that can read a credential out of
// the machine: bash running `env`, read_file on a .env, fetch on a URL
// that echoes its headers back.
type envDumpTool struct{ out string }

func (envDumpTool) Name() string                                           { return "dump" }
func (envDumpTool) Description() string                                    { return "dumps" }
func (envDumpTool) Schema() json.RawMessage                                { return json.RawMessage(`{"type":"object"}`) }
func (envDumpTool) Mutating() bool                                         { return false }
func (d envDumpTool) Run(context.Context, json.RawMessage) (string, error) { return d.out, nil }

const leakedDump = "SHELL=/bin/bash\n" +
	"DEEPSEEK_API_KEY_PERSONAL=sk-dcaf42a37674b1e9c0d5a8f3b27e6104\n" +
	"DASHSCOPE_API_KEY=sk-sp-6133bcaa91de40f7b8c2\n" +
	"PWD=/w\n"

// A tool result is the one place everything downstream reads from: the
// message the model gets, the event the front-ends and the Jev wire
// render, the session log on disk, an eval's recorded output. Redacting
// once, where the tool returns, covers all of them — so this test looks
// at both ends at once.
func TestToolResultsAreScrubbedEverywhereAtOnce(t *testing.T) {
	f := &fake{responses: []*llm.Response{toolCall("c1", "dump", `{}`), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.Tools = tool.NewRegistry(envDumpTool{out: leakedDump})
	mem := &session.Memory{}
	a.Log = mem
	var events []Event
	if err := a.Run(context.Background(), "go", func(e Event) { events = append(events, e) }); err != nil {
		t.Fatal(err)
	}

	var result string
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult {
				result = b.Text
			}
		}
	}
	if result == "" {
		t.Fatal("no tool result in the conversation")
	}
	seen := map[string]string{"the model's tool result": result}
	for _, e := range events {
		if e.Kind == EvToolResult {
			seen["the tool-result event"] = e.Output
		}
	}
	for _, e := range mem.Events() {
		if e.Type == session.TypeToolResult && e.Output != "" {
			seen["the session log"] = e.Output
		}
	}
	if len(seen) != 3 {
		t.Fatalf("only reached %d of the three sinks: %v", len(seen), keysOf(seen))
	}
	for where, got := range seen {
		for _, gone := range []string{"sk-dcaf42a37674", "sk-sp-6133bc"} {
			if strings.Contains(got, gone) {
				t.Errorf("a key reached %s:\n%s", where, got)
			}
		}
		// The names and the ordinary variables have to survive, or the
		// model is reasoning about a blanked-out screen.
		for _, keep := range []string{"SHELL=/bin/bash", "DEEPSEEK_API_KEY_PERSONAL=", "PWD=/w"} {
			if !strings.Contains(got, keep) {
				t.Errorf("%s lost %q:\n%s", where, keep, got)
			}
		}
	}
}

// An error message carries whatever the command printed before it
// failed, which is exactly how a key gets out of a failing curl.
func TestToolErrorsAreScrubbedToo(t *testing.T) {
	f := &fake{responses: []*llm.Response{toolCall("c1", "fail", `{}`), text("done")}}
	a, _ := newAgent(t, f, nil)
	a.Tools = tool.NewRegistry(failTool{})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockToolResult && strings.Contains(b.Text, "sk-sp-6133bc") {
				t.Fatalf("a key reached the model through an error: %s", b.Text)
			}
		}
	}
}

type failTool struct{}

func (failTool) Name() string            { return "fail" }
func (failTool) Description() string     { return "fails" }
func (failTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (failTool) Mutating() bool          { return false }
func (failTool) Run(context.Context, json.RawMessage) (string, error) {
	return "", errAuth
}

var errAuth = &authError{}

type authError struct{}

func (*authError) Error() string {
	return "401 from https://api.example.com with Authorization: Bearer sk-sp-6133bcaa91de40f7b8c2"
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
