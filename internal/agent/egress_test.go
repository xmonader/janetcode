package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/egress"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/memory"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/skill"
)

func TestMemoryWhyListsWhatTheTurnAttachedAndWhy(t *testing.T) {
	mem := memory.Open(t.TempDir(), t.TempDir())
	mem.Put(memory.Entry{Name: "goose", Description: "Database migrations use goose", Body: "goose up from db/"})
	a, _ := newAgent(t, &fake{responses: []*llm.Response{text("ok"), text("ok")}}, nil)
	a.Prepare = skill.TurnPrep(&skill.Injector{}, mem)
	_, emit := collect()
	if got := a.MemoryWhy(); !strings.Contains(got, "no turn has run") {
		t.Fatalf("before any turn: %q", got)
	}
	if err := a.Run(context.Background(), "add a database migration", emit); err != nil {
		t.Fatal(err)
	}
	got := a.MemoryWhy()
	for _, want := range []string{"goose", "matched", "migration", "/memory expire"} {
		if !strings.Contains(got, want) {
			t.Errorf("MemoryWhy lacks %q:\n%s", want, got)
		}
	}
	if err := a.Run(context.Background(), "hello there", emit); err != nil {
		t.Fatal(err)
	}
	if got := a.MemoryWhy(); !strings.Contains(got, "attached no memory") {
		t.Errorf("an unrelated turn: %q", got)
	}
}

// hostTool is a non-mutating tool that reaches a host named in its input:
// a stand-in for web_search or a read-only remote MCP tool.
type hostTool struct{ ran *bool }

func (hostTool) Name() string            { return "lookup" }
func (hostTool) Description() string     { return "looks something up on a host" }
func (hostTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (hostTool) Mutating() bool          { return false }
func (h hostTool) Run(context.Context, json.RawMessage) (string, error) {
	*h.ran = true
	return "found it; token=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH", nil
}
func (hostTool) EgressHosts(in json.RawMessage) []string {
	var v struct{ Host string }
	_ = json.Unmarshal(in, &v)
	return []string{v.Host}
}

func TestEgressReportCoversTheTurn(t *testing.T) {
	ran := false
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"a.txt"}`),
		toolCall("t2", "lookup", `{"host":"docs.example.com"}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	a.Tools.Add(hostTool{ran: &ran})
	_, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	rep, ok := a.EgressLast()
	if !ok {
		t.Fatal("no report after the turn")
	}
	txt := rep.Text()
	for _, want := range []string{"a.txt", "read_file", "lookup", "secret-shaped values redacted"} {
		if !strings.Contains(txt, want) {
			t.Errorf("report lacks %q:\n%s", want, txt)
		}
	}
	// The tool answered with an API key. It must reach neither the report,
	// its JSON, the summary nor the session log's egress record.
	const key = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGH"
	for name, s := range map[string]string{"text": txt, "json": rep.JSON(), "summary": a.EgressSummary()} {
		if strings.Contains(s, key) || strings.Contains(s, "abcdefghijklmnopqrstuvwxyz") {
			t.Errorf("the %s of the egress report carries the key:\n%s", name, s)
		}
	}
	if !strings.Contains(a.EgressSummary(), "every host allowed") {
		t.Errorf("summary = %s", a.EgressSummary())
	}
}

func TestEgressPolicyBlocksAndAsks(t *testing.T) {
	ran := false
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "lookup", `{"host":"evil.example"}`),
		text("ok"),
	}}
	a, _ := newAgent(t, f, nil)
	a.Tools.Add(hostTool{ran: &ran})
	a.EgressPolicy = &egress.Policy{Default: egress.Block, Allow: []string{"docs.example.com"}}
	_, emit := collect()
	if err := a.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	if ran {
		t.Fatal("a call to a blocked host ran")
	}
	if rep, _ := a.EgressLast(); !strings.Contains(rep.Text(), "BLOCKED") {
		t.Fatalf("the refusal is not in the report:\n%s", rep.Text())
	}

	// ask: a read-only call to an unlisted host is put to the user even
	// under --yolo, and a refusal keeps it from running.
	ran = false
	asked := 0
	f2 := &fake{responses: []*llm.Response{toolCall("t1", "lookup", `{"host":"other.example"}`), text("ok")}}
	a2, _ := newAgent(t, f2, egressApprover(func(c ToolCall) {
		asked++
		if !strings.Contains(c.Warning, "other.example") {
			t.Errorf("the prompt does not name the host: %q", c.Warning)
		}
	}))
	a2.Tools.Add(hostTool{ran: &ran})
	a2.AutoApprove = true
	a2.EgressPolicy = &egress.Policy{Default: egress.Ask}
	if err := a2.Run(context.Background(), "go", emit); err != nil {
		t.Fatal(err)
	}
	if asked != 1 || ran {
		t.Fatalf("asked %d, ran %v", asked, ran)
	}
}

type egressApprover func(ToolCall)

func (f egressApprover) Approve(ctx context.Context, c ToolCall) (Decision, error) {
	f(c)
	return Deny, nil
}

// The permission audit names what refused a call. A refusal by the
// egress policy was logged with no basis at all, and so as "unknown".
func TestEgressRefusalIsAuditedAsThePolicy(t *testing.T) {
	ran := false
	f := &fake{responses: []*llm.Response{toolCall("t1", "lookup", `{"host":"evil.example"}`), text("ok")}}
	a, _ := newAgent(t, f, nil)
	log := &session.Memory{}
	a.Log = log
	a.Tools.Add(hostTool{ran: &ran})
	a.EgressPolicy = &egress.Policy{Default: egress.Block}
	if err := a.Run(context.Background(), "go", func(Event) {}); err != nil {
		t.Fatal(err)
	}
	for _, e := range log.Events() {
		if e.Type == session.TypePermission {
			if !e.Denied || e.Reason != "egress policy" {
				t.Fatalf("audited as %q (denied %v), want a denial by the egress policy", e.Reason, e.Denied)
			}
			return
		}
	}
	t.Fatal("the refusal left no permission record")
}
