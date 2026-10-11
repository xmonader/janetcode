package llm_test

import (
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
	"github.com/xmonader/janetcode/internal/llm/anthropic"
	"github.com/xmonader/janetcode/internal/llm/gemini"
	"github.com/xmonader/janetcode/internal/llm/openai"
	"github.com/xmonader/janetcode/internal/llm/responses"
)

// Which providers the effort controller may move the tier on: only one
// that both sends a tier and keeps its cache across a change. Anthropic
// sends one but re-bills the history when it changes, and Gemini's cache
// is not documented either way, so both stay out; an OpenAI-compatible
// server is not sent a tier at all.
func TestEffortChangeKeepsCache(t *testing.T) {
	cases := []struct {
		name string
		prov llm.Provider
		want bool
	}{
		{"responses", &responses.Provider{}, true},
		{"anthropic", &anthropic.Provider{}, false},
		{"gemini", &gemini.Provider{}, false},
		{"openai-compatible", &openai.Provider{}, false},
	}
	for _, tc := range cases {
		if got := llm.EffortChangeKeepsCache(tc.prov); got != tc.want {
			t.Errorf("%s: keeps cache = %v, want %v", tc.name, got, tc.want)
		}
	}
}
