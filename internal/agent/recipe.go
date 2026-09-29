package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/xmonader/janetcode/internal/change"
	"strings"

	"github.com/xmonader/janetcode/internal/diff"
	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/perm"
	"github.com/xmonader/janetcode/internal/recipe"
	"github.com/xmonader/janetcode/internal/recipe/run"
	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/textfmt"
	"github.com/xmonader/janetcode/internal/tool"
)

// mcpToolPrefix, NeedsMCP, RecipeFailure, RecipeHandover and the replay
// loop itself live in internal/recipe/run, which has no conversation
// behind it. These keep the names the front-ends already call.

// RecipeFailure is the step a recipe stopped on; see run.Failure.
type RecipeFailure = run.Failure

// NeedsMCP reports whether any step of r calls a tool an MCP server
// provides; see run.NeedsMCP.
func NeedsMCP(r recipe.Recipe) bool { return run.NeedsMCP(r) }

// RecipeHandover is the prompt given to the model after a recipe failed;
// see run.Handover.
func RecipeHandover(r recipe.Recipe, f RecipeFailure) string { return run.Handover(r, f) }

// Execute runs one tool call the way a call the model made runs, so a
// recipe replay passes every gate a turn does (see runNested). It is
// what makes *Agent a run.Dispatcher.
func (a *Agent) Execute(ctx context.Context, id, name string, input json.RawMessage) (string, bool, bool) {
	return a.runNested(ctx, id, name, input)
}

// RunRecipe replays a recipe's steps through exactly the pipeline a model
// turn uses: each step runs through runNested, so authorize, approvals,
// guards, checkpoints, provenance and the event log all see it as they
// would a call the model made. It stops at the first failing step.
//
// It runs as its own logged turn, so /changes, undo and verdicts work on
// it the way they do on any other turn — but there is no model in the
// loop to open one the usual way (RunBlocks calls the provider). Rather
// than reimplement a model turn's bookkeeping a second time, this opens
// and closes the turn with the same beginTurn/endTurn RunBlocks uses, with the
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
	if a.Off["recipe"] {
		return "", nil, errors.New("recipe is off")
	}
	steps, err := recipe.Expand(r, args)
	if err != nil {
		return "", nil, err
	}

	a.beginTurn(ctx, llm.Block{Type: llm.BlockText, Text: run.Label(r.Name, args), Synthetic: true})
	defer a.endTurn(ctx)

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

	logStep := func(ev session.Event) {
		ev.Turn = a.turnN
		a.logEvent(ev)
	}
	text, stepIDs, failed, cerr := run.Steps(ctx, a, a.Tools, r.Name, steps, logStep)
	var b strings.Builder
	b.WriteString(text)
	switch {
	case cerr != nil:
		a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "cancelled"})
		return strings.TrimRight(b.String(), "\n"), nil, cerr
	case failed != nil:
		a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "recipe-failed"})
		return finish(b.String(), failed)
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

	// A clean run is inspectable rather than taken on faith: a compact
	// per-file +/- line count of everything the run's own steps actually
	// changed, so "it finished" and "it changed what it should have"
	// are not the same claim.
	if stat := recipeDiffStat(a.Changes(), stepIDs); stat != "" {
		fmt.Fprintf(&b, "\n%s\n", stat)
	}
	fmt.Fprintf(&b, "\n[recipe %s finished, %s]", r.Name, textfmt.Plural(len(steps), "step"))
	a.logEvent(session.Event{Type: session.TypeTurnEnd, Turn: a.turnN, Reason: "recipe-done"})
	return finish(b.String(), nil)
}

