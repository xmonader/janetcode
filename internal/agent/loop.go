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
	// alternateNudgeAt repetitions of a two-call A-B-A-B alternation say
	// so, alternateStopAt ends the turn. Unlike cycle these need no
	// failing step: two calls traded back and forth are not progress
	// even when both succeed.
	alternateNudgeAt = 5
	alternateStopAt  = 8
	// thrashWindow recent calls holding thrashNudgeAt reads of one file
	// say so, thrashStopAt end the turn. A file read eight times in
	// fifteen calls is being re-read instead of remembered.
	thrashWindow  = 15
	thrashNudgeAt = 5
	thrashStopAt  = 8
	// maxTurnCalls is the most tool calls one turn may make. Past this a
	// turn is not working, it is running.
	maxTurnCalls = 100
	// sameTextNudgeAt identical assistant messages in a turn say so,
	// sameTextStopAt end it. Shorter texts are acknowledgements, not an
	// answer being repeated.
	sameTextNudgeAt = 2
	sameTextStopAt  = 3
	minRepeatedText = 40
)

// ErrStuck ends a turn that keeps repeating the same tool call.
var ErrStuck = errors.New("stuck repeating the same tool call")

// EvNotice carries a harness note for the UI (a nudge, an integrity
// warning). Text holds the message.
const EvNotice EventKind = "notice"

type loopDetector struct {
	calls map[string]int // tool + canonical input → count this turn
	fails map[string]int
	// texts counts identical assistant messages this turn.
	texts map[string]int
	// total is every tool call this turn, however varied.
	total int
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
	// changed marks a mutating call that succeeded: it moved the world
	// on, so repeating around it is work rather than a loop.
	changed bool
}

func (d *loopDetector) reset() {
	d.calls, d.fails, d.texts = map[string]int{}, map[string]int{}, map[string]int{}
	d.seq, d.reason, d.total = nil, "", 0
}

func newSignature(name string, input []byte, isErr, changed bool) signature {
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
	return signature{key: fmt.Sprintf("%s\x00%s\x00%t", name, subject, isErr), label: label, name: name, subject: subject, failed: isErr, changed: changed}
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

// alternation reports how many times the turn's last calls have traded
// between two different signatures — A B A B — and what they are. A
// two-call cycle is the commonest shape of a model that is not getting
// anywhere, and unlike cycle it counts even when nothing fails: writing
// a file and reading it back, twice, is work; eight times is not.
func (d *loopDetector) alternation() (reps int, a, b string) {
	n := len(d.seq)
	if n < 4 {
		return 0, "", ""
	}
	x, y := d.seq[n-2], d.seq[n-1]
	if x.key == y.key || x.changed || y.changed {
		// A pair with a successful edit in it is an edit-and-check
		// rhythm, which is how the work is meant to go.
		return 0, "", ""
	}
	reps = 1
	for i := n - 4; i >= 0; i -= 2 {
		if d.seq[i].key != x.key || d.seq[i+1].key != y.key {
			break
		}
		reps++
	}
	return reps, x.label, y.label
}

// thrash reports the file read most often in the recent calls and how
// many times, when that is more than once. A model re-reading one file
// every few calls has lost what it already read, and reading it again
// will not help.
func (d *loopDetector) thrash() (path string, n int) {
	seq := d.seq
	if len(seq) > thrashWindow {
		seq = seq[len(seq)-thrashWindow:]
	}
	counts := map[string]int{}
	for _, s := range seq {
		if s.name == "read_file" && s.subject != "" {
			counts[s.subject]++
		}
	}
	for p, c := range counts {
		if c > n {
			path, n = p, c
		}
	}
	return path, n
}

// recordText notes an assistant message and calls out a model saying the
// same thing over and over — the shape a small model falls into when it
// has stopped reading its own tool results.
func (d *loopDetector) recordText(text string) (note string, stop bool) {
	if d.texts == nil {
		d.reset()
	}
	text = strings.TrimSpace(text)
	if len(text) < minRepeatedText {
		return "", false
	}
	d.texts[text]++
	switch n := d.texts[text]; {
	case n >= sameTextStopAt:
		d.reason = fmt.Sprintf("the model wrote the same message %d times", n)
		return "", true
	case n >= sameTextNudgeAt:
		return fmt.Sprintf("\n\n[harness] You have written this same message %d times this turn. Repeating it is not making progress: do the next thing, or say what is blocking you.", n), false
	}
	return "", false
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
	d.seq = append(d.seq, newSignature(name, input, isErr, mutating && !isErr))
	d.total++
	if d.total >= maxTurnCalls {
		d.reason = fmt.Sprintf("the turn made %d tool calls without finishing", d.total)
		return "", true
	}
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
	if reps, a, b := d.alternation(); reps >= alternateNudgeAt {
		if reps >= alternateStopAt {
			d.reason = fmt.Sprintf("the model alternated between two calls %d times: %s ⇄ %s", reps, a, b)
			return "", true
		}
		return fmt.Sprintf("\n\n[harness] You have gone back and forth between %s and %s %d times. Alternating between two calls is not progress: do something different, or tell the user what is blocking you.", a, b, reps), false
	}
	if path, reads := d.thrash(); reads >= thrashNudgeAt {
		if reads >= thrashStopAt {
			d.reason = fmt.Sprintf("the model read %s %d times in %d calls", path, reads, min(len(d.seq), thrashWindow))
			return "", true
		}
		return fmt.Sprintf("\n\n[harness] You have read %s %d times in the last %d calls. You already have it: work from what it said, or read a different part of it with offset and limit.", path, reads, min(len(d.seq), thrashWindow)), false
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
