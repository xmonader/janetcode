// Package cron is the session-scoped scheduler behind /loop and the cron
// tool: prompts that fire on an interval for as long as the session
// lasts. It knows nothing about turns — the front-end wires OnFire to
// starting one — and nothing about agents.
package cron

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/session"
	"github.com/xmonader/janetcode/internal/tool"
)

const (
	// maxJobs bounds how many session loops can be armed at once, whether
	// they come from /loop, the tool, or a session file being restored.
	maxJobs = 16
	// minEvery and maxEvery bound an interval: below a second a loop is a
	// busy wait, above a day it will outlive the session.
	minEvery = time.Second
	maxEvery = 24 * time.Hour
)

// Cron is a session-scoped scheduler: /loop and the cron tool add jobs
// that fire a prompt on an interval. Armed jobs are persisted on the
// session record (OnChange) and restored on resume. Close (process quit)
// cancels timers without wiping the saved list.
//
// Every field below is guarded by mu, including the two callbacks: they
// are read from timer goroutines. Callers that change a callback after
// the first Add or Restore must use SetOnFire/SetOnChange; assigning the
// field directly is only safe before any job is armed.
type Cron struct {
	mu     sync.Mutex
	jobs   map[string]*cronJob
	nextID int
	// seq orders jobs for List/Specs by the time they were armed. It is
	// separate from nextID because Restore accepts ids it did not mint.
	nextSeq int
	// version counts mutations, so that a snapshot taken by one goroutine
	// is never persisted on top of a newer one taken by another.
	version int
	closed  bool
	// OnFire is called from the timer goroutine with the job id and prompt.
	// The TUI starts a turn; if a turn is already running the tick is skipped.
	OnFire func(id, prompt string)
	// OnChange is called after Add/Cancel/CancelAll with the armed set, so
	// the session file can keep it. Close does not fire it, and Restore
	// does not either. It must not call back into the scheduler.
	OnChange func([]session.LoopSpec)

	// notifyMu serialises OnChange calls; notified is the version of the
	// newest snapshot already handed over.
	notifyMu sync.Mutex
	notified int

	// journal records every occurrence (see journal.go); nil is fine.
	journal *Journal
}

type cronJob struct {
	ID     string
	Every  time.Duration
	Prompt string
	Next   time.Time
	seq    int
	timer  *time.Timer
}

// Info is the public snapshot of one job.
type Info struct {
	ID     string
	Every  time.Duration
	Prompt string
	Next   time.Time
}

// New returns an empty scheduler.
func New() *Cron {
	return &Cron{jobs: map[string]*cronJob{}}
}

// SetOnFire installs the fire callback. Safe to call while jobs are armed.
func (c *Cron) SetOnFire(f func(id, prompt string)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.OnFire = f
}

// SetOnChange installs the persistence callback. Safe to call while jobs
// are armed. It does not fire; the next mutation does.
func (c *Cron) SetOnChange(f func([]session.LoopSpec)) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.OnChange = f
}

// SetJournal installs the occurrence journal. Call it before Restore so a
// resumed session can catch up on occurrences missed while it was down.
func (c *Cron) SetJournal(j *Journal) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.journal = j
}

// Journal is the occurrence journal, or nil.
func (c *Cron) Journal() *Journal {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.journal
}

// Note lets the front end record what became of a firing: KindSkipped when
// a turn was already running, KindDone when the turn ended.
func (c *Cron) Note(id, kind, detail string) {
	c.Journal().Append(Occurrence{Job: id, Kind: kind, Detail: detail})
}

// checkEvery is the one place an interval is bounds-checked.
func checkEvery(every time.Duration) error {
	if every < minEvery {
		return fmt.Errorf("interval must be at least %s", minEvery)
	}
	if every > maxEvery {
		return fmt.Errorf("interval must be at most %s", maxEvery)
	}
	return nil
}

