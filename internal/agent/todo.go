package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

// A plan of steps the model keeps as it works. A long task drifts: the
// model finishes something, moves on, and three turns later has lost the
// two things it had not started. Writing the steps down and marking them
// off keeps the whole of the work in view — and because each write is an
// event, the plan survives compaction, can be read back afterwards, and
// says what the agent thought it was doing at any point in the session.

// Todo states. At most one step is in progress at a time: a plan with
// three things half-done is not a plan.
const (
	TodoPending = "pending"
	TodoDoing   = "in_progress"
	TodoDone    = "completed"
)

// Todo is one step of the plan.
type Todo struct {
	Text   string `json:"text"`
	Status string `json:"status"`
	// BlockedBy names steps that must finish first, by their text. It is
	// what turns a list into a plan: a model that writes "wait for the
	// migration" as step 4 otherwise starts it anyway.
	BlockedBy []string `json:"blocked_by,omitempty"`
}

// Done reports whether the step is finished.
func (t Todo) Done() bool { return t.Status == TodoDone }

// FoldTodos returns the plan as of the last write, or nil when none was
// made. The whole list is written each time, so the latest event is the
// plan and no merging is needed.
func FoldTodos(events []session.Event) []Todo {
	var out []Todo
	for _, e := range events {
		if e.Type != session.TypeTodoWrite || len(e.Extra) == 0 {
			continue
		}
		var list []Todo
		if json.Unmarshal(e.Extra, &list) == nil {
			out = list
		}
	}
	return out
}

// Todos is the current plan.
func (a *Agent) Todos() []Todo {
	if a.Log == nil {
		return nil
	}
	return FoldTodos(a.Log.Events())
}

// todosWrittenThisTurn reports whether the plan now in force was written
// during the turn that is running.
func (a *Agent) todosWrittenThisTurn() bool {
	if a.Log == nil {
		return false
	}
	for _, e := range a.Log.Events() {
		if e.Type == session.TypeTodoWrite {
			if e.Turn == a.turnN {
				return true
			}
		}
	}
	return false
}

// countDone is how many steps of a plan are finished.
func countDone(todos []Todo) int {
	n := 0
	for _, t := range todos {
		if t.Done() {
			n++
		}
	}
	return n
}

// noPlan is what a read of an empty plan says, and what PlanDelta takes
// as "there was nothing here before".
const noPlan = "no plan has been written"

