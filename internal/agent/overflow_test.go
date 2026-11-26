package agent

import (
	"errors"
	"testing"
)

// TestOverflowKimiAndLookalikes: Moonshot's wording is recognised, and
// words that merely resemble a pattern inside identifiers or rate-limit
// text are not.
func TestOverflowKimiAndLookalikes(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"exceeded model token limit", true},
		{"Invalid request: Your request exceeded model token limit: 262144 (requested: 270000)", true},
		{`400 {"error":{"message":"Invalid request: Your request exceeded model token limit","type":"invalid_request_error"}}`, true},
		{"context_length_exceeded", true},
		{"This model's maximum context length is 262144 tokens", true},
		{"rate limit: tokens per minute", false},
		{"429 rate limit reached: token limit per minute, exceeded model token limit", false},
		{"tool token_limit_check failed: exit 1", false},
		{"tool exceeded_model_token_limit_probe not found", false},
		{"unknown tool: context_length_exceeded_handler", false},
		{"token limit", false},
	}
	for _, c := range cases {
		if got := isContextOverflow(errors.New(c.msg)); got != c.want {
			t.Errorf("isContextOverflow(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// TestOverflowNotConfusedWithThrottling: a rate limit is not a context
// overflow, and compacting in response to one throws away history to fix a
// problem that would have cleared on a retry.
func TestOverflowNotConfusedWithThrottling(t *testing.T) {
	overflow := []string{
		"prompt is too long: 210000 tokens > 200000 maximum",
		"This model's maximum context length is 128000 tokens",
		"input token count 1050000 exceeds the maximum number of tokens allowed",
		"the request exceeds the context window",
		"maximum prompt length is 131072",
		"please reduce the length of the messages",
		"input (9000 tokens) is longer than the model's context length",
		"exceeds the available context size",
		"range of input length should be [1, 129024]",
		// Anthropic, when the prompt fits but not with room for max_tokens.
		"input length and `max_tokens` exceed context limit: 188240 + 21333 > 200000, decrease input length or `max_tokens` and try again",
	}
	notOverflow := []string{
		"ThrottlingException: Too many tokens, please wait before trying again.",
		"429 Too Many Requests",
		"rate limit exceeded",
		"Service unavailable: upstream returned 503",
		"Throttling error: slow down",
	}
	for _, m := range overflow {
		if !isContextOverflow(errors.New(m)) {
			t.Errorf("missed an overflow: %q", m)
		}
	}
	for _, m := range notOverflow {
		if isContextOverflow(errors.New(m)) {
			t.Errorf("treated a rate limit as an overflow: %q", m)
		}
	}
}
