package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/xmonader/janetcode/internal/cron"
)

// The scheduler itself lives in internal/cron: it needs a session record
// and a callback, not an agent. These keep the names the front-ends use.

// Cron is the session-scoped scheduler; see cron.Cron.
type Cron = cron.Cron

// CronInfo is the public snapshot of one job; see cron.Info.
type CronInfo = cron.Info

// CronTool lets the model add, list or cancel session loops; see cron.Tool.
type CronTool = cron.Tool

// NewCron returns an empty scheduler; see cron.New.
func NewCron() *Cron { return cron.New() }

// ParseEvery parses a loop interval ("30s", "5m", "1h").
func ParseEvery(s string) (time.Duration, error) { return cron.ParseEvery(s) }

// LoopArg reads `/loop [interval] <prompt>`: the interval when the first
// word is one (five minutes otherwise) and the prompt after it. A lone
// interval has nothing to send, and a duration out of range is still a
// duration: both are errors rather than a loop sending "10m" or "1s
// ping" to the model every five minutes.
func LoopArg(arg string) (time.Duration, string, error) {
	arg = strings.TrimSpace(arg)
	every, prompt := 5*time.Minute, arg
	f, rest, _ := strings.Cut(arg, " ")
	if d, err := ParseEvery(f); err == nil {
		every, prompt = d, strings.TrimSpace(rest)
	} else if _, perr := time.ParseDuration(f); perr == nil {
		return 0, "", err
	}
	if prompt == "" {
		return 0, "", fmt.Errorf("/loop %s needs something to send: /loop [interval] <prompt>", arg)
	}
	return every, prompt, nil
}
