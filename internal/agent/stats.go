package agent

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Stats are cumulative counters for the process lifetime. Unlike the
// context report they survive compaction and cover every turn.
type Stats struct {
	Turns       int           // user turns started
	ModelCalls  int           // requests sent to the provider
	ModelErrors int           // requests that failed (before any retry)
	ModelTime   time.Duration // wall time spent waiting on the provider
	Compactions int
	// Recoveries counts tool calls parsed back out of assistant text
	// (see Agent.recoverToolCalls) — turns a model would otherwise have
	// ended having done nothing.
	Recoveries int
	// WatchdogKills counts streamed calls the watchdog ended because they
	// stopped producing, and Fallbacks counts calls that only succeeded
	// on another model. Both are runs the harness saved rather than runs
	// that went well, and a benchmark that cannot see them is comparing
	// a harness against its own repair work without knowing it.
	WatchdogKills int
	Fallbacks     int
	// Retries counts model calls that failed transiently and succeeded on
	// a later attempt. They are not a rescue in the sense Saves means —
	// the same model answered in the end — but a run that spent four
	// attempts on every call is not the run the timings suggest.
	Retries int
	Tools   []ToolStat // sorted by calls, descending
	// Approvals counts permission prompts actually shown to the user
	// (mutating calls the rules and the auto modes did not already
	// settle), and Refusals how many of them were denied. A call
	// answered by AllowAlways still counts once here; only the rule it
	// saves keeps the next call like it from prompting again.
	Approvals int
	Refusals  int
	// Models is the per-model breakdown of every call this session made:
	// how many, their tokens and how many failed. A session that never
	// routes or falls back has exactly one entry; RouteCheap and
	// Fallbacks are the two ways a second one appears.
	Models []ModelStat
	// RoutedCalls counts model calls the experimental step router (see
	// steproute.go) sent to RouteCheap instead of Model, so /stats can
	// report "routed N of M model calls to <cheap>" — the number the
	// feature has to earn its keep against, not a guess.
	RoutedCalls int
}

// ModelStat is the tally for one model actually used in this session.
type ModelStat struct {
	Name   string
	Calls  int
	Errors int
	Time   time.Duration
	Usage  llm.Usage
}

// RouteLine is "routed N of M model calls to <cheap>", worded once so
// both front-ends' /stats say the same thing (see PromptLine). Empty when
// nothing was ever routed: route_cheap is off, the policy was never
// eligible this session, or every routed call failed and fell back to
// the main model before it could count.
func (s Stats) RouteLine(cheap string) string {
	if cheap == "" || s.RoutedCalls == 0 {
		return ""
	}
	return fmt.Sprintf("routed %d of %s to %s", s.RoutedCalls, textfmt.Plural(s.ModelCalls, "model call"), cheap)
}

// Saves is how many times the harness rescued a call that would
// otherwise have been a wasted turn or a lost run.
func (s Stats) Saves() int { return s.Recoveries + s.WatchdogKills + s.Fallbacks + s.Retries }

// PromptLine is "asked N times · M refused" for /stats, worded once for
// both front-ends. Empty until the first prompt has been shown.
func (s Stats) PromptLine() string {
	if s.Approvals == 0 {
		return ""
	}
	return fmt.Sprintf("asked %s · %d refused", textfmt.Plural(s.Approvals, "time"), s.Refusals)
}

// ToolStat is the tally for one tool.
type ToolStat struct {
	Name   string
	Calls  int
	Errors int
	Time   time.Duration
}

// ToolCalls is the total across tools.
func (s Stats) ToolCalls() (calls, errors int) {
	for _, t := range s.Tools {
		calls += t.Calls
		errors += t.Errors
	}
	return calls, errors
}

type statsCounter struct {
	mu     sync.Mutex
	s      Stats
	tools  map[string]*ToolStat
	models map[string]*ModelStat
}

func (c *statsCounter) turn() {
	c.mu.Lock()
	c.s.Turns++
	c.mu.Unlock()
}

// model records one call to the provider: name is the model that
// actually answered it (Model, a fallback, or RouteCheap), so the
// per-model breakdown reflects where the calls really went rather than
// only the session's nominal model.
func (c *statsCounter) model(name string, d time.Duration, u llm.Usage, failed bool) {
	c.mu.Lock()
	c.s.ModelCalls++
	c.s.ModelTime += d
	if failed {
		c.s.ModelErrors++
	}
	if c.models == nil {
		c.models = map[string]*ModelStat{}
	}
	m := c.models[name]
	if m == nil {
		m = &ModelStat{Name: name}
		c.models[name] = m
	}
	m.Calls++
	m.Time += d
	if failed {
		m.Errors++
	} else {
		m.Usage.Add(u)
	}
	c.mu.Unlock()
}

// routed records one model call the step router sent to RouteCheap
// instead of Model.
func (c *statsCounter) routed() {
	c.mu.Lock()
	c.s.RoutedCalls++
	c.mu.Unlock()
}

func (c *statsCounter) compacted() {
	c.mu.Lock()
	c.s.Compactions++
	c.mu.Unlock()
}

func (c *statsCounter) recovered(n int) {
	c.mu.Lock()
	c.s.Recoveries += n
	c.mu.Unlock()
}

// retried records one model call that needed n retries before it
// answered. The call is counted once, not once per attempt: the question
// it answers is how many calls were not clean.
func (c *statsCounter) retried(n int) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	c.s.Retries++
	c.mu.Unlock()
}

func (c *statsCounter) watchdogKill() {
	c.mu.Lock()
	c.s.WatchdogKills++
	c.mu.Unlock()
}

func (c *statsCounter) fellBack() {
	c.mu.Lock()
	c.s.Fallbacks++
	c.mu.Unlock()
}

// approval records one permission prompt that was actually shown, and
// whether the user refused it.
func (c *statsCounter) approval(refused bool) {
	c.mu.Lock()
	c.s.Approvals++
	if refused {
		c.s.Refusals++
	}
	c.mu.Unlock()
}

func (c *statsCounter) tool(name string, d time.Duration, failed bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tools == nil {
		c.tools = map[string]*ToolStat{}
	}
	t := c.tools[name]
	if t == nil {
		t = &ToolStat{Name: name}
		c.tools[name] = t
	}
	t.Calls++
	t.Time += d
	if failed {
		t.Errors++
	}
}

// switches are the behaviours Agent.Off can disable.
var switches = map[string]bool{"coach": true, "syntax": true, "loops": true, "prune": true, "verify": true, "repomap": true, "working": true, "richskip": true, "faults": true, "verdict": true, "todo": true, "script": true, "lineedit": true, "extratools": true, "intent": true, "oversized": true, "plan": true, "critic": true}

// Switches names every behaviour Off understands, in order. The error for
// a misspelled one lists these rather than a copy that drifts: a switch
// that exists but is not listed is a switch nobody finds.
func Switches() []string {
	out := make([]string, 0, len(switches))
	for n := range switches {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// KnownSwitch reports whether Off understands the name.
func KnownSwitch(name string) bool { return switches[name] }
