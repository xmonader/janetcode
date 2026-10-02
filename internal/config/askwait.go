package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// AskWaitForever is ParseAskWait's "wait as long as it takes"; it is the
// same value as agent.AskWaitForever, which this package cannot import.
const AskWaitForever time.Duration = -1

// DefaultAskTimeoutUnattended is the unattended wait when the config sets
// none (agent.DefaultAskWaitUnattended).
const DefaultAskTimeoutUnattended = 2 * time.Minute

// ParseAskWait reads an ask_timeout value: "" is def, "forever" (or
// "never", "off") waits as long as it takes, a bare number is seconds,
// and anything else is a Go duration ("90s", "5m"). "0" does not wait.
func ParseAskWait(key, s string, def time.Duration) (time.Duration, error) {
	v := strings.ToLower(strings.TrimSpace(s))
	switch v {
	case "":
		return def, nil
	case "forever", "never", "off":
		return AskWaitForever, nil
	}
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return time.Duration(n) * time.Second, nil
	}
	if d, err := time.ParseDuration(v); err == nil && d >= 0 {
		return d, nil
	}
	return 0, fmt.Errorf("%s %q: want a duration like 90s or 5m, 0 to go on at once with the recommendation, or forever", key, s)
}

// AskWaits is the interactive and the unattended wait this config sets.
func (c *Config) AskWaits() (interactive, unattended time.Duration, err error) {
	if interactive, err = ParseAskWait("ask_timeout", c.AskTimeout, AskWaitForever); err != nil {
		return 0, 0, err
	}
	unattended, err = ParseAskWait("ask_timeout_unattended", c.AskTimeoutUnattended, DefaultAskTimeoutUnattended)
	return interactive, unattended, err
}
