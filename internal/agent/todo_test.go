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

// The model rewrites the whole plan on every status change, and the tool
// used to answer with the whole plan every time: a seven-step plan
// updated seven times put its seven lines into the context seven times
// over. An update now answers with the steps that changed and the tally;
// creation and an explicit read still answer whole.
func TestTodoUpdatesAnswerWithWhatChanged(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	tl := &TodoTool{Parent: a}
	ctx := context.Background()

	steps := []string{
		"read the importer", "add the currency column", "migrate the table",
		"update the callers", "write the tests", "run the suite", "update the README",
	}
	plan := func(done int) []byte {
		var b strings.Builder
		b.WriteString(`{"todos":[`)
		for i, s := range steps {
			if i > 0 {
				b.WriteString(",")
			}
			status := "pending"
			switch {
			case i < done:
				status = "completed"
			case i == done:
				status = "in_progress"
			}
			b.WriteString(`{"text":"` + s + `","status":"` + status + `"}`)
		}
		b.WriteString(`]}`)
		return []byte(b.String())
	}

	// Creation: whole, so the model can see the plan it just made.
	first, err := tl.Run(ctx, plan(0))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if !strings.Contains(first, s) {
			t.Fatalf("the new plan left out %q:\n%s", s, first)
		}
	}

	// Seven updates, each marking one step done and starting the next.
	whole, delta := len(first), len(first)
	for done := 1; done <= len(steps); done++ {
		out, err := tl.Run(ctx, plan(done))
		if err != nil {
			t.Fatal(err)
		}
		whole += len(RenderTodos(a.Todos()))
		delta += len(out)
		// Two lines change per update — the step finished and the one
		// started — plus the tally; the last update finishes the plan and
		// starts nothing.
		if lines := strings.Count(out, "\n") + 1; lines > 3 {
			t.Fatalf("update %d returned %d lines:\n%s", done, lines, out)
		}
		if !strings.Contains(out, "[x] "+steps[done-1]) {
			t.Fatalf("update %d did not report the step it finished:\n%s", done, out)
		}
		if !strings.Contains(out, "of 7 done") {
			t.Fatalf("update %d has no tally:\n%s", done, out)
		}
		if done < len(steps) && strings.Contains(out, steps[len(steps)-1]) && done != len(steps)-1 {
			t.Fatalf("update %d carried an untouched step:\n%s", done, out)
		}
	}
	if delta >= whole/2 {
		t.Fatalf("a seven-step plan updated seven times: %d bytes whole, %d as deltas", whole, delta)
	}
	t.Logf("seven-step plan, seven updates: %d bytes whole, %d as deltas, %d saved (%d%%)",
		whole, delta, whole-delta, 100*(whole-delta)/whole)

	// An explicit read still answers with the whole plan.
	read, err := tl.Run(ctx, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if !strings.Contains(read, s) {
			t.Fatalf("a read left out %q:\n%s", s, read)
		}
	}
	// And the plan carried across a compaction is the log's, not the
	// result's, so it is untouched by any of this.
	if note := renderOpenPlan(openTodos(a.Todos())); note != "" {
		t.Fatalf("a finished plan carried %q forward", note)
	}
}

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
