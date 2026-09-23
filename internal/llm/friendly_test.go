package llm

import (
	"errors"
	"strings"
	"testing"
)

var tuiRemedies = Remedies{Key: "with /connect", Model: "with /model"}

// TestErrorMessageShape: every provider failure reads the same way —
// Headline (code): why. What to do.
func TestErrorMessageShape(t *testing.T) {
	cases := []struct {
		err              error
		head, code, todo string
	}{
		{errors.New("Post \"https://x/v1\": dial tcp 10.0.0.1:443: connect: connection refused"), "cannot reach the API", "refused", "/connect"},
		{errors.New("http 401: invalid x-api-key"), "authentication failed", "http 401", "/connect"},
		{errors.New("Unauthorized (HTTP 401)"), "authentication failed", "http 401", "/connect"},
		{errors.New("http 429: rate limit exceeded"), "rate limited or out of quota", "http 429", "/model"},
		{errors.New("http 404: model not found"), "model not available", "http 404", "/model"},
		{errors.New("context deadline exceeded"), "the request timed out", "timeout", "Resend"},
		{errors.New("http 503: upstream unavailable"), "the provider failed", "http 503", "Resend"},
		{Truncate(errors.New("openai: stream ended without finish_reason")), "the connection dropped mid-answer", "cut off", "Resend"},
	}
	for _, c := range cases {
		got := FriendlyError(c.err, "openai", tuiRemedies)
		if !strings.HasPrefix(got, c.head+" ("+c.code+"): ") {
			t.Errorf("%q\n  got %q\n want it to start %q", c.err, got, c.head+" ("+c.code+"): ")
		}
		if !strings.HasSuffix(got, ".") || !strings.Contains(got, c.todo) {
			t.Errorf("%q gave no advice: %q", c.err, got)
		}
		// The provider's own words survive between the two.
		if !strings.Contains(got, strings.TrimSpace(c.err.Error())) {
			t.Errorf("%q lost the reason: %q", c.err, got)
		}
	}
	if got := FriendlyError(errors.New("something odd"), "openai", tuiRemedies); got != "something odd" {
		t.Errorf("unrecognised error = %q", got)
	}
}

// The advice names only what the reader's front end has: the web page
// has no /connect, so a key failure there must not send them to it.
func TestErrorAdviceUsesTheFrontEndsRemedy(t *testing.T) {
	web := Remedies{Key: "in the config file (janetcode --doctor shows which key each provider has)", Model: "with the model picker"}
	got := FriendlyError(errors.New("Unauthorized (HTTP 401)"), "dashscope", web)
	if strings.Contains(got, "/connect") || !strings.Contains(got, "--doctor") || !strings.Contains(got, "dashscope") {
		t.Fatalf("got %q", got)
	}
}
