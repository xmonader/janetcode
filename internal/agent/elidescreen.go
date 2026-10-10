package agent

import (
	"strings"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/shell"
	"github.com/xmonader/janetcode/internal/tool"
)

// ElideScreen is the offline pre-screen for round-trip elision (elide.go),
// counted from a recorded run with no model call: how often the reply
// after an edit-completing one is the check the harness would have run.
// Its kill line was set before the feature was built: below a 60% hit
// rate the harness would mostly be running checks nobody wanted next.
type ElideScreen struct {
	// Replies counts model replies that made tool calls.
	Replies int
	// Batched counts replies that edited and then checked in the same
	// reply, which elision leaves alone.
	Batched int
	// Triggers counts edit-completing replies: the last change the reply
	// made was a file edit that landed, and nothing checked after it.
	Triggers int
	// HitAlone and HitMixed are triggers whose next reply checks before
	// it changes anything: with nothing else (the whole reply is the
	// round trip elision removes) or beside other calls.
	HitAlone, HitMixed int
	// SameCommand counts hits whose check is, word for word, the last
	// check the model ran before: what the learned-command fallback would
	// have run, and so a repeat the cache would have answered.
	SameCommand int
	// MissEdit counts triggers whose next reply changed the tree first
	// (the edit set was not complete); MissEnd those after which the turn
	// ended; MissOther the rest.
	MissEdit, MissEnd, MissOther int
	// HitBytes is the recorded output of the hit checks; ElidedBytes the
	// same results as elision's note would have carried them.
	HitBytes, ElidedBytes int
}

// Add folds another run's screen into this one.
func (s *ElideScreen) Add(o ElideScreen) {
	s.Replies += o.Replies
	s.Batched += o.Batched
	s.Triggers += o.Triggers
	s.HitAlone += o.HitAlone
	s.HitMixed += o.HitMixed
	s.SameCommand += o.SameCommand
	s.MissEdit += o.MissEdit
	s.MissEnd += o.MissEnd
	s.MissOther += o.MissOther
	s.HitBytes += o.HitBytes
	s.ElidedBytes += o.ElidedBytes
}

// Hits is every trigger whose next call was the check.
func (s ElideScreen) Hits() int { return s.HitAlone + s.HitMixed }

// screenCall is one recorded tool call with what elision needs of it.
type screenCall struct {
	name, args, out string
	failed          bool
}

// ScreenElision replays one recorded run's replies and counts what
// elision would have done after each of them.
func ScreenElision(events []session.Event) ElideScreen {
	type reply struct {
		turn  int
		calls []*screenCall
	}
	var replies []*reply
	byID := map[string]*screenCall{}
	for _, e := range events {
		switch e.Type {
		case session.TypeAssistant:
			replies = append(replies, &reply{turn: e.Turn})
		case session.TypeToolCall:
			// A derived call (then_run, an elided check) is the harness's,
			// not a reply's own; then_run is read off the edit's input.
			if len(replies) == 0 || strings.Contains(e.CallID, "~") {
				continue
			}
			c := &screenCall{name: e.Name, args: e.Arguments}
			r := replies[len(replies)-1]
			r.calls = append(r.calls, c)
			byID[e.CallID] = c
		case session.TypeToolResult:
			if c := byID[e.CallID]; c != nil && e.CallID != "" {
				c.out, c.failed = e.Output, e.IsError || e.Denied
			}
		}
	}
	var s ElideScreen
	lastCheck := ""
	for i, r := range replies {
		if len(r.calls) == 0 {
			continue
		}
		s.Replies++
		lastChange, checked, edited := -1, false, false
		for j, c := range r.calls {
			isCheck := screenCheck(c) != ""
			switch ClassifyCall(c.name, c.args) {
			case PhaseExplore:
				continue
			case PhaseEdit:
				if editTools[c.name] {
					edited = edited || !c.failed
				}
			}
			lastChange = j
			checked = isCheck
			if isCheck && edited {
				s.Batched++
				edited = false
			}
		}
		for _, c := range r.calls {
			if cmd := screenCheck(c); cmd != "" {
				lastCheck = cmd
			}
		}
		if lastChange < 0 || checked {
			continue
		}
		if c := r.calls[lastChange]; !editTools[c.name] || c.failed {
			continue
		}
		s.Triggers++
		if i+1 >= len(replies) || replies[i+1].turn != r.turn || len(replies[i+1].calls) == 0 {
			s.MissEnd++
			continue
		}
		next := replies[i+1].calls
		first := -1
		for j, c := range next {
			if ClassifyCall(c.name, c.args) != PhaseExplore {
				first = j
				break
			}
		}
		if first < 0 {
			s.MissOther++
			continue
		}
		cmd := screenCheck(next[first])
		if cmd == "" {
			if ClassifyCall(next[first].name, next[first].args) == PhaseEdit {
				s.MissEdit++
			} else {
				s.MissOther++
			}
			continue
		}
		alone := true
		for _, c := range next {
			if screenCheck(c) == "" {
				alone = false
			}
		}
		if alone {
			s.HitAlone++
		} else {
			s.HitMixed++
		}
		if lastCheck != "" && lastCheck == strings.TrimSpace(cmd) {
			s.SameCommand++
		}
		out := next[first].out
		s.HitBytes += len(out)
		s.ElidedBytes += elidedSize(out)
	}
	return s
}

// screenCheck is the project check a recorded call runs, if any.
func screenCheck(c *screenCall) string {
	var cmd string
	switch {
	case c.name == "bash":
		cmd = shell.CommandOf(c.args)
	case editTools[c.name]:
		cmd, _ = tool.TakeThenRun([]byte(c.args))
	}
	if elideProjectCheck(cmd) {
		return cmd
	}
	return ""
}

// elidedSize is how many bytes elision's note would carry for a check
// that printed out: one line for a pass, the output up to the budget for
// a failure. The header is counted as a fixed hundred bytes.
func elidedSize(out string) int {
	const header = 100
	if !strings.Contains(lastLine(out), "[exit code") {
		return header + min(len(lastLine(out)), 200)
	}
	return header + min(len(strings.TrimSpace(out)), elideBudget)
}
