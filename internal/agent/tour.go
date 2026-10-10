package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/tour"
)

// The review tour and the comprehension-debt meter (see package tour):
// the agent's side is the provenance record they read, and the ledger of
// which changes a person has actually had in front of them. Both are
// deterministic and cost no model call; "tour" in Off turns them off.

// reachBudget bounds the whole of a tour's reach lookups: one analysis
// per file with a behaviour hunk, each already bounded by impact.Budget,
// could otherwise add up to many seconds on a turn that touched twenty
// files.
const reachBudget = 2 * time.Second

// reviewLedger is which changes were seen whole: by call, for an edit
// whose diff was on screen in full when it was approved (marked before
// the change exists), and by change number, for one shown afterwards by
// /tour or /changes <n>. Not persisted: a resumed session starts with
// every earlier change unread, which overstates the debt rather than
// hiding any.
type reviewLedger struct {
	mu    sync.Mutex
	calls map[string]bool
	seqs  map[int]bool
}

func (r *reviewLedger) markCall(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.calls == nil {
		r.calls = map[string]bool{}
	}
	r.calls[id] = true
}

func (r *reviewLedger) markSeqs(seqs []int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.seqs == nil {
		r.seqs = map[int]bool{}
	}
	for _, s := range seqs {
		r.seqs[s] = true
	}
}

func (r *reviewLedger) read(c Change) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return c.Call != "" && r.calls[c.Call] || r.seqs[c.Seq]
}

// MarkReviewed records that the person saw call's whole diff before it
// ran: a front-end calls it when an edit is approved at a prompt that
// showed the diff in full, or expanded to it. The changes the call then
// makes are not comprehension debt.
func (a *Agent) MarkReviewed(callID string) {
	if callID != "" {
		a.review.markCall(callID)
	}
}

// PreviewShown says the call carries a preview of what it will write for
// a prompt to show, rather than none or the note that one could not be
// made (see exec.go). Only approving such a call can count as having
// read its lines.
func (c ToolCall) PreviewShown() bool {
	return c.Preview != "" && !strings.HasPrefix(c.Preview, "preview unavailable")
}

// MarkChangeReviewed records that change seq was shown whole, by
// /changes <n>.
func (a *Agent) MarkChangeReviewed(seq int) { a.review.markSeqs([]int{seq}) }

// tourChanges are the changes of one turn the tour reads: not the undos
// (the person's own act) and not what was undone since. turn <= 0 is
// the latest turn that changed anything; the turn read is returned.
func (a *Agent) tourChanges(turn int) ([]Change, int) {
	all := a.Changes()
	if turn <= 0 {
		for _, c := range all {
			if c.Tool != "undo" && c.Turn > turn {
				turn = c.Turn
			}
		}
	}
	var out []Change
	for _, c := range all {
		if c.Turn == turn && c.Tool != "undo" && !c.Undone {
			out = append(out, c)
		}
	}
	return out, turn
}

// TourBrief is the latest changing turn's tour in one line, for the end
// of a turn: "" when the tour is off or nothing was changed. It asks
// nothing of the repository, so it is cheap enough for every turn.
func (a *Agent) TourBrief() string {
	if a.Off["tour"] {
		return ""
	}
	cs, _ := a.tourChanges(0)
	return tour.Build(cs, nil).Brief()
}

// TourText is /tour: "[turn] [all]". It renders that turn's hunks (the
// latest changing turn by default) riskiest first, with the mechanical
// ones collapsed unless all is given, and marks every change in it
// reviewed, since every hunk that is not mechanical was shown whole.
// Paths are shown relative to root, as /changes shows them.
func (a *Agent) TourText(ctx context.Context, arg, root string) string {
	if a.Off["tour"] {
		return "the review tour is off (remove tour from JANETCODE_OFF / --off)"
	}
	turn, all := 0, false
	for _, f := range strings.Fields(arg) {
		if f == "all" {
			all = true
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(f, "turn"))
		if err != nil || n <= 0 {
			return "usage: /tour [turn] [all]"
		}
		turn = n
	}
	cs, turn := a.tourChanges(turn)
	if len(cs) == 0 {
		if turn > 0 {
			return fmt.Sprintf("turn %d changed no files", turn)
		}
		return "no file changes in this session yet"
	}
	ctx, cancel := context.WithTimeout(ctx, reachBudget)
	defer cancel()
	t := tour.Build(cs, a.reach(ctx))
	a.review.markSeqs(t.Seqs)
	head := fmt.Sprintf("review tour of turn %d · %s", turn, t.Brief())
	head = strings.TrimSuffix(head, " · /tour")
	body := t.Format(root, all)
	if line := a.DebtLine(root); line != "" {
		body += "\n\n" + line
	}
	return head + "\n\n" + body
}

// reach adapts the Reach hook to tour.Reach under ctx, which carries
// the whole tour's budget: once it has run out, the files left rank by
// class and size alone. Nil when there is no hook.
func (a *Agent) reach(ctx context.Context) tour.Reach {
	if a.Reach == nil {
		return nil
	}
	return func(path, before, after string) int {
		if ctx.Err() != nil {
			return 0
		}
		return a.Reach(ctx, path, before, after)
	}
}

// DebtLine is the comprehension-debt meter in one line: how many changed
// lines this session were approved without being seen whole, by package.
// A package is a changed file's directory relative to root. "" when
// there are none, or the tour is off.
func (a *Agent) DebtLine(root string) string {
	if a.Off["tour"] {
		return ""
	}
	return tour.DebtLine(tour.Meter(a.Changes(), a.review.read, root), 3)
}
