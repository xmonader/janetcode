package agent

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/provenance"
	"github.com/xmonader/janetcode/internal/textfmt"
)

// Stats are cumulative counters for the process lifetime. Unlike the
// context report they survive compaction and cover every turn.
type Stats struct {
	Turns       int           // user turns started
	ModelCalls  int           // requests sent to the provider
	ModelErrors int           // requests that failed (before any retry)
	ModelTime   time.Duration // wall time spent waiting on the provider
	// ModelTTFT is how much of ModelTime was spent before the provider
	// produced anything at all.
	ModelTTFT time.Duration
	// TurnTime is wall time inside Run: everything between a turn
	// starting and it ending. The session is open for longer than this,
	// and the difference is the user reading and typing — a report that
	// does not separate the two blames the harness for both.
	TurnTime time.Duration
	// WaitTime is the part of TurnTime spent blocked on the user (a
	// permission prompt, a question from the ask tool), and WaitCount
	// how many such blocks there were. It is deducted from the tool
	// call it happened inside, so tool time means work done.
	WaitTime    time.Duration
	WaitCount   int
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
	// Mismatches counts model calls whose response named another vendor
	// than the requested model's (see internal/provenance), and Mismatch
	// is the first one's verdict, with the requested model. A run where
	// this is non-zero measured a different model than it claims to.
	Mismatches int
	Mismatch   string
	// Served is the last call's provenance, for a result file.
	Served llm.Provenance
	Tools  []ToolStat // sorted by calls, descending
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
	// AdvisorConsults counts the consults the harness forced (see
	// advisorforce.go), not the ones the model asked for with the tool.
	// An arm that measures a forced advisor needs to know it actually
	// fired: a feature that is on and never consulted is a null result
	// about the wiring, not about the advisor.
	AdvisorConsults int
	// DecisionCalls counts calls to the optional decision provider
	// (internal/jev) and DecisionErrors how many failed. DecisionTime is
	// the wall time they cost, so /stats can price the feature in its
	// own currency — the latency the turn paid for its opinions.
	DecisionCalls  int
	DecisionErrors int
	DecisionTime   time.Duration
	// DecisionCost is the USD the provider itself reported for those
	// calls, and DecisionTokensOut the tokens it billed. The figure is
	// the server's, never a local estimate: a decision model's price is
	// not in the harness's rate table, so an unreported cost stays zero
	// rather than becoming a guess.
	DecisionCost      float64
	DecisionTokensOut int
	// IntakeKind is what the log-only intake consumer (jevintake.go)
	// called the session's first prompt: explain / implement / debug /
	// review / docs / other, or "" when it was never asked or never
	// answered. Nothing in the harness reads it — it is here so a
	// session can be told what the classifier thought of it.
	IntakeKind string
	// DecisionActed counts the decisions the harness did something
	// different because of — a triage verdict that skipped a repair
	// round, an intake classification that routed a turn. It is the
	// number a log-only consumer can never move, and the one an A/B has
	// to explain any difference with.
	DecisionActed int
}

// ModelStat is the tally for one model actually used in this session.
type ModelStat struct {
	Name   string
	Calls  int
	Errors int
	Time   time.Duration
	// Slowest is the longest single call. An average hides the shape of
	// provider latency, which on some providers is the whole story: ten
	// fast calls and one that took two minutes average out to "fine".
	Slowest time.Duration
	// TTFT is the total time this model spent before producing its
	// first token, across every call. Against Time it says whether a
	// slow model is slow to start or slow to finish — the provider
	// queueing, or the answers being long.
	TTFT  time.Duration
	Usage llm.Usage
	// Served counts, per model name the provider said answered, how many
	// of this model's calls it answered. A gateway may serve something
	// other than what was asked for, and an escalation that quietly
	// lands back on the cheap model is a measurement error rather than a
	// saving -- so the asked name keys the row and the served names are
	// counted inside it. Empty when the provider named nothing.
	Served map[string]int
}

