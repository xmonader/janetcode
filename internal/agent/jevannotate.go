package agent

import (
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/jev"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Offline session annotation (item 5 of the surviving list in
// docs/notes/jev.md): label a recorded turn — did it get anywhere, was
// any of it wasted, was a tool used wrongly, how did it end — so the
// local critic has something to fit and so rules worth graduating into
// Go can be found in data rather than guessed at.
//
// Nothing in a live session calls any of this. It runs from
// `--decisions-annotate` over sessions already on disk, which is what
// makes it the one consumer on the list with no live risk at all: the
// worst a wrong answer can do is put a wrong row in a training file.

const (
	qAnnOutcome  = "turn_outcome"
	qAnnProgress = "progress"
	qAnnWasted   = "wasted"
	qAnnMisuse   = "tool_misuse"
)

// Budgets for AnnotateState. A turn can be long; the annotation is of
// the turn's shape, not its content, so the window is bounded the same
// way the mid-turn state is.
const (
	annPromptBudget = 1200
	annLineBudget   = 220
	annStateBudget  = 4000
	// AnnotateWindow is how many of a turn's tool calls the state
	// describes. A turn with more than this is rendered as its first and
	// last calls with the count of what was dropped between them, so a
	// long turn still reads as a long turn.
	AnnotateWindow = 16
)

// AnnotateState renders one recorded turn: what it was asked to do,
// what it did, and how the harness graded it. The verdict line is the
// harness's own deterministic reading (verdict.go) and is included
// deliberately — an annotation that contradicts it is more interesting
// than one made without it.
func AnnotateState(prompt string, steps []StuckStep, verdict string) string {
	var b strings.Builder
	b.WriteString("Request: " + textfmt.Clip(prompt, annPromptBudget) + "\n")
	if verdict != "" {
		b.WriteString("Harness verdict for the turn: " + textfmt.Clip(verdict, annLineBudget) + "\n")
	}
	if len(steps) == 0 {
		b.WriteString("Tool calls in this turn: none; the agent answered from what it had\n")
		return textfmt.Clip(b.String(), annStateBudget)
	}
	b.WriteString("Tool calls in this turn, oldest first:\n")
	head, tail := steps, []StuckStep(nil)
	var dropped int
	if len(steps) > AnnotateWindow {
		keep := AnnotateWindow / 2
		head, tail = steps[:keep], steps[len(steps)-keep:]
		dropped = len(steps) - 2*keep
	}
	n := 0
	write := func(s StuckStep) {
		n++
		line := strconv.Itoa(n) + ". " + s.Tool + " " + s.Args + " -> "
		if s.Failed {
			line += "ERROR: "
		}
		line += s.Outcome
		b.WriteString(textfmt.Clip(line, annLineBudget) + "\n")
	}
	for _, s := range head {
		write(s)
	}
	if dropped > 0 {
		b.WriteString("... " + strconv.Itoa(dropped) + " further calls omitted ...\n")
		n += dropped
	}
	for _, s := range tail {
		write(s)
	}
	return textfmt.Clip(b.String(), annStateBudget)
}

// AnnotateQuestions is what a recorded turn is labelled with. Four
// answers, one call: the outcome a session-level reader wants, and the
// three judgements the critic and any rule-finding would be fitted on.
func AnnotateQuestions() []jev.Question {
	return []jev.Question{
		{ID: qAnnOutcome, Kind: jev.Choice,
			Instructions: "How did this turn end, judged only by what the record above shows?",
			Options: map[string]string{
				"success":   "the turn did what the request asked and the record shows it working or checked",
				"partial":   "the turn did some of what was asked, or did it without showing that it works",
				"failed":    "the turn tried and the record shows it did not get there",
				"abandoned": "the turn stopped without finishing and without saying it had, for instance it ran out of room or was cut off",
			}},
		{ID: qAnnProgress, Kind: jev.Noul,
			Instructions: "Did this turn move the request forward at all?",
			NoulCriteria: map[string]string{
				"true":  "something was learned, changed or ruled out that the next turn can build on",
				"false": "the turn ends where it started: the same questions open and nothing changed",
			}},
		{ID: qAnnWasted, Kind: jev.Noul,
			Instructions: "Was a material part of this turn's work wasted?",
			NoulCriteria: map[string]string{
				"true":  "calls that failed and were repeated the same way, files read again with nothing having changed, or work thrown away unused",
				"false": "nearly every call earned its place, even the ones that came back empty",
			}},
		{ID: qAnnMisuse, Kind: jev.Noul,
			Instructions: "Was a tool used wrongly in this turn?",
			NoulCriteria: map[string]string{
				"true":  "a tool was called with arguments that could not work, or used for a job another tool does, or called when its result was already in hand",
				"false": "each call is a reasonable use of the tool it names",
			}},
	}
}

// Annotation is one labelled turn, and the row `--decisions-annotate`
// writes. The field names are the JSONL schema; nothing else reads them
// positionally.
type Annotation struct {
	Session   string  `json:"session"`
	Turn      int     `json:"turn"`
	Outcome   string  `json:"outcome"`
	OutConf   float64 `json:"outcome_confidence"`
	Progress  float64 `json:"progress"`
	Wasted    float64 `json:"wasted"`
	Misuse    float64 `json:"tool_misuse"`
	Calls     int     `json:"calls"`
	Failed    int     `json:"failed_calls"`
	Verdict   string  `json:"harness_verdict,omitempty"`
	Model     string  `json:"model,omitempty"`
	LatencyMs int64   `json:"latency_ms"`
	Err       string  `json:"error,omitempty"`
}

// AnnotateOf maps a result onto that row's answer fields. Pure, so the
// driver and any later consumer read the answers the same way.
func AnnotateOf(res *jev.Result) Annotation {
	var a Annotation
	if res == nil {
		return a
	}
	a.Outcome, a.OutConf, _ = res.ChoiceAt(qAnnOutcome)
	a.Progress, _ = res.NoulAt(qAnnProgress)
	a.Wasted, _ = res.NoulAt(qAnnWasted)
	a.Misuse, _ = res.NoulAt(qAnnMisuse)
	a.Model, a.LatencyMs = res.Model, res.Latency.Milliseconds()
	return a
}

// AnnotateWasteThreshold is the P(wasted) at which the annotation calls
// a turn wasteful. It is what the agreement check against the
// by-construction waste labels is measured at.
const AnnotateWasteThreshold = 0.5

// AnnotateTurn is one recorded turn folded out of a session log: what
// drove it, what it did, and the harness's own verdict on it.
type AnnotateTurn struct {
	Turn    int
	Prompt  string
	Steps   []StuckStep
	Verdict string
	Failed  int
}

// AnnotateTurns folds a whole session log into the turns an annotation
// run labels. It lives here rather than in the driver so the state the
// replay renders and the state a later live reader would render come
// from one piece of code.
func AnnotateTurns(events []session.Event) []AnnotateTurn {
	prompts := map[int]string{}
	for _, p := range session.Turns(events) {
		prompts[p.Turn] = p.Text
	}
	order := []int{}
	byTurn := map[int]*AnnotateTurn{}
	pending := map[string]int{} // call id -> index into that turn's steps
	pendTurn := map[string]int{}
	get := func(turn int) *AnnotateTurn {
		t := byTurn[turn]
		if t == nil {
			t = &AnnotateTurn{Turn: turn, Prompt: prompts[turn]}
			byTurn[turn], order = t, append(order, turn)
		}
		return t
	}
	for _, e := range events {
		switch e.Type {
		case session.TypeTurnStart:
			get(e.Turn)
		case session.TypeToolCall:
			t := get(e.Turn)
			t.Steps = append(t.Steps, StuckStep{Tool: e.Name, Args: textfmt.Clip(string(e.Arguments), annLineBudget)})
			if e.CallID != "" {
				pending[e.CallID], pendTurn[e.CallID] = len(t.Steps)-1, e.Turn
			}
		case session.TypeToolResult:
			i, ok := pending[e.CallID]
			if !ok {
				continue
			}
			t := get(pendTurn[e.CallID])
			if i >= len(t.Steps) {
				continue
			}
			t.Steps[i].Failed = e.IsError
			t.Steps[i].Outcome = textfmt.Clip(textfmt.FirstLine(e.Output, annLineBudget), annLineBudget)
			if e.IsError {
				t.Failed++
			}
			delete(pending, e.CallID)
			delete(pendTurn, e.CallID)
		}
	}
	out := make([]AnnotateTurn, 0, len(order))
	for _, n := range order {
		t := byTurn[n]
		// A call that never came back is as much a fact about the turn as
		// one that failed; it reads as what it is rather than as blank.
		for i := range t.Steps {
			if t.Steps[i].Outcome == "" && !t.Steps[i].Failed {
				t.Steps[i].Outcome = "(no result recorded)"
			}
		}
		t.Verdict = FoldOutcome(events, n).Line()
		out = append(out, *t)
	}
	return out
}
