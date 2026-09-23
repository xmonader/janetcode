// Package trajectory folds a session's event log into a ledger: one
// record per thing that happened, grouped by turn and step.
//
// The log is not the ledger. A tool call and its result are two events
// but one thing — bash ran, it took 1.2s, it printed this — and a view
// that prints them as two lines makes the reader do the join. A step's
// model call, its assistant message and the tools it dispatched are four
// or five events and one step. Everything here exists to turn the first
// shape into the second, so that a five-hundred-event session can be
// read down a page instead of scrolled through.
//
// Nothing is computed that was not observed. A duration with no pair of
// timestamps behind it stays zero and prints as "—", never as a guess.
package trajectory

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
)

// Kind is what a record is. The set is small on purpose: it is a legend
// the reader has to hold in their head while scanning.
type Kind string

const (
	KindSystem    Kind = "system"    // the harness's own prompt-level changes
	KindUser      Kind = "user"      // what you typed
	KindContext   Kind = "context"   // what the harness put in front of the model on its own
	KindMessage   Kind = "message"   // one model call's answer
	KindTool      Kind = "tool"      // a tool call and its result, together
	KindCompacted Kind = "compacted" // the context being summarised away
	KindVerdict   Kind = "verdict"   // what the turn was judged to have done
)

// Tag is the short uppercase label this kind wears in the ledger.
func (k Kind) Tag() string {
	switch k {
	case KindMessage:
		return "MODEL"
	case KindCompacted:
		return "COMPACT"
	default:
		return strings.ToUpper(string(k))
	}
}

// Usage is one record's token bill, in the five buckets a provider
// actually distinguishes. Input is the uncached part only: a single
// "input" number that silently includes a 100k cache read tells you
// nothing about either.
type Usage struct {
	Input, CacheRead, CacheWrite, Output int
	// CostUSD is what the provider said the call cost, when it says so.
	// It is not an estimate: an unpriced record carries zero and the
	// views say "unpriced" rather than "free".
	CostUSD float64
}

// Total is every token the record was billed for.
func (u Usage) Total() int {
	return u.Input + u.CacheRead + u.CacheWrite + u.Output
}

// Empty reports whether nothing was recorded, which is different from
// zero tokens having been used.
func (u Usage) Empty() bool { return u == Usage{} }

func (u *Usage) add(o Usage) {
	u.Input += o.Input
	u.CacheRead += o.CacheRead
	u.CacheWrite += o.CacheWrite
	u.Output += o.Output
	u.CostUSD += o.CostUSD
}

func usageOf(u *llm.Usage) Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{
		Input:      u.InputTokens,
		CacheRead:  u.CacheReadTokens,
		CacheWrite: u.CacheWriteTokens,
		Output:     u.OutputTokens,
		CostUSD:    u.CostUSD,
	}
}

// Record is one line of the ledger.
type Record struct {
	Index int // 1-based position in the whole ledger, and what /trajectory <n> takes
	Seq   int // the source event's seq, so a record can be traced back to the log
	Turn  int
	Step  int
	Kind  Kind

	// Text is the fixed part of the line — a tool's name, a heading —
	// and Preview the one-line squeeze of the content. Either may be
	// empty; a line with both reads "text · preview".
	Text    string
	Preview string
	// Result is the tool result's preview, shown after an arrow on the
	// same line. A call and its result are one record because they are
	// one event in the reader's head.
	Result  string
	IsError bool
	Denied  bool
	Running bool // dispatched, never answered: the turn was cancelled or is still going

	CallID string
	Tool   string
	Intent string
	Model  string
	// Slot, Route and RouteWhy are the routing evidence for a model
	// call (see session.Event.Slot and friends): which slot served it,
	// what kind of detour it was — "route", "escalate", "fallback" —
	// and the reason in the policy's own words. Empty on main calls,
	// which are not a route, and on every record that is not a model
	// call.
	Slot     string
	Route    string
	RouteWhy string

	Started  time.Time
	Duration time.Duration
	// TTFT and Decode split a model call into waiting and generating.
	// They are the difference between a slow provider and a long answer,
	// which a single duration cannot tell apart.
	TTFT   time.Duration
	Decode time.Duration

	Usage Usage
	// Cumulative is the session's running total up to and including this
	// record, so any line answers "what had this cost by here?".
	Cumulative Usage

	// Detail is the record's full input (a tool's arguments, your
	// message) and Output its full result (a tool's output, the model's
	// answer). The ledger shows previews; these are what the inspector
	// and the search read.
	Detail string
	Output string
}