// ServedElsewhere reports the model the provider named for this row when
// it was not the model asked for, and how many calls went that way. It is
// the audit line an escalation arm needs: asked X, got Y, N times.
func (m ModelStat) ServedElsewhere() (string, int) {
	for got, n := range m.Served {
		// A dated snapshot or a gateway's vendor/ prefix is the same
		// model; reporting "asked claude-sonnet-4-6, got
		// claude-sonnet-4-6-20260101" would cry wolf on every call.
		if !provenance.SameModel(m.Name, got) {
			return got, n
		}
	}
	return "", 0
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

// DecisionLine is "decisions: N calls · M errors · avg Xms · $X" for
// /stats.
// Empty until the first decision call, so a session with the feature
// configured but never triggered says nothing.
func (s Stats) DecisionLine() string {
	if s.DecisionCalls == 0 {
		return ""
	}
	line := fmt.Sprintf("decisions: %s", textfmt.Plural(s.DecisionCalls, "call"))
	if s.DecisionErrors > 0 {
		line += fmt.Sprintf(" · %d errored", s.DecisionErrors)
	}
	if s.DecisionTime > 0 {
		line += fmt.Sprintf(" · avg %s", textfmt.Duration(s.DecisionTime/time.Duration(max(1, s.DecisionCalls))))
	}
	if s.DecisionCost > 0 {
		line += " · " + llm.FormatUSD(s.DecisionCost)
	}
	if s.IntakeKind != "" {
		line += " · first prompt read as " + s.IntakeKind
	}
	return line
}

// ToolStat is the tally for one tool.
type ToolStat struct {
	Name    string
	Calls   int
	Errors  int
	Time    time.Duration
	Slowest time.Duration // the longest single call, for the same reason as ModelStat.Slowest
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
	// mismatchSeen is the (requested model, served vendor) pairs already
	// announced this session.
	mismatchSeen map[string]bool
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
func (c *statsCounter) model(name, served string, d time.Duration, u llm.Usage, failed bool) {
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
	if d > m.Slowest {
		m.Slowest = d
	}
	if failed {
		m.Errors++
	} else {
		m.Usage.Add(u)
	}
	if served != "" {
		if m.Served == nil {
			m.Served = map[string]int{}
		}
		m.Served[served]++
	}
	c.mu.Unlock()
}

// served records what a call's response said about who served it, and
// the verdict against the requested model. It reports whether this is
// the first mismatch of the session for this requested model and served
// vendor, so the caller says so once rather than on every call.
func (c *statsCounter) served(requested string, p llm.Provenance, v provenance.Verdict) (first bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.s.Served = p
	if !v.Mismatch {
		return false
	}
	c.s.Mismatches++
	if c.s.Mismatch == "" {
		c.s.Mismatch = "requested " + requested + ": " + v.String()
	}
	key := requested + "\x00" + v.Served
	if c.mismatchSeen == nil {
		c.mismatchSeen = map[string]bool{}
	}
	if c.mismatchSeen[key] {
		return false
	}
	c.mismatchSeen[key] = true
	return true
}

// firstToken records how long one call waited before the provider
// produced anything. Zero means the call never streamed a token, which
// is not the same as an instant first token, so it is not counted.
func (c *statsCounter) firstToken(name string, d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.s.ModelTTFT += d
	if m := c.models[name]; m != nil {
		m.TTFT += d
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

// advisorConsult records one consult the harness forced.
func (c *statsCounter) advisorConsult() {
	c.mu.Lock()
	c.s.AdvisorConsults++
	c.mu.Unlock()
}

// decision records one call to the decision provider, and whether it
// errored. The call counts even when it failed: the point is to show
// how much of the feature's promise the session actually used. cost and
// tokensOut are the provider's own billing figures, zero for a call
// that failed or answered without reporting them.
func (c *statsCounter) decision(failed bool, d time.Duration, cost float64, tokensOut int) {
	c.mu.Lock()
	c.s.DecisionCalls++
	if d > 0 {
		c.s.DecisionTime += d
	}
	if failed {
		c.s.DecisionErrors++
	}
	if cost > 0 {
		c.s.DecisionCost += cost
	}
	if tokensOut > 0 {
		c.s.DecisionTokensOut += tokensOut
	}
	c.mu.Unlock()
}

// decisionActed records that a decision changed what the harness did.
// It is counted beside, not instead of, the call itself.
func (c *statsCounter) decisionActed() {
	c.mu.Lock()
	c.s.DecisionActed++
	c.mu.Unlock()
}

// intake records the log-only classification of the session's first
// prompt. It is the only thing kept from that call beyond its price.
func (c *statsCounter) intake(kind string) {
	c.mu.Lock()
	c.s.IntakeKind = kind
	c.mu.Unlock()
}

// turnTook records one finished turn's wall time.
func (c *statsCounter) turnTook(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.s.TurnTime += d
	c.mu.Unlock()
}

// waited records time the harness spent blocked on the user.
func (c *statsCounter) waited(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.s.WaitTime += d
	c.s.WaitCount++
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
	if d < 0 {
		d = 0 // a call that was nothing but a prompt: the wait was all of it
	}
	t.Time += d
	if d > t.Slowest {
		t.Slowest = d
	}
	if failed {
		t.Errors++
	}
}

// switches are the behaviours Agent.Off can disable.
var switches = map[string]bool{
	"coach": true, "syntax": true, "loops": true, "prune": true, "verify": true,
	"repomap": true, "working": true, "richskip": true, "faults": true, "verdict": true,
	"todo": true, "script": true, "lineedit": true, "extratools": true, "intent": true,
	"oversized": true, "plan": true, "critic": true,
	"accept": true, "recipe": true, "proof": true, "impact": true, "routing": true,
	"protected": true, "ladder": true, "undoabout": true, "mermaid": true,
	"keeprevert": true, "memoryrank": true, "sleep": true, "decisions": true,
	"escalate": true, "advisor": true, "scrub": true, "opentodos": true,
	"orient": true, "baseline": true, "thenrun": true, "inline": true, "asyncdiag": true, "headlesscore": true, "leanprompt": true, "planbudget": true, "burstcap": true, "specreview": true, "testguard": true, "claimcheck": true, "tour": true, "testledger": true,
	"evidence": true,
	"taint":    true, "honeytoken": true, "depgate": true,
	"probes":    true,
	"plancarry": true, "readdedup": true, "edgerules": true, "compactskip": true, "steplimit": true,
	"idlebound": true, "hold": true, "editecho": true, "testhint": true, "failexcerpt": true, "astedit": true, "emptyprobe": true, "toolnudge": true, "memarchive": true,

	"wrapup": true, "wraprules": true, "literal": true, "scope": true, "wrapstop": true, "slimtools": true, "probememory": true, "readblame": true,
	"numclaim":  true,
	"typosquat": true,
	"dossier":   true,
	"mmr":       true, "queryexpand": true, "memchunk": true, "flush": true,
	"lspsupervise": true,
}

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