// Add arms a job that fires after every, then again on that interval.
func (c *Cron) Add(every time.Duration, prompt string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no scheduler in this session")
	}
	if err := checkEvery(every); err != nil {
		return "", err
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", fmt.Errorf("scheduler has stopped")
	}
	if c.jobs == nil {
		c.jobs = map[string]*cronJob{}
	}
	if len(c.jobs) >= maxJobs {
		c.mu.Unlock()
		return "", fmt.Errorf("at most %d loops in one session", maxJobs)
	}
	c.nextID++
	id := fmt.Sprintf("loop-%d", c.nextID)
	c.armLocked(id, every, prompt)
	c.journal.Append(Occurrence{Job: id, Kind: KindAdded, Detail: every.String()})
	snap, cb, ver := c.changeLocked()
	c.mu.Unlock()
	c.notify(snap, cb, ver)
	return id, nil
}

// armLocked creates and starts one job. c.mu must be held, the id must be
// free and the scheduler open.
func (c *Cron) armLocked(id string, every time.Duration, prompt string) {
	c.nextSeq++
	e := &cronJob{ID: id, Every: every, Prompt: prompt, Next: time.Now().Add(every), seq: c.nextSeq}
	e.timer = time.AfterFunc(every, func() { c.tick(id, e) })
	c.jobs[id] = e
}

// catchUpLocked applies the catch-up policy to a just-restored job: from the
// journal's last run of id, occurrences missed during downtime coalesce into
// one firing shortly after restore (or are dropped as stale). A job with no
// journal history gets a fresh baseline and no catch-up. c.mu must be held.
func (c *Cron) catchUpLocked(id string, every time.Duration) {
	if c.journal == nil {
		return
	}
	now := time.Now()
	last, ok := c.journal.LastRun(id)
	if !ok {
		c.journal.Append(Occurrence{Job: id, Kind: KindAdded, Detail: "restored"})
		return
	}
	missed, run := Missed(last, now, every)
	if missed == 0 {
		return
	}
	if !run {
		c.journal.Append(Occurrence{Job: id, Kind: KindStale, Missed: missed, Detail: "down " + now.Sub(last).Round(time.Second).String()})
		return
	}
	e := c.jobs[id]
	e.timer.Stop()
	e.Next = now.Add(catchupDelay)
	e.timer = time.AfterFunc(catchupDelay, func() { c.fire(id, e, KindCatchup, missed) })
}

// listLocked snapshots armed jobs, oldest first. c.mu must be held.
func (c *Cron) listLocked() []Info {
	ordered := make([]*cronJob, 0, len(c.jobs))
	for _, e := range c.jobs {
		ordered = append(ordered, e)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].seq < ordered[j].seq })
	out := make([]Info, 0, len(ordered))
	for _, e := range ordered {
		out = append(out, Info{ID: e.ID, Every: e.Every, Prompt: e.Prompt, Next: e.Next})
	}
	return out
}

// List is a snapshot of armed jobs, oldest first.
func (c *Cron) List() []Info {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.listLocked()
}

// Cancel stops one job. Unknown ids return false.
func (c *Cron) Cancel(id string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	e := c.jobs[id]
	if e == nil {
		c.mu.Unlock()
		return false
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	delete(c.jobs, id)
	c.journal.Append(Occurrence{Job: id, Kind: KindCancelled})
	snap, cb, ver := c.changeLocked()
	c.mu.Unlock()
	c.notify(snap, cb, ver)
	return true
}

// CancelAll stops every job.
func (c *Cron) CancelAll() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	n := len(c.jobs)
	for _, e := range c.jobs {
		if e.timer != nil {
			e.timer.Stop()
		}
		c.journal.Append(Occurrence{Job: e.ID, Kind: KindCancelled})
	}
	c.jobs = map[string]*cronJob{}
	snap, cb, ver := c.changeLocked()
	c.mu.Unlock()
	c.notify(snap, cb, ver)
	return n
}