// RenderTodos draws the plan the way the model and the user both read it.
func RenderTodos(todos []Todo) string {
	if len(todos) == 0 {
		return noPlan
	}
	done := 0
	var b strings.Builder
	for _, t := range todos {
		mark := "[ ]"
		switch t.Status {
		case TodoDone:
			mark, done = "[x]", done+1
		case TodoDoing:
			mark = "[>]"
		}
		fmt.Fprintf(&b, "%s %s", mark, t.Text)
		if len(t.BlockedBy) > 0 && !t.Done() {
			fmt.Fprintf(&b, " (after: %s)", strings.Join(t.BlockedBy, "; "))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n%d of %d done", done, len(todos))
	return b.String()
}

// TodoTool lets the model keep its plan.
type TodoTool struct{ Parent *Agent }

func (*TodoTool) Name() string { return "todo" }
func (*TodoTool) Description() string {
	return "Keep the plan for a multi-step task: write the whole list of steps with their status, and rewrite it as you go. " +
		"Call it whenever the work has three or more distinct steps, or the user asked for several things — count them rather than judging whether the job feels complicated, and write the plan before starting rather than partway through. " +
		"Mark exactly one step in_progress while you work on it, mark it completed as soon as it is done rather than in a batch at the end, and add steps as you discover them. " +
		"The plan is what the user watches to see what is done and what is left, and it survives compaction, so it is also how you remember your own work on a long job. " +
		"One edit or one question needs no plan. A rewrite answers with the steps that changed and how many are done; calling it with no todos shows the whole plan."
}
func (*TodoTool) Mutating() bool { return false }
func (*TodoTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"todos":{"type":"array","description":"The whole plan, in order; omit to read the current one","items":{
"type":"object","properties":{
"text":{"type":"string","description":"What the step is, in a few words"},
"status":{"type":"string","enum":["pending","in_progress","completed"]},
"blocked_by":{"type":"array","items":{"type":"string"},"description":"Optional: the exact text of steps that must finish first"}},
"required":["text","status"],"additionalProperties":false}}},
"additionalProperties":false}`)
}

func (t *TodoTool) Run(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Todos *[]Todo `json:"todos"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("bad input: %w", err)
		}
	}
	if t.Parent == nil {
		return "", fmt.Errorf("the todo tool has no agent")
	}
	if in.Todos == nil {
		return RenderTodos(t.Parent.Todos()), nil
	}
	todos, err := validateTodos(*in.Todos)
	if err != nil {
		return "", err
	}
	// A plan rewritten from scratch in the middle of the turn that filled
	// it in loses what was finished. On a recorded session the model
	// replaced a plan with six of seven steps done with seven fresh
	// pending ones, under new wording, and the work it had already done
	// vanished from the plan the user was watching. Between turns this is
	// ordinary — a new request gets a new plan — so only the same turn is
	// refused, and only a list no shorter than the one it replaces:
	// cutting a plan down to fewer steps is narrowing it, which is the
	// model's to do.
	current := t.Parent.Todos()
	if done := countDone(current); done > 0 && len(todos) >= len(current) && countDone(todos) == 0 && t.Parent.todosWrittenThisTurn() {
		return "", fmt.Errorf("this rewrite marks none of its steps completed, but the plan you are working from has %d of %d done; rewriting it from scratch mid-task loses that. Keep the finished steps (call todo with no todos to read the plan back), or mark the new list's finished work completed", done, len(current))
	}

	// The model rewrites the whole plan on every status change, and
	// answering with the whole plan every time puts the same seven lines
	// in the context seven times over. It already knows what it wrote;
	// what it needs back is confirmation of what changed and where that
	// leaves the work. The plan whole on creation, the changed steps and
	// the tally after that, and the whole plan whenever it is asked for
	// by calling with no todos.
	prev := RenderTodos(current)
	t.Parent.WriteTodos(todos)
	return PlanDelta(prev, RenderTodos(todos)), nil
}

// WriteTodos records a plan on the log, so it outlives the tool result
// that carried it and can be read back after a compaction.
func (a *Agent) WriteTodos(todos []Todo) {
	raw, err := json.Marshal(todos)
	if err != nil {
		return
	}
	a.logEvent(session.Event{Type: session.TypeTodoWrite, Tokens: len(todos), Extra: raw})
}

// maxPlanNote caps the plan carried across a compaction. The whole plan
// is on the log and the todo tool reads it back in full; what the note
// is for is knowing there is a plan and what is next, and a long one
// would be re-read on every request for the rest of the session.
const maxPlanNote = 800

// renderOpenPlan draws the open steps for the compaction note, within
// maxPlanNote characters, saying how many it left out.
func renderOpenPlan(todos []Todo) string {
	var b strings.Builder
	for i, t := range todos {
		mark := "[ ]"
		if t.Status == TodoDoing {
			mark = "[>]"
		}
		line := mark + " " + t.Text
		if len(t.BlockedBy) > 0 {
			line += " (after: " + strings.Join(t.BlockedBy, "; ") + ")"
		}
		line += "\n"
		if b.Len()+len(line) > maxPlanNote {
			fmt.Fprintf(&b, "… and %d more steps; call todo to read the whole plan\n", len(todos)-i)
			break
		}
		b.WriteString(line)
	}
	return b.String()
}

