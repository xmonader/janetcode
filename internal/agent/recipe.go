package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// RecipeFailure is the step a recipe stopped on: enough for the model to
// pick up where it left off (see RecipeHandover), and for a front-end to
// say what happened.
type RecipeFailure struct {
	Step   int
	Tool   string
	Output string
}

// RunRecipe replays a recipe's steps through exactly the pipeline a model
// turn uses: each step runs through runNested, so authorize, approvals,
// guards, checkpoints, provenance and the event log all see it as they
// would a call the model made. It stops at the first failing step.
//
// It runs as its own logged turn, so /changes, undo and verdicts work on
// it the way they do on any other turn — but there is no model in the
// loop to open one the usual way (RunBlocks calls the provider). Rather
// than reimplement a model turn's bookkeeping a second time, this reuses
// the same internals RunBlocks opens and closes a turn with (turnN,
// cp.beginTurn/endTurn, the turn/start and turn/end log events), with the
// turn's user message the synthetic text "/recipe run <name> ...": from
// the log's point of view this looks exactly like a turn whose "model"
// happened to be a fixed script instead of the provider. What it does not
// do is run the verify gate or compaction, both of which exist to react
// to what a model just said; there is none here to react to.
//
// emit is the live event sink, as for RunBlocks; nil discards events.
func (a *Agent) RunRecipe(ctx context.Context, r recipe.Recipe, args map[string]string, emit func(Event)) (report string, failed *RecipeFailure, err error) {
	steps, err := recipe.Expand(r, args)
	if err != nil {
		return "", nil, err
	}

	before := len(a.Messages)
	merged := a.appendUserBlocks(llm.Block{Type: llm.BlockText, Text: recipeLabel(r.Name, args), Synthetic: true})
	a.turnN++
	a.stepN = 0
	if a.turnN == 1 {
		a.recordStart()
	}
	a.logEvent(session.Event{Type: session.TypeTurnStart, Turn: a.turnN})
	a.logUserMessage(merged)
	a.cp.beginTurn(a.turnN, before)
	a.mutated, a.mutations = false, 0
	defer a.endTurn()

	if emit == nil {
		emit = func(Event) {}
	}
	a.emitMu.Lock()
	a.emitNow = emit
	a.emitMu.Unlock()
	notice := fmt.Sprintf("recipe %s: replaying %s", r.Name, textfmt.Plural(len(steps), "step"))
	emit(Event{Kind: EvNotice, Text: notice})
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Output: notice})

	var b strings.Builder
	fmt.Fprintf(&b, "recipe %s (%s)\n", r.Name, textfmt.Plural(len(steps), "step"))
	for i, st := range steps {
		if err := ctx.Err(); err != nil {
			a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "cancelled"})
			return strings.TrimRight(b.String(), "\n"), nil, err
		}
		id := fmt.Sprintf("recipe-%s-%d", r.Name, i+1)
		out, isErr := a.runNested(ctx, id, st.Tool, st.Input)
		fmt.Fprintf(&b, "## step %d: %s\n%s\n", i+1, st.Tool, strings.TrimRight(out, "\n"))
		if isErr {
			a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "recipe-failed"})
			return strings.TrimRight(b.String(), "\n"), &RecipeFailure{Step: i + 1, Tool: st.Tool, Output: out}, nil
		}
	}
	fmt.Fprintf(&b, "\n[recipe %s finished, %s]", r.Name, textfmt.Plural(len(steps), "step"))
	a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "recipe-done"})
	return strings.TrimRight(b.String(), "\n"), nil, nil
}

// recipeLabel is the synthetic user message a recipe run's turn carries,
// so a session read back later shows what happened. session.Synthetic
// treats it as machinery, the same as an undo or a goal round.
func recipeLabel(name string, args map[string]string) string {
	s := "/recipe run " + name
	for k, v := range args {
		s += " " + k + "=" + v
	}
	return s
}

// RecipeHandover is the prompt given to the model after a recipe step
// failed: which step failed and its output, which steps already
// succeeded, and the recipe's own description as the goal to finish. A
// front-end starts an ordinary turn with it (Run/RunBlocks), so
// everything downstream — approvals, verify, verdicts — behaves exactly
// as it would for anything else the user asked for.
func RecipeHandover(r recipe.Recipe, f RecipeFailure) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The recipe %q stopped at step %d of %d: %s failed.\n", r.Name, f.Step, len(r.Steps), f.Tool)
	if f.Step > 1 {
		names := make([]string, 0, f.Step-1)
		for _, st := range r.Steps[:f.Step-1] {
			names = append(names, st.Tool)
		}
		fmt.Fprintf(&b, "Already ran and succeeded: %s.\n", strings.Join(names, ", "))
	}
	fmt.Fprintf(&b, "\n%s's output:\n%s\n", f.Tool, strings.TrimSpace(f.Output))
	if d := strings.TrimSpace(r.Description); d != "" {
		fmt.Fprintf(&b, "\nWhat this recipe is for: %s\n", d)
	}
	b.WriteString("\nWork out why this step failed and finish the job yourself, using whatever tools and judgement it takes.")
	return b.String()
}
