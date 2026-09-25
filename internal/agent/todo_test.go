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

// On a recorded session the model had six of seven steps done and then
// wrote the whole plan again from scratch, reworded and all pending. The
// tool took it, and the progress the user was watching disappeared.
func TestAPlanIsNotResetMidTask(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	todo := &TodoTool{Parent: a}
	first := `{"todos":[{"text":"write the package","status":"completed"},{"text":"write the CLI","status":"completed"},{"text":"run the example suite","status":"in_progress"}]}`
	if _, err := todo.Run(context.Background(), json.RawMessage(first)); err != nil {
		t.Fatal(err)
	}
	reset := `{"todos":[{"text":"create project structure","status":"pending"},{"text":"create the runner","status":"pending"},{"text":"verify it works","status":"pending"}]}`
	_, err := todo.Run(context.Background(), json.RawMessage(reset))
	if err == nil || !strings.Contains(err.Error(), "2 of 3 done") {
		t.Fatalf("err = %v", err)
	}
	if got := len(a.Todos()); got != 3 || countDone(a.Todos()) != 2 {
		t.Fatalf("the plan was overwritten anyway: %+v", a.Todos())
	}
	// The same rewrite, keeping what was finished, goes through.
	kept := `{"todos":[{"text":"write the package","status":"completed"},{"text":"write the CLI","status":"completed"},{"text":"verify it works","status":"in_progress"}]}`
	if _, err := todo.Run(context.Background(), json.RawMessage(kept)); err != nil {
		t.Fatalf("a rewrite that keeps the finished steps: %v", err)
	}
	// A new turn starts a new task, and a fresh plan there is ordinary.
	a.turnN = 2
	if _, err := todo.Run(context.Background(), json.RawMessage(reset)); err != nil {
		t.Fatalf("a fresh plan in a new turn: %v", err)
	}
}

// A model that writes a plan and ends the turn with steps still open is
// sent back once, told which steps; a second stop is let through, and so
// is a stop that asks the user something, or one under an older plan.
func TestAnOpenPlanSendsTheTurnBackOnce(t *testing.T) {
	a := &Agent{Log: &session.Memory{}}
	a.turnN = 1
	todo := &TodoTool{Parent: a}
	plan := `{"todos":[{"text":"add the parser","status":"completed"},{"text":"wire the CLI flag","status":"in_progress"},{"text":"update the README","status":"pending"}]}`
	if _, err := todo.Run(context.Background(), json.RawMessage(plan)); err != nil {
		t.Fatal(err)
	}
	var notices []string
	emit := func(e Event) {
		if e.Kind == EvNotice {
			notices = append(notices, e.Text)
		}
	}

	// A question to the user is a legitimate stop.
	nudged := false
	if a.openTodoGate("Should the flag be --strict or --pedantic?", &nudged, emit) || nudged {
		t.Fatal("a question to the user was sent back")
	}

	if !a.openTodoGate("Done: the parser is in.", &nudged, emit) {
		t.Fatal("a turn ending with two open steps was let through")
	}
	msgs := a.Messages
	last := msgs[len(msgs)-1].Text()
	// The note leaves room for a user who asked for part of the plan only.
	for _, want := range []string{"2 open step(s)", "wire the CLI flag (in_progress)", "update the README (pending)", "If the user asked you to stop before them"} {
		if !strings.Contains(last, want) {
			t.Fatalf("the note back lacks %q:\n%s", want, last)
		}
	}
	if strings.Contains(last, "add the parser") || len(notices) != 1 {
		t.Fatalf("note = %q, notices = %q", last, notices)
	}
	if a.openTodoGate("Done.", &nudged, emit) {
		t.Fatal("sent back a second time in the same turn")
	}

	// Next turn: the plan is from an earlier request.
	a.turnN = 2
	if a.openTodoGate("Here is the answer.", new(bool), emit) {
		t.Fatal("an earlier turn's plan held a new turn")
	}
	// A finished plan never holds a turn.
	a.turnN = 3
	finished := `{"todos":[{"text":"add the parser","status":"completed"},{"text":"wire the CLI flag","status":"completed"},{"text":"update the README","status":"completed"}]}`
	if _, err := todo.Run(context.Background(), json.RawMessage(finished)); err != nil {
		t.Fatal(err)
	}
	if a.openTodoGate("All done.", new(bool), emit) {
		t.Fatal("a finished plan held the turn")
	}
}
