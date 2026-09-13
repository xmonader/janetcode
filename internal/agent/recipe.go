package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// mcpToolPrefix names a tool an MCP server contributed to the registry:
// internal/mcp's Client.AgentTools registers every one of a server's
// tools (and its resource helpers) through ToolName as
// "mcp__<server>__<tool>". It is the only marker a recipe step carries
// that says an MCP server has to be running for it to work.
const mcpToolPrefix = "mcp__"

// NeedsMCP reports whether any step of r calls a tool an MCP server
// provides. A caller building the agent for `janetcode --recipe` uses
// this to decide whether starting configured MCP servers — each its own
// process, handshake and tool listing — is worth the time at all: most
// recipes are plain file and shell steps and never need one.
func NeedsMCP(r recipe.Recipe) bool {
	for _, st := range r.Steps {
		if strings.HasPrefix(st.Tool, mcpToolPrefix) {
			return true
		}
	}
	return false
}

// RecipeFailure is the step a recipe stopped on: enough for the model to
// pick up where it left off (see RecipeHandover), and for a front-end to
// say what happened. A failure of the project's verify gate, run after
// every step has already succeeded, is reported the same way with Step
// left at its zero value, Tool "verify" and Verify set, rather than as a
// new kind of result the front-ends would need a second case for.
type RecipeFailure struct {
	Step   int
	Tool   string
	Output string
	// Verify marks a failure of the project's verify gate rather than of
	// a step: every step succeeded, but the checks that ran afterwards
	// did not.
	Verify bool
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
// happened to be a fixed script instead of the provider.
//
// Once the steps are done it also runs the two things RunBlocks does
// after a model turn ends, reusing their exact functions rather than a
// second copy of them: the project's verify gate (verifyGate), when the
// turn mutated files and a gate is configured, and the same compaction
// check (see maybeCompactAfterRecipe) so a long recipe does not leave the
// history swollen with tool results for whatever turn comes next. Both
// are guarded so they do nothing extra when there is no model in this
// process to react to them — a verify failure is handed back as a
// RecipeFailure instead of looping (there is no model call here to loop
// with), and compaction only ever spends a provider call when one is
// actually configured and the history is over threshold, exactly as
// RunBlocks would.
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

	// finish is the one place every successful return path (a step
	// failing, the verify gate failing, or the recipe finishing clean)
	// goes through: it logs the turn's verdict the way an ordinary model
	// turn does at the end of RunBlocks, gives the history a chance to
	// compact before whatever turn comes next sees it, and hands the
	// text back trimmed the way every return here already did.
	finish := func(text string, fail *RecipeFailure) (string, *RecipeFailure, error) {
		outcome := a.Outcome()
		a.logOutcome(outcome)
		if !a.Off["verdict"] && outcome.Verdict != VerdictNoChanges {
			emit(Event{Kind: EvVerdict, Text: outcome.Line(), IsError: !outcome.OK()})
		}
		a.maybeCompactAfterRecipe(ctx, emit)
		return strings.TrimRight(text, "\n"), fail, nil
	}

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
			return finish(b.String(), &RecipeFailure{Step: i + 1, Tool: st.Tool, Output: out})
		}
	}

	// Verify gate: the same function RunBlocks calls after a model turn
	// that mutated files ends — a recipe's steps all succeeding says
	// nothing about whether the project's own checks still pass. Called
	// once, since there is no model loop here for it to send a failure
	// back into: on a fresh gateState the first failure always takes the
	// "send it back" branch (rounds starts at 0, MaxRounds is 3), which
	// is also the branch that appends the check's own feedback message
	// to a.Messages — so the handover turn a front-end starts afterwards
	// already carries it in history, in addition to it being reported
	// here as the failure's Output.
	if a.mutated && !a.Off["verify"] {
		afterMsgs := len(a.Messages)
		gate := gateState{}
		a.verifyGate(ctx, &gate, emit)
		if gate.rounds > 0 {
			out := a.working.Verify // set by verifyGate either way; the one-line summary
			if len(a.Messages) > afterMsgs {
				// The richer message verifyGate just appended (command,
				// exit code and output) rather than the one-line summary.
				out = a.Messages[len(a.Messages)-1].Text()
			}
			fmt.Fprintf(&b, "\n## verify\n%s\n", strings.TrimRight(out, "\n"))
			a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "recipe-verify-failed"})
			return finish(b.String(), &RecipeFailure{Tool: "verify", Output: out, Verify: true})
		}
	}

	fmt.Fprintf(&b, "\n[recipe %s finished, %s]", r.Name, textfmt.Plural(len(steps), "step"))
	a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "recipe-done"})
	return finish(b.String(), nil)
}

// maybeCompactAfterRecipe applies the same compaction check RunBlocks
// makes before every model call (see its loop in RunBlocks), once, at
// the end of a recipe's own turn. The gap this closes is narrow: a
// failure hands over to an ordinary model turn, which already compacts
// itself on its first call if it needs to — but only after paying to
// send the whole swollen history that once. Doing the same check here
// first means that first call, if any, starts from a history already
// the right size.
//
// Computing the estimate and comparing it to the threshold never touches
// the provider — that part is always safe to run. Only a.Compact itself
// can pay for one, and even then only when pruning the history is not
// enough on its own to get under threshold, exactly as in RunBlocks. A
// headless `--recipe` run may have no provider configured at all (there
// would be nothing to hand a step failure over to either); Compact would
// have nothing to call in that case, so this skips it entirely rather
// than risk turning an otherwise-successful recipe into an error over a
// context-window nicety.
func (a *Agent) maybeCompactAfterRecipe(ctx context.Context, emit func(Event)) {
	if a.Provider == nil {
		return
	}
	compactAt := a.raiseCompactFloor(emit)
	if compactAt <= 0 || a.EstimateTokens() <= compactAt {
		return
	}
	if err := a.Compact(ctx, emit); err != nil && !errors.Is(err, ErrNothingToCompact) {
		emit(Event{Kind: EvNotice, Text: "recipe: compaction failed: " + err.Error()})
	}
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

// RecipeHandover is the prompt given to the model after a recipe failed —
// either a step, or the project's verify gate once every step had
// succeeded: which is which, its output, which steps already succeeded,
// and the recipe's own description as the goal to finish. A front-end
// starts an ordinary turn with it (Run/RunBlocks), so everything
// downstream — approvals, verify, verdicts — behaves exactly as it would
// for anything else the user asked for.
func RecipeHandover(r recipe.Recipe, f RecipeFailure) string {
	var b strings.Builder
	if f.Verify {
		fmt.Fprintf(&b, "The recipe %q ran all %s and every one of them succeeded, but the project's checks failed afterwards.\n", r.Name, textfmt.Plural(len(r.Steps), "step"))
		fmt.Fprintf(&b, "\n%s\n", strings.TrimSpace(f.Output))
		if d := strings.TrimSpace(r.Description); d != "" {
			fmt.Fprintf(&b, "\nWhat this recipe is for: %s\n", d)
		}
		b.WriteString("\nFix it, then finish the job yourself, using whatever tools and judgement it takes.")
		return b.String()
	}
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
