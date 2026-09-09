package agent

import (
	"errors"
	"testing"
)

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
