package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/tool"
)

// Every layer that asks says which one it is and how to stop it: the
// call that reaches the approver carries Why and Stop, and Explain joins
// them into the one line the prompts show.
func TestEveryAskSaysWhyAndHowToStop(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(a *Agent, root string)
		calls    []*llm.Response
		source   string
		why      string
		stopWord string
	}{
		{
			name:     "mode",
			setup:    func(a *Agent, root string) {},
			calls:    []*llm.Response{toolCall("t1", "write_file", `{"path":"x","content":"a"}`), text("done")},
			why:      "permission mode Ask: edits and commands ask",
			stopWord: "Always allow",
		},
		{
			name:     "mode edits asks for commands",
			setup:    func(a *Agent, root string) { a.AutoEdits = true },
			calls:    []*llm.Response{toolCall("t1", "bash", `{"command":"go version"}`), text("done")},
			why:      "permission mode Edits",
			stopWord: "/auto",
		},
		{
			name:     "protected path",
			setup:    func(a *Agent, root string) { a.Protected = perm.NewProtected(nil) },
			calls:    []*llm.Response{toolCall("t1", "write_file", `{"path":".npmrc","content":"x"}`), text("done")},
			source:   "protected",
			why:      "a protected path: ",
			stopWord: "Allow all",
		},
		{
			name: "ask rule",
			setup: func(a *Agent, root string) {
				rs, err := perm.File{Ask: []string{"bash(git push*)"}}.Rules()
				if err != nil {
					t.Fatal(err)
				}
				a.Rules = rs
			},
			calls:    []*llm.Response{toolCall("t1", "bash", `{"command":"git push origin"}`), text("done")},
			source:   "rule",
			why:      "your ask rule bash(git push*)",
			stopWord: "permissions.json",
		},
		{
			name:     "tool guard",
			setup:    func(a *Agent, root string) { a.AutoEdits = true },
			calls:    []*llm.Response{toolCall("t1", "bash", `{"command":"rm ../x"}`), text("done")},
			source:   "tool",
			why:      "the tool's own guard",
			stopWord: "/yolo",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var seen []ToolCall
			a, root := newAgent(t, &fake{responses: c.calls}, asks(&seen))
			a.Root = root
			a.Tools = tool.NewRegistry(tool.WriteFile{Root: root}, tool.Bash{Root: root})
			os.WriteFile(filepath.Join(root, "x"), []byte("a"), 0o644)
			c.setup(a, root)
			if err := a.Run(context.Background(), "go", nil); err != nil {
				t.Fatal(err)
			}
			if len(seen) != 1 {
				t.Fatalf("asked %d times", len(seen))
			}
			got := seen[0]
			if got.Source != c.source || !strings.Contains(got.Why, c.why) || !strings.Contains(got.Stop, c.stopWord) {
				t.Fatalf("source=%q why=%q stop=%q", got.Source, got.Why, got.Stop)
			}
			if line := got.Explain(); !strings.HasPrefix(line, "Asked because ") || !strings.Contains(line, "; to stop: ") {
				t.Fatalf("explain = %q", line)
			}
			if last, ok := a.LastAsk(); !ok || last.Why != got.Why {
				t.Fatalf("last ask = %+v", last)
			}
			if !strings.Contains(a.WhyLastAsk(), "Asked because") {
				t.Fatalf("why = %q", a.WhyLastAsk())
			}
		})
	}
}

// The egress layer asks for a read-only call too, and says which host.
func TestEgressAskSaysWhyAndHowToStop(t *testing.T) {
	var seen []ToolCall
	ran := false
	f := &fake{responses: []*llm.Response{toolCall("t1", "lookup", `{"host":"api.x.com"}`), text("ok")}}
	a, _ := newAgent(t, f, asks(&seen))
	a.Tools.Add(hostTool{ran: &ran})
	a.EgressPolicy = &egress.Policy{Default: egress.Ask}
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0].Source != "egress" || !strings.Contains(seen[0].Why, "egress policy: sends data to api.x.com") || !strings.Contains(seen[0].Stop, "egress.allow") {
		t.Fatalf("seen = %+v", seen)
	}
}

// The layers decided far from the prompt (a hook, taint) fill the same
// fields, the first cause stands, and a managed policy changes the mode
// hint to what is true under it.
func TestSetAskFirstCauseStandsAndExplainFormat(t *testing.T) {
	c := &ToolCall{}
	setAsk(c, "taint", "this session read untrusted content (taint), and x", stopTaint)
	setAsk(c, "rule", "your ask rule r", stopRule)
	if c.Source != "taint" || !strings.Contains(c.Why, "taint") || c.Stop != stopTaint {
		t.Fatalf("call = %+v", c)
	}
	if got := c.Explain(); got != "Asked because this session read untrusted content (taint), and x; to stop: "+stopTaint {
		t.Fatalf("explain = %q", got)
	}
	if (ToolCall{}).Explain() != "" {
		t.Fatal("a call with no explanation must print nothing")
	}
	for name, stop := range map[string]string{"hook": stopHook, "rule": stopRule, "tool": stopTool, "protected": stopProtected, "egress": stopEgress, "taint": stopTaint} {
		if stop == "" {
			t.Errorf("%s has no stop hint", name)
		}
	}
}
