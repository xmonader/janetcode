package llm

import (
	"fmt"
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

// Explanation is a provider failure explained in parts, so each front end can
// lay it out its own way: the headline says what failed in a few words,
// Next is the one thing to try, Code is what you would search for, and
// Reason is the provider's own words with the harness's plumbing taken
// out. Headline is empty when the error is not one this recognises; then
// Reason is the error as it came.
type Explanation struct {
	Headline string `json:"headline,omitempty"`
	Next     string `json:"next,omitempty"`
	Code     string `json:"code,omitempty"`
	Reason   string `json:"reason,omitempty"`
	// Retry reports whether sending the same request again can help. A
	// rejected key or a missing model fails the same way until the user
	// changes something, so offering a retry there is a button that lies.
	Retry bool `json:"retry"`
}

// String is the failure on one line:
//
//	Headline (code): reason. Next.
func (f Explanation) String() string {
	if f.Headline == "" {
		return f.Reason
	}
	s := f.Headline
	if f.Code != "" {
		s += " (" + f.Code + ")"
	}
	if f.Reason != "" {
		s += ": " + strings.TrimRight(f.Reason, ".")
	}
	return s + ". " + f.Next
}

// Explain sorts a provider failure into what failed and what to do.
func Explain(err error, provider string, how Remedies) Explanation {
	raw := strings.TrimSpace(err.Error())
	l := strings.ToLower(raw)
	f := Explanation{Code: errorCode(l), Reason: reasonOf(raw), Retry: true}
	switch {
	case strings.Contains(l, "connection refused") || strings.Contains(l, "no such host") || strings.Contains(l, "dial tcp"):
		f.Headline, f.Next = "Cannot reach the API", fmt.Sprintf("Check the base URL for %s %s, and your network.", provider, how.Key)
		f.Reason = networkReason(raw)
	case strings.Contains(l, "http 401") || strings.Contains(l, "http 403") || strings.Contains(l, "authentication") || strings.Contains(l, "invalid x-api-key") || strings.Contains(l, "incorrect api key"):
		f.Headline, f.Next, f.Retry = "Authentication failed", fmt.Sprintf("Check the API key for %s %s.", provider, how.Key), false
	case strings.Contains(l, "http 429") || strings.Contains(l, "rate limit") || strings.Contains(l, "quota"):
		f.Headline, f.Next = "Rate limited", "Wait a moment and resend, or switch model "+how.Model+"."
		if PermanentLimit(l) {
			f.Headline, f.Next, f.Retry = "Out of quota", "Top up the account for "+provider+", or switch model "+how.Model+".", false
		}
	case strings.Contains(l, "http 404") || strings.Contains(l, "model not found") || strings.Contains(l, "not_found"),
		strings.Contains(l, "http 400") && strings.Contains(l, "model"):
		f.Headline, f.Next, f.Retry = "Model not available", "Pick another "+how.Model+".", false
	case strings.Contains(l, "stream stalled"):
		f.Headline, f.Next = "The model stopped sending", "Resend it. If long answers keep stalling, raise stream_idle_timeout in the config."
		f.Reason = ""
	case Truncated(err) || strings.Contains(l, "stream ended without") || strings.Contains(l, "unexpected eof"):
		// The raw text is the harness's own ("stream ended without
		// finish_reason"): it names a field of a wire format, not a cause.
		f.Headline, f.Next, f.Code, f.Reason = "The connection dropped mid-answer", "Resend it. If it keeps happening, a proxy may be closing long responses.", "", ""
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		f.Headline, f.Next = "The request timed out", "Resend it. If it keeps happening, the model may be overloaded."
	case strings.Contains(l, "http 5"):
		f.Headline, f.Next = "The provider failed", "Resend it; the failure is on their side."
	default:
		f.Code, f.Reason = "", raw
	}
	return f
}

// internalPrefix is the "openai: " or "anthropic: " a client puts in front
// of its errors: the reader already knows which provider they are using.
var internalPrefix = regexp.MustCompile(`^(?:openai|anthropic|gemini|llm|openrouter):\s*`)

// statusSuffix is the " (HTTP 401)" the providers' errors end in; the code
// is already said in the headline.
var statusSuffix = regexp.MustCompile(`\s*\(HTTP \d{3}\)\s*$`)

// statusPrefix is the same code at the front: "http 404: model not found".
var statusPrefix = regexp.MustCompile(`(?i)^(?:http|status) \d{3}:\s*`)

// reasonOf is the provider's words without the harness's plumbing. A
// reason that is only a restatement of the status ("Unauthorized") is
// kept: it is short, and it is what a search would find.
func reasonOf(raw string) string {
	s := internalPrefix.ReplaceAllString(raw, "")
	s = statusSuffix.ReplaceAllString(s, "")
	s = statusPrefix.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// errorCode is the short thing you would search for: the HTTP status when
// there is one, otherwise the kind of network failure.
var httpStatusRe = regexp.MustCompile(`(?:http |status |code )(\d{3})`)

func errorCode(l string) string {
	if m := httpStatusRe.FindStringSubmatch(l); m != nil {
		return "HTTP " + m[1]
	}
	switch {
	case strings.Contains(l, "no such host"):
		return "DNS"
	case strings.Contains(l, "connection refused"):
		return "refused"
	case strings.Contains(l, "deadline exceeded") || strings.Contains(l, "timeout"):
		return "timeout"
	}
	return ""
}

var (
	dialTarget = regexp.MustCompile(`dial tcp ([^\s]+?):? (?:connect|i/o)`)
	lookupHost = regexp.MustCompile(`lookup ([^\s:]+)`)
)

// networkReason says a network failure in plain words: Go's own text
// ("Post \"...\": dial tcp 127.0.0.1:9: connect: connection refused") is
// the URL, the syscall and the errno, when all that matters is where.
func networkReason(raw string) string {
	l := strings.ToLower(raw)
	switch {
	case strings.Contains(l, "no such host"):
		if m := lookupHost.FindStringSubmatch(raw); m != nil {
			return "no host named " + m[1]
		}
	case strings.Contains(l, "connection refused"):
		if m := dialTarget.FindStringSubmatch(raw); m != nil {
			return "nothing is listening at " + m[1]
		}
	}
	return reasonOf(raw)
}
