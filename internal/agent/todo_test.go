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

func TestTodoWriteReadAndFold(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	tl := &TodoTool{Parent: a}
	ctx := context.Background()

	// Reading before anything is written says so rather than failing.
	if out, err := tl.Run(ctx, []byte(`{}`)); err != nil || !strings.Contains(out, "no plan") {
		t.Fatalf("empty = %q %v", out, err)
	}
	out, err := tl.Run(ctx, []byte(`{"todos":[
		{"text":"read the parser","status":"completed"},
		{"text":"add the currency column","status":"in_progress"},
		{"text":"update the README","status":"pending"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[x] read the parser") || !strings.Contains(out, "[>] add the currency column") ||
		!strings.Contains(out, "[ ] update the README") || !strings.Contains(out, "1 of 3 done") {
		t.Fatalf("render:\n%s", out)
	}
	// The plan is on the log, so it outlives the tool result.
	if got := a.Todos(); len(got) != 3 || got[1].Status != TodoDoing {
		t.Fatalf("todos = %+v", got)
	}
	// Rewriting replaces it whole rather than merging.
	if _, err := tl.Run(ctx, []byte(`{"todos":[{"text":"only this","status":"pending"}]}`)); err != nil {
		t.Fatal(err)
	}
	if got := a.Todos(); len(got) != 1 || got[0].Text != "only this" {
		t.Fatalf("after rewrite = %+v", got)
	}
	// Reading it back with no argument shows the current plan.
	if out, _ := tl.Run(ctx, []byte(`{}`)); !strings.Contains(out, "only this") {
		t.Fatalf("read back = %q", out)
	}
}

func TestTodoValidation(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	tl := &TodoTool{Parent: a}
	ctx := context.Background()
	for _, bad := range []string{
		`{"todos":[{"text":"  ","status":"pending"}]}`,
		`{"todos":[{"text":"x","status":"maybe"}]}`,
		`{"todos":[{"text":"a","status":"in_progress"},{"text":"b","status":"in_progress"}]}`,
	} {
		if _, err := tl.Run(ctx, []byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	// A rejected plan does not replace the good one.
	if len(a.Todos()) != 0 {
		t.Fatalf("a rejected write was recorded: %+v", a.Todos())
	}
	// A missing status defaults to pending rather than failing.
	if _, err := tl.Run(ctx, []byte(`{"todos":[{"text":"x"}]}`)); err != nil {
		t.Fatal(err)
	}
	if got := a.Todos(); len(got) != 1 || got[0].Status != TodoPending {
		t.Fatalf("default status = %+v", got)
	}
}

// The plan is what a long task is held together by, so what is left of it
// must survive the compaction that throws the conversation away.
func TestOpenTodosSurviveCompaction(t *testing.T) {
	f := &fake{responses: []*llm.Response{text(`{"facts":["done"]}`)}}
	mem := &session.Memory{}
	a := &Agent{Provider: f, Tools: tool.NewRegistry(), Model: "m", Log: mem, Messages: []llm.Message{
		llm.TextMessage(llm.RoleUser, strings.Repeat("investigate the flaky suite ", 80)),
		llm.TextMessage(llm.RoleAssistant, "working"),
	}}
	a.WriteTodos([]Todo{
		{Text: "read the parser", Status: TodoDone},
		{Text: "add the currency column", Status: TodoDoing},
		{Text: "update the README", Status: TodoPending},
	})
	if err := a.ForceCompact(context.Background(), func(Event) {}); err != nil {
		t.Fatal(err)
	}
	note := a.Messages[0].Text()
	if !strings.Contains(note, "Plan still open") {
		t.Fatalf("the plan did not survive:\n%s", note)
	}
	if !strings.Contains(note, "add the currency column") || !strings.Contains(note, "update the README") {
		t.Fatalf("open steps missing:\n%s", note)
	}
	if strings.Contains(note, "read the parser") {
		t.Fatalf("a finished step was carried forward:\n%s", note)
	}
	// And it is still readable from the log afterwards.
	if got := a.Todos(); len(got) != 3 {
		t.Fatalf("plan lost from the log: %+v", got)
	}
}

func TestFoldTodosIgnoresOtherEvents(t *testing.T) {
	raw, _ := json.Marshal([]Todo{{Text: "a", Status: TodoPending}})
	ev := []session.Event{
		{Type: session.TypeToolCall, Name: "todo"},
		{Type: session.TypeTodoWrite, Extra: raw},
		{Type: session.TypeTodoWrite, Extra: json.RawMessage("not json")},
	}
	if got := FoldTodos(ev); len(got) != 1 || got[0].Text != "a" {
		t.Fatalf("fold = %+v", got)
	}
}
