package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

func scopeProject(t *testing.T) string {
	root := t.TempDir()
	for rel, body := range map[string]string{
		"shop/cart.py":       "def total(xs): return sum(xs)\n",
		"tests/test_cart.py": "from shop import cart\n",
		"auth/login.py":      "def login(): pass\n",
	} {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(body), 0o644)
	}
	return root
}

func scopeAgent(f *fake, root string) *Agent {
	return &Agent{Provider: f, Model: "m", Log: &session.Memory{}, AutoApprove: true, Root: root,
		Tools: tool.NewRegistry(tool.WriteFile{Root: root})}
}

// scopeSaysAndWrites is a reply that says why, then writes path.
func scopeSaysAndWrites(id, say, path, content string) *llm.Response {
	in, _ := json.Marshal(map[string]string{"path": path, "content": content})
	var blocks []llm.Block
	if say != "" {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: say})
	}
	blocks = append(blocks, llm.Block{Type: llm.BlockToolUse, ID: id, Name: "write_file", Input: in})
	return &llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: blocks}, StopReason: llm.StopToolUse, Usage: llm.Usage{InputTokens: 1, OutputTokens: 1}}
}

func scopeNotices(a *Agent) []session.Event {
	var out []session.Event
	for _, e := range a.Log.Events() {
		if e.Type == session.TypeNotice && e.Name == "scope" {
			out = append(out, e)
		}
	}
	return out
}

func TestScopeRecordsAnOutOfScopeEditWithItsIntentAndNeverBlocks(t *testing.T) {
	root := scopeProject(t)
	f := &fake{responses: []*llm.Response{
		scopeSaysAndWrites("w1", "Round half-even in the cart.", "shop/cart.py", "def total(xs): return round(sum(xs))\n"),
		scopeSaysAndWrites("w2", "login reads the old total, so it has to change too.", "auth/login.py", "def login(): return 1\n"),
		scopeSaysAndWrites("w3", "", "auth/login.py", "def login(): return 2\n"),
		text("done"),
	}}
	a := scopeAgent(f, root)
	if err := a.Run(context.Background(), "fix shop/cart.py rounding", nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "auth/login.py")); string(b) != "def login(): return 2\n" {
		t.Fatalf("the out-of-scope edit did not land: %q", b)
	}
	ns := scopeNotices(a)
	if len(ns) != 1 || ns[0].Arguments != "auth/login.py" || !strings.Contains(ns[0].Output, "login reads the old total") {
		t.Fatalf("notices %+v, want one for auth/login.py with its intent", ns)
	}
	if strings.Contains(lastToolResult(a), "[scope]") {
		t.Error("the justification request was added with scope_justify off")
	}
	r := a.ScopeReport()
	if r == nil || r.Edited != 2 || len(r.Outside) != 1 || r.Creep != 0.5 || r.Outside[0].Unjustified {
		t.Fatalf("report %+v", r)
	}
}

func TestScopeJustifyAsksAndRecordsTheAnswer(t *testing.T) {
	root := scopeProject(t)
	f := &fake{responses: []*llm.Response{
		scopeSaysAndWrites("w1", "", "auth/login.py", "x = 1\n"),
		text("It is needed because login caches cart totals."),
	}}
	a := scopeAgent(f, root)
	a.ScopeJustify = true
	if err := a.Run(context.Background(), "fix shop/cart.py rounding", nil); err != nil {
		t.Fatal(err)
	}
	if res := lastToolResult(a); !strings.Contains(res, "[scope] auth/login.py is outside what the task names (shop/, shop/cart.py)") {
		t.Fatalf("tool result %q lacks the request", res)
	}
	r := a.ScopeReport()
	if r == nil || len(r.Outside) != 1 || r.Outside[0].Unjustified || !strings.Contains(r.Outside[0].Justification, "login caches cart totals") {
		t.Fatalf("report %+v, want the answer as the justification", r)
	}
}

// A file the run creates is not part of the tree the contract is
// inferred from: a new module that imports the named one is still creep.
func TestScopeACreatedImporterIsNotInScope(t *testing.T) {
	root := scopeProject(t)
	f := &fake{responses: []*llm.Response{
		scopeSaysAndWrites("w1", "a helper", "extra/helper.py", "from shop import cart\n"),
		scopeSaysAndWrites("w2", "the fix", "shop/cart.py", "def total(xs): return 0\n"),
		scopeSaysAndWrites("w3", "a new file in the package", "shop/rounding.py", "R = 2\n"),
		text("done"),
	}}
	a := scopeAgent(f, root)
	if err := a.Run(context.Background(), "fix shop/cart.py", nil); err != nil {
		t.Fatal(err)
	}
	r := a.ScopeReport()
	if r == nil || len(r.Outside) != 1 || r.Outside[0].Path != "extra/helper.py" {
		t.Fatalf("report %+v, want only extra/helper.py outside", r)
	}
}

func TestScopeOffAndVagueTask(t *testing.T) {
	root := scopeProject(t)
	f := &fake{responses: []*llm.Response{scopeSaysAndWrites("w1", "x", "auth/login.py", "x\n"), text("done")}}
	a := scopeAgent(f, root)
	a.Off = map[string]bool{"scope": true}
	if err := a.Run(context.Background(), "fix shop/cart.py", nil); err != nil {
		t.Fatal(err)
	}
	if r := a.ScopeReport(); r != nil || len(scopeNotices(a)) != 0 {
		t.Fatalf("scope off still reported %+v", r)
	}

	f = &fake{responses: []*llm.Response{scopeSaysAndWrites("w1", "x", "auth/login.py", "y\n"), text("done")}}
	a = scopeAgent(f, root)
	if err := a.Run(context.Background(), "make it better", nil); err != nil {
		t.Fatal(err)
	}
	if r := a.ScopeReport(); r != nil || len(scopeNotices(a)) != 0 {
		t.Fatalf("a task naming nothing reported creep: %+v", r)
	}
}
