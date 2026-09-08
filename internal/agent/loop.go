package agent

import (
	"errors"
	"fmt"
	"strings"
)

// Loop detection: a model that repeats the identical tool call is not
// making progress. The ladder is advisory first (a note appended to the
// result), then terminal: the turn ends with ErrStuck and the diff intact
// so the user can steer.

const (
	// loopNudgeAt identical calls appends a note; loopStopAt ends the turn.
	loopNudgeAt = 3
	loopStopAt  = 5
	// loopFailNudgeAt identical *failing* calls nudge earlier.
	loopFailNudgeAt = 2
	// cycleNudgeAt repetitions of a longer loop (edit a file, run the
	// failing test, edit it again, run it again) append a note;
	// cycleStopAt ends the turn. Only loops with a failing step count:
	// a converging edit-test loop ends with a pass and never gets there.
	cycleNudgeAt = 3
	cycleStopAt  = 6
	maxCycle     = 4
)

// ErrStuck ends a turn that keeps repeating the same tool call.
var ErrStuck = errors.New("agent: stuck repeating the same tool call")

// EvNotice carries a harness note for the UI (a nudge, an integrity
// warning). Text holds the message.
const EvNotice EventKind = "notice"

type loopDetector struct {
	calls map[string]int // tool + canonical input → count this turn
	fails map[string]int
	// seq is the turn's calls as coarse signatures (tool, subject,
	// failed) for cycle detection; reason says why the turn stopped.
	seq    []signature
	reason string
}

// signature is a call reduced to what a loop looks like from outside:
// the tool, what it was about, and whether it failed.
type signature struct {
	key     string
	label   string
	name    string
	subject string
	failed  bool
}

func (d *loopDetector) reset() {
	d.calls, d.fails = map[string]int{}, map[string]int{}
	d.seq, d.reason = nil, ""
}

func newSignature(name string, input []byte, isErr bool) signature {
	subject := ""
	if s := subjectsOf(name, input); len(s) > 0 {
		subject = s[0]
	}
	label := name
	if subject != "" {
		label += " " + quoteTrunc(subject, 40)
	}
	if isErr {
		label += " fails"
	}
	return signature{key: fmt.Sprintf("%s\x00%s\x00%t", name, subject, isErr), label: label, name: name, subject: subject, failed: isErr}
}

// cycle reports the shortest period (2..maxCycle) the turn's recent
// calls repeat with, containing at least one failing step, and how many
// consecutive repetitions end the sequence; 0 when there is none.
func (d *loopDetector) cycle() (period, reps int) {
	n := len(d.seq)
	for k := 2; k <= maxCycle && 2*k <= n; k++ {
		same := func(i, j int) bool { return d.seq[i].key == d.seq[j].key }
		failing, distinct := false, false
		for i := n - k; i < n; i++ {
			failing = failing || d.seq[i].failed
			distinct = distinct || !same(i, n-k)
		}
		if !failing || !distinct {
			continue
		}
		r := 1
		for start := n - 2*k; start >= 0; start -= k {
			ok := true
			for i := 0; i < k; i++ {
				if !same(start+i, start+k+i) {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
			r++
		}
		if r >= 2 {
			return k, r
		}
	}
	return 0, 0
}

func (d *loopDetector) describeCycle(period int) string {
	var parts []string
	for _, s := range d.seq[len(d.seq)-period:] {
		parts = append(parts, s.label)
	}
	return strings.Join(parts, " → ")
}

// record notes a finished call and returns a note for the model, and
// whether the turn should stop.
//
// A successful mutating call changes the world, so the counts of
// identical calls start over: re-running a test or re-reading a file
// after an edit is progress, not repetition (the cycle check covers
// loops that never converge).
func (d *loopDetector) record(name string, input []byte, isErr, mutating bool) (note string, stop bool) {
	if d.calls == nil {
		d.reset()
	}
	key := name + "\x00" + canonical(input)
	d.calls[key]++
	n := d.calls[key]
	switch {
	case isErr:
		d.fails[key]++
	case mutating:
		d.calls, d.fails = map[string]int{}, map[string]int{}
	default:
		d.fails[key] = 0
	}
	d.seq = append(d.seq, newSignature(name, input, isErr))
	if period, reps := d.cycle(); period > 0 {
		switch {
		case reps >= cycleStopAt:
			d.reason = fmt.Sprintf("the model went round the same loop %d times: %s", reps, d.describeCycle(period))
			return "", true
		case reps >= cycleNudgeAt:
			return fmt.Sprintf("\n\n[harness] You have gone round the same loop %d times: %s. It is not converging: read the failure again, try a different fix, or tell the user what is blocking you.", reps, d.describeCycle(period)), false
		}
	}
	switch {
	case n >= loopStopAt:
		d.reason = fmt.Sprintf("the model repeated the same tool call %d times", n)
		return "", true
	case d.fails[key] >= loopFailNudgeAt:
		return fmt.Sprintf("\n\n[harness] This exact %s call has now failed %d times with the same input. Repeating it will not change the result: read the error, change the input or the approach, or tell the user what is blocking you.", name, d.fails[key]), false
	case n >= loopNudgeAt:
		return fmt.Sprintf("\n\n[harness] This is the %s identical %s call this turn. If you already have what you need from it, move on; if not, change the input.", ordinal(n), name), false
	}
	return "", false
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "1st"
	case 2:
		return "2nd"
	case 3:
		return "3rd"
	}
	return fmt.Sprintf("%dth", n)
}
