package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/rules"
	"github.com/xmonader/janetcode/internal/tool"
)

func TestTouchedPathsFromInputAndToucher(t *testing.T) {
	got := touchedPaths(tool.ReadFile{Root: "."}, json.RawMessage(`{"path":"a/b.go","offset":3}`))
	if len(got) != 1 || got[0] != "a/b.go" {
		t.Fatalf("paths = %v", got)
	}
	// A command is not a path: a rule must not be brought in by a word
	// in a shell line.
	if got := touchedPaths(tool.Bash{Root: "."}, json.RawMessage(`{"command":"grep sql -r ."}`)); len(got) != 0 {
		t.Fatalf("paths = %v", got)
	}
	if got := touchedPaths(tool.ReadFile{Root: "."}, json.RawMessage(`{"paths":["x.go","y.go"]}`)); len(got) != 2 {
		t.Fatalf("paths = %v", got)
	}
}

// A rule names paths relative to the project, and a model that passes
// the absolute path of a file in it (or a file tool that resolves one)
// still brings the rule in.
func TestPathRuleMatchesAnAbsolutePathInTheProject(t *testing.T) {
	f := &fake{}
	a, root := newAgent(t, f, nil)
	a.Root = root
	in := &rules.Injector{Rules: func() []rules.Rule {
		return []rules.Rule{{Name: "mig", Paths: []string{"db/migrations/*.sql"}, Body: "append-only"}}
	}}
	a.PathRules = in.For
	abs := filepath.Join(root, "db", "migrations", "001.sql")
	input, _ := json.Marshal(map[string]string{"path": abs})
	a.applyPathRules(tool.ReadFile{Root: root}, input, func(Event) {})
	if !in.Sent("mig") {
		t.Fatalf("a rule for db/migrations/*.sql did not match %s", abs)
	}
	// A path outside the project is not made to look like one inside it.
	in2 := &rules.Injector{Rules: in.Rules}
	a.PathRules = in2.For
	input, _ = json.Marshal(map[string]string{"path": filepath.Join(filepath.Dir(root), "db", "migrations", "001.sql")})
	a.applyPathRules(tool.ReadFile{Root: root}, input, func(Event) {})
	if in2.Sent("mig") {
		t.Fatal("a path outside the project brought the rule in")
	}
}

func TestPathRuleReachesTheModelOnceAsHarnessText(t *testing.T) {
	f := &fake{responses: []*llm.Response{
		toolCall("t1", "read_file", `{"path":"a.txt"}`),
		toolCall("t2", "read_file", `{"path":"a.txt","offset":1}`),
		text("done"),
	}}
	a, _ := newAgent(t, f, nil)
	asked := 0
	a.PathRules = func(paths []string) []string {
		asked++
		if asked > 1 {
			return nil // the injector sends each rule once
		}
		return []string{"[project rule: txt — applies to *.txt]\nThese files are generated."}
	}
	evs, emit := collect()
	if err := a.Run(context.Background(), "read it", emit); err != nil {
		t.Fatal(err)
	}
	if asked < 2 {
		t.Fatalf("the rules were consulted %d times, want one per call", asked)
	}
	// The rule rides with the tool results of the call that brought it
	// in, marked synthetic so nothing takes it for the user's words.
	found := false
	for _, m := range a.Messages {
		for _, b := range m.Blocks {
			if b.Type == llm.BlockText && strings.Contains(b.Text, "generated") {
				if !b.Synthetic {
					t.Fatal("a rule must be marked as harness text")
				}
				if m.Role != llm.RoleUser {
					t.Fatalf("a rule went into a %s message", m.Role)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("the rule never reached the conversation: %+v", a.Messages)
	}
	// The history stays valid: no user message comes between a call and
	// its results.
	for i, m := range a.Messages {
		if m.Role != llm.RoleAssistant {
			continue
		}
		if len(m.ToolUses()) == 0 || i+1 >= len(a.Messages) {
			continue
		}
		next := a.Messages[i+1]
		if next.Role != llm.RoleUser || next.Blocks[0].Type != llm.BlockToolResult {
			t.Fatalf("message %d follows tool calls with %+v", i+1, next)
		}
	}
	notice := false
	for _, e := range *evs {
		if e.Kind == EvNotice && strings.Contains(e.Text, "project rule") {
			notice = true
		}
	}
	if !notice {
		t.Fatal("nothing said a rule had come in")
	}
}
