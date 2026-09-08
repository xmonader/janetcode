package agent

import (
	"errors"
	"fmt"
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
)

// ErrStuck ends a turn that keeps repeating the same tool call.
var ErrStuck = errors.New("agent: stuck repeating the same tool call")

// EvNotice carries a harness note for the UI (a nudge, an integrity
// warning). Text holds the message.
const EvNotice EventKind = "notice"

type loopDetector struct {
	calls map[string]int // tool + canonical input → count this turn
	fails map[string]int
}

func (d *loopDetector) reset() {
	d.calls, d.fails = map[string]int{}, map[string]int{}
}

// record notes a finished call and returns a note for the model, and
// whether the turn should stop.
func (d *loopDetector) record(name string, input []byte, isErr bool) (note string, stop bool) {
	if d.calls == nil {
		d.reset()
	}
	key := name + "\x00" + canonical(input)
	d.calls[key]++
	n := d.calls[key]
	if isErr {
		d.fails[key]++
	} else {
		d.fails[key] = 0
	}
	switch {
	case n >= loopStopAt:
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
