package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/tool"
)

// planfileEditor stands in for the user's editor: when the model calls
// it, the plan file is rewritten from outside the agent's own tools, the
// way an edit made in another window lands mid-turn.
type planfileEditor struct{ path, content string }

func (planfileEditor) Name() string            { return "wait" }
func (planfileEditor) Description() string     { return "waits" }
func (planfileEditor) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (planfileEditor) Mutating() bool          { return false }
func (e planfileEditor) Run(context.Context, json.RawMessage) (string, error) {
	if e.content == "" {
		return "waited", os.Remove(e.path)
	}
	return "waited", os.WriteFile(e.path, []byte(e.content), 0o644)
}

func planfileAgent(t *testing.T, plan string, responses ...*llm.Response) (*Agent, *fake, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, filepath.FromSlash(PlanFilePath))
	if plan != "" {
		os.MkdirAll(filepath.Dir(path), 0o755)
		os.WriteFile(path, []byte(plan), 0o644)
	}
	f := &fake{responses: responses}
	a := &Agent{Provider: f, Model: "m", AutoApprove: true}
	a.Root = root
	a.PlanFile = true
	return a, f, path
}

func planfileSent(req llm.Request) string {
	var b strings.Builder
	for _, m := range req.Messages {
		for _, bl := range m.Blocks {
			b.WriteString(bl.Text + "\n")
		}
	}
	return b.String()
}

func TestPlanfileSendsOnlyTheUsersMidTurnEdit(t *testing.T) {
	plan := "# Plan\n1. parse the config\n2. validate it\n"
	a, f, path := planfileAgent(t, plan, toolCall("c1", "wait", `{}`), text("done"))
	a.Tools = tool.NewRegistry(planfileEditor{path: path, content: "# Plan\n1. parse the config\n2. validate it\n3. log every rejected key\n"})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	first := planfileSent(f.requests[0])
	if !strings.Contains(first, "[live plan]") || !strings.Contains(first, "2. validate it") {
		t.Fatalf("the first call did not carry the plan:\n%s", first)
	}
	second := f.requests[1].Messages[len(f.requests[1].Messages)-1]
	var note string
	for _, b := range second.Blocks {
		if strings.Contains(b.Text, "[live plan edited]") {
			note = b.Text
			if !b.Synthetic {
				t.Fatal("the plan note is not marked as the harness's")
			}
		}
	}
	if !strings.Contains(note, "+ 3. log every rejected key") || !strings.Contains(note, "  2. validate it") {
		t.Fatalf("change not sent with its context:\n%s", note)
	}
	if strings.Contains(note, "1. parse the config") {
		t.Fatalf("unchanged lines beyond one of context were sent:\n%s", note)
	}
}

func TestPlanfileAgentsOwnWriteIsNotSteering(t *testing.T) {
	a, f, path := planfileAgent(t, "1. one\n",
		toolCall("c1", "write_file", `{"path":".janetcode/plan.md","content":"1. one\n2. two\n"}`), text("done"))
	a.Tools = tool.NewRegistry(tool.WriteFile{Root: a.Root})
	a.Approver = ApproverFunc(func(context.Context, ToolCall) (Decision, error) { return Allow, nil })
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "1. one\n2. two\n" {
		t.Fatalf("plan = %q; sent:\n%s", got, planfileSent(f.requests[1]))
	}
	if s := planfileSent(f.requests[1]); strings.Contains(s, "[live plan edited]") {
		t.Fatalf("the model's own write was sent back as the user's edit:\n%s", s)
	}
}

func TestPlanfileOffSendsNothing(t *testing.T) {
	a, f, _ := planfileAgent(t, "1. one\n", text("done"))
	a.PlanFile = false
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if s := planfileSent(f.requests[0]); strings.Contains(s, "[live plan") {
		t.Fatalf("plan sent while off:\n%s", s)
	}
}

func TestPlanfileNoFileNoNote(t *testing.T) {
	a, f, _ := planfileAgent(t, "", text("one"), text("two"))
	for _, p := range []string{"go", "again"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range f.requests {
		if s := planfileSent(r); strings.Contains(s, "[live plan") {
			t.Fatalf("a note with no plan file:\n%s", s)
		}
	}
}

func TestPlanfileUnchangedIsSentOnce(t *testing.T) {
	a, f, _ := planfileAgent(t, "1. one\n", text("one"), text("two"))
	for _, p := range []string{"go", "again"} {
		if err := a.Run(context.Background(), p, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(planfileSent(f.requests[1]), "[live plan"); n != 1 {
		t.Fatalf("plan notes in the conversation: %d, want 1", n)
	}
}

func TestPlanfileDeletionIsSaid(t *testing.T) {
	a, f, path := planfileAgent(t, "1. one\n", toolCall("c1", "wait", `{}`), text("done"))
	a.Tools = tool.NewRegistry(planfileEditor{path: path})
	if err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	if s := planfileSent(f.requests[1]); !strings.Contains(s, "deleted "+PlanFilePath) {
		t.Fatalf("deletion not said:\n%s", s)
	}
}

func TestPlanfileNote(t *testing.T) {
	// Whitespace churn is not an edit.
	if note, _, _ := planfileNote("1. a\n2. b\n", "1. a  \n\n2. b\n", false, true); note != "" {
		t.Fatalf("whitespace-only change produced a note:\n%s", note)
	}
	// An empty plan seen first says nothing.
	if note, _, _ := planfileNote("", "\n\n", true, true); note != "" {
		t.Fatalf("empty plan produced a note:\n%s", note)
	}
	note, added, removed := planfileNote("1. a\n2. b\n3. c\n", "1. a\n2. B, but faster\n3. c\n", false, true)
	if added != 1 || removed != 1 || !strings.Contains(note, "- 2. b") || !strings.Contains(note, "+ 2. B, but faster") {
		t.Fatalf("+%d -%d:\n%s", added, removed, note)
	}
}

func TestPlanfileDiffReorder(t *testing.T) {
	diff, added, removed := planfileDiff([]string{"a", "b", "c"}, []string{"c", "a", "b"})
	if added != 1 || removed != 1 {
		t.Fatalf("+%d -%d:\n%s", added, removed, diff)
	}
	big := make([]string, 700)
	for i := range big {
		big[i] = "x"
	}
	if _, added, removed := planfileDiff(big, big[:1]); added != 1 || removed != 700 {
		t.Fatalf("oversized fallback: +%d -%d", added, removed)
	}
}

func TestPlanfileSkipsWhatIsNotAPlan(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "plan.md")
	os.WriteFile(bin, []byte("a\x00b"), 0o644)
	if _, _, ok := planfileRead(bin); ok {
		t.Fatal("a binary file read as a plan")
	}
	if _, _, ok := planfileRead(dir); ok {
		t.Fatal("a directory read as a plan")
	}
	if _, exists, ok := planfileRead(filepath.Join(dir, "absent.md")); exists || !ok {
		t.Fatal("an absent plan is a readable nothing")
	}
}
