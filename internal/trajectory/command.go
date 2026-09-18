package trajectory

import (
	"strconv"
	"strings"

	"github.com/xmonader/janetcode/internal/session"
)

// Help is what /trajectory accepts, worded once so both front-ends
// answer a wrong argument with the same sentence.
const Help = `/trajectory                 the ledger: turn, step, one line per record
/trajectory all             every record, however long the session
/trajectory <n>             one record in full: arguments, output, timing, tokens
/trajectory turns           one line per turn
/trajectory turn <n>        one turn's records
/trajectory slow [n]        the records that took longest, longest first
/trajectory grep <words>    records matching every word, anywhere in the record
/trajectory raw             the unfolded event log, as it is on disk`

// tail is how many records /trajectory shows by default. An hour-long
// session has hundreds, and a screenful of the most recent ones is what
// the command is nearly always opened for; `all` is one word away.
const tail = 60

// slowDefault is how many rows /trajectory slow ranks when no count is
// given: enough to see the shape, short enough to read.
const slowDefault = 10

// Command answers one /trajectory invocation. args is the command's
// words with the command itself removed, width the terminal's. The
// second return is false when the arguments made no sense, so the
// caller can show it as an error rather than as output.
func Command(events []session.Event, args []string, width int) (string, bool) {
	l := Fold(events)
	if len(args) == 0 {
		return Format(l, Options{Width: width, Last: tail}), true
	}
	switch args[0] {
	case "all":
		return Format(l, Options{Width: width}), true
	case "turns":
		return Turns(l), true
	case "turn":
		if len(args) < 2 {
			return "usage: /trajectory turn <n>", false
		}
		n, err := strconv.Atoi(args[1])
		if err != nil {
			return "/trajectory turn takes a turn number, not " + strconv.Quote(args[1]), false
		}
		return Format(l, Options{Width: width, Turn: n}), true
	case "slow":
		n := slowDefault
		if len(args) > 1 {
			v, err := strconv.Atoi(args[1])
			if err != nil || v < 1 {
				return "/trajectory slow takes a count, not " + strconv.Quote(args[1]), false
			}
			n = v
		}
		return FormatSlowest(l, n, width), true
	case "grep", "search":
		q := strings.Join(args[1:], " ")
		if strings.TrimSpace(q) == "" {
			return "usage: /trajectory grep <words>", false
		}
		return FormatSearch(Search(l, q), q, width), true
	case "help", "?":
		return Help, true
	}
	if n, err := strconv.Atoi(args[0]); err == nil {
		return Inspect(l, n), true
	}
	return "unknown: /trajectory " + strings.Join(args, " ") + "\n\n" + Help, false
}