// validateTodos rejects a plan that cannot be worked from: empty steps,
// an unknown status, several steps claimed to be in progress at once, a
// dependency on a step that is not in the plan, or a cycle of them.
func validateTodos(todos []Todo) ([]Todo, error) {
	out := make([]Todo, 0, len(todos))
	doing := 0
	for i, t := range todos {
		text := strings.TrimSpace(t.Text)
		if text == "" {
			return nil, fmt.Errorf("step %d has no text", i+1)
		}
		switch t.Status {
		case TodoPending, TodoDoing, TodoDone:
		case "":
			t.Status = TodoPending
		default:
			return nil, fmt.Errorf("step %d has status %q; use pending, in_progress or completed", i+1, t.Status)
		}
		if t.Status == TodoDoing {
			doing++
		}
		var blocked []string
		for _, b := range t.BlockedBy {
			if b = strings.TrimSpace(b); b != "" && b != text {
				blocked = append(blocked, b)
			}
		}
		out = append(out, Todo{Text: text, Status: t.Status, BlockedBy: blocked})
	}
	if doing > 1 {
		return nil, fmt.Errorf("%d steps are in_progress; work on one at a time", doing)
	}
	if err := checkBlocks(out); err != nil {
		return nil, err
	}
	return out, nil
}

// checkBlocks rejects dependencies that name nothing, and cycles. A plan
// whose steps wait for each other is not a plan, and finding that out at
// step four costs a turn.
func checkBlocks(todos []Todo) error {
	index := map[string]int{}
	for i, t := range todos {
		index[t.Text] = i
	}
	for i, t := range todos {
		for _, b := range t.BlockedBy {
			if _, ok := index[b]; !ok {
				return fmt.Errorf("step %d is blocked_by %q, which is not a step in this plan; use the exact text of the step it waits for", i+1, b)
			}
		}
	}
	// Depth-first search; state 1 is on the stack, 2 is finished.
	state := make([]int, len(todos))
	var path []string
	var visit func(int) error
	visit = func(i int) error {
		switch state[i] {
		case 1:
			return fmt.Errorf("these steps wait for each other and none of them can start: %s", strings.Join(append(path, todos[i].Text), " → "))
		case 2:
			return nil
		}
		state[i] = 1
		path = append(path, todos[i].Text)
		for _, b := range todos[i].BlockedBy {
			if err := visit(index[b]); err != nil {
				return err
			}
		}
		path = path[:len(path)-1]
		state[i] = 2
		return nil
	}
	for i := range todos {
		if err := visit(i); err != nil {
			return err
		}
	}
	return nil
}

// openTodos is the part of the plan still to do, for the note carried
// across a compaction. A finished plan carries nothing forward.
func openTodos(todos []Todo) []Todo {
	var out []Todo
	for _, t := range todos {
		if !t.Done() {
			out = append(out, t)
		}
	}
	return out
}

// RenderCall implements tool.Renderer: the plan's own line says how many
// steps it holds, not the JSON it was written as.
func (*TodoTool) RenderCall(input json.RawMessage, _ tool.RenderContext) []string {
	var in struct {
		Todos []struct{} `json:"todos"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Todos) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("plan · %d steps", len(in.Todos))}
}

// RenderResult leaves the result alone. It used to delta the plan for the
// transcript, because the model rewrote the whole list on every status
// change; the tool itself answers that way now, so the result is already
// whole the first time and the changed lines after that, and deltaing a
// delta would drop lines.
func (*TodoTool) RenderResult(json.RawMessage, string, tool.RenderContext) []string { return nil }

// PlanDelta is the lines of cur that differ from prev, with the tally.
// prev "" or a prev that says no plan was written yields cur whole: that
// is a plan being created, and there is nothing to compare it against.
func PlanDelta(prev, cur string) string {
	if prev == "" || prev == noPlan || prev == cur {
		return cur
	}
	before := map[string]bool{}
	for _, l := range strings.Split(prev, "\n") {
		before[l] = true
	}
	var changed []string
	tally := ""
	for _, l := range strings.Split(cur, "\n") {
		switch {
		case strings.HasPrefix(l, "[") && !before[l]:
			changed = append(changed, l)
		case strings.Contains(l, " of ") && strings.HasSuffix(l, "done"):
			tally = l
		}
	}
	if len(changed) == 0 {
		return tally
	}
	if tally != "" {
		changed = append(changed, tally)
	}
	return strings.Join(changed, "\n")
}
