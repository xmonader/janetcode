package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/openai"
)

func TestKeyRejected(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{&openai.APIError{Status: 401, Message: "bad key"}, true},
		{fmt.Errorf("turn: %w", &openai.APIError{Status: 403, Message: "forbidden"}), true},
		{fmt.Errorf("turn: %w", &anthropic.APIError{Status: 401, Message: "invalid x-api-key"}), true},
		{&openai.APIError{Status: 429, Message: "slow down"}, false},
		{&anthropic.APIError{Status: 500, Message: "boom"}, false},
		// Only the typed error counts, not a message that looks like one.
		{errors.New("proxy said (HTTP 401)"), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := keyRejected(c.err); got != c.want {
			t.Errorf("keyRejected(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