// Throughput is output tokens per second of generation, and false when
// the log does not have both halves. It is reported over the decode
// window rather than the whole call: including the wait for the first
// token measures the queue, not the model.
func (r Record) Throughput() (float64, bool) {
	if r.Decode <= 0 || r.Usage.Output <= 0 {
		return 0, false
	}
	return float64(r.Usage.Output) / r.Decode.Seconds(), true
}

// Group is one step of a turn, or the turn's opening message.
type Group struct {
	Title   string // "Message", "Step 3", "Compaction"
	Step    int
	Records []Record
}

// Span is the wall time from the first record starting to the last one
// finishing. Records that overlap are covered once, which is what makes
// this a span and not a sum.
func (g Group) Span() time.Duration {
	var first, last time.Time
	for _, r := range g.Records {
		if r.Started.IsZero() {
			continue
		}
		end := r.Started.Add(r.Duration)
		if first.IsZero() || r.Started.Before(first) {
			first = r.Started
		}
		if end.After(last) {
			last = end
		}
	}
	if first.IsZero() {
		return 0
	}
	return last.Sub(first)
}

// Tools is the group's tool calls as "bash×6, read_file", busiest
// first — the shape of a step at a glance.
func (g Group) Tools() string {
	count := map[string]int{}
	var order []string
	for _, r := range g.Records {
		if r.Kind != KindTool || r.Tool == "" {
			continue
		}
		if count[r.Tool] == 0 {
			order = append(order, r.Tool)
		}
		count[r.Tool]++
	}
	sort.SliceStable(order, func(i, j int) bool { return count[order[i]] > count[order[j]] })
	var parts []string
	for _, n := range order {
		if count[n] > 1 {
			parts = append(parts, n+"×"+strconv.Itoa(count[n]))
			continue
		}
		parts = append(parts, n)
	}
	return strings.Join(parts, ", ")
}

// Turn is one user turn: everything from what you asked to what came
// back.
type Turn struct {
	Turn   int
	Groups []Group
}

// Records flattens the turn back into ledger order.
func (t Turn) Records() []Record {
	var out []Record
	for _, g := range t.Groups {
		out = append(out, g.Records...)
	}
	return out
}

// Usage is what the whole turn was billed.
func (t Turn) Usage() Usage {
	var u Usage
	for _, r := range t.Records() {
		u.add(r.Usage)
	}
	return u
}

// Calls is how many model calls and tool calls the turn made — the two
// numbers a folded turn is worth summarising by.
func (t Turn) Calls() (steps, tools int) {
	for _, r := range t.Records() {
		switch r.Kind {
		case KindMessage:
			steps++
		case KindTool:
			tools++
		}
	}
	return steps, tools
}

// Span is the turn's wall time, first record to last.
func (t Turn) Span() time.Duration {
	var first, last time.Time
	for _, r := range t.Records() {
		if r.Started.IsZero() {
			continue
		}
		end := r.Started.Add(r.Duration)
		if first.IsZero() || r.Started.Before(first) {
			first = r.Started
		}
		if end.After(last) {
			last = end
		}
	}
	if first.IsZero() {
		return 0
	}
	return last.Sub(first)
}

// Ledger is the whole session, as records and as the turn/step tree over
// them. Both views are over the same Record values, so an index into
// All is the index the tree shows.
type Ledger struct {
	All   []Record
	Turns []Turn
}

// At finds the record with this 1-based index.
func (l Ledger) At(index int) (Record, bool) {
	for _, r := range l.All {
		if r.Index == index {
			return r, true
		}
	}
	return Record{}, false
}

// Usage is the session's whole bill.
func (l Ledger) Usage() Usage {
	if len(l.All) == 0 {
		return Usage{}
	}
	return l.All[len(l.All)-1].Cumulative
}
