package agent

import (
	"regexp"
	"strconv"
	"strings"
)

// notOverflow are errors that mention tokens or length but mean the
// request was refused for capacity, not size. They are checked first:
// Bedrock's "ThrottlingException: Too many tokens" would otherwise be read
// as an overflow, and the answer to a rate limit is to wait, not to throw
// away history.
var notOverflow = []string{
	"throttl",
	"rate limit",
	"too many requests",
	"service unavailable",
	"try again later",
	"overloaded",
}

// overflowPatterns are the ways providers say the prompt no longer fits.
// Each is a real message from a real endpoint; they disagree about almost
// every word, so the list is long and grows by observation rather than by
// guessing at what a provider ought to say.
var overflowPatterns = []string{
	"prompt is too long",                // Anthropic
	"request_too_large",                 // Anthropic
	"context_length_exceeded",           // OpenAI
	"maximum context length",            // OpenAI, OpenRouter
	"exceeds the context window",        // OpenAI
	"context window",                    // generic
	"model_context_window_exceeded",     // generic
	"input is too long",                 // generic
	"exceeds the maximum",               // Google
	"input token count",                 // Google
	"maximum prompt length",             // xAI
	"reduce the length of the messages", // Groq
	"is longer than the model",          // Together
	"available context size",            // llama.cpp
	"greater than the context length",   // LM Studio
	"context window exceeds limit",      // MiniMax
	"exceeded model token limit",        // Kimi
	"too large for model with",          // Mistral
	"range of input length should be",   // DashScope
	"exceeds the limit of",              // GitHub Copilot
}

// isContextOverflow recognises the provider errors that mean the prompt no
// longer fits the model's window.
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range notOverflow {
		if strings.Contains(msg, needle) {
			return false
		}
	}
	for _, needle := range overflowPatterns {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

var windowRe = regexp.MustCompile(`(?i)(?:>\s*|maximum context length is |context length of |limit of |window of )(\d{4,7})(?:\s*(?:tokens|maximum|max))?`)

// windowFromError pulls the model's context limit out of an overflow error
// such as "prompt is too long: 210000 tokens > 200000 maximum", or 0.
func windowFromError(err error) int {
	if err == nil {
		return 0
	}
	m := windowRe.FindStringSubmatch(err.Error())
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}
