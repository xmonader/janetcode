package llm

import (
	"errors"
	"strings"
	"testing"
)

var tuiRemedies = Remedies{Key: "with /connect", Model: "with /model"}

// TestExplainSortsFailures: every provider failure reads the same way —
// Headline (code): reason. Next. — with the code said once, no harness
// plumbing in the reason, and a retry offered only where it can help.
func TestExplainSortsFailures(t *testing.T) {
	cases := []struct {
		err   error
		want  string
		retry bool
	}{
		{errors.New("Post \"https://x/v1\": dial tcp 10.0.0.1:443: connect: connection refused"),
			"Cannot reach the API (refused): nothing is listening at 10.0.0.1:443. Check the base URL for openai with /connect, and your network.", true},
		{errors.New("Unauthorized (HTTP 401)"),
			"Authentication failed (HTTP 401): Unauthorized. Check the API key for openai with /connect.", false},
		{errors.New("openai: http 403: invalid x-api-key"),
			"Authentication failed (HTTP 403): invalid x-api-key. Check the API key for openai with /connect.", false},
		{errors.New("Rate limit exceeded: 60 requests per minute (HTTP 429)"),
			"Rate limited (HTTP 429): Rate limit exceeded: 60 requests per minute. Wait a moment and resend, or switch model with /model.", true},
		{errors.New("http 404: model not found"),
			"Model not available (HTTP 404): model not found. Pick another with /model.", false},
		{errors.New("context deadline exceeded"),
			"The request timed out (timeout): context deadline exceeded. Resend it. If it keeps happening, the model may be overloaded.", true},
		{errors.New("upstream unavailable (HTTP 503)"),
			"The provider failed (HTTP 503): upstream unavailable. Resend it; the failure is on their side.", true},
		{Truncate(errors.New("openai: stream ended without finish_reason")),
			"The connection dropped mid-answer. Resend it. If it keeps happening, a proxy may be closing long responses.", true},
	}
	for _, c := range cases {
		f := Explain(c.err, "openai", tuiRemedies)
		if got := f.String(); got != c.want {
			t.Errorf("%q\n  got  %q\n want %q", c.err, got, c.want)
		}
		if f.Retry != c.retry {
			t.Errorf("%q: retry = %v, want %v", c.err, f.Retry, c.retry)
		}
		if strings.Count(strings.ToLower(f.String()), "http ") > 1 {
			t.Errorf("%q says its code twice: %q", c.err, f.String())
		}
	}
	// Anything unrecognised is passed through rather than dressed up.
	if f := Explain(errors.New("something odd"), "openai", tuiRemedies); f.String() != "something odd" || f.Headline != "" {
		t.Errorf("unrecognised error = %+v", f)
	}
}

// The advice names only what the reader's front end has: the web page
// has no /connect, so a key failure there must not send them to it.
func TestExplainUsesTheFrontEndsRemedy(t *testing.T) {
	web := Remedies{Key: "in the config file (janetcode --doctor shows which key each provider has)", Model: "with the model picker"}
	got := Explain(errors.New("Unauthorized (HTTP 401)"), "dashscope", web).String()
	if strings.Contains(got, "/connect") || !strings.Contains(got, "--doctor") || !strings.Contains(got, "dashscope") {
		t.Fatalf("got %q", got)
	}
}

func TestNetworkFailuresSayWhereNotHow(t *testing.T) {
	for raw, want := range map[string]string{
		`Post "http://127.0.0.1:9/v1/chat/completions": dial tcp 127.0.0.1:9: connect: connection refused`: "nothing is listening at 127.0.0.1:9",
		`Post "https://nope.invalid/v1/chat/completions": dial tcp: lookup nope.invalid: no such host`:      "no host named nope.invalid",
		`Post "https://nope.invalid/v1": dial tcp: lookup nope.invalid on 127.0.0.53:53: no such host`:    "no host named nope.invalid",
	} {
		x := Explain(errors.New(raw), "openai", Remedies{Key: "with /connect"})
		if x.Headline != "Cannot reach the API" || x.Reason != want {
			t.Errorf("%s:\n got %q / %q, want reason %q", raw, x.Headline, x.Reason, want)
		}
	}
}
