package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func TestClampRecap(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"  hello  ", "hello"},
		{"", ""},
	} {
		if got := clampRecap(c.in); got != c.want {
			t.Errorf("clampRecap(%q)=%q want %q", c.in, got, c.want)
		}
	}
	long := clampRecap(strings.Repeat("é", recapMax+50))
	if r := []rune(long); len(r) != recapMax+1 || !strings.HasSuffix(long, "…") {
		t.Errorf("long recap not clamped: %d runes", len(r))
	}
}

func TestAskAtEnd(t *testing.T) {
	user := llm.Message{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "a"}}}
	asst := llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockText, Text: "b"}}}
	got := askAtEnd([]llm.Message{user}, "q")
	if len(got) != 1 || len(got[0].Blocks) != 2 {
		t.Errorf("user-ending history should take the ask into it: %+v", got)
	}
	got = askAtEnd([]llm.Message{user, asst}, "q")
	if len(got) != 3 || got[2].Role != llm.RoleUser {
		t.Errorf("assistant-ending history should get a new user message: %+v", got)
	}
}