// recipeDiffStat summarises, one line per path in the order first
// touched, the net effect of the changes whose call id is in ids — the
// steps this run of RunRecipe itself made, as opposed to any other change
// recorded earlier in the session.
func recipeDiffStat(changes []Change, ids map[string]bool) string {
	type stat struct{ add, del int }
	var order []string
	stats := map[string]*stat{}
	for _, c := range changes {
		if !ids[c.Call] {
			continue
		}
		s, ok := stats[c.Path]
		if !ok {
			s = &stat{}
			stats[c.Path] = s
			order = append(order, c.Path)
		}
		add, del := c.Stats()
		s.add += add
		s.del += del
	}
	if len(order) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("changed:\n")
	for _, p := range order {
		s := stats[p]
		fmt.Fprintf(&b, "  %s (+%d -%d)\n", p, s.add, s.del)
	}
	return strings.TrimRight(b.String(), "\n")
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
	if a.Settings().Provider == nil {
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

// RecipeSteps is what /recipe save crystallises a turn into. It starts
// from recipe.FromEvents (the turn's successful, replayable tool calls,
// in order) and then converts every step whose effect on disk was decided
// by position rather than by content — a write_file that overwrote a file
// that already existed, or an edit_file call that located itself by
// from_line/to_line/from_anchor/to_anchor — into a content-anchored
// edit_file text-form step (or, for a write_file that created a brand
// new file, the same write_file plus a precondition; see recipe.Expect).
//
// recipe.FromEvents itself only ever sees the event log, which has the
// call and its result but not the file's content before and after —
// exactly what a positional edit needs turned into an anchor. This is the
// one place that has both: the event log (for FromEvents) and this
// session's own change provenance (Change, in provenance.go), matched to
// a step by the tool-call id FromEvents already carries on recipe.Step
// (CallID; never written to the saved file — see its own comment).
//
// A step this cannot safely convert — no recorded Change for its call
// (a no-op write, or provenance that was not captured for some reason),
// or a hunk with nothing to anchor on — is kept exactly as extracted
// rather than dropped or guessed at; RunRecipe and edit_file's own
// uniqueness check are what make even that safe to replay (see
// checkExpect and internal/tool/files.go's EditFile.apply).
func (a *Agent) RecipeSteps(turn int) ([]recipe.Step, error) {
	var events []session.Event
	if a.Log != nil {
		events = a.Log.Events()
	}
	steps, err := recipe.FromEvents(events, turn)
	if err != nil {
		return nil, err
	}
	changes := make(map[string]Change, len(steps))
	for _, c := range a.Changes() {
		changes[c.Call] = c
	}
	out := make([]recipe.Step, 0, len(steps))
	for _, st := range steps {
		out = append(out, a.convertStep(st, changes)...)
	}
	return out, nil
}

// convertStep applies RecipeSteps's conversion to one extracted step,
// returning the one or more steps it should be saved as.
func (a *Agent) convertStep(st recipe.Step, changes map[string]Change) []recipe.Step {
	switch st.Tool {
	case "write_file":
		return a.convertWriteFile(st, changes)
	case "edit_file":
		return a.convertEditFile(st, changes)
	default:
		return []recipe.Step{st}
	}
}

func (a *Agent) convertWriteFile(st recipe.Step, changes map[string]Change) []recipe.Step {
	c, ok := changes[st.CallID]
	if !ok {
		return []recipe.Step{st}
	}
	if !c.Existed {
		// Nothing existed to anchor against; the file's absence (or
		// agreement with what this step would write anyway) is the whole
		// precondition RunRecipe can check for it. See recipe.Expect.
		st.Expect = &recipe.Expect{Absent: true}
		return []recipe.Step{st}
	}
	return a.hunksToEditSteps(st, c)
}

func (a *Agent) convertEditFile(st recipe.Step, changes map[string]Change) []recipe.Step {
	if !editFileUsesLineForm(st.Input) {
		return []recipe.Step{st} // already a text-form edit from the model
	}
	c, ok := changes[st.CallID]
	if !ok {
		return []recipe.Step{st}
	}
	return a.hunksToEditSteps(st, c)
}

// lineFormFields is the set of field names that put an edit_file call into
// its line-range form; see internal/tool/files.go's oneEdit.byLine.
type lineFormFields struct {
	FromLine   json.RawMessage `json:"from_line"`
	ToLine     json.RawMessage `json:"to_line"`
	FromAnchor json.RawMessage `json:"from_anchor"`
	ToAnchor   json.RawMessage `json:"to_anchor"`
}

func (f lineFormFields) byLine() bool {
	return len(f.FromLine) > 0 || len(f.ToLine) > 0 || len(f.FromAnchor) > 0 || len(f.ToAnchor) > 0
}

// editFileUsesLineForm reports whether an edit_file call's input locates
// any of its edits by line or anchor rather than by old_string, at the
// top level or inside its edits list.
func editFileUsesLineForm(input json.RawMessage) bool {
	var in struct {
		lineFormFields
		Edits []lineFormFields `json:"edits"`
	}
	if json.Unmarshal(input, &in) != nil {
		return false
	}
	if in.byLine() {
		return true
	}
	for _, e := range in.Edits {
		if e.byLine() {
			return true
		}
	}
	return false
}

// hunksToEditSteps turns a change's line-level diff into one edit_file
// text-form step per changed region. Each step's old_string is that
// region's own changed lines plus enough surrounding, unchanged context —
// starting at none and growing symmetrically — to be unique in the file
// as it was recorded; a region this cannot even build a non-empty anchor
// for (nothing textual differs — a shape-only rewrite, say) falls back to
// the original step rather than emit something that cannot possibly
// match anything.
//
// This deliberately does not reuse Change.Hunks(): that pads every region
// with makeHunks' fixed three lines of context (and merges regions within
// six lines of each other) for a human reading a diff, which is often far
// more than an anchor needs and, worse, bakes in surrounding text that
// will legitimately differ the next time the same recipe runs — a
// changelog's body text a line below the heading, say. rawChangeRegions
// gives the change's true boundaries with no context at all, so growth
// here adds only as much as uniqueness actually demands.
func (a *Agent) hunksToEditSteps(st recipe.Step, c Change) []recipe.Step {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(st.Input, &in) != nil || in.Path == "" {
		return []recipe.Step{st}
	}
	before := diff.SplitLinesKeepEOL(string(c.Before))
	after := diff.SplitLinesKeepEOL(string(c.After))
	regions := rawChangeRegions(change.DiffLines(before, after))
	if len(regions) == 0 {
		return []recipe.Step{st}
	}
	out := make([]recipe.Step, 0, len(regions))
	// Replay applies the steps in order, so each region is anchored in
	// the file as the steps before it leave it: earlier regions already
	// in their new form, later ones still old. Anchored in the file as
	// recorded, a region whose context had to grow into a neighbour
	// carried the neighbour's old text, and in a repetitive file that
	// matched somewhere else once the neighbour had changed: the replay
	// changed a line the recorded turn never touched.
	cur := before
	for _, h := range regions {
		// Every earlier region is applied, so this one starts at its
		// new-side position in cur, with its old-side length.
		lo := h.NewStart - 1
		if lo < 0 || lo+h.OldCount > len(cur) || lo+h.NewCount > len(after) {
			return []recipe.Step{st}
		}
		next := make([]string, 0, len(cur)-h.OldCount+h.NewCount)
		next = append(next, cur[:lo]...)
		next = append(next, after[lo:lo+h.NewCount]...)
		next = append(next, cur[lo+h.OldCount:]...)
		oldText, newText, ok := anchoredEditText(cur, next, Hunk{OldStart: lo + 1, OldCount: h.OldCount, NewStart: lo + 1, NewCount: h.NewCount})
		if !ok || oldText == newText {
			return []recipe.Step{st}
		}
		cur = next
		input, err := json.Marshal(map[string]string{"path": in.Path, "old_string": oldText, "new_string": newText})
		if err != nil {
			return []recipe.Step{st}
		}
		out = append(out, recipe.Step{Tool: "edit_file", Input: input, Intent: st.Intent})
	}
	return out
}

// rawChangeRegions groups a line-level diff into its changed regions with
// no surrounding context at all: each contiguous run of non-equal edits
// becomes one Hunk (OldStart/OldCount/NewStart/NewCount only — Lines is
// left unset, nothing here needs it), unmerged with its neighbours no
// matter how close. Compare Hunks()/makeHunks in linediff.go, which pads
// every region with a fixed few lines of context for a human reading a
// diff; this is that same walk without the padding, because
// hunksToEditSteps grows its own context outward only as far as
// uniqueness demands.
func rawChangeRegions(edits []change.Edit) []Hunk {
	var out []Hunk
	oldLine, newLine := 1, 1
	i := 0
	for i < len(edits) {
		if edits[i].Op == '=' {
			oldLine++
			newLine++
			i++
			continue
		}
		h := Hunk{OldStart: oldLine, NewStart: newLine}
		j := i
		for j < len(edits) && edits[j].Op != '=' {
			if edits[j].Op != '+' {
				h.OldCount++
			}
			if edits[j].Op != '-' {
				h.NewCount++
			}
			j++
		}
		out = append(out, h)
		oldLine += h.OldCount
		newLine += h.NewCount
		i = j
	}
	return out
}

// maxAnchorGrowth bounds how far a hunk's context is grown looking for a
// unique anchor, so a huge file with a genuinely repeated block does not
// walk the growth loop across the whole file. Recipes are recorded from
// small, deliberate edits; a real hunk needs a handful of lines of context
// at most.
const maxAnchorGrowth = 200

// anchoredEditText turns one line-diff hunk into the old/new text an
// edit_file text-form step would take. before/after are the file's lines
// (with their line endings) as recorded; h names where in each the hunk's
// changed lines sit. The lines just outside the hunk are unchanged by
// construction, so growing the window by one line on each side — using
// the same source line for both old and new — cannot itself introduce a
// difference; only which lines bound the replacement changes.
//
// The result is not guaranteed unique: growth stops at the file's edges
// or after maxAnchorGrowth lines, whichever comes first. Ambiguity beyond
// that is left for edit_file's own match count to catch at replay time
// (see internal/tool/files.go's oneEdit.apply): it refuses rather than
// guessing among several matches, which is the same "fail rather than
// corrupt" rule this whole conversion exists to uphold.
func anchoredEditText(before, after []string, h Hunk) (oldText, newText string, ok bool) {
	oldLo, oldHi := h.OldStart-1, h.OldStart-1+h.OldCount
	newLo, newHi := h.NewStart-1, h.NewStart-1+h.NewCount
	if oldLo < 0 || oldHi > len(before) || newLo < 0 || newHi > len(after) {
		return "", "", false
	}
	whole := strings.Join(before, "")
	// Overlapping occurrences count: strings.Count does not see them, so
	// "k\nx\nk\n" was unique in "k\nx\nk\nx\nk\n", and edit_file, which
	// takes the first, replaced the wrong x on replay.
	unique := func() bool {
		needle := strings.Join(before[oldLo:oldHi], "")
		if needle == "" {
			return false
		}
		i := strings.Index(whole, needle)
		return i >= 0 && !strings.Contains(whole[i+1:], needle)
	}
	for i := 0; !unique() && i < maxAnchorGrowth; i++ {
		grew := false
		if oldLo > 0 && newLo > 0 {
			oldLo--
			newLo--
			grew = true
		}
		if oldHi < len(before) && newHi < len(after) {
			oldHi++
			newHi++
			grew = true
		}
		if !grew {
			break
		}
	}
	oldText = strings.Join(before[oldLo:oldHi], "")
	newText = strings.Join(after[newLo:newHi], "")
	return oldText, newText, oldText != ""
}

// StepsNeedingApproval reports, for a resolved (post-recipe.Expand) list
// of steps, which ones this agent's current permission rules and auto
// modes cannot clear on their own — the steps an interactive approver
// would be asked about. A headless caller whose approver cannot ask (no
// terminal on stdin, see cmd/janetcode's runRecipeFlag) uses this to fail
// before starting anything rather than after every one of them is
// auto-denied and the run hands over to a model whose own calls are then
// denied the same way.
//
// A step a rule denies outright is not included: authorize refuses it
// immediately, without ever reaching the approver, so a missing terminal
// changes nothing about how it fails. Hooks are not consulted either —
// running one here for a call that will not actually happen would be a
// side effect this is supposed to have none of — so a PreTool hook that
// would in fact allow a call is not accounted for and this can occasionally
// over-report; that is the safe direction to be wrong in.
func (a *Agent) StepsNeedingApproval(steps []recipe.Step) []string {
	s := a.Settings()
	var need []string
	for i, st := range steps {
		t, ok := a.Tools.Get(st.Tool)
		if !ok || !tool.MutatingCall(t, st.Input) {
			continue
		}
		mustAsk := false
		if g, ok := t.(tool.Guard); ok {
			if ask, _ := g.Guard(st.Input); ask {
				mustAsk = true
			}
		}
		subject := perm.SubjectIn(st.Tool, st.Input, a.displayRoot())
		verdict, _, hard := a.decideCall(a.permCall(st.Tool, st.Input, subject))
		if hard != "" {
			continue // refused outright by authorize; no prompt involved
		}
		if !a.Off["protected"] && a.Protected.Reason(a.permCall(st.Tool, st.Input, subject)) != "" {
			mustAsk = true // a protected path asks under every mode
		}
		if !mustAsk {
			switch {
			case verdict == perm.Denied:
				continue // authorize refuses this on its own; no prompt involved
			case s.AutoApprove:
				continue
			case verdict == perm.Allowed:
				continue
			case (s.AutoEdits || s.AutoMode) && editTool(st.Tool):
				continue
			case s.AutoMode && a.autoAllows(llm.Block{Name: st.Tool, Input: st.Input}):
				continue
			}
		}
		need = append(need, fmt.Sprintf("step %d: %s %s", i+1, st.Tool, textfmt.Clip(textfmt.OneLine(string(st.Input)), 100)))
	}
	return need
}
