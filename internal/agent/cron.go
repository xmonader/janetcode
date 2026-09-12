package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/xmonader/janetcode/internal/tool"
)

// maxCron bounds how many session loops can be armed at once.
const maxCron = 16

// Cron is a session-scoped scheduler: /loop and the cron tool add jobs
// that fire a prompt on an interval. Nothing is persisted; Close (session
// quit) cancels every timer.
type Cron struct {
	mu     sync.Mutex
	jobs   map[string]*cronJob
	nextID int
	closed bool
	// OnFire is called from the timer goroutine with the job id and prompt.
	// The TUI starts a turn; if a turn is already running the tick is skipped.
	OnFire func(id, prompt string)
}

type cronJob struct {
	ID     string
	Every  time.Duration
	Prompt string
	Next   time.Time
	timer  *time.Timer
}

// CronInfo is the public snapshot of one job.
type CronInfo struct {
	ID     string
	Every  time.Duration
	Prompt string
	Next   time.Time
}

// NewCron returns an empty scheduler.
func NewCron() *Cron {
	return &Cron{jobs: map[string]*cronJob{}}
}

// Add arms a job that fires after every, then again on that interval.
func (c *Cron) Add(every time.Duration, prompt string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("no scheduler in this session")
	}
	if every < time.Second {
		return "", fmt.Errorf("interval must be at least 1s")
	}
	if every > 24*time.Hour {
		return "", fmt.Errorf("interval must be at most 24h")
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "", fmt.Errorf("prompt must not be empty")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", fmt.Errorf("scheduler has stopped")
	}
	if c.jobs == nil {
		c.jobs = map[string]*cronJob{}
	}
	if len(c.jobs) >= maxCron {
		return "", fmt.Errorf("at most %d loops in one session", maxCron)
	}
	c.nextID++
	id := fmt.Sprintf("loop-%d", c.nextID)
	e := &cronJob{ID: id, Every: every, Prompt: prompt, Next: time.Now().Add(every)}
	e.timer = time.AfterFunc(every, func() { c.tick(id) })
	c.jobs[id] = e
	return id, nil
}

// List is a snapshot of armed jobs, oldest first.
func (c *Cron) List() []CronInfo {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]CronInfo, 0, len(c.jobs))
	for i := 1; i <= c.nextID; i++ {
		id := fmt.Sprintf("loop-%d", i)
		e := c.jobs[id]
		if e == nil {
			continue
		}
		out = append(out, CronInfo{ID: e.ID, Every: e.Every, Prompt: e.Prompt, Next: e.Next})
	}
	return out
}

// Cancel stops one job. Unknown ids return false.
func (c *Cron) Cancel(id string) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.jobs[id]
	if e == nil {
		return false
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	delete(c.jobs, id)
	return true
}

// CancelAll stops every job.
func (c *Cron) CancelAll() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.jobs)
	for _, e := range c.jobs {
		if e.timer != nil {
			e.timer.Stop()
		}
	}
	c.jobs = map[string]*cronJob{}
	return n
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

func (c *Cron) tick(id string) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	e := c.jobs[id]
	if e == nil {
		c.mu.Unlock()
		return
	}
	prompt := e.Prompt
	e.Next = time.Now().Add(e.Every)
	e.timer = time.AfterFunc(e.Every, func() { c.tick(id) })
	fire := c.OnFire
	c.mu.Unlock()
	if fire != nil {
		fire(id, prompt)
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
		return 0, fmt.Errorf("want 30s, 5m or 1h")
	}
	if d < time.Second {
		return 0, fmt.Errorf("interval must be at least 1s")
	}
	if d > 24*time.Hour {
		return 0, fmt.Errorf("interval must be at most 24h")
	}
	return d, nil
}

// CronTool lets the model add, list or cancel session loops.
type CronTool struct {
	Cron *Cron
}

func (*CronTool) Name() string { return "cron" }
func (*CronTool) Description() string {
	return "Schedule a prompt to run on an interval for this session only (dies when the session ends). action=add needs every (30s, 5m, 1h) and prompt; action=list; action=cancel needs id. Not a persistent cron: nothing is written to disk."
}
func (*CronTool) Mutating() bool { return false }
func (*CronTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{
"action":{"type":"string","description":"add, list or cancel"},
"every":{"type":"string","description":"Interval for add: 30s, 5m, 1h"},
"prompt":{"type":"string","description":"The prompt to send when the loop fires"},
"id":{"type":"string","description":"Job id for cancel"}},
"required":["action"],"additionalProperties":false}`)
}

func (t *CronTool) Run(ctx context.Context, input json.RawMessage) (string, error) {
	_ = ctx
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
		id, err := t.Cron.Add(d, in.Prompt)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("scheduled %s every %s: %s", id, d, strings.TrimSpace(in.Prompt)), nil
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
		if strings.TrimSpace(in.ID) == "" {
			return "", fmt.Errorf("id is required to cancel")
		}
		if !t.Cron.Cancel(in.ID) {
			return "", fmt.Errorf("no loop %q", in.ID)
		}
		return "cancelled " + in.ID, nil
	default:
		return "", fmt.Errorf("action must be add, list or cancel")
	}
}

var _ tool.Tool = (*CronTool)(nil)
