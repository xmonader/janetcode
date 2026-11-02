package agent

import (
	"strings"
	"testing"

	"github.com/xmonader/janetcode/internal/llm"
)

func planReply(thinkChars int, tool, path string) llm.Message {
	bl := []llm.Block{{Type: llm.BlockThinking, Text: strings.Repeat("x", thinkChars)}}
	if tool != "" {
		bl = append(bl, llm.Block{Type: llm.BlockToolUse, ID: "c1", Name: tool, Input: []byte(`{"path":"` + path + `"}`)})
	}
	return llm.Message{Role: llm.RoleAssistant, Blocks: bl}
}

func TestIsPlanReply(t *testing.T) {
	cases := []struct {
		name  string
		chars int
		tool  string
		path  string
		want  bool
	}{
		{"big plan writes product", 6000, "write_file", "core.go", true},
		{"short thinking", 5000, "write_file", "core.go", false},
		{"reads only", 9000, "read_file", "core.go", false},
		{"writes only a test", 9000, "write_file", "core_test.go", false},
		{"no calls", 9000, "", "", false},
	}
	for _, c := range cases {
		if got := isPlanReply(planReply(c.chars, c.tool, c.path), llm.Usage{}); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	if !isPlanReply(planReply(10, "edit_file", "a.go"), llm.Usage{ReasoningTokens: 2000}) {
		t.Error("reported reasoning tokens should count")
	}
}

func TestPlanCarryOncePerTurnAndOff(t *testing.T) {
	a := &Agent{}
	resp := &llm.Response{Message: planReply(8000, "write_file", "core.go")}
	if a.planCarryNote(resp) == "" {
		t.Fatal("first plan reply should get the note")
	}
	if a.planCarryNote(resp) != "" {
		t.Fatal("second in the same turn must not")
	}
	a.pl.startTurn()
	if a.planCarryNote(resp) == "" {
		t.Fatal("a new turn carries again")
	}
	b := &Agent{Off: map[string]bool{"plancarry": true}}
	if b.planCarryNote(resp) != "" {
		t.Fatal("off switch ignored")
	}
}
