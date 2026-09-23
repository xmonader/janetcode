package llm

import (
	"regexp"
	"strings"
)

// Remedies are the words each front end uses for the fixes a failure
// calls for: the terminal has /connect, the web page does not, and
// advice naming a command the reader cannot run is worse than none.
type Remedies struct {
	// Key says how to fix the API key or base URL, e.g. "with /connect".
	Key string
	// Model says how to pick another model, e.g. "with /model".
	Model string
}

// FriendlyError turns a provider failure into one shape:
//
//	Headline (code): why. What to do.
//
// The headline says what failed in three words, the code is what you
// would search for, the reason is the provider's own words, and the last
// sentence is the one thing to try. A bare Go error is none of those.
// Anything unrecognised is passed through rather than dressed up.
func FriendlyError(err error, provider string, how Remedies) string {
	s := strings.TrimSpace(err.Error())
	l := strings.ToLower(s)
	shape := func(head, code, todo string) string {
		if code != "" {
			head += " (" + code + ")"
		}
		why := strings.TrimRight(s, ".")
		return head + ": " + why + ". " + todo
	}
	switch {
	case strings.Contains(l, "connection refused") || strings.Contains(l, "no such host") || strings.Contains(l, "dial tcp"):
		return shape("cannot reach the API", errorCode(l), "Check the base URL for provider "+provider+" "+how.Key+", and your network.")
	case strings.Contains(l, "http 401") || strings.Contains(l, "http 403") || strings.Contains(l, "authentication") || strings.Contains(l, "invalid x-api-key") || strings.Contains(l, "incorrect api key"):
		return shape("authentication failed", errorCode(l), "Check the API key for provider "+provider+" "+how.Key+".")
	case strings.Contains(l, "http 429") || strings.Contains(l, "rate limit") || strings.Contains(l, "quota"):
		return shape("rate limited or out of quota", errorCode(l), "Wait and resend, or switch model "+how.Model+".")
	case strings.Contains(l, "http 404") || strings.Contains(l, "model not found") || strings.Contains(l, "not_found"),
		strings.Contains(l, "http 400") && strings.Contains(l, "model"):
		return shape("model not available", errorCode(l), "Pick another "+how.Model+".")
	case strings.Contains(l, "stream stalled"):
		return shape("the model stopped sending", errorCode(l), "Resend it; raise stream_idle_timeout or stream_max_duration in the config if it keeps happening on long answers.")
	case Truncated(err) || strings.Contains(l, "stream ended without") || strings.Contains(l, "unexpected eof"):
		return shape("the connection dropped mid-answer", "cut off", "Resend it; if it keeps happening the provider or a proxy is closing long responses.")
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return shape("the request timed out", errorCode(l), "Resend it; if it keeps happening the model may be overloaded.")
	case strings.Contains(l, "http 5"):
		return shape("the provider failed", errorCode(l), "Resend it; the failure is on their side.")
	}
	return s
}

// errorCode is the short thing you would search for: the HTTP status when
// there is one, otherwise the kind of network failure.
var httpStatusRe = regexp.MustCompile(`(?:http |status |code )(\d{3})`)

func errorCode(l string) string {
	if m := httpStatusRe.FindStringSubmatch(l); m != nil {
		return "http " + m[1]
	}
	switch {
	case strings.Contains(l, "no such host"):
		return "dns"
	case strings.Contains(l, "connection refused"):
		return "refused"
	case strings.Contains(l, "stream stalled"):
		return "stalled stream"
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return "timeout"
	}
	return ""
}
