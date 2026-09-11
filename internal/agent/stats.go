package agent

import (
	"sort"
	"sync"
	"time"
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
	Tools         []ToolStat // sorted by calls, descending
}

// Saves is how many times the harness rescued a call that would
// otherwise have been a wasted turn or a lost run.
func (s Stats) Saves() int { return s.Recoveries + s.WatchdogKills + s.Fallbacks }

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
	mu    sync.Mutex
	s     Stats
	tools map[string]*ToolStat
}

func (c *statsCounter) turn() {
	c.mu.Lock()
	c.s.Turns++
	c.mu.Unlock()
}

func (c *statsCounter) model(d time.Duration, failed bool) {
	c.mu.Lock()
	c.s.ModelCalls++
	c.s.ModelTime += d
	if failed {
		c.s.ModelErrors++
	}
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

// Stats returns a snapshot of the counters.
func (a *Agent) Stats() Stats {
	a.stats.mu.Lock()
	defer a.stats.mu.Unlock()
	s := a.stats.s
	s.Tools = make([]ToolStat, 0, len(a.stats.tools))
	for _, t := range a.stats.tools {
		s.Tools = append(s.Tools, *t)
	}
	sort.Slice(s.Tools, func(i, j int) bool {
		if s.Tools[i].Calls != s.Tools[j].Calls {
			return s.Tools[i].Calls > s.Tools[j].Calls
		}
		return s.Tools[i].Name < s.Tools[j].Name
	})
	return s
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
