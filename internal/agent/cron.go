package agent

import (
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
