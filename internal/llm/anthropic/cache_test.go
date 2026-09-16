package anthropic

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

// The cache breakpoint goes where the stable prefix ends, which is the
// same place every provider splits it (llm.StablePrefix): below the
// environment heading sit the working directory and the date, and a
// breakpoint under those pays for a cache write the next session never
// reads back.
func TestSystemPromptIsCachedOnlyToTheStablePrefix(t *testing.T) {
	params, err := buildParams(llm.Request{
		Model:    "m",
		System:   "you are a coding agent\n\n# Environment\n- Working directory: /tmp/eval-abc\n",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")},
	}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(params.System) != 2 {
		t.Fatalf("system blocks = %+v", params.System)
	}
	stable, volatile := params.System[0], params.System[1]
	if strings.Contains(stable.Text, "Working directory") || stable.CacheControl.Type == "" {
		t.Fatalf("stable block = %+v", stable)
	}
	if !strings.Contains(volatile.Text, "Working directory") || volatile.CacheControl.Type != "" {
		t.Fatalf("volatile block = %+v", volatile)
	}

	// A prompt with no such heading has no stable part to split off, so
	// the whole of it is cached, as it always was.
	params, err = buildParams(llm.Request{
		Model:    "m",
		System:   "just instructions",
		Messages: []llm.Message{llm.TextMessage(llm.RoleUser, "hi")},
	}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(params.System) != 1 || params.System[0].CacheControl.Type == "" {
		t.Fatalf("system blocks = %+v", params.System)
	}
}
