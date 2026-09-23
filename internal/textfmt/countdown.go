package textfmt

import (
	"fmt"
	"regexp"
	"time"
)

// waitRe matches the "in 12s" a provider puts at the end of a phase to
// say how long it is about to sleep.
var waitRe = regexp.MustCompile(`(.*\bin )([0-9.]+)(m?s)$`)

// SplitCountdown splits a status line ending in a wait — "retrying 1/2 ·
// rate limited · in 6s" — into the text before the number and the wait.
// Shown as sent, that line sat unchanged for six seconds next to a
// counter running the other way, which reads as a frozen screen; kept as
// a deadline, it can count down to the moment something happens.
func SplitCountdown(text string) (prefix string, wait time.Duration, ok bool) {
	g := waitRe.FindStringSubmatch(text)
	if g == nil {
		return text, 0, false
	}
	d, err := time.ParseDuration(g[2] + g[3])
	if err != nil || d <= 0 {
		return text, 0, false
	}
	return g[1], d, true
}

// Countdown is prefix followed by the time left until deadline at now,
// in whole seconds, never below zero.
func Countdown(prefix string, deadline, now time.Time) string {
	left := deadline.Sub(now)
	if left < 0 {
		left = 0
	}
	if left < time.Minute {
		return fmt.Sprintf("%s%ds", prefix, int(left.Round(time.Second)/time.Second))
	}
	return prefix + Duration(left.Round(time.Second))
}