// Restore arms jobs from a session record without firing OnChange.
//
// Session files are state, not user input: an entry with an unusable
// interval, an empty prompt or a duplicate id is skipped rather than
// failing the resume, and the list is capped like any other. The only
// errors are "there is no scheduler" and "it has stopped".
func (c *Cron) Restore(specs []session.LoopSpec) error {
	if c == nil {
		return fmt.Errorf("no scheduler in this session")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.restoreLocked(specs)
}

// restoreLocked is Restore with c.mu held.
func (c *Cron) restoreLocked(specs []session.LoopSpec) error {
	if c.closed {
		return fmt.Errorf("scheduler has stopped")
	}
	if c.jobs == nil {
		c.jobs = map[string]*cronJob{}
	}
	// Seed the id counter from every explicit id first, so a spec with no
	// id cannot be minted onto one a later spec claims.
	for _, spec := range specs {
		if n, ok := loopIndex(strings.TrimSpace(spec.ID)); ok && n > c.nextID {
			c.nextID = n
		}
	}
	for _, spec := range specs {
		if len(c.jobs) >= maxJobs {
			break
		}
		every, err := ParseEvery(spec.Every)
		if err != nil {
			continue
		}
		prompt := strings.TrimSpace(spec.Prompt)
		if prompt == "" {
			continue
		}
		id := strings.TrimSpace(spec.ID)
		if id == "" {
			c.nextID++
			id = fmt.Sprintf("loop-%d", c.nextID)
		}
		if c.jobs[id] != nil {
			continue
		}
		c.armLocked(id, every, prompt)
		c.catchUpLocked(id, every)
	}
	c.version++
	return nil
}

// Replace stops every armed job and arms specs in their place, with f as
// the persistence callback from now on: the scheduler moving to another
// session (/clear, /resume). Neither the old callback nor the new one
// fires — the session left behind keeps the loops it had saved, to be
// restored when it is resumed, and specs are already the new session's.
func (c *Cron) Replace(specs []session.LoopSpec, f func([]session.LoopSpec)) error {
	if c == nil {
		return fmt.Errorf("no scheduler in this session")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.jobs {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
	c.jobs = map[string]*cronJob{}
	c.nextID = 0
	c.OnChange = f
	return c.restoreLocked(specs)
}

func loopIndex(id string) (int, bool) {
	rest, ok := strings.CutPrefix(id, "loop-")
	if !ok || rest == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(rest); i++ {
		if rest[i] < '0' || rest[i] > '9' || n > 1<<20 {
			return 0, false
		}
		n = n*10 + int(rest[i]-'0')
	}
	if n <= 0 {
		return 0, false
	}
	return n, true
}

// changeLocked bumps the version and returns everything notify needs.
// c.mu must be held; the callback is invoked after it is released.
func (c *Cron) changeLocked() ([]session.LoopSpec, func([]session.LoopSpec), int) {
	c.version++
	return specsOf(c.listLocked()), c.OnChange, c.version
}

// notify hands a snapshot to OnChange, never out of order: a snapshot
// older than one already delivered is dropped, so two concurrent
// mutations cannot persist the losing view.
func (c *Cron) notify(snap []session.LoopSpec, cb func([]session.LoopSpec), ver int) {
	if cb == nil {
		return
	}
	c.notifyMu.Lock()
	defer c.notifyMu.Unlock()
	if ver <= c.notified {
		return
	}
	c.notified = ver
	cb(snap)
}

func specsOf(jobs []Info) []session.LoopSpec {
	out := make([]session.LoopSpec, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, session.LoopSpec{ID: j.ID, Every: j.Every.String(), Prompt: j.Prompt})
	}
	return out
}

// Specs is the persistable snapshot of armed jobs.
func (c *Cron) Specs() []session.LoopSpec {
	return specsOf(c.List())
}

// Close cancels every job. Further Add calls fail.
func (c *Cron) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, e := range c.jobs {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
	c.jobs = nil
}

// tick fires job e, if it is still the job armed under id. A timer that
// had already fired when Cancel or Replace stopped it still runs, and
// Replace can arm another session's job under the same id: that job is
// not this one, and firing it would run it early and leave it with a
// second timer nothing can stop.
func (c *Cron) tick(id string, e *cronJob) { c.fire(id, e, KindFired, 0) }

// fire is tick with the journal kind: KindCatchup carries the coalesced
// count of missed occurrences. The next regular firing is a full interval
// after this one.
func (c *Cron) fire(id string, e *cronJob, kind string, missed int) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	if c.jobs[id] != e {
		c.mu.Unlock()
		return
	}
	prompt := e.Prompt
	e.Next = time.Now().Add(e.Every)
	e.timer = time.AfterFunc(e.Every, func() { c.tick(id, e) })
	onFire := c.OnFire
	c.journal.Append(Occurrence{Job: id, Kind: kind, Missed: missed})
	c.mu.Unlock()
	if onFire != nil {
		onFire(id, prompt)
	}
}

// ParseEvery reads 30s / 5m / 1h (time.ParseDuration).
func ParseEvery(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty interval")
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("bad interval %q: want 30s, 5m or 1h", s)
	}
	if err := checkEvery(d); err != nil {
		return 0, err
	}
	return d, nil
}

