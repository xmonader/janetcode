package llm

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
)

// Infrastructure reports whether a failed model call failed for a reason
// outside the model: the provider refusing (rate limit, overload, a 5xx,
// an exhausted balance or spend cap, a rejected key), the network, or a
// stream that died and stayed dead through the retries. A run that ended
// on one never got its answer from the model, so scoring it as the
// model's failure measures the gateway — hero_agent published "0/30" for
// a model that was never reached, and a spend cap turned sixteen tasks
// into failures (hero_agent verifications/swebench/reports/
// 2026-07-29-insights.md).
//
// A cancellation or a deadline is not infrastructure: the caller decided
// to stop, and a task's own time budget is part of the task. Errors that
// did not come from the provider — the harness's own limits — are not
// either.
func Infrastructure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, ErrStreamWatchdog) {
		return false
	}
	var se StatusError
	if errors.As(err, &se) {
		s := se.HTTPStatus()
		switch {
		case s == 401 || s == 402 || s == 403 || s == 408 || s == 429 || s >= 500:
			return true
		}
		return PermanentLimit(ClassifyText(err))
	}
	if errors.Is(err, ErrStreamWatchdog) || Truncated(err) {
		return true
	}
	var ne net.Error
	var ue *url.Error
	var oe *net.OpError
	if errors.As(err, &ne) || errors.As(err, &ue) || errors.As(err, &oe) {
		return true
	}
	// A transport error some client wrapped as text only.
	l := strings.ToLower(err.Error())
	for _, w := range []string{"connection refused", "connection reset", "no such host", "tls handshake", "eof"} {
		if strings.Contains(l, w) && (strings.Contains(l, "dial") || strings.Contains(l, "post ") || strings.Contains(l, "http")) {
			return true
		}
	}
	return false
}
