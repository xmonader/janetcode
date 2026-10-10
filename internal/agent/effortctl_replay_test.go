package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/session"
)

// effortReplay is what the controller would have asked for over recorded
// sessions: the offline pre-screen for effort_control, zero tokens. It
// answers the two questions worth asking before a live arm: does the
// controller sit low on the replies that reasoned the most (the ones it
// exists for), and how often would it change the tier a turn (each change
// is a cache risk on a provider that keys on it).
type effortReplay struct {
	sessions, calls int
	byTier          map[string]int // calls at each tier
	thinkByTier     map[string]int // reasoning characters at each tier
	outByTier       map[string]int // output tokens at each tier
	heavy           map[string]int // replies over the plan budget's size, by tier
	moves           []int          // tier changes per turn
}

// heavyReply is the plan budget's size: a reply that reasoned this much
// is the over-planning the controller is meant to head off.
const heavyReply = 32_000

func newEffortReplay() *effortReplay {
	return &effortReplay{byTier: map[string]int{}, thinkByTier: map[string]int{}, outByTier: map[string]int{}, heavy: map[string]int{}}
}

// add replays one session's events through the controller, for a
// session set to tier. A verify gate sending a turn back is not in the
// replay — the log keeps the gate, not which reply it answered — so the
// replay can only under-count trouble.
func (r *effortReplay) add(events []session.Event, tier string) {
	r.sessions++
	var c effortControl
	inTurn := false
	var last *llm.Message
	closeTurn := func() {
		if inTurn {
			r.moves = append(r.moves, c.moves)
		}
	}
	for _, e := range events {
		switch e.Type {
		case session.TypeTurnStart:
			closeTurn()
			c.start(tier)
			inTurn = true
			last = nil
		case session.TypeAssistant:
			if !inTurn || e.Message == nil {
				continue
			}
			at := c.tier()
			r.calls++
			r.byTier[at]++
			think := 0
			for _, b := range e.Message.Blocks {
				if b.Type == llm.BlockThinking {
					think += len(b.Text)
				}
			}
			r.thinkByTier[at] += think
			if e.Usage != nil {
				r.outByTier[at] += e.Usage.OutputTokens
			}
			if think >= heavyReply {
				r.heavy[at]++
			}
			m := *e.Message
			last = &m
			// A reply that only reasoned is a plan-budget or output-limit
			// cut; the log does not keep the stop reason itself.
			if len(m.ToolUses()) == 0 && strings.TrimSpace(m.Text()) == "" && think > 0 {
				c.observe(effortOverthink)
			}
		case session.TypeToolResult:
			if !inTurn || !e.Surface || e.Message == nil || last == nil {
				continue
			}
			uses := last.ToolUses()
			byID := map[string]llm.Block{}
			for _, b := range e.Message.Blocks {
				if b.Type == llm.BlockToolResult {
					byID[b.ToolUseID] = b
				}
			}
			results := make([]llm.Block, 0, len(uses))
			for _, u := range uses {
				id := u.ID
				if u.ReplyID != "" {
					if _, ok := byID[id]; !ok {
						id = u.ReplyID
					}
				}
				results = append(results, byID[id])
			}
			c.observe(stepEffortSignal(uses, results, c.edited))
			last = nil
		}
	}
	closeTurn()
}

func (r *effortReplay) String() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d sessions, %d turns, %d model calls\n", r.sessions, r.turns(), r.calls)
	fmt.Fprintf(&sb, "%-8s %7s %12s %12s %8s\n", "tier", "calls", "think chars", "output tok", "heavy")
	for _, t := range llm.EffortTiers {
		if r.byTier[t] == 0 {
			continue
		}
		fmt.Fprintf(&sb, "%-8s %7d %12d %12d %8d\n", t, r.byTier[t], r.thinkByTier[t], r.outByTier[t], r.heavy[t])
	}
	moves := append([]int(nil), r.moves...)
	sort.Ints(moves)
	total, capped, zero := 0, 0, 0
	for _, m := range moves {
		total += m
		if m >= maxEffortMoves {
			capped++
		}
		if m == 0 {
			zero++
		}
	}
	if len(moves) > 0 {
		fmt.Fprintf(&sb, "tier changes a turn: mean %.2f, median %d, max %d; %d of %d turns never changed, %d hit the cap of %d\n",
			float64(total)/float64(len(moves)), moves[len(moves)/2], moves[len(moves)-1], zero, len(moves), capped, maxEffortMoves)
	}
	return sb.String()
}

func (r *effortReplay) turns() int { return len(r.moves) }

// TestEffortReplay runs the pre-screen over a directory of session logs
// named by JANETCODE_EFFORT_REPLAY, for a session set to the tier in
// JANETCODE_EFFORT_REPLAY_TIER (default: none, so a ceiling of high), and
// prints the table. Without the variable it replays a small synthetic
// session so the replay itself stays tested.
func TestEffortReplay(t *testing.T) {
	dir := os.Getenv("JANETCODE_EFFORT_REPLAY")
	if dir == "" {
		r := newEffortReplay()
		r.add(syntheticReplaySession(), "")
		if r.calls != 3 || r.byTier["low"] != 2 || r.byTier["medium"] != 1 || r.heavy["low"] != 1 {
			t.Fatalf("synthetic replay:\n%s", r)
		}
		return
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	r := newEffortReplay()
	for _, p := range paths {
		sess, _, err := session.Open(dir, strings.TrimSuffix(filepath.Base(p), ".jsonl"))
		if err != nil {
			continue
		}
		r.add(sess.Events(), os.Getenv("JANETCODE_EFFORT_REPLAY_TIER"))
	}
	t.Logf("effort_control replay over %s:\n%s", dir, r)
}

// syntheticReplaySession is one turn: a reply that reasons past the plan
// budget and writes a file with a missing path (trouble), then one that
// writes it properly (progress), then the answer.
func syntheticReplaySession() []session.Event {
	asst := func(blocks ...llm.Block) session.Event {
		m := llm.Message{Role: llm.RoleAssistant, Blocks: blocks}
		return session.Event{Type: session.TypeAssistant, Message: &m, Usage: &llm.Usage{OutputTokens: 10}}
	}
	result := func(id, text string, isErr bool) session.Event {
		m := llm.ToolResultMessage(llm.ToolResult(id, text, isErr))
		return session.Event{Type: session.TypeToolResult, Surface: true, Message: &m}
	}
	return []session.Event{
		{Type: session.TypeTurnStart},
		asst(llm.Block{Type: llm.BlockThinking, Text: strings.Repeat("x", heavyReply)}, llm.Block{Type: llm.BlockToolUse, ID: "1", Name: "write_file", Input: []byte(`{"content":"x"}`)}),
		result("1", "path is required", true),
		asst(llm.Block{Type: llm.BlockToolUse, ID: "2", Name: "write_file", Input: []byte(`{"path":"a","content":"x"}`)}),
		result("2", "wrote a", false),
		asst(llm.Block{Type: llm.BlockText, Text: "done"}),
		{Type: session.TypeTurnEnd},
	}
}