// Tool lets the model add, list or cancel session loops.
type Tool struct {
	Cron *Cron
}

func (*Tool) Name() string { return "cron" }
func (*Tool) Description() string {
	return "Schedule a prompt to run on an interval for this session. Jobs are saved on the session and restored on resume; they stop when cancelled or when this process exits. action=add needs every (30s, 5m, 1h) and prompt; action=list; action=cancel needs id; action=history shows recent firings (id filters)."
}
func (*Tool) Mutating() bool { return false }
func (*Tool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","description":"add, list, cancel or history"},
"every":{"type":"string","description":"Interval for add: 30s, 5m, 1h"},
"prompt":{"type":"string","description":"The prompt to send when the loop fires"},
"id":{"type":"string","description":"Job id for cancel or history"}},
"required":["action"],"additionalProperties":false}`)
}

func (t *Tool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var in struct {
		Action string `json:"action"`
		Every  string `json:"every"`
		Prompt string `json:"prompt"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("bad input: %w", err)
	}
	if t.Cron == nil {
		return "", fmt.Errorf("no scheduler in this session")
	}
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "add":
		d, err := ParseEvery(in.Every)
		if err != nil {
			return "", err
		}
		prompt := strings.TrimSpace(in.Prompt)
		id, err := t.Cron.Add(d, prompt)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("scheduled %s every %s: %s", id, d, prompt), nil
	case "list":
		jobs := t.Cron.List()
		if len(jobs) == 0 {
			return "no loops armed", nil
		}
		var b strings.Builder
		for _, j := range jobs {
			fmt.Fprintf(&b, "%s every %s next %s: %s\n", j.ID, j.Every, j.Next.Format(time.Kitchen), j.Prompt)
		}
		return strings.TrimRight(b.String(), "\n"), nil
	case "cancel":
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return "", fmt.Errorf("id is required to cancel")
		}
		if !t.Cron.Cancel(id) {
			return "", fmt.Errorf("no loop %q", id)
		}
		return "cancelled " + id, nil
	case "history":
		occ := t.Cron.Journal().Recent(20, strings.TrimSpace(in.ID))
		if len(occ) == 0 {
			return "no occurrences recorded", nil
		}
		var b strings.Builder
		for _, o := range occ {
			fmt.Fprintf(&b, "%s %s %s", o.Time.Format("01-02 15:04:05"), o.Job, o.Kind)
			if o.Missed > 0 {
				fmt.Fprintf(&b, " (%d missed)", o.Missed)
			}
			if o.Detail != "" {
				b.WriteString(" " + o.Detail)
			}
			b.WriteByte('\n')
		}
		return strings.TrimRight(b.String(), "\n"), nil
	default:
		return "", fmt.Errorf("action must be add, list, cancel or history")
	}
}

var _ tool.Tool = (*Tool)(nil)
