package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// AskWaitForever is the wait policy that waits for the user however long
// they take: an interactive session where somebody is at the keyboard.
const AskWaitForever time.Duration = -1

// DefaultAskWaitUnattended is how long a question waits in a turn nobody
// is necessarily watching — a --bg run, a goal round, a /loop firing —
// before the turn goes on with the model's recommended choice. Long
// enough for a notification to be read and answered from a phone, short
// enough that an unattended run does not stall on it.
const DefaultAskWaitUnattended = 2 * time.Minute

// askWait is how long the ask tool waits for an answer before going on
// with the recommendation (see AskTool.Run). Interactive turns and
// unattended ones have separate budgets; a front-end marks a turn it
// started on its own (MarkUnattendedTurn) so a goal round in the terminal
// is held to the unattended one even though somebody might be there.
type askWait struct {
	mu          sync.Mutex
	interactive time.Duration // AskWaitForever, 0 (do not wait), or a bound
	unattended  time.Duration
	set         bool // false: interactive waits forever, unattended uses the default
	turnAlone   bool // this turn was started by the harness, not typed
}

// SetAskWait sets the two waits. AskWaitForever waits as long as it takes,
// 0 goes on at once with the recommendation, and anything else is a bound.
func (a *Agent) SetAskWait(interactive, unattended time.Duration) {
	a.askWait.mu.Lock()
	a.askWait.interactive, a.askWait.unattended, a.askWait.set = interactive, unattended, true
	a.askWait.mu.Unlock()
}

// MarkUnattendedTurn says the next turn is one the harness started on its
// own — a goal round, a /loop firing — so a question in it waits only as
// long as an unattended one. It holds until that turn ends.
func (a *Agent) MarkUnattendedTurn() {
	a.askWait.mu.Lock()
	a.askWait.turnAlone = true
	a.askWait.mu.Unlock()
}

func (a *Agent) endAskTurn() {
	a.askWait.mu.Lock()
	a.askWait.turnAlone = false
	a.askWait.mu.Unlock()
}

// AskWaitFor is how long a question asked now waits, and whether the turn
// counts as unattended. A session with no answerer at all is decided in
// AskTool.Run, not here.
func (a *Agent) AskWaitFor() (time.Duration, bool) {
	a.askWait.mu.Lock()
	defer a.askWait.mu.Unlock()
	interactive, unattended := AskWaitForever, DefaultAskWaitUnattended
	if a.askWait.set {
		interactive, unattended = a.askWait.interactive, a.askWait.unattended
	}
	if a.askWait.turnAlone || a.Unattended {
		return unattended, true
	}
	return interactive, false
}

// AskTimeoutNotifier is an Asker that wants to know when a question it is
// showing stopped holding the turn: the wait ran out and the turn went on
// with the recommendation. The question stays open — an answer given now
// still reaches the model, as a late answer — so a front-end can say so
// instead of looking like it is still blocking.
type AskTimeoutNotifier interface {
	AskTimedOut(qs []Question, proceeded []Answer)
}

// recommended is the answers the model said it would pick, or ok false
// when any question has none: the turn can only go on without the user
// when every question has a default the model chose itself.
func recommended(qs []Question) ([]Answer, bool) {
	out := make([]Answer, len(qs))
	for i, q := range qs {
		if q.Recommended == "" {
			return nil, false
		}
		out[i] = Answer{Label: q.Recommended}
	}
	return out, true
}

// formatProceeded is what the model reads when it goes on without an
// answer: which choice it is taking, that the choice was its own, and
// whether the user can still answer.
func formatProceeded(qs []Question, why string, late bool) string {
	var b strings.Builder
	b.WriteString(why)
	b.WriteString(" Proceeding with your recommended choice")
	if len(qs) > 1 {
		b.WriteString("s")
	}
	b.WriteString(" — the user did not choose ")
	if len(qs) > 1 {
		b.WriteString("them")
	} else {
		b.WriteString("it")
	}
	b.WriteString(":\n")
	for _, q := range qs {
		fmt.Fprintf(&b, "Q: %s\nA: %s (recommended)\n", q.Prompt, q.Recommended)
	}
	if late {
		b.WriteString("The question is still open: if the user answers later, the answer reaches you as a message. Prefer choices that are cheap to change until then, and say in your reply which choice you assumed.")
	} else {
		b.WriteString("Say in your reply which choice you assumed, so the user can correct it.")
	}
	return b.String()
}

// answered reports whether ans carries a real choice, not a question the
// user dismissed (esc answers "skipped") or left empty.
func answered(ans []Answer) bool {
	for _, a := range ans {
		l := strings.TrimSpace(a.Label)
		if l != "" && !strings.EqualFold(l, "skipped") {
			return true
		}
	}
	return false
}

// deliverLateAnswer hands the model an answer that came after its turn
// went on with the recommendation. Steer covers both cases the user
// cares about: a turn still running reads it before its next model call,
// and one that has ended takes it with the next message.
func (a *Agent) deliverLateAnswer(qs []Question, ans []Answer, after time.Duration) {
	if !answered(ans) {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Late answer to the question you asked (after %s you went on with your recommended choice):\n", after.Round(time.Second))
	b.WriteString(FormatAnswers(qs, ans))
	same := true
	for i, q := range qs {
		if i >= len(ans) || !strings.EqualFold(strings.TrimSpace(ans[i].Label), q.Recommended) {
			same = false
		}
	}
	if same {
		b.WriteString("\nThat is the choice you assumed; carry on.")
	} else {
		b.WriteString("\nIt differs from what you assumed: change course to match it, undoing what the assumption got wrong.")
	}
	a.Steer(b.String())
	if emit := a.emitter(); emit != nil {
		emit(Event{Kind: EvNotice, Text: "late answer sent to the model: " + strings.ReplaceAll(FormatAnswers(qs, ans), "\n", " · ")})
	}
}

// askBounded asks with a deadline. It returns the answers, or proceeded
// true with the recommendations when the wait ran out first. In that case
// the question stays with the asker on a context of its own, and an
// answer that arrives later is delivered as a late answer.
func (a *Agent) askBounded(ctx context.Context, asker Asker, qs []Question, wait time.Duration) ([]Answer, bool, error) {
	rec, _ := recommended(qs)
	type result struct {
		ans []Answer
		err error
	}
	// Detached from the tool call's context: past the deadline the turn
	// goes on and that context ends with it, but the question is still
	// on screen and an answer is still wanted.
	askCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	res := make(chan result, 1)
	go func() {
		ans, err := asker.Ask(askCtx, qs)
		res <- result{ans, err}
	}()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case r := <-res:
		cancel()
		return r.ans, false, r.err
	case <-ctx.Done():
		cancel()
		return nil, false, ctx.Err()
	case <-timer.C:
	}
	if n, ok := asker.(AskTimeoutNotifier); ok {
		n.AskTimedOut(qs, rec)
	}
	go func() {
		defer cancel()
		r := <-res
		if r.err == nil {
			a.deliverLateAnswer(qs, r.ans, wait)
		}
	}()
	return rec, true, nil
}
