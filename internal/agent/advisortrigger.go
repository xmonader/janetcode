package agent

import (
	"context"
	"crypto/sha256"
	"regexp"
	"strings"
	"sync"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
)

// The triggered advisor: the forced advisor of advisorforce.go, bought
// only when the harness sees the executor stuck. "always" has to be
// measured first; this mode is the cheaper policy to try if it pays, and
// it is experimental and unmeasured until then.
//
// Every signal is one the harness already computes. A failed check is
// runFailed (the verdict's own reading of a bash result, so a command
// that never started or went to the background is not a failure); a
// check is what the trajectory calls a test command; an edit that comes
// back is read off the checkpoint log's before/after contents. Nothing
// here parses test output beyond comparing two failures with each other.

// Trigger names, as recorded in Stats.AdvisorTriggers and the notice.
const (
	triggerRepeatFail = "repeated failure"
	triggerGreenToRed = "green to red"
	triggerRevert     = "edit reverted"
)

// cmdRun is the last result of one check command this turn.
type cmdRun struct {
	failed bool
	sig    string
}

// advisorTriggers is the turn's watch. Nothing is recorded unless the
// mode is on, so the other modes pay nothing for it.
type advisorTriggers struct {
	mu      sync.Mutex
	last    map[string]cmdRun
	passed  map[string]bool
	fired   map[string]bool
	pending []string
}

func (t *advisorTriggers) reset() {
	t.mu.Lock()
	t.last, t.passed, t.fired, t.pending = map[string]cmdRun{}, map[string]bool{}, map[string]bool{}, nil
	t.mu.Unlock()
}

var (
	digitsRe = regexp.MustCompile(`[0-9]+`)
	spacesRe = regexp.MustCompile(`\s+`)
)

// failureSig reduces a failing output to what makes it the same failure
// again: digits go (durations, addresses, counts) and so does spacing,
// and only the tail is kept, where a check prints its verdict.
func failureSig(out string) string {
	out = spacesRe.ReplaceAllString(digitsRe.ReplaceAllString(out, "#"), " ")
	if len(out) > 2000 {
		out = out[len(out)-2000:]
	}
	sum := sha256.Sum256([]byte(out))
	return string(sum[:])
}

// observe notes one finished tool call.
func (t *advisorTriggers) observe(on bool, name string, input []byte, out string, isErr bool) {
	if !on || name != "bash" {
		return
	}
	cmd := shell.CommandOf(string(input))
	if !testCommandRe.MatchString(cmd) {
		return
	}
	e := session.Event{Name: name, Arguments: string(input), Output: out, IsError: isErr}
	if neverRan(e) || unfinished(e) {
		return
	}
	key := strings.Join(strings.Fields(cmd), " ")
	failed := runFailed(e)
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last, t.passed, t.fired = map[string]cmdRun{}, map[string]bool{}, map[string]bool{}
	}
	prev, seen := t.last[key]
	run := cmdRun{failed: failed}
	if failed {
		run.sig = failureSig(out)
		switch {
		case seen && prev.failed && prev.sig == run.sig:
			t.pending = append(t.pending, triggerRepeatFail)
		case t.passed[key] && !(seen && prev.failed):
			t.pending = append(t.pending, triggerGreenToRed)
		}
	} else {
		t.passed[key] = true
	}
	t.last[key] = run
}

// revertedPath names a file the turn's edits returned to an earlier
// content, or "". The checkpoint log holds every change's before and
// after, so the file's states are the first before and each after; the
// latest equalling any state older than the one just before it is a
// revert, which an edit of a file back and forth is not until it has
// actually come back.
func revertedPath(changes []Change) string {
	states := map[string][]string{}
	var order []string
	for _, c := range changes {
		if c.Undone || c.Attempt != "" {
			continue
		}
		s := states[c.Path]
		if s == nil {
			order = append(order, c.Path)
			if c.Existed {
				s = append(s, string(c.Before))
			} else {
				s = append(s, "\x00absent")
			}
		}
		if c.Gone {
			s = append(s, "\x00absent")
		} else {
			s = append(s, string(c.After))
		}
		states[c.Path] = s
	}
	for _, p := range order {
		s := states[p]
		last := s[len(s)-1]
		for _, old := range s[:max(len(s)-2, 0)] {
			if old == last {
				return p
			}
		}
	}
	return ""
}

// advisorTriggeredEnabled reports whether the mode is triggered and not
// switched off.
func (a *Agent) advisorTriggeredEnabled() bool {
	if a.Off["advisor"] {
		return false
	}
	mode, _ := a.advisorForcedSettings()
	return mode == AdvisorTriggered
}

// adviseOnTriggers buys consults for events seen since the last step,
// within the turn's cap. Each trigger fires once per turn: the second
// time the same thing happens, the first consult's advice is already in
// the context and a second would only repeat it.
func (a *Agent) adviseOnTriggers(ctx context.Context, emit func(Event)) {
	if !a.advisorTriggeredEnabled() {
		return
	}
	t := &a.trig
	t.mu.Lock()
	pending := t.pending
	t.pending = nil
	t.mu.Unlock()
	revert := ""
	if !a.triggerFired(triggerRevert) {
		revert = a.revertedThisTurn()
	}
	if revert != "" {
		pending = append(pending, triggerRevert)
	}
	for _, why := range pending {
		if !t.claim(why) {
			continue
		}
		if a.consult(ctx, why, triggerQuestion(why, revert), emit) {
			a.stats.advisorTrigger(why)
		}
	}
}

func (a *Agent) triggerFired(why string) bool {
	a.trig.mu.Lock()
	defer a.trig.mu.Unlock()
	return a.trig.fired[why]
}

// claim marks a trigger spent, reporting whether it was still unspent.
// It is spent even when the consult then fails or the cap is gone: a
// trigger that retried every step would make the cap the only limit.
func (t *advisorTriggers) claim(why string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired == nil {
		t.fired = map[string]bool{}
	}
	if t.fired[why] {
		return false
	}
	t.fired[why] = true
	return true
}

// revertedThisTurn reads the checkpoint log for the current turn.
func (a *Agent) revertedThisTurn() string {
	var mine []Change
	for _, c := range a.Changes() {
		if c.Turn == a.turnN {
			mine = append(mine, c)
		}
	}
	return revertedPath(mine)
}

func triggerQuestion(why, path string) string {
	switch why {
	case triggerRepeatFail:
		return "The executor ran the same check twice in a row and it failed the same way both times. Look at the transcript and say what it is misreading and what to change, concretely."
	case triggerGreenToRed:
		return "A check the executor had passed earlier in this turn now fails. Look at the transcript and say which change most likely broke it and how to fix that without losing the work."
	}
	return "The executor put " + path + " back to a content it had earlier in this turn, so an approach was abandoned. Look at the transcript and say why the approach failed and what to try instead."
}
