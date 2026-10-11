package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/xmonader/janetcode/internal/difffuzz"
	"github.com/xmonader/janetcode/internal/session"
)

// Differential fuzzing for refactors (see package difffuzz). When the task
// says the change keeps behaviour the same — a refactor, an optimisation,
// a clean-up, or "without changing behaviour" in so many words — a turn
// that changed Go code is, before it ends, fuzzed against the code as it
// was before the turn: each changed exported function is called with the
// same generated inputs on both sides for a few seconds, and the first
// input they disagree on sends the turn back once with the call, the old
// result and the new one.
//
// It is opt-in (config diff_fuzz, the seconds to fuzz for), because it
// builds and runs code for that long while the model waits and can send
// a turn back, which is a model call. It runs in a temporary copy of the
// module and writes nothing to the workspace.

// difffuzzGateState is one turn's differential fuzz.
type difffuzzGateState struct {
	seen int  // a.mutations when it last ran
	sent bool // the turn has been sent back on a divergence
}

// difffuzzRunner runs a comparison; tests replace it.
var difffuzzRunner = difffuzz.Run

// difffuzzTaskFiles are the task statements read beside the request: a
// headless run's prompt often only says to read them.
var difffuzzTaskFiles = []string{"BRIEF.md", "TASK.md"}

// difffuzzTask is the text the behaviour-preserving test reads: the
// request and the task files at the root of the workspace.
func difffuzzTask(root, request string) string {
	text := request
	for _, name := range difffuzzTaskFiles {
		if data, err := readSmall(filepath.Join(root, name)); err == nil {
			text += "\n" + string(data)
		}
	}
	return text
}

// diffFuzzGate fuzzes the turn's changed Go functions against their
// pre-turn selves when the task says behaviour must not change. It
// reports whether the turn was sent back.
func (a *Agent) diffFuzzGate(ctx context.Context, g *difffuzzGateState, emit func(Event)) bool {
	budget := a.DiffFuzzBudget()
	if budget <= 0 || a.mutations == 0 || a.mutations == g.seen || a.readOnly() || a.DryRunOn() {
		return false
	}
	g.seen = a.mutations
	root := a.displayRoot()
	if root == "" || !difffuzz.BehaviourPreserving(difffuzzTask(root, a.integ.userText)) {
		return false
	}
	var files []difffuzz.File
	for _, f := range a.turnFiles(root) {
		files = append(files, difffuzz.File{Rel: f.rel, Before: f.before, After: f.after, Existed: f.existed, Exists: f.exists})
	}
	if len(files) == 0 {
		return false
	}
	emit(Event{Kind: EvNotice, Text: "the task keeps behaviour the same: fuzzing the changed functions against the code before the turn"})
	rep := difffuzzRunner(ctx, difffuzz.Request{Root: root, Files: files, Budget: budget})
	raw, _ := json.Marshal(rep)
	a.logEvent(session.Event{Type: session.TypeNotice, Turn: a.turnN, Name: "difffuzz", IsError: rep.Diverged != nil, Output: rep.Summary(), Extra: raw})
	emit(Event{Kind: EvNotice, Text: rep.Summary(), IsError: rep.Diverged != nil})
	if rep.Diverged == nil || g.sent || ctx.Err() != nil {
		return false
	}
	g.sent = true
	a.appendHarnessText(difffuzzNote(*rep.Diverged))
	return true
}

func difffuzzNote(d difffuzz.Divergence) string {
	return "[harness] The task says the behaviour must stay the same, but " + d.Func +
		" now gives a different result from the code before your change. Fuzzing both versions with the same input found:\n" +
		"  " + d.Input + "\n  before: " + d.Old + "\n  after:  " + d.New + "\n" +
		"Make the new code agree with the old on this input (and inputs like it), or, if the old result was itself a bug the task asks you to keep, say so plainly."
}

// SetDiffFuzz sets the differential fuzz budget in seconds; zero turns it
// off.
func (a *Agent) SetDiffFuzz(seconds int) {
	a.featMu.Lock()
	a.DiffFuzz = seconds
	a.featMu.Unlock()
}

// DiffFuzzBudget is DiffFuzz as a duration, safe to read while a turn
// runs; zero is off.
func (a *Agent) DiffFuzzBudget() time.Duration {
	a.featMu.RLock()
	defer a.featMu.RUnlock()
	return time.Duration(max(a.DiffFuzz, 0)) * time.Second
}
